package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/flate"
)

// Dataset downloads: the images and their annotations as a zip archive in
// VOC, X-AnyLabeling or YOLO format. The archive is generated while it is
// sent, so a download needs no disk space and starts at once. A download is
// prepared first (CreateExport validates the request and returns a token),
// then fetched with a plain GET that browsers save natively, however large
// the dataset.

// exportTTL is how long a prepared download can be fetched (repeatedly:
// browsers and download managers may retry).
const exportTTL = time.Hour

// xAnyLabelingVersion is the X-AnyLabeling release whose file format the
// exported JSON files follow.
const xAnyLabelingVersion = "4.2.3"

var errExportExpired = &Error{Kind: ErrNotFound, Message: "下载链接已失效，请重新下载"}

// ExportInput selects the format of a download.
type ExportInput struct {
	Format string // Format*
	// YOLO: the class names by id, the class list of the project the
	// dataset is exported for (see project.Service.ExportClasses) or, for
	// a dataset in no project, an order given by hand.
	Classes []string
	// Versions selects the versions the download combines (see
	// VersionSelection); nil is the base version alone.
	Versions *VersionSelection
}

// ExportTicket is a prepared download.
type ExportTicket struct {
	Token     string
	FileName  string
	ExpiresAt time.Time
}

type exportRequest struct {
	datasetID int64
	format    string
	classes   []string
	// The selection: the base, and the review versions newest first.
	includeBase bool
	versionIDs  []int64
	top         string // the folder in the archive
	fileName    string
	expires     time.Time
}

// exportTickets holds the prepared downloads in memory (the service runs as
// a single instance).
type exportTickets struct {
	mu sync.Mutex
	m  map[string]exportRequest
}

func (t *exportTickets) put(token string, r exportRequest, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]exportRequest{}
	}
	for k, old := range t.m {
		if now.After(old.expires) {
			delete(t.m, k)
		}
	}
	t.m[token] = r
}

func (t *exportTickets) get(token string, now time.Time) (exportRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.m[token]
	return r, ok && !now.After(r.expires)
}

// CreateExport prepares the download of a dataset in the given format, of
// the versions in.Versions.
func (s *Service) CreateExport(ctx context.Context, datasetID int64, in ExportInput) (ExportTicket, error) {
	format, classes, err := normalizeFormat(in.Format, in.Classes, "导出")
	if err != nil {
		return ExportTicket{}, err
	}
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return ExportTicket{}, err
	}
	sv := selectedVersions{includeBase: true}
	if in.Versions != nil {
		if sv, err = resolveSelection(ctx, s.db, d.ID, *in.Versions); err != nil {
			return ExportTicket{}, err
		}
	}
	if !s.dirExists(d.DirName) {
		return ExportTicket{}, errDatasetDirMissing(s.datasetDir(d.DirName))
	}
	if d.ImageCount == 0 {
		return ExportTicket{}, &Error{Kind: ErrConflict, Message: "数据集中没有图片，无法下载"}
	}
	top := d.DirName
	if !sv.baseOnly() {
		var n int
		if err := s.db.QueryRow(ctx, "WITH "+layerSQL+", "+composedSQL+" SELECT count(*) FROM composed",
			d.ID, sv.ids(), sv.includeBase).Scan(&n); err != nil {
			return ExportTicket{}, err
		}
		if n == 0 {
			return ExportTicket{}, &Error{Kind: ErrConflict, Message: "所选版本中没有可以导出的图片：不选基础版本时只导出审图中标为需加样的图片，" +
				"这些版本中没有这样的图片，或它们已不在数据集中"}
		}
		top = exportTop(d.DirName, sv)
	}
	now := time.Now()
	r := exportRequest{datasetID: d.ID, format: format, classes: classes, includeBase: sv.includeBase,
		versionIDs: sv.ids(), top: top, fileName: top + "-" + format + ".zip", expires: now.Add(exportTTL)}
	token := rand.Text()
	s.exports.put(token, r, now)
	return ExportTicket{Token: token, FileName: r.fileName, ExpiresAt: r.expires.UTC()}, nil
}

