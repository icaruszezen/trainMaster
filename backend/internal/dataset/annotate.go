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
	"os"
	"path"
	"path/filepath"
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

// readVersionedAnnotation reads and parses the VOC file at rel. The
// version (and the content) is returned whenever the file could be read,
// also when it cannot be parsed (so a broken file can be replaced on
// purpose).
func readVersionedAnnotation(root *os.Root, rel string) (a *vocAnnotation, version string, data []byte, err error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, "", nil, err
	}
	defer f.Close()
	// One byte more than parseVOC reads, so an oversized file still fails
	// to parse and gets a version different from its first 16 MiB.
	data, err = io.ReadAll(io.LimitReader(f, maxAnnotationBytes+1))
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
	// unpaired target is where a new file would go; it may already exist
	// (written by another tool after the last scan).
	Paired bool
	// Owner is set when the target cannot be used for this image: another
	// image already uses the file. It names that image.
	Owner string
}

// resolveAnnotationTarget finds the VOC file of img: the one the index
// paired with it, otherwise <stem>.xml next to it, or, for an image in a
// Pascal VOC "JPEGImages" (or "images") folder with a sibling
// "Annotations" folder, <stem>.xml in there, which is where a scan would
// look for it (see pairAnnotations).
func resolveAnnotationTarget(ctx context.Context, q querier, root *os.Root, img Image) (annotationTarget, error) {
	if img.AnnotationPath != nil {
		return annotationTarget{Path: *img.AnnotationPath, Paired: true}, nil
	}
	t := annotationTarget{Path: defaultAnnotationPath(root, img.Path)}
	// Another image may own that file: a.jpg and a.png both want a.xml.
	err := q.QueryRow(ctx, `SELECT rel_path FROM dataset_images
		WHERE dataset_id = $1 AND id <> $2 AND lower(annotation_path) = lower($3)
		ORDER BY `+ImageOrder+` LIMIT 1`, img.DatasetID, img.ID, t.Path).Scan(&t.Owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return t, err
	}
	return t, nil
}

// defaultAnnotationPath is where a new VOC file for the image at rel goes.
func defaultAnnotationPath(root *os.Root, rel string) string {
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if dir != "" {
		parent, name := path.Split(strings.TrimSuffix(dir, "/"))
		if d := strings.ToLower(name); d == "jpegimages" || d == "images" {
			if ann := siblingAnnotationsDir(root, parent); ann != "" {
				return path.Join(parent, ann, stem+".xml")
			}
		}
	}
	return dir + stem + ".xml"
}

// siblingAnnotationsDir returns the name of the real folder called
// "Annotations" (any case) in the folder parent ("" for the top level), or
// "" if there is none.
func siblingAnnotationsDir(root *os.Root, parent string) string {
	dir := strings.TrimSuffix(parent, "/")
	if dir == "" {
		dir = "."
	}
	f, err := root.Open(filepath.FromSlash(dir))
	if err != nil {
		return ""
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		// DirEntry types do not follow symbolic links.
		if e.IsDir() && strings.EqualFold(e.Name(), "Annotations") && (found == "" || e.Name() < found) {
			found = e.Name()
		}
	}
	return found
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
	switch {
	case errors.Is(err, fs.ErrNotExist):
		detail.AnnotationState = annotationNone
		if t.Paired { // a VOC file that is gone keeps its path, so the viewer can say which
			detail.AnnotationError = "标注文件已不存在，请重新扫描"
		}
		return detail, nil
	case version == "": // could not be read at all
		detail.AnnotationState, detail.AnnotationError = annotationInvalid, annotationErrorMessage(err)
	case err == nil && !t.Paired && a.Filename != "" && !strings.EqualFold(a.Filename, img.FileName):
		// A file written after the last scan that names another image
		// (a.xml of a.png next to a.jpg) is not this image's annotation.
		if other, err := s.imageAt(ctx, img.DatasetID, path.Join(path.Dir(img.Path), a.Filename)); err != nil {
			return detail, err
		} else if other != "" {
			detail.AnnotationState = annotationNone
			return detail, nil
		}
		fallthrough
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
	default:
		detail.AnnotationState, detail.AnnotationError = annotationInvalid, annotationErrorMessage(err)
	}
	p := t.Path
	detail.AnnotationPath = &p
	detail.AnnotationVersion = version
	return detail, nil
}

// imageAt returns the indexed path that equals rel ignoring case, or "".
func (s *Service) imageAt(ctx context.Context, datasetID int64, rel string) (string, error) {
	var found string
	err := s.db.QueryRow(ctx, `SELECT rel_path FROM dataset_images WHERE dataset_id = $1 AND lower(rel_path) = lower($2)
		ORDER BY rel_path = $2 DESC, `+ImageOrder+` LIMIT 1`, datasetID, rel).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return found, err
}

