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
	// StaleOwner is set when the index pairs the file with Owner but a scan
	// would no longer do so (see stalePairing): a rescan settles who the
	// file belongs to.
	StaleOwner bool
}

// resolveAnnotationTarget finds the VOC file of img: the file a scan would
// pair with it now (see annotationCandidate), which another tool may have
// written since the last scan, or else where a new file goes (see
// defaultAnnotationPath). Showing, saving and the next scan thus agree on
// the file.
//
// For an image the index paired with a file, only the candidate a scan
// tries first is looked for: <stem>.xml next to the image (LabelImg writes
// there by default) takes over from a paired file elsewhere, such as in
// Annotations/. That costs one stat, so viewing stays cheap; an edit looks
// for the other candidates, and for other images claiming the paired file,
// too (see checkPairedTarget).
//
// The file may belong to another image: the index pairs it with another
// image, or a scan would give it to another image that claims it too (a.jpg
// and a.png both want a.xml, see claimOwner). Owner then names that image,
// and the annotation of img can neither be shown nor saved. A new file is
// put where a scan gives it to img (see defaultAnnotationPath), except in
// one layout where there is no such place (see annotationsFolderRival).
func resolveAnnotationTarget(ctx context.Context, q querier, root *os.Root, img Image) (annotationTarget, error) {
	var t annotationTarget
	found := false
	if img.AnnotationPath != nil {
		t = annotationTarget{Path: *img.AnnotationPath, Paired: true}
		exact := exactAnnotationPath(img.Path)
		if path.Dir(t.Path) == path.Dir(img.Path) || !isRegularFile(root, exact) {
			return t, nil
		}
		t, found = annotationTarget{Path: exact}, true
	} else {
		t.Path = annotationCandidate(root, img.Path)
		found = t.Path != ""
		if !found {
			t.Path = defaultAnnotationPath(root, img.Path)
		}
	}
	var err error
	if t.Owner, t.StaleOwner, err = indexedOwner(ctx, q, root, img, t.Path); err != nil {
		return t, err
	}
	switch {
	case t.Owner != "":
	case found:
		if t.Owner, err = claimOwner(ctx, q, root, img, t.Path, ""); err != nil {
			return t, err
		}
	default:
		t.Owner = annotationsFolderRival(root, img, t.Path)
	}
	return t, nil
}

// indexedOwner returns the image other than img that the index pairs with
// the VOC file rel, "" if there is none, and whether that pairing is out of
// date (see stalePairing). A path that differs in case only counts when it
// names the same file (on a file system that ignores case), not another
// file such as Annotations/a.xml for annotations/a.xml.
func indexedOwner(ctx context.Context, q querier, root *os.Root, img Image, rel string) (owner string, stale bool, err error) {
	rows, err := q.Query(ctx, `SELECT rel_path, annotation_path FROM dataset_images
		WHERE dataset_id = $1 AND id <> $2 AND lower(annotation_path) = lower($3)
		ORDER BY `+ImageOrder, img.DatasetID, img.ID, rel)
	if err != nil {
		return "", false, err
	}
	type pairing struct{ image, file string }
	pairings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pairing, error) {
		var p pairing
		err := row.Scan(&p.image, &p.file)
		return p, err
	})
	if err != nil {
		return "", false, err
	}
	for _, p := range pairings {
		if p.file == rel || sameFile(root, p.file, rel) {
			return p.image, stalePairing(root, img, p.image, p.file, rel), nil
		}
	}
	return "", false, nil
}

// stalePairing reports whether a scan would no longer pair the image owner
// with its file ownerPath in the index, which is rel (or the same file in
// another case), a candidate of img or where a new file of img goes: the
// image or the file is gone, a file next to the image takes over from a
// file elsewhere (see resolveAnnotationTarget), or the <filename> recorded
// in rel names img and not owner (see claimOwner). It takes a few stats and
// one read, and only runs for an image that another one blocks.
func stalePairing(root *os.Root, img Image, owner, ownerPath, rel string) bool {
	if !isRegularFile(root, owner) || !isRegularFile(root, ownerPath) {
		return true
	}
	if path.Dir(ownerPath) != path.Dir(owner) && isRegularFile(root, exactAnnotationPath(owner)) {
		return true
	}
	a, _, _, err := readVersionedAnnotation(root, rel)
	return err == nil && a.Filename == path.Base(img.Path) && a.Filename != path.Base(owner)
}