// maxExportTopBytes bounds the folder name in the archive of a download
// of versions (and so its file name) well below the usual 255-byte limit.
const maxExportTopBytes = 200

// exportTop is the folder name in the archive of a download of versions:
// the dataset's folder name and the versions, shortened to
// maxExportTopBytes (the versions first, then the folder name).
func exportTop(dirName string, sv selectedVersions) string {
	label := sv.label()
	if len(dirName)+1+len(label) > maxExportTopBytes {
		label = sv.shortLabel()
	}
	if over := len(dirName) + 1 + len(label) - maxExportTopBytes; over > 0 {
		n := len(dirName) - over
		for n > 0 && !utf8.RuneStart(dirName[n]) {
			n--
		}
		dirName = dirName[:n]
	}
	return dirName + "-" + label
}

// Export is an opened download. The caller writes it with Write and then
// closes it.
type Export struct {
	FileName string

	db      querier
	root    *os.Root
	top     string // the folder everything is in: the dataset's folder name (and the versions)
	format  string
	classes []string
	images  []exportImage
	log     *slog.Logger

	// subset: the download has only some of the dataset's images (versions
	// without the base); allInImages: all the dataset's images are in
	// images/ (YOLO).
	subset, allInImages bool

	entries map[string]bool // archive entry names written so far
	// reserved are the annotation entry names of the images that keep
	// their base annotation (see reserve).
	reserved map[string]bool
	// loaded holds the annotations of the review version images of the
	// current chunk, by index in images (see versionAnnotation).
	loaded map[int]*vocAnnotation

	// Counts for the log.
	written, missing, annotations, fromVersions, nameTaken int
}

type exportImage struct {
	Path    string
	ModTime time.Time
	Width   *int
	Height  *int
	AnnPath *string // the base VOC file
	// VersionID is the review version whose annotation replaces the base
	// one (nil: the base).
	VersionID *int64
}

// versionChunk is how many version annotations an export loads at a time.
const versionChunk = 500