// normalizeObjects validates the boxes of an annotation to save. Labels
// are trimmed; coordinates are clamped to the image (when its size is
// known) and must then span at least one pixel.
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
// "empty" annotation (a negative sample). It returns the image as it is
// now.
func (s *Service) SaveAnnotation(ctx context.Context, datasetID, imageID int64, in SaveAnnotationInput) (ImageDetail, error) {
	return s.editAnnotation(ctx, datasetID, imageID, in.BaseVersion, in.Force, func(e *annotationEdit) error {
		info := stagedImageInfo(e.root, e.img.Path)
		if info.Width == 0 && e.img.Width != nil && e.img.Height != nil {
			info.Width, info.Height = *e.img.Width, *e.img.Height
		}
		if info.Width == 0 && e.current != nil && e.current.Width > 0 && e.current.Height > 0 {
			info.Width, info.Height = e.current.Width, e.current.Height
		}
		objects, err := normalizeObjects(in.Objects, info.Width, info.Height)
		if err != nil {
			return err
		}
		// Edit the existing file in place (keeping what other tools wrote,
		// see patchVOC); write a new one when there is none or it cannot
		// be parsed.
		var data []byte
		if e.data != nil && e.current != nil {
			data, err = patchVOC(e.data, info, objects)
		}
		if data == nil {
			var b bytes.Buffer
			if err := writeVOC(&b, path.Base(path.Dir(e.img.Path)), e.img.FileName, info, objects); err != nil {
				return err
			}
			data = b.Bytes()
		}
		if err := writeAnnotationFile(e.root, e.target.Path, data); err != nil {
			return err
		}
		fi, err := e.root.Lstat(filepath.FromSlash(e.target.Path))
		if err != nil {
			return fmt.Errorf("stat saved annotation: %w", err)
		}
		labels := map[string]int{}
		for _, o := range objects {
			labels[o.Name]++
		}
		state := annotationEmpty
		if len(objects) > 0 {
			state = annotationAnnotated
		}
		return e.updateIndex(&e.target.Path, dbTime(fi.ModTime()), fi.Size(), state, len(objects), labels)
	})
}

// DeleteAnnotation removes the VOC file of an image, which becomes
// unannotated. baseVersion and force work as in SaveAnnotation.
func (s *Service) DeleteAnnotation(ctx context.Context, datasetID, imageID int64, baseVersion string, force bool) (ImageDetail, error) {
	return s.editAnnotation(ctx, datasetID, imageID, baseVersion, force, func(e *annotationEdit) error {
		if e.exists {
			if err := e.root.Remove(filepath.FromSlash(e.target.Path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove annotation: %w", err)
			}
		}
		return e.updateIndex(nil, time.Time{}, 0, annotationNone, 0, map[string]int{})
	})
}

// annotationEdit is the state an annotation change works on.
type annotationEdit struct {
	ctx     context.Context
	tx      pgx.Tx
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

// updateIndex records the image's new annotation in its index row and
// refreshes the dataset counters. annPath nil means no file.
func (e *annotationEdit) updateIndex(annPath *string, modTime time.Time, size int64, state string, objects int, labels map[string]int) error {
	var mod *time.Time
	var sz *int64
	if annPath != nil {
		mod, sz = &modTime, &size
	}
	tag, err := e.tx.Exec(e.ctx, `UPDATE dataset_images SET annotation_path = $3, annotation_mod_time = $4,
			annotation_size = $5, annotation_state = $6, object_count = $7, label_counts = $8, updated_at = now()
		WHERE dataset_id = $1 AND id = $2`, e.img.DatasetID, e.img.ID, annPath, mod, sz, state, objects, labels)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errImageNotFound // removed by a scan meanwhile
	}
	return recomputeAggregates(e.ctx, e.tx, e.img.DatasetID, false)
}

// editAnnotation runs change on the annotation of an image after checking
// that the file is still at baseVersion. Changes of one image are
// serialized, so the check and the write cannot interleave with another
// save in this process; the index update commits only if change succeeds.
func (s *Service) editAnnotation(ctx context.Context, datasetID, imageID int64, baseVersion string, force bool,
	change func(e *annotationEdit) error) (ImageDetail, error) {
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
		e := &annotationEdit{ctx: ctx, tx: tx, root: root, img: img, target: t}
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
		return change(e)
	})
	if err != nil {
		return ImageDetail{}, diskFullAnnotation(err)
	}
	img, err := queryImage(ctx, s.db, datasetID, imageID)
	if err != nil {
		return ImageDetail{}, err
	}
	return s.imageDetail(ctx, d, img)
}

// writeAnnotationFile replaces the file at rel with data: it writes a
// hidden temporary file in the same folder and renames it over the target,
// so readers (and a crash) never see half a file. The permissions of an
// existing file are kept.
func writeAnnotationFile(root *os.Root, rel string, data []byte) error {
	dir := path.Dir(rel)
	if err := ensureDirs(root, dir); err != nil {
		if errors.Is(err, errUnsafeTarget) {
			return newError(ErrConflict, "标注文件所在的文件夹 %s 不是普通文件夹（可能是符号链接），无法保存", dir)
		}
		return err
	}
	perm := fs.FileMode(0o644)
	if fi, err := root.Lstat(filepath.FromSlash(rel)); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}
	tmp := path.Join(dir, ".tm-annotation-"+rand.Text()[:12]+".tmp")
	f, err := root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("create annotation: %w", err)
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
	if err == nil {
		// os.Root has no Rename in Go 1.24. Both names are inside root and
		// their folder was checked by ensureDirs; rename never follows a
		// symbolic link at the target, it replaces the link itself.
		err = os.Rename(filepath.Join(root.Name(), filepath.FromSlash(tmp)), filepath.Join(root.Name(), filepath.FromSlash(rel)))
	}
	if err != nil {
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
