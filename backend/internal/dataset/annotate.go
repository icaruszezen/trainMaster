package dataset

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Annotation editing (the annotation workspace). An image's boxes are read
// from and written back to its Pascal VOC file in the dataset folder, so
// the folder stays usable by other tools; the index row of the image and
// the dataset counters are updated in the same request, without a scan.
//
// Several people may edit the same dataset. Every read of an annotation
// returns a version (a hash of the file's content); a save or delete names
// the version it started from and fails with ErrAnnotationChanged when the
// file has changed since, instead of silently overwriting someone else's
// work.

// maxSavedObjects bounds the boxes of one annotation file.
const maxSavedObjects = 10_000

// maxLabelRunes bounds an object's label, like a class name.
const maxLabelRunes = maxClassNameRunes

// annotationVersion identifies the content of an annotation file. It is
// opaque to clients; "" means "no file".
func annotationVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// readAnnotationFile reads the file at rel, up to one byte more than
// parseVOC reads, so an oversized file still fails to parse and gets a
// version different from its first 16 MiB.
func readAnnotationFile(root *os.Root, rel string) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxAnnotationBytes+1))
}

// readVersionedAnnotation reads and parses the VOC file at rel. The
// version (and the content) is returned whenever the file could be read,
// also when it cannot be parsed (so a broken file can be replaced on
// purpose).
func readVersionedAnnotation(root *os.Root, rel string) (a *vocAnnotation, version string, data []byte, err error) {
	data, err = readAnnotationFile(root, rel)
	if err != nil {
		return nil, "", nil, err
	}
	version = annotationVersion(data)
	a, err = parseVOC(bytes.NewReader(data))
	return a, version, data, err
}

// annotationTarget is the VOC file an image's annotation is read from and
// written to.
type annotationTarget struct {
	Path string // slash-separated, relative to the dataset folder
	// Paired is true when the index paired the file with the image. An
	// unpaired target is where a new file would go, or a file written by
	// another tool after the last scan.
	Paired bool
	// Owner is set when the target cannot be used for this image because
	// it belongs to another image (see resolveAnnotationTarget). It names
	// that image.
	Owner string
}

// resolveAnnotationTarget finds the VOC file of img: the one the index
// paired with it; for an image without one, the file a scan would pair
// with it now (see annotationCandidate), which another tool may have
// written since the last scan, or else where a new file goes (see
// defaultAnnotationPath). Showing, saving and the next scan thus agree on
// the file.
//
// An unpaired file may belong to another image: the index pairs it with
// another image, or a scan would give it to another image that claims it
// too (a.jpg and a.png both want a.xml, see claimOwner). Owner then names
// that image, and the annotation of img can neither be shown nor saved.
func resolveAnnotationTarget(ctx context.Context, q querier, root *os.Root, img Image) (annotationTarget, error) {
	if img.AnnotationPath != nil {
		return annotationTarget{Path: *img.AnnotationPath, Paired: true}, nil
	}
	t := annotationTarget{Path: annotationCandidate(root, img.Path)}
	found := t.Path != ""
	if !found {
		t.Path = defaultAnnotationPath(root, img.Path)
	}
	err := q.QueryRow(ctx, `SELECT rel_path FROM dataset_images
		WHERE dataset_id = $1 AND id <> $2 AND lower(annotation_path) = lower($3)
		ORDER BY `+ImageOrder+` LIMIT 1`, img.DatasetID, img.ID, t.Path).Scan(&t.Owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return t, err
	}
	if t.Owner == "" && found {
		owner, err := claimOwner(ctx, q, root, img, t.Path)
		if err != nil {
			return t, err
		}
		t.Owner = owner
	}
	return t, nil
}