// OpenExport opens a download prepared by CreateExport for the dataset.
// The selected versions must still exist.
func (s *Service) OpenExport(ctx context.Context, datasetID int64, token string) (*Export, error) {
	r, ok := s.exports.get(token, time.Now())
	if !ok || r.datasetID != datasetID {
		return nil, errExportExpired
	}
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return nil, err
	}
	// The selection and the images in one snapshot: a version deleted
	// after it fails the download while it is written (versionAnnotation).
	var images []exportImage
	var allInImages bool
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			if len(r.versionIDs) > 0 {
				_, err := resolveSelection(ctx, tx, d.ID, VersionSelection{IncludeBase: r.includeBase, VersionIDs: r.versionIDs})
				if errors.Is(err, ErrNotFound) {
					return &Error{Kind: ErrNotFound, Message: "所选的版本已被删除，请重新下载"}
				}
				if err != nil {
					return err
				}
			}
			// The archive lists the images in image order, which also decides
			// which of two files with the same entry name is kept.
			rows, err := tx.Query(ctx, "WITH "+layerSQL+", "+composedSQL+`
				SELECT rel_path, mod_time, width, height, annotation_path, version_id
				FROM composed ORDER BY sort_key, id`, d.ID, r.versionIDs, r.includeBase)
			if err != nil {
				return err
			}
			if images, err = pgx.CollectRows(rows, pgx.RowToStructByPos[exportImage]); err != nil {
				return fmt.Errorf("load images: %w", err)
			}
			return tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM dataset_images
				WHERE dataset_id = $1 AND NOT starts_with(rel_path, 'images/'))`, d.ID).Scan(&allInImages)
		})
	if err != nil {
		return nil, err
	}
	if len(images) == 0 && !r.includeBase {
		return nil, &Error{Kind: ErrConflict, Message: "所选版本中已没有可以导出的图片，请重新下载"}
	}
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return nil, err
	}
	log := s.log.With("dataset_id", d.ID, "format", r.format)
	if len(r.versionIDs) > 0 {
		log = log.With("versions", r.versionIDs, "base", r.includeBase)
	}
	return &Export{FileName: r.fileName, db: s.db, root: root, top: r.top, format: r.format, classes: r.classes,
		images: images, log: log, subset: !r.includeBase, allInImages: allInImages,
		entries: map[string]bool{}, loaded: map[int]*vocAnnotation{}}, nil
}

// Close releases the dataset folder.
func (e *Export) Close() error { return e.root.Close() }

// Write sends the zip archive to w. Images that have disappeared since the
// last scan are left out; an error means the archive is incomplete.
func (e *Export) Write(ctx context.Context, w io.Writer) error {
	start := time.Now()
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, newFlateWriter)
	var err error
	switch e.format {
	case FormatYOLO:
		err = e.writeYOLO(ctx, zw)
	case FormatXAnyLabeling:
		err = e.writeXAnyLabeling(ctx, zw)
	default:
		err = e.writeVOC(ctx, zw)
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return err
	}
	e.log.Info("dataset exported", "images", e.written, "images_missing", e.missing,
		"annotations", e.annotations, "from_versions", e.fromVersions, "annotation_names_taken", e.nameTaken,
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

// flateWriters reuses Deflate compressors across entries and downloads: a
// new one allocates about 800 KB, more than most label files.
var flateWriters sync.Pool

// pooledFlate returns its compressor to flateWriters when the entry is done.
type pooledFlate struct{ *flate.Writer }

func (p pooledFlate) Close() error {
	err := p.Writer.Close()
	p.Writer.Reset(io.Discard) // do not keep the response alive
	flateWriters.Put(p.Writer)
	return err
}

func newFlateWriter(out io.Writer) (io.WriteCloser, error) {
	if fw, ok := flateWriters.Get().(*flate.Writer); ok {
		fw.Reset(out)
		return pooledFlate{fw}, nil
	}
	fw, err := flate.NewWriter(out, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	return pooledFlate{fw}, nil
}

// storedExtensions are image formats that are compressed already; deflating
// them again costs CPU for nothing. BMP and TIFF (often uncompressed) are
// deflated.
var storedExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
}

// copyFile adds the dataset file rel to the archive as name. ok is false
// (and nothing is written) when the file cannot be opened, e.g. it was
// deleted since the last scan, or an earlier file took the name.
func (e *Export) copyFile(zw *zip.Writer, rel, name string) (ok bool, err error) {
	f, err := e.root.Open(filepath.FromSlash(rel))
	if err != nil {
		e.log.Warn("export: skipping file that cannot be opened", "path", rel, "err", err)
		return false, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		e.log.Warn("export: skipping file that is not a regular file", "path", rel, "err", err)
		return false, nil
	}
	method := zip.Deflate
	if storedExtensions[strings.ToLower(path.Ext(rel))] {
		method = zip.Store
	}
	h, ok := e.header(name, method, fi.ModTime())
	if !ok {
		return false, nil
	}
	out, err := zw.CreateHeader(h)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, f); err != nil {
		return false, err
	}
	return true, nil
}

// addFile adds generated content to the archive; ok is false (and nothing
// is written) when an earlier file took the name.
func (e *Export) addFile(zw *zip.Writer, name string, data []byte, modTime time.Time) (ok bool, err error) {
	h, ok := e.header(name, zip.Deflate, modTime)
	if !ok {
		return false, nil
	}
	out, err := zw.CreateHeader(h)
	if err != nil {
		return false, err
	}
	_, err = out.Write(data)
	return err == nil, err
}

// header returns the header of the entry for name, or ok false when an
// earlier entry has the same name: extracting the archive would overwrite
// one file with the other.
func (e *Export) header(name string, method uint16, modTime time.Time) (h *zip.FileHeader, ok bool) {
	// Windows tools treat "\" in entry names as a folder separator, so a
	// file named "..\x.png" on the server could be unpacked outside the
	// target folder. Uploads never create such names.
	name = e.top + "/" + strings.ReplaceAll(name, `\`, "_")
	if e.entries[name] {
		e.log.Warn("export: archive name taken by an earlier file; leaving this one out", "name", name)
		return nil, false
	}
	e.entries[name] = true
	h = &zip.FileHeader{Name: name, Method: method, Modified: modTime}
	h.SetMode(0o644)
	return h, true
}

// copyImage adds an image under name; ok is false when it is missing or
// an earlier file took the name.
func (e *Export) copyImage(zw *zip.Writer, img exportImage, name string) (bool, error) {
	ok, err := e.copyFile(zw, img.Path, name)
	if ok {
		e.written++
	} else if err == nil {
		e.missing++
	}
	return ok, err
}

// annotation returns the annotation of the image at index i: its review
// version's, else its VOC file as it is on disk now; nil when it has none
// or the file cannot be used.
func (e *Export) annotation(ctx context.Context, i int) (*vocAnnotation, error) {
	img := e.images[i]
	if img.VersionID != nil {
		return e.versionAnnotation(ctx, i)
	}
	if img.AnnPath == nil {
		return nil, nil
	}
	a, err := newAnnotationCache(e.root).get(*img.AnnPath)
	if err != nil {
		e.log.Info("export: annotation not usable", "path", *img.AnnPath, "err", err)
		return nil, nil
	}
	return a, nil
}

// versionAnnotation returns the review version annotation of the image at
// index i. They are loaded a chunk at a time, so that a large download
// does not hold them all in memory. A version deleted during the download
// fails it: leaving the image with the wrong annotation would go unnoticed.
func (e *Export) versionAnnotation(ctx context.Context, i int) (*vocAnnotation, error) {
	if a, ok := e.loaded[i]; ok {
		return a, nil
	}
	clear(e.loaded)
	var indexes []int32
	var versionIDs []int64
	var paths []string
	for j := i; j < len(e.images) && len(indexes) < versionChunk; j++ {
		if v := e.images[j].VersionID; v != nil {
			indexes = append(indexes, int32(j))
			versionIDs = append(versionIDs, *v)
			paths = append(paths, e.images[j].Path)
		}
	}
	rows, err := e.db.Query(ctx, `SELECT k.i, vi.objects, vi.width, vi.height
		FROM unnest($1::int[], $2::bigint[], $3::text[]) AS k (i, v, p)
		JOIN dataset_version_images vi ON vi.version_id = k.v AND vi.rel_path = k.p
		JOIN dataset_versions dv ON dv.id = vi.version_id AND dv.deleted_at IS NULL`, indexes, versionIDs, paths)
	if err != nil {
		return nil, fmt.Errorf("load version annotations: %w", err)
	}
	var j int32
	var objects []AnnotationObject
	var w, h *int
	if _, err := pgx.ForEachRow(rows, []any{&j, &objects, &w, &h}, func() error {
		a := &vocAnnotation{Filename: path.Base(e.images[j].Path), Objects: objects}
		if w != nil && h != nil {
			a.Width, a.Height = *w, *h
		}
		e.loaded[int(j)] = a
		objects = nil
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load version annotations: %w", err)
	}
	if a, ok := e.loaded[i]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("the annotation of %s in version %d is gone (the version was deleted during the download)",
		e.images[i].Path, *e.images[i].VersionID)
}

// imageSize is the size of an image from the index, else from its VOC file
// (0 when unknown).
func imageSize(img exportImage, a *vocAnnotation) (int, int) {
	if img.Width != nil && img.Height != nil && *img.Width > 0 && *img.Height > 0 {
		return *img.Width, *img.Height
	}
	return a.Width, a.Height
}

// sidecarPath is <stem><ext> next to the image at rel.
func sidecarPath(rel, ext string) string {
	dir, base := path.Split(rel)
	return dir + strings.TrimSuffix(base, path.Ext(base)) + ext
}

// reserve records the annotation entry names (by name) of the images that
// own them (by owns). A review version annotation does not take one of
// them: when a.jpg (from a version) and a.png (a.xml paired with it) share
// a name, a.png keeps its annotation whatever their order, as in a download
// of the base alone, where a.jpg has none.
func (e *Export) reserve(name func(exportImage) string, owns func(exportImage) bool) {
	if e.reserved == nil {
		e.reserved = map[string]bool{}
	}
	for _, img := range e.images {
		if owns(img) {
			e.reserved[name(img)] = true
		}
	}
}

// keepsBaseAnnotation reports whether img is written with its own VOC file.
func keepsBaseAnnotation(img exportImage) bool {
	return img.VersionID == nil && img.AnnPath != nil
}

// nameReserved reports whether the annotation entry name of a review
// version image is reserved by another image (and logs it).
func (e *Export) nameReserved(img exportImage, name string) bool {
	if img.VersionID == nil || !e.reserved[name] || (img.AnnPath != nil && *img.AnnPath == name) {
		return false
	}
	e.nameTaken++
	e.log.Warn("export: annotation name taken by another image's annotation; leaving the version annotation out",
		"image", img.Path, "name", name)
	return true
}

// ---- VOC ----

// writeVOC copies the images and their VOC files as they are. The
// annotation of a review version is written as a new VOC file in place of
// the base one: where the image's VOC file is, else where a new one would
// go in the dataset folder (next to the image, or in a sibling
// Annotations/ folder, see defaultAnnotationPath).
func (e *Export) writeVOC(ctx context.Context, zw *zip.Writer) error {
	// A VOC file belongs to the image it is paired with, also when the
	// annotation comes from a version (and is written in its place): a.jpg
	// from a version without a VOC file does not take a.xml from a.png.
	e.reserve(func(img exportImage) string { return *img.AnnPath },
		func(img exportImage) bool { return img.AnnPath != nil })
	for i, img := range e.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := e.copyImage(zw, img, img.Path)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if img.VersionID != nil {
			a, err := e.versionAnnotation(ctx, i)
			if err != nil {
				return err
			}
			name := defaultAnnotationPath(e.root, img.Path)
			if img.AnnPath != nil {
				name = *img.AnnPath
			}
			if e.nameReserved(img, name) {
				continue
			}
			w, h := imageSize(img, a)
			data, err := EncodeVOC(path.Base(path.Dir(img.Path)), path.Base(img.Path), w, h, a.Objects)
			if err != nil {
				return err
			}
			if ok, err = e.addFile(zw, name, data, img.ModTime); err != nil {
				return err
			}
			if ok {
				e.annotations++
				e.fromVersions++
			}
			continue
		}
		if img.AnnPath == nil {
			continue
		}
		ok, err = e.copyFile(zw, *img.AnnPath, *img.AnnPath)
		if err != nil {
			return err
		}
		if ok {
			e.annotations++
		}
	}
	return nil
}

// ---- X-AnyLabeling ----

// xalExportFile is an X-AnyLabeling annotation file (its template in
// anylabeling/views/labeling/schema.py, plus "description").
type xalExportFile struct {
	Version     string           `json:"version"`
	Flags       struct{}         `json:"flags"`
	Checked     bool             `json:"checked"`
	Shapes      []xalExportShape `json:"shapes"`
	ImagePath   string           `json:"imagePath"`
	ImageData   *string          `json:"imageData"`
	ImageHeight int              `json:"imageHeight"`
	ImageWidth  int              `json:"imageWidth"`
	Description string           `json:"description"`
}

type xalExportShape struct {
	Label       string       `json:"label"`
	Score       *float64     `json:"score"`
	Points      [][2]float64 `json:"points"`
	GroupID     *int         `json:"group_id"`
	Description string       `json:"description"`
	Difficult   bool         `json:"difficult"`
	ShapeType   string       `json:"shape_type"`
	Flags       struct{}     `json:"flags"`
	Attributes  struct{}     `json:"attributes"`
	KieLinking  [][2]int     `json:"kie_linking"`
}

// writeXAnyLabeling copies the images and writes <stem>.json next to each
// image with a usable VOC file.
func (e *Export) writeXAnyLabeling(ctx context.Context, zw *zip.Writer) error {
	e.reserve(func(img exportImage) string { return sidecarPath(img.Path, ".json") }, keepsBaseAnnotation)
	for i, img := range e.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := e.copyImage(zw, img, img.Path)
		if err != nil {
			return err
		}
		name := sidecarPath(img.Path, ".json")
		if !ok || e.nameReserved(img, name) {
			continue
		}
		a, err := e.annotation(ctx, i)
		if err != nil {
			return err
		}
		if a == nil {
			continue
		}
		data, err := xAnyLabelingJSON(path.Base(img.Path), a, img)
		if err != nil {
			return err
		}
		// a.jpg and a.png share a.json: the second keeps none.
		ok, err = e.addFile(zw, name, data, img.ModTime)
		if err != nil {
			return err
		}
		if ok {
			e.annotations++
			if img.VersionID != nil {
				e.fromVersions++
			}
		}
	}
	return nil
}

func xAnyLabelingJSON(imageName string, a *vocAnnotation, img exportImage) ([]byte, error) {
	w, h := imageSize(img, a)
	if w == 0 || h == 0 {
		w, h = -1, -1 // X-AnyLabeling's value for unknown
	}
	f := xalExportFile{Version: xAnyLabelingVersion, Shapes: []xalExportShape{}, ImagePath: imageName,
		ImageWidth: w, ImageHeight: h}
	for _, o := range a.Objects {
		x1, x2 := float64(min(o.Xmin, o.Xmax)), float64(max(o.Xmin, o.Xmax))
		y1, y2 := float64(min(o.Ymin, o.Ymax)), float64(max(o.Ymin, o.Ymax))
		f.Shapes = append(f.Shapes, xalExportShape{
			Label:      o.Name,
			Points:     [][2]float64{{x1, y1}, {x2, y1}, {x2, y2}, {x1, y2}},
			Difficult:  o.Difficult,
			ShapeType:  "rectangle",
			KieLinking: [][2]int{},
		})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(f)
	return b.Bytes(), err
}

// ---- YOLO ----

// writeYOLO writes the Ultralytics layout: images/ with the images (their
// sub-folders kept), labels/ with one .txt per annotated image, classes.txt
// and data.yaml.
func (e *Export) writeYOLO(ctx context.Context, zw *zip.Writer) error {
	// A dataset imported from a YOLO layout has all images in images/
	// already; another level would give images/images/. Decided by all the
	// dataset's images, so that every download of a dataset has the same
	// layout.
	prefix := "images/"
	if e.allInImages {
		prefix = ""
	}
	now := time.Now()
	if _, err := e.addFile(zw, yoloClassesFile, []byte(strings.Join(e.classes, "\n")+"\n"), now); err != nil {
		return err
	}
	if _, err := e.addFile(zw, "data.yaml", yoloDataYAML(e.top, e.classes, e.images, prefix, e.subset), now); err != nil {
		return err
	}
	e.reserve(func(img exportImage) string { return ultralyticsLabelPath(prefix + img.Path) }, keepsBaseAnnotation)

	ids := make(map[string]int, len(e.classes))
	for i, name := range e.classes {
		ids[name] = i
	}
	for i, img := range e.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := prefix + img.Path
		ok, err := e.copyImage(zw, img, name)
		if err != nil {
			return err
		}
		label := ultralyticsLabelPath(name)
		if !ok || e.nameReserved(img, label) {
			continue
		}
		a, err := e.annotation(ctx, i)
		if err != nil {
			return err
		}
		if a == nil {
			continue
		}
		data, ok := yoloLabels(a, img, ids)
		if !ok {
			e.log.Info("export: image size unknown, no YOLO labels", "image", img.Path)
			continue
		}
		// a.jpg and a.png share a.txt: the second keeps none.
		ok, err = e.addFile(zw, label, data, img.ModTime)
		if err != nil {
			return err
		}
		if ok {
			e.annotations++
			if img.VersionID != nil {
				e.fromVersions++
			}
		}
	}
	return nil
}

// ultralyticsLabelPath is where Ultralytics looks for the labels of the
// image at rel: the last "images" folder becomes "labels" and the extension
// ".txt" (img2label_paths, which matches the name exactly).
func ultralyticsLabelPath(rel string) string {
	segs := strings.Split(sidecarPath(rel, ".txt"), "/")
	for i := len(segs) - 2; i >= 0; i-- {
		if segs[i] == "images" {
			segs[i] = "labels"
			break
		}
	}
	return strings.Join(segs, "/")
}

// yoloLabels renders the objects whose label is in ids as normalized
// "class cx cy w h" lines, boxes clipped to the image. ok is false when the
// image size is unknown.
func yoloLabels(a *vocAnnotation, img exportImage, ids map[string]int) ([]byte, bool) {
	w, h := imageSize(img, a)
	if w == 0 || h == 0 {
		return nil, false
	}
	W, H := float64(w), float64(h)
	var b bytes.Buffer
	for _, o := range a.Objects {
		id, ok := ids[o.Name]
		if !ok {
			continue
		}
		x1, x2 := clamp(float64(min(o.Xmin, o.Xmax)), 0, W), clamp(float64(max(o.Xmin, o.Xmax)), 0, W)
		y1, y2 := clamp(float64(min(o.Ymin, o.Ymax)), 0, H), clamp(float64(max(o.Ymin, o.Ymax)), 0, H)
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		fmt.Fprintf(&b, "%d %.6f %.6f %.6f %.6f\n", id, (x1+x2)/2/W, (y1+y2)/2/H, (x2-x1)/W, (y2-y1)/H)
	}
	return b.Bytes(), true
}

// yoloSplits are the sub-folders of images/ that data.yaml can use as the
// dataset split.
var yoloSplits = []string{"train", "val", "valid", "test"}

// yoloDataYAML renders data.yaml for Ultralytics. Without a "path" key the
// paths are relative to the file. When the images are split into
// images/train, images/val (or valid) and optionally images/test, those are
// used; otherwise train and val both point to all images. subset says the
// download has only some of the dataset's images.
func yoloDataYAML(name string, classes []string, images []exportImage, prefix string, subset bool) []byte {
	dirs := map[string]bool{}
	split := true
	for _, img := range images {
		first, _, nested := strings.Cut(strings.TrimPrefix(prefix+img.Path, "images/"), "/")
		if !nested || !slices.Contains(yoloSplits, first) {
			split = false
			break
		}
		dirs[first] = true
	}
	val := "val"
	if !dirs[val] {
		val = "valid"
	}
	split = split && dirs["train"] && dirs[val]

	// The folder name is in a comment: a line break in it (registered
	// folders are not checked for one; YAML also breaks lines at U+2028
	// and U+2029) must not start a YAML key.
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, name)
	var b strings.Builder
	fmt.Fprintf(&b, "# trainMaster 导出的 YOLO 数据集“%s”。\n# 路径相对于本文件所在的文件夹；类别编号与 classes.txt 一致。\n", name)
	if split {
		if subset {
			b.WriteString("# 本次导出只含数据集的部分图片。\n")
		}
		fmt.Fprintf(&b, "train: images/train\nval: images/%s\n", val)
		if dirs["test"] {
			b.WriteString("test: images/test\n")
		}
	} else {
		if subset {
			b.WriteString("# 本次导出只含数据集的部分图片，")
		} else {
			b.WriteString("# ")
		}
		b.WriteString("图片没有按 train / val 子文件夹划分，train 与 val 都指向全部图片；正式训练前请先划分验证集。\n")
		b.WriteString("train: images\nval: images\n")
	}
	fmt.Fprintf(&b, "nc: %d\nnames:\n", len(classes))
	for i, c := range classes {
		// A JSON string is a valid YAML double-quoted scalar.
		q, _ := json.Marshal(c)
		fmt.Fprintf(&b, "  %d: %s\n", i, q)
	}
	return []byte(b.String())
}