// checkPairedTarget makes sure that an edit of the paired image img goes to
// the file the next scan pairs with it: it looks for all the candidates of
// a scan and for other images claiming the file (resolveAnnotationTarget,
// for viewing, only looks for the first candidate). When a scan would pick
// another file, for example one written in another case or in another
// "Annotations" folder, or one found instead of a paired file that is gone,
// or would give the file to another image (another tool changed its
// <filename>, or that image lost a file of its own), the edit is refused
// until a rescan. A paired file that is gone, with no other file found, is
// written anew (create) where a new file of an image without one goes.
func checkPairedTarget(ctx context.Context, q querier, root *os.Root, img Image, t annotationTarget, create bool) (annotationTarget, error) {
	if !t.Paired {
		return t, nil // t is what a scan would pick (see resolveAnnotationTarget)
	}
	c := annotationCandidate(root, img.Path)
	switch {
	case c == "" && create: // a rescan would leave the image without a file
		unpaired := img
		unpaired.AnnotationPath = nil
		return resolveAnnotationTarget(ctx, q, root, unpaired)
	case c == "":
		return t, nil // nothing to remove
	case !sameFile(root, c, t.Path):
		return t, newError(ErrConflict, "数据集文件夹中出现了这张图片的另一个标注文件 %s，请先重新扫描数据集，再编辑这张图片的标注", c)
	}
	owner, err := claimOwner(ctx, q, root, img, t.Path, "")
	if err != nil {
		return t, err
	}
	if owner != "" {
		return t, newError(ErrConflict, "标注文件 %s 按 VOC 的配对规则现在属于%s %s（上次扫描之后文件夹中的文件有变化），"+
			"请先重新扫描数据集，再编辑这张图片的标注", t.Path, otherImage(img, owner), owner)
	}
	return t, nil
}

// sameFile reports whether the paths a and b name the same file (on a file
// system that ignores case, a.xml and A.XML do).
func sameFile(root *os.Root, a, b string) bool {
	if a == b {
		return true
	}
	fa, err := root.Lstat(filepath.FromSlash(a))
	if err != nil {
		return false
	}
	fb, err := root.Lstat(filepath.FromSlash(b))
	return err == nil && os.SameFile(fa, fb)
}