// claimOwner decides, like pairAnnotations, which image the VOC file rel
// (found for img and not paired in the index) belongs to when other
// indexed images claim it too: a.jpg and a.png both take a.xml, and
// JPEGImages/a.jpg and images/a.jpg both take Annotations/a.xml. The
// <filename> recorded in the file decides; without a match, the first
// image in sorted order gets it. It returns that image, or "" if it is
// img.
func claimOwner(ctx context.Context, q querier, root *os.Root, img Image, rel string) (string, error) {
	dir, base := path.Split(img.Path)
	stem := strings.TrimSuffix(base, path.Ext(base))
	// Only images with the same stem (ignoring case) can claim rel: in the
	// folder of img or, for a VOC image folder, in another one next to it.
	patterns := []string{likePrefix(dir+stem) + ".%"}
	if parent, ok := vocImageFolder(dir); ok {
		patterns = append(patterns, likePrefix(parent+"jpegimages/"+stem)+".%", likePrefix(parent+"images/"+stem)+".%")
	}
	rows, err := q.Query(ctx, `SELECT rel_path FROM dataset_images WHERE dataset_id = $1 AND id <> $2
		AND lower(rel_path) LIKE ANY (SELECT lower(p) FROM unnest($3::text[]) AS p)`, img.DatasetID, img.ID, patterns)
	if err != nil {
		return "", err
	}
	others, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	claims := []string{img.Path}
	for _, other := range others {
		// A scan only sees the images that are still there.
		if exists, _ := regularFileExists(root, other); exists && annotationCandidate(root, other) == rel {
			claims = append(claims, other)
		}
	}
	if len(claims) == 1 {
		return "", nil
	}
	slices.Sort(claims)
	filename := ""
	if a, _, _, err := readVersionedAnnotation(root, rel); err == nil {
		filename = a.Filename
	}
	if winner := pickByFilename(claims, filename); winner != img.Path {
		return winner, nil
	}
	return "", nil
}

// likePrefix escapes the LIKE wildcards of s (with the default escape
// character, the backslash).
func likePrefix(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// annotationCandidate returns the VOC file a scan would pair with the
// image at rel, trying the candidates of pairAnnotations in its order:
// <stem>.xml next to the image, the same name ignoring case, then, for an
// image in a VOC image folder, <stem>.xml (ignoring case) in a sibling
// "Annotations" folder. Like a scan it only takes regular files, and it
// does not consider other images claiming the file (see claimOwner). It
// returns "" when there is none.
func annotationCandidate(root *os.Root, rel string) string {
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if exact := dir + stem + ".xml"; isRegularFile(root, exact) {
		return exact
	}
	want := strings.ToLower(stem + ".xml")
	for _, name := range namesIn(root, dir, want) {
		if isRegularFile(root, dir+name) {
			return dir + name
		}
	}
	parent, ok := vocImageFolder(dir)
	if !ok {
		return ""
	}
	found := ""
	for _, ann := range annotationsDirs(root, parent) {
		for _, name := range namesIn(root, parent+ann+"/", want) {
			if p := parent + ann + "/" + name; isRegularFile(root, p) {
				if found == "" || p < found {
					found = p
				}
				break
			}
		}
	}
	return found
}

func isRegularFile(root *os.Root, rel string) bool {
	ok, _ := regularFileExists(root, rel)
	return ok
}

// vocImageFolder reports whether the folder dir ("" for the top level,
// else slash-terminated) is a Pascal VOC image folder, "JPEGImages" or
// "images" in any case, and returns its parent folder.
func vocImageFolder(dir string) (parent string, ok bool) {
	if dir == "" {
		return "", false
	}
	parent, name := path.Split(strings.TrimSuffix(dir, "/"))
	d := strings.ToLower(name)
	return parent, d == "jpegimages" || d == "images"
}

// folderPath turns a folder ("" for the top level, else slash-terminated)
// into a name for os.Root.
func folderPath(dir string) string {
	if dir = strings.TrimSuffix(dir, "/"); dir == "" {
		return "."
	}
	return filepath.FromSlash(dir)
}

// namesIn returns the names in the folder dir ("" for the top level, else
// slash-terminated) that are want in lower case and valid UTF-8, sorted.
// It reads names only, so a folder of many images is listed quickly.
func namesIn(root *os.Root, dir, want string) []string {
	f, err := root.Open(folderPath(dir))
	if err != nil {
		return nil
	}
	defer f.Close()
	var found []string
	for {
		names, err := f.Readdirnames(1024)
		for _, name := range names {
			if strings.ToLower(name) == want && utf8.ValidString(name) {
				found = append(found, name)
			}
		}
		if err != nil { // io.EOF at the end
			break
		}
	}
	slices.Sort(found)
	return found
}

// annotationsDirs returns the names of the real folders called
// "Annotations" (any case) in the folder parent ("" for the top level,
// else slash-terminated), sorted.
func annotationsDirs(root *os.Root, parent string) []string {
	var dirs []string
	for _, name := range namesIn(root, parent, "annotations") {
		// Lstat does not follow symbolic links, like a scan.
		if fi, err := root.Lstat(filepath.FromSlash(parent + name)); err == nil && fi.IsDir() {
			dirs = append(dirs, name)
		}
	}
	return dirs
}

// defaultAnnotationPath is where a new VOC file for the image at rel goes:
// <stem>.xml next to it, or, for an image in a Pascal VOC "JPEGImages" (or
// "images") folder with a sibling "Annotations" folder, <stem>.xml in
// there. A scan pairs the image with that file (see pairAnnotations) as
// long as no other candidate exists (see annotationCandidate).
func defaultAnnotationPath(root *os.Root, rel string) string {
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if parent, ok := vocImageFolder(dir); ok {
		if ann := siblingAnnotationsDir(root, parent); ann != "" {
			return parent + ann + "/" + stem + ".xml"
		}
	}
	return dir + stem + ".xml"
}

// siblingAnnotationsDir returns the name of the real folder called
// "Annotations" (any case) in the folder parent ("" for the top level),
// the first in byte order if there are several, or "" if there is none.
func siblingAnnotationsDir(root *os.Root, parent string) string {
	if dirs := annotationsDirs(root, parent); len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// imageDetail describes img with its annotation as it is on disk now.
func (s *Service) imageDetail(ctx context.Context, d Dataset, img Image) (ImageDetail, error) {
	detail := ImageDetail{Image: img, Objects: []AnnotationObject{}}
	detail.Annotation = nil
	root, err := s.openDatasetRoot(d)
	if err != nil {
		var ue *Error
		if errors.As(err, &ue) { // the dataset folder is gone
			detail.AnnotationState, detail.AnnotationError = annotationNone, ue.Message
			return detail, nil
		}
		return detail, err
	}
	defer root.Close()
	t, err := resolveAnnotationTarget(ctx, s.db, root, img)
	if err != nil {
		return detail, err
	}
	if t.Owner != "" {
		detail.AnnotationState = annotationNone
		detail.AnnotationBlocked = ownerConflict(t).Message
		return detail, nil
	}
	a, version, _, err := readVersionedAnnotation(root, t.Path)
	describeAnnotation(&detail, t, a, version, err)
	return detail, nil
}

// describeAnnotation sets the annotation fields of detail for the VOC file
// of the target t, read by readVersionedAnnotation as a, version and err.
func describeAnnotation(detail *ImageDetail, t annotationTarget, a *vocAnnotation, version string, err error) {
	detail.Objects = []AnnotationObject{}
	detail.Annotation = nil
	switch {
	case errors.Is(err, fs.ErrNotExist):
		detail.AnnotationState = annotationNone
		if t.Paired { // a VOC file that is gone keeps its path, so the viewer can say which
			detail.AnnotationError = "标注文件已不存在，请重新扫描"
		}
		return
	case err == nil:
		if a.Objects != nil {
			detail.Objects = a.Objects
		}
		detail.AnnotationUnsupported = a.Unsupported
		detail.AnnotationState = annotationEmpty
		if len(a.Objects) > 0 {
			detail.AnnotationState = annotationAnnotated
		}
		detail.Annotation = &ImageAnnotation{Path: t.Path, ObjectCount: len(a.Objects), Labels: sortedLabels(a.labelCounts())}
	default: // could not be read, or not parsed
		detail.AnnotationState, detail.AnnotationError = annotationInvalid, annotationErrorMessage(err)
	}
	p := t.Path
	detail.AnnotationPath = &p
	detail.AnnotationVersion = version
}

// normalizeObjects validates the boxes of an annotation to save. Labels
// are trimmed; coordinates are clamped to the image (when its size is
// known), must then be at most math.MaxInt32 (what parseVOC reads back)
// and span at least one pixel.
func normalizeObjects(objects []AnnotationObject, width, height int) ([]AnnotationObject, error) {
	if len(objects) > maxSavedObjects {
		return nil, newError(ErrInvalid, "一张图片最多保存 %d 个目标", maxSavedObjects)
	}
	out := make([]AnnotationObject, len(objects))
	for i, o := range objects {
		o.Name = strings.TrimSpace(o.Name)
		switch {
		case o.Name == "":
			return nil, newError(ErrInvalid, "第 %d 个目标没有类别", i+1)
		case !utf8.ValidString(o.Name) || strings.ContainsFunc(o.Name, unicode.IsControl):
			return nil, newError(ErrInvalid, "第 %d 个目标的类别名称包含换行或控制字符", i+1)
		case utf8.RuneCountInString(o.Name) > maxLabelRunes:
			return nil, newError(ErrInvalid, "第 %d 个目标的类别名称不能超过 %d 个字符", i+1, maxLabelRunes)
		}
		o.Xmin, o.Xmax = clampCoord(o.Xmin, width), clampCoord(o.Xmax, width)
		o.Ymin, o.Ymax = clampCoord(o.Ymin, height), clampCoord(o.Ymax, height)
		if max(o.Xmin, o.Ymin, o.Xmax, o.Ymax) > math.MaxInt32 {
			return nil, newError(ErrInvalid, "第 %d 个目标（%s）的坐标超出范围（不能大于 %d）", i+1, o.Name, math.MaxInt32)
		}
		if o.Xmin >= o.Xmax || o.Ymin >= o.Ymax {
			return nil, newError(ErrInvalid, "第 %d 个目标（%s）的框在图片范围内的宽或高为 0", i+1, o.Name)
		}
		out[i] = o
	}
	return out, nil
}

// clampCoord limits a box coordinate to [0, limit]; limit <= 0 means the
// image size is unknown and only negative values are clamped.
func clampCoord(v, limit int) int {
	if v < 0 {
		return 0
	}
	if limit > 0 && v > limit {
		return limit
	}
	return v
}

// SaveAnnotationInput is an annotation to write.
type SaveAnnotationInput struct {
	Objects []AnnotationObject
	// BaseVersion is the annotation_version of the file the edit started
	// from ("" when the image had none). Unless Force is set, the save
	// fails with ErrAnnotationChanged when the file is no longer that
	// version.
	BaseVersion string
	Force       bool
}

// errAnnotationChanged is returned when an annotation was changed since the
// version a save or delete started from.
var errAnnotationChanged = &Error{Kind: ErrAnnotationChanged,
	Message: "这张图片的标注文件已被修改（可能有其他人同时在标注它），请重新加载后再编辑，或选择覆盖"}

// SaveAnnotation writes the boxes of an image to its VOC file, creating the
// file if needed (see resolveAnnotationTarget), and updates the index and
// the dataset counters. An empty list writes a file without objects: an
// "empty" annotation (a negative sample). It returns the image with the
// annotation it wrote.
func (s *Service) SaveAnnotation(ctx context.Context, datasetID, imageID int64, in SaveAnnotationInput) (ImageDetail, error) {
	return s.editAnnotation(ctx, datasetID, imageID, in.BaseVersion, in.Force, func(e *annotationEdit) ([]byte, error) {
		info := stagedImageInfo(e.root, e.img.Path)
		if info.Width == 0 && e.img.Width != nil && e.img.Height != nil {
			info.Width, info.Height = *e.img.Width, *e.img.Height
		}
		if info.Width == 0 && e.current != nil && e.current.Width > 0 && e.current.Height > 0 {
			info.Width, info.Height = e.current.Width, e.current.Height
		}
		objects, err := normalizeObjects(in.Objects, info.Width, info.Height)
		if err != nil {
			return nil, err
		}
		// Edit the existing file in place, keeping what other tools wrote
		// (see patchVOC). A file that cannot be edited so is replaced by a
		// new one only on purpose (force); so is a file that cannot be
		// parsed (see editAnnotation).
		if e.current != nil {
			data, err := patchVOC(e.data, info, objects)
			if err == nil {
				return data, nil
			}
			if !in.Force {
				return nil, newError(ErrAnnotationUnpatchable, "标注文件 %s 无法在保留其中其它内容的情况下修改；"+
					"如果仍要保存，将按平台的格式重新生成整个文件，文件中平台不编辑的内容会丢失", e.target.Path)
			}
		}
		var b bytes.Buffer
		if err := writeVOC(&b, path.Base(path.Dir(e.img.Path)), e.img.FileName, info, objects); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	})
}

// DeleteAnnotation removes the VOC file of an image, which becomes
// unannotated. baseVersion and force work as in SaveAnnotation.
func (s *Service) DeleteAnnotation(ctx context.Context, datasetID, imageID int64, baseVersion string, force bool) (ImageDetail, error) {
	return s.editAnnotation(ctx, datasetID, imageID, baseVersion, force, func(*annotationEdit) ([]byte, error) {
		return nil, nil
	})
}

// annotationEdit is the state an annotation change works on.
type annotationEdit struct {
	root    *os.Root
	img     Image
	target  annotationTarget
	exists  bool           // the target file exists
	data    []byte         // its content
	current *vocAnnotation // the target parsed, nil if missing or invalid
}

// ownerConflict is the error for an image whose VOC file belongs to
// another image (see annotationTarget.Owner).
func ownerConflict(t annotationTarget) *Error {
	return newError(ErrConflict, "标注文件 %s 属于同一文件夹中的另一张图片 %s（VOC 按文件名配对），"+
		"无法为这张图片单独保存标注；请先重命名其中一张图片", t.Path, t.Owner)
}

// updateAnnotationIndex records the new annotation of img in its index row
// and refreshes the dataset counters. annPath nil means no file.
func updateAnnotationIndex(ctx context.Context, tx pgx.Tx, img Image, annPath *string, modTime time.Time, size int64,
	state string, objects int, labels map[string]int) error {
	var mod *time.Time
	var sz *int64
	if annPath != nil {
		mod, sz = &modTime, &size
	}
	tag, err := tx.Exec(ctx, `UPDATE dataset_images SET annotation_path = $3, annotation_mod_time = $4,
			annotation_size = $5, annotation_state = $6, object_count = $7, label_counts = $8, updated_at = now()
		WHERE dataset_id = $1 AND id = $2`, img.DatasetID, img.ID, annPath, mod, sz, state, objects, labels)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errImageNotFound // removed by a scan meanwhile
	}
	return recomputeAggregates(ctx, tx, img.DatasetID, false)
}

// editAnnotation replaces the annotation file of an image with the content
// change returns (nil removes the file) after checking that the file is
// still at baseVersion, and updates the index and the dataset counters.
// Changes of one image are serialized, so the check and the write cannot
// interleave with another save in this process.
//
// The database is updated first and the file is put in place as the last
// step before the commit; when the commit fails, the previous file is put
// back, so the file and the index stay in step. The result describes the
// file this request wrote (no file after a delete), so its version is the
// one the next edit starts from even if the file changes again meanwhile.
func (s *Service) editAnnotation(ctx context.Context, datasetID, imageID int64, baseVersion string, force bool,
	change func(e *annotationEdit) ([]byte, error)) (ImageDetail, error) {
	unlock, err := s.annotationLocks.lock(ctx, imageID)
	if err != nil {
		return ImageDetail{}, err
	}
	defer unlock()

	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return ImageDetail{}, err
	}
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return ImageDetail{}, err
	}
	defer root.Close()

	var detail ImageDetail
	var file *annotationFileChange
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Lock the dataset row like an import does before touching its
		// counters, then read the image.
		if _, err := queryDataset(ctx, tx, datasetID, true); err != nil {
			return err
		}
		img, err := queryImage(ctx, tx, datasetID, imageID)
		if err != nil {
			return err
		}
		if exists, err := regularFileExists(root, img.Path); err != nil || !exists {
			if err != nil && !errors.Is(err, errUnsafeTarget) && !errors.Is(err, errOccupied) {
				return err
			}
			return imageFileError(fs.ErrNotExist)
		}
		t, err := resolveAnnotationTarget(ctx, tx, root, img)
		if err != nil {
			return err
		}
		if t.Owner != "" {
			return ownerConflict(t)
		}
		e := &annotationEdit{root: root, img: img, target: t}
		exists, err := regularFileExists(root, t.Path)
		switch {
		case errors.Is(err, errUnsafeTarget):
			return newError(ErrConflict, "标注文件 %s 是符号链接或位于符号链接中，不能在平台中修改", t.Path)
		case errors.Is(err, errOccupied):
			return newError(ErrConflict, "%s 不是普通文件，无法写入标注", t.Path)
		case err != nil:
			return err
		}
		e.exists = exists
		version := ""
		if exists {
			a, v, data, rerr := readVersionedAnnotation(root, t.Path)
			if v == "" && rerr != nil {
				return fmt.Errorf("read annotation: %w", rerr)
			}
			version, e.data = v, data
			if rerr == nil {
				e.current = a
			}
		}
		if !force && version != baseVersion {
			return errAnnotationChanged
		}
		data, err := change(e)
		if err != nil {
			return err
		}

		// The database first; the file is changed last (see above).
		file = &annotationFileChange{root: root, rel: t.Path, data: data, existed: exists, previous: e.data}
		var written *vocAnnotation
		if data != nil {
			if len(data) > maxAnnotationBytes {
				return newError(ErrInvalid, "标注文件将超过 %d MiB，无法保存", maxAnnotationBytes>>20)
			}
			if written, err = parseVOC(bytes.NewReader(data)); err != nil {
				return fmt.Errorf("parse new annotation: %w", err)
			}
			fi, err := file.stage()
			if err != nil {
				return err
			}
			state := annotationEmpty
			if len(written.Objects) > 0 {
				state = annotationAnnotated
			}
			err = updateAnnotationIndex(ctx, tx, img, &t.Path, dbTime(fi.ModTime()), fi.Size(), state,
				len(written.Objects), written.labelCounts())
			if err != nil {
				return err
			}
		} else if err := updateAnnotationIndex(ctx, tx, img, nil, time.Time{}, 0, annotationNone, 0, map[string]int{}); err != nil {
			return err
		}
		if img, err = queryImage(ctx, tx, datasetID, imageID); err != nil {
			return err
		}
		detail = ImageDetail{Image: img}
		if data != nil {
			describeAnnotation(&detail, annotationTarget{Path: t.Path, Paired: true}, written, annotationVersion(data), nil)
		} else {
			describeAnnotation(&detail, annotationTarget{}, nil, "", fs.ErrNotExist)
		}

		// Refreshing the counters of a large dataset takes a while: make
		// sure nobody changed the file meanwhile.
		if !force && !annotationUnchanged(root, t.Path, version) {
			return errAnnotationChanged
		}
		return file.apply()
	})
	if err != nil {
		if file != nil {
			file.undo(s.log)
		}
		return ImageDetail{}, diskFullAnnotation(err)
	}
	if file != nil {
		file.finish(s.log)
	}
	return detail, nil
}