// claimOwner decides, like pairAnnotations, which image a scan gives the
// VOC file rel, a candidate of img, when other indexed images claim it too:
// a.jpg and a.png both take a.xml, and JPEGImages/a.jpg, images/a.jpg and
// even an image Annotations/a.jpg all take Annotations/a.xml. The
// <filename> recorded in the file decides; without a match, the first image
// in byte order of the path gets it. The file gone ("" for none) counts as
// removed. It returns that image, or "" if it is img.
func claimOwner(ctx context.Context, q querier, root *os.Root, img Image, rel, gone string) (string, error) {
	others, err := possibleClaimants(ctx, q, root, img, rel)
	if err != nil {
		return "", err
	}
	claims := []string{img.Path}
	for _, other := range others {
		if annotationCandidateWithout(root, other.Path, gone) == rel {
			claims = append(claims, other.Path)
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

// possibleClaimants returns the indexed images other than img that are
// still there (a scan only sees those) and may take the VOC file rel as
// their candidate (see annotationCandidate): the images of its stem
// (ignoring case) in its folder and, for a file in an "Annotations" folder,
// in the VOC image folders next to it.
func possibleClaimants(ctx context.Context, q querier, root *os.Root, img Image, rel string) ([]Image, error) {
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	patterns := []string{likePrefix(dir+stem) + ".%"}
	if parent, name := path.Split(strings.TrimSuffix(dir, "/")); dir != "" && strings.ToLower(name) == "annotations" {
		patterns = append(patterns, likePrefix(parent+"jpegimages/"+stem)+".%", likePrefix(parent+"images/"+stem)+".%")
	}
	rows, err := q.Query(ctx, `SELECT id, rel_path FROM dataset_images WHERE dataset_id = $1 AND id <> $2
		AND lower(rel_path) LIKE ANY (SELECT lower(p) FROM unnest($3::text[]) AS p)`, img.DatasetID, img.ID, patterns)
	if err != nil {
		return nil, err
	}
	others, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Image, error) {
		other := Image{DatasetID: img.DatasetID}
		err := row.Scan(&other.ID, &other.Path)
		return other, err
	})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(others, func(other Image) bool { return !isRegularFile(root, other.Path) }), nil
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
	return annotationCandidateWithout(root, rel, "")
}

// annotationCandidateWithout is annotationCandidate as if the file gone
// ("" for none) had been removed.
func annotationCandidateWithout(root *os.Root, rel, gone string) string {
	ok := func(p string) bool { return isRegularFile(root, p) && (gone == "" || !sameFile(root, p, gone)) }
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if exact := exactAnnotationPath(rel); ok(exact) {
		return exact
	}
	want := strings.ToLower(stem + ".xml")
	for _, name := range namesIn(root, dir, want) {
		if ok(dir + name) {
			return dir + name
		}
	}
	parent, isVOC := vocImageFolder(dir)
	if !isVOC {
		return ""
	}
	found := ""
	for _, ann := range annotationsDirs(root, parent) {
		for _, name := range namesIn(root, parent+ann+"/", want) {
			if p := parent + ann + "/" + name; ok(p) {
				if found == "" || p < found {
					found = p
				}
				break
			}
		}
	}
	return found
}

// exactAnnotationPath is the first candidate of a scan for the image at
// rel: <stem>.xml next to it.
func exactAnnotationPath(rel string) string {
	return strings.TrimSuffix(rel, path.Ext(rel)) + ".xml"
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
// long as no other candidate exists (see annotationCandidate), and gives it
// to that image when other images claim it too, by the <filename> it
// records (see SaveAnnotation). That only fails to tell two images apart
// that have the same file name, such as JPEGImages/a.jpg and images/a.jpg
// sharing Annotations/, or JPEGImages/a.jpg and an image Annotations/a.jpg;
// which of them a scan picks would then depend on whether the other one has
// a file of its own at the time. Such an image gets <stem>.xml next to it
// instead, which no image of another folder claims. (An image in an
// "Annotations" folder itself has no other place: see
// annotationsFolderRival.)
func defaultAnnotationPath(root *os.Root, rel string) string {
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if parent, ok := vocImageFolder(dir); ok {
		if ann := siblingAnnotationsDir(root, parent); ann != "" && !hasNamesake(root, parent, rel) {
			return parent + ann + "/" + stem + ".xml"
		}
	}
	return dir + stem + ".xml"
}

// hasNamesake reports whether another VOC image folder ("JPEGImages" or
// "images", any case) or an "Annotations" folder in the folder parent holds
// an image of the same file name as the image at rel.
func hasNamesake(root *os.Root, parent, rel string) bool {
	base := path.Base(rel)
	for _, folder := range []string{"jpegimages", "images", "annotations"} {
		for _, name := range namesIn(root, parent, folder) {
			if other := parent + name + "/" + base; isRegularFile(root, other) && !sameFile(root, other, rel) {
				return true
			}
		}
	}
	return false
}

// annotationsFolderRival returns the image a scan would give the new VOC
// file rel of img (see defaultAnnotationPath) instead of img, or "" if
// there is none. That can only happen when img is itself in an
// "Annotations" folder, so rel goes next to it: an image of the same file
// name in a VOC image folder next to it ("JPEGImages" or "images", any
// case) takes rel too unless it has a file of its own that comes first (see
// annotationCandidate), the <filename> recorded in rel cannot tell the two
// apart, and the first in byte order of the path gets it (see claimOwner).
func annotationsFolderRival(root *os.Root, img Image, rel string) string {
	dir, base := path.Split(img.Path)
	parent, name := path.Split(strings.TrimSuffix(dir, "/"))
	if dir == "" || strings.ToLower(name) != "annotations" {
		return ""
	}
	rival := ""
	for _, folder := range []string{"jpegimages", "images"} {
		for _, n := range namesIn(root, parent, folder) {
			other := parent + n + "/" + base
			if other >= img.Path || (rival != "" && other >= rival) || !isRegularFile(root, other) {
				continue
			}
			// rel would come first among its candidates in the Annotations
			// folders, but not before a file in its own folder.
			if c := annotationCandidate(root, other); c == "" || (path.Dir(c) != path.Dir(other) && rel < c) {
				rival = other
			}
		}
	}
	return rival
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
		detail.AnnotationState, detail.AnnotationPath = annotationNone, nil
		detail.AnnotationBlocked = ownerConflict(img, t).Message
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
	// Regenerate allows replacing an existing file that cannot be edited
	// while keeping its other content (see patchVOC) by a new one; without
	// it such a save fails with ErrAnnotationUnpatchable. It is a consent of
	// its own: Force does not imply it, and it does not skip the version
	// check.
	Regenerate bool
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
		// new one only on purpose (Regenerate); so is a file that cannot be
		// parsed (see editAnnotation).
		if e.current != nil {
			data, err := patchVOC(e.data, info, objects)
			if err == nil {
				return data, nil
			}
			if !in.Regenerate {
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
// unannotated, or is paired with another candidate file that remains (see
// remainingAnnotation). baseVersion and force work as in SaveAnnotation.
func (s *Service) DeleteAnnotation(ctx context.Context, datasetID, imageID int64, baseVersion string, force bool) (ImageDetail, error) {
	return s.editAnnotation(ctx, datasetID, imageID, baseVersion, force, nil)
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

// ownerConflict is the error for the image img whose VOC file belongs to
// another image (see annotationTarget.Owner).
func ownerConflict(img Image, t annotationTarget) *Error {
	if t.StaleOwner {
		return newError(ErrConflict, "标注文件 %s 在索引中仍与图片 %s 配对，但该配对已过期（上次扫描之后文件夹中的文件有变化）；"+
			"请先重新扫描数据集，再编辑这张图片的标注", t.Path, t.Owner)
	}
	return newError(ErrConflict, "标注文件 %s 属于%s %s（VOC 按文件名配对），"+
		"无法为这张图片单独保存标注；请先重命名其中一张图片", t.Path, otherImage(img, t.Owner), t.Owner)
}

// otherImage names the image other in a message about img.
func otherImage(img Image, other string) string {
	if path.Dir(other) == path.Dir(img.Path) {
		return "同一文件夹中的另一张图片"
	}
	return "另一张图片"
}

// remainingAnnotation finds the VOC file a scan pairs img with once the
// file gone is removed: another candidate of img (see annotationCandidate),
// such as Annotations/a.xml after a.xml next to the image took over from
// it. The result is empty when there is none; its Owner is set when the
// file belongs to another image by the scan's rule (img is then left
// without annotation, see resolveAnnotationTarget).
//
// The other images that claim gone now and lose it to img (a.png when a.jpg
// wins a.xml, see claimOwner) each claim their own next candidate then,
// too. The removal is refused when a file the index pairs with another
// image would then go to img or to one of those images: the boxes of that
// image would move with it at the next scan.
func remainingAnnotation(ctx context.Context, q querier, root *os.Root, img Image, gone string) (annotationTarget, error) {
	others, err := possibleClaimants(ctx, q, root, img, gone)
	if err != nil {
		return annotationTarget{}, err
	}
	freed := []Image{img} // the images that claim gone now
	for _, other := range others {
		if c := annotationCandidate(root, other.Path); c != "" && sameFile(root, c, gone) {
			freed = append(freed, other)
		}
	}
	var t annotationTarget
	for i, c := range freed {
		r := annotationCandidateWithout(root, c.Path, gone)
		if i == 0 {
			t.Path = r
		} else if t.Path != "" && sameFile(root, r, t.Path) {
			continue // decided with img, which claims r too
		}
		if r == "" {
			continue
		}
		winner, err := claimOwner(ctx, q, root, c, r, gone)
		if err != nil {
			return t, err
		}
		if winner == "" {
			winner = c.Path
		}
		owner, stale, err := indexedOwner(ctx, q, root, c, r)
		if err != nil {
			return t, err
		}
		// The removal moves r away from its owner when a scan then gives it
		// to an image that claims it only because gone is removed. A winner
		// that claims r already shows an index out of date, which the
		// removal does not change.
		moved := owner != "" && owner != img.Path && owner != winner &&
			slices.ContainsFunc(freed, func(f Image) bool { return f.Path == winner })
		switch {
		case moved && stale:
			who := "这张图片"
			if winner != img.Path {
				who = otherImage(img, winner) + " " + winner + " "
			}
			return t, newError(ErrConflict, "删除 %s 后，%s会改用标注文件 %s，它在索引中仍与图片 %s 配对，但该配对已过期；"+
				"请先重新扫描数据集，再删除这张图片的标注", gone, who, r, owner)
		case moved && winner == img.Path:
			return t, newError(ErrConflict, "删除 %s 后，重新扫描会把%s %s 的标注文件 %s 配对给这张图片（VOC 按文件名配对），"+
				"那张图片的框会转到这张图片上，因此不能删除；如需清空这张图片的框，请把它标为空标注", gone, otherImage(img, owner), owner, r)
		case moved:
			return t, newError(ErrConflict, "删除 %s 后，重新扫描会把图片 %s 的标注文件 %s 改为配对给%s %s（VOC 按文件名配对），"+
				"框也会跟着转过去，因此不能删除；如需清空这张图片的框，请把它标为空标注", gone, owner, r, otherImage(img, winner), winner)
		case i == 0 && winner != img.Path:
			t.Owner, t.StaleOwner = winner, winner == owner && stale
		}
	}
	return t, nil
}

// indexAnnotation records in the index row of img that it is paired with
// the VOC file rel (fi describes it), read as a or failing with err, as a
// scan does (see indexImage), and refreshes the dataset counters.
func indexAnnotation(ctx context.Context, tx pgx.Tx, img Image, rel string, fi fs.FileInfo, a *vocAnnotation, err error) error {
	state, objects, labels := annotationInvalid, 0, map[string]int{}
	if err == nil {
		state, objects, labels = annotationEmpty, len(a.Objects), a.labelCounts()
		if objects > 0 {
			state = annotationAnnotated
		}
	}
	return updateAnnotationIndex(ctx, tx, img, &rel, dbTime(fi.ModTime()), fi.Size(), state, objects, labels)
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
// change returns (a nil change removes the file) after checking that the
// file is still at baseVersion, and updates the index and the dataset
// counters. Changes of one image are serialized, so the check and the
// write cannot interleave with another save in this process.
//
// The database is updated first and the file is put in place as the last
// step before the commit; when the commit fails, the previous file is put
// back, so the file and the index stay in step. The result describes the
// file this request wrote, so its version is the one the next edit starts
// from even if the file changes again meanwhile. After a delete it
// describes the file a scan pairs the image with now (see
// remainingAnnotation), usually none.
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
		if t, err = checkPairedTarget(ctx, tx, root, img, t, change != nil); err != nil {
			return err
		}
		if t.Owner != "" {
			return ownerConflict(img, t)
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
		var data []byte
		var rest annotationTarget // after a delete: the file a scan pairs img with then
		if change != nil {
			data, err = change(e)
		} else {
			rest, err = remainingAnnotation(ctx, tx, root, img, t.Path)
		}
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
			if err := indexAnnotation(ctx, tx, img, t.Path, fi, written, nil); err != nil {
				return err
			}
		}
		// After a delete, a remaining candidate that is free becomes the file
		// of img, as with a scan (see remainingAnnotation).
		var restInfo fs.FileInfo // nil: no such file
		var restRead *vocAnnotation
		var restVersion string
		var restErr error
		if data == nil && rest.Path != "" && rest.Owner == "" {
			if fi, err := root.Lstat(filepath.FromSlash(rest.Path)); err == nil {
				restRead, restVersion, _, restErr = readVersionedAnnotation(root, rest.Path)
				if !errors.Is(restErr, fs.ErrNotExist) { // not removed meanwhile
					restInfo = fi
				}
			}
		}
		switch {
		case data != nil: // indexed above
		case restInfo != nil:
			if err := indexAnnotation(ctx, tx, img, rest.Path, restInfo, restRead, restErr); err != nil {
				return err
			}
		default:
			if err := updateAnnotationIndex(ctx, tx, img, nil, time.Time{}, 0, annotationNone, 0, map[string]int{}); err != nil {
				return err
			}
		}
		if img, err = queryImage(ctx, tx, datasetID, imageID); err != nil {
			return err
		}
		detail = ImageDetail{Image: img}
		switch {
		case data != nil:
			describeAnnotation(&detail, annotationTarget{Path: t.Path, Paired: true}, written, annotationVersion(data), nil)
		case restInfo != nil:
			describeAnnotation(&detail, annotationTarget{Path: rest.Path, Paired: true}, restRead, restVersion, restErr)
		default:
			describeAnnotation(&detail, annotationTarget{}, nil, "", fs.ErrNotExist)
			if rest.Owner != "" {
				detail.AnnotationBlocked = ownerConflict(img, rest).Message
			}
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