// annotationUnchanged reports whether the file at rel still has the given
// version ("" for no file).
func annotationUnchanged(root *os.Root, rel, version string) bool {
	exists, err := regularFileExists(root, rel)
	if err != nil || !exists {
		return err == nil && version == ""
	}
	data, err := readAnnotationFile(root, rel)
	return err == nil && annotationVersion(data) == version
}

// annotationFileChange replaces (data set) or removes (data nil) the
// annotation file rel. It becomes final with the database commit: stage
// writes the new content to a hidden file next to rel, apply puts it in
// place or moves the old file aside, and finish (after the commit) or undo
// (when anything failed) tidies up.
type annotationFileChange struct {
	root     *os.Root
	rel      string
	data     []byte // the new content, nil to remove the file
	existed  bool   // the file existed before
	previous []byte // its content then (at most maxAnnotationBytes+1 bytes)
	tmp      string // the hidden file: the new content, or the removed file
	applied  bool   // rel has been replaced or removed
}

// stage writes the new content to the hidden file and returns its
// information; renaming keeps the modification time.
func (c *annotationFileChange) stage() (fs.FileInfo, error) {
	tmp, err := writeTempAnnotation(c.root, c.rel, c.data)
	if err != nil {
		return nil, err
	}
	c.tmp = tmp
	fi, err := c.root.Lstat(filepath.FromSlash(tmp))
	if err != nil {
		return nil, fmt.Errorf("stat saved annotation: %w", err)
	}
	return fi, nil
}

// apply puts the staged file in place, or moves the file to remove aside.
func (c *annotationFileChange) apply() error {
	if c.data != nil {
		if err := renameInRoot(c.root, c.tmp, c.rel); err != nil {
			return fmt.Errorf("write annotation: %w", err)
		}
		c.tmp, c.applied = "", true
		return nil
	}
	if !c.existed {
		return nil
	}
	dir := path.Dir(c.rel)
	if err := ensureDirs(c.root, dir); err != nil {
		if errors.Is(err, errUnsafeTarget) {
			return newError(ErrConflict, "标注文件所在的文件夹 %s 不是普通文件夹（可能是符号链接），无法删除标注", dir)
		}
		return err
	}
	tmp := tempAnnotationName(dir)
	if err := renameInRoot(c.root, c.rel, tmp); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed meanwhile
		}
		return fmt.Errorf("remove annotation: %w", err)
	}
	c.tmp, c.applied = tmp, true
	return nil
}

// finish removes the file a committed delete moved aside.
func (c *annotationFileChange) finish(log *slog.Logger) {
	if c.tmp == "" {
		return
	}
	if err := c.root.Remove(filepath.FromSlash(c.tmp)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("could not remove a deleted annotation file", "path", c.tmp, "err", err)
	}
}

// undo reverts the file after a failure: it removes the staged file, or
// puts the previous file back unless the file has changed again since.
func (c *annotationFileChange) undo(log *slog.Logger) {
	var err error
	switch {
	case !c.applied:
		if c.tmp != "" {
			err = c.root.Remove(filepath.FromSlash(c.tmp))
		}
	case c.data == nil: // a removal: move the file back
		if exists, lerr := regularFileExists(c.root, c.rel); lerr == nil && !exists {
			err = renameInRoot(c.root, c.tmp, c.rel)
		} else {
			err = fmt.Errorf("another file took its place; the removed file is kept as %s", c.tmp)
		}
	case !fileHolds(c.root, c.rel, c.data):
		// Changed again meanwhile: leave it.
	case !c.existed:
		err = c.root.Remove(filepath.FromSlash(c.rel))
	case len(c.previous) > maxAnnotationBytes:
		err = errors.New("the previous file is too large to restore")
	default:
		err = writeAnnotationFile(c.root, c.rel, c.previous)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Error("could not restore an annotation file after a failed save", "path", c.rel, "err", err)
	}
}

// fileHolds reports whether the file at rel has exactly the content data.
func fileHolds(root *os.Root, rel string, data []byte) bool {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return false
	}
	defer f.Close()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
	return err == nil && bytes.Equal(got, data)
}

// tempAnnotationName returns a new name for a hidden temporary file in the
// folder dir. Scans skip hidden files.
func tempAnnotationName(dir string) string {
	return path.Join(dir, ".tm-annotation-"+rand.Text()[:12]+".tmp")
}

// writeTempAnnotation writes data to a new hidden temporary file in the
// folder of the annotation file rel (creating the folder if needed), with
// the permissions of the file at rel if there is one, and returns its
// name.
func writeTempAnnotation(root *os.Root, rel string, data []byte) (string, error) {
	dir := path.Dir(rel)
	if err := ensureDirs(root, dir); err != nil {
		if errors.Is(err, errUnsafeTarget) {
			return "", newError(ErrConflict, "标注文件所在的文件夹 %s 不是普通文件夹（可能是符号链接），无法保存", dir)
		}
		return "", err
	}
	perm := fs.FileMode(0o644)
	if fi, err := root.Lstat(filepath.FromSlash(rel)); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}
	tmp := tempAnnotationName(dir)
	f, err := root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return "", fmt.Errorf("create annotation: %w", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm) // not reduced by the umask
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(filepath.FromSlash(tmp))
		return "", fmt.Errorf("write annotation: %w", err)
	}
	return tmp, nil
}

// renameInRoot renames the file from to the name to, replacing it; both
// are slash-separated paths in root.
func renameInRoot(root *os.Root, from, to string) error {
	// os.Root has no Rename in Go 1.24. Both names are inside root and
	// their folder was checked by ensureDirs; rename never follows a
	// symbolic link at the target, it replaces the link itself.
	return os.Rename(filepath.Join(root.Name(), filepath.FromSlash(from)), filepath.Join(root.Name(), filepath.FromSlash(to)))
}

// writeAnnotationFile replaces the file at rel with data: it writes a
// hidden temporary file in the same folder and renames it over the target,
// so readers (and a crash) never see half a file. The permissions of an
// existing file are kept.
func writeAnnotationFile(root *os.Root, rel string, data []byte) error {
	tmp, err := writeTempAnnotation(root, rel, data)
	if err != nil {
		return err
	}
	if err := renameInRoot(root, tmp, rel); err != nil {
		_ = root.Remove(filepath.FromSlash(tmp))
		return fmt.Errorf("write annotation: %w", err)
	}
	return nil
}

// errAnnotationDiskFull reports a save that failed because the disk is full.
var errAnnotationDiskFull = &Error{Kind: ErrInsufficientStorage, Message: "磁盘空间已满，标注未能保存；请清理磁盘空间后重试"}

func diskFullAnnotation(err error) error {
	if errors.Is(diskFull(err), errDiskFull) {
		return errAnnotationDiskFull
	}
	return err
}
