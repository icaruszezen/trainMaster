package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
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
	Format  string   // Format*
	Classes []string // YOLO: the class names by id
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
	fileName  string
	expires   time.Time
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

// CreateExport prepares the download of a dataset in the given format.
func (s *Service) CreateExport(ctx context.Context, datasetID int64, in ExportInput) (ExportTicket, error) {
	format, classes, err := normalizeFormat(in.Format, in.Classes, "导出")
	if err != nil {
		return ExportTicket{}, err
	}
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return ExportTicket{}, err
	}
	if !s.dirExists(d.DirName) {
		return ExportTicket{}, errDatasetDirMissing(s.datasetDir(d.DirName))
	}
	if d.ImageCount == 0 {
		return ExportTicket{}, &Error{Kind: ErrConflict, Message: "数据集中没有图片，无法下载"}
	}
	now := time.Now()
	r := exportRequest{datasetID: d.ID, format: format, classes: classes,
		fileName: d.DirName + "-" + format + ".zip", expires: now.Add(exportTTL)}
	token := rand.Text()
	s.exports.put(token, r, now)
	return ExportTicket{Token: token, FileName: r.fileName, ExpiresAt: r.expires.UTC()}, nil
}

// Export is an opened download. The caller writes it with Write and then
// closes it.
type Export struct {
	FileName string

	root    *os.Root
	top     string // the folder everything is in: the dataset's folder name
	format  string
	classes []string
	images  []exportImage
	log     *slog.Logger

	// Counts for the log.
	written, missing, annotations int
}

type exportImage struct {
	Path    string
	ModTime time.Time
	Width   *int
	Height  *int
	AnnPath *string
}

// OpenExport opens a download prepared by CreateExport for the dataset.
func (s *Service) OpenExport(ctx context.Context, datasetID int64, token string) (*Export, error) {
	r, ok := s.exports.get(token, time.Now())
	if !ok || r.datasetID != datasetID {
		return nil, errExportExpired
	}
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT rel_path, mod_time, width, height, annotation_path
		FROM dataset_images WHERE dataset_id = $1`, d.ID)
	if err != nil {
		return nil, err
	}
	images, err := pgx.CollectRows(rows, pgx.RowToStructByPos[exportImage])
	if err != nil {
		return nil, fmt.Errorf("load images: %w", err)
	}
	slices.SortFunc(images, func(a, b exportImage) int { return strings.Compare(a.Path, b.Path) })
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return nil, err
	}
	return &Export{FileName: r.fileName, root: root, top: d.DirName, format: r.format, classes: r.classes,
		images: images, log: s.log.With("dataset_id", d.ID, "format", r.format)}, nil
}

// Close releases the dataset folder.
func (e *Export) Close() error { return e.root.Close() }

// Write sends the zip archive to w. Images that have disappeared since the
// last scan are left out; an error means the archive is incomplete.
func (e *Export) Write(ctx context.Context, w io.Writer) error {
	start := time.Now()
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})
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
		"annotations", e.annotations, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// storedExtensions are image formats that are compressed already; deflating
// them again costs CPU for nothing. BMP and TIFF (often uncompressed) are
// deflated.
var storedExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
}

// copyFile adds the dataset file rel to the archive as name. ok is false
// (and nothing is written) when the file cannot be opened, e.g. it was
// deleted since the last scan.
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
	out, err := zw.CreateHeader(e.header(name, method, fi.ModTime()))
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, f); err != nil {
		return false, err
	}
	return true, nil
}

// addFile adds generated content to the archive.
func (e *Export) addFile(zw *zip.Writer, name string, data []byte, modTime time.Time) error {
	out, err := zw.CreateHeader(e.header(name, zip.Deflate, modTime))
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

func (e *Export) header(name string, method uint16, modTime time.Time) *zip.FileHeader {
	// Windows tools treat "\" in entry names as a folder separator, so a
	// file named "..\x.png" on the server could be unpacked outside the
	// target folder. Uploads never create such names.
	name = strings.ReplaceAll(name, `\`, "_")
	h := &zip.FileHeader{Name: e.top + "/" + name, Method: method, Modified: modTime}
	h.SetMode(0o644)
	return h
}

// copyImage adds an image under name; ok is false when it is missing.
func (e *Export) copyImage(zw *zip.Writer, img exportImage, name string) (bool, error) {
	ok, err := e.copyFile(zw, img.Path, name)
	if ok {
		e.written++
	} else if err == nil {
		e.missing++
	}
	return ok, err
}

// readAnnotation parses the VOC file of an image as it is on disk now; nil
// when the image has none or it cannot be used.
func (e *Export) readAnnotation(img exportImage) *vocAnnotation {
	if img.AnnPath == nil {
		return nil
	}
	a, err := newAnnotationCache(e.root).get(*img.AnnPath)
	if err != nil {
		e.log.Info("export: annotation not usable", "path", *img.AnnPath, "err", err)
		return nil
	}
	return a
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

// ---- VOC ----

// writeVOC copies the images and their VOC files as they are.
func (e *Export) writeVOC(ctx context.Context, zw *zip.Writer) error {
	for _, img := range e.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := e.copyImage(zw, img, img.Path)
		if err != nil {
			return err
		}
		if !ok || img.AnnPath == nil {
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
	taken := map[string]bool{}
	for _, img := range e.images {
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
		a := e.readAnnotation(img)
		if a == nil {
			continue
		}
		name := sidecarPath(img.Path, ".json")
		if taken[name] {
			e.log.Warn("export: two images share an annotation file name; the second keeps none", "image", img.Path, "file", name)
			continue
		}
		taken[name] = true
		data, err := xAnyLabelingJSON(path.Base(img.Path), a, img)
		if err != nil {
			return err
		}
		if err := e.addFile(zw, name, data, img.ModTime); err != nil {
			return err
		}
		e.annotations++
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
	// already; another level would give images/images/.
	prefix := "images/"
	if !slices.ContainsFunc(e.images, func(img exportImage) bool { return !strings.HasPrefix(img.Path, "images/") }) {
		prefix = ""
	}
	now := time.Now()
	if err := e.addFile(zw, yoloClassesFile, []byte(strings.Join(e.classes, "\n")+"\n"), now); err != nil {
		return err
	}
	if err := e.addFile(zw, "data.yaml", yoloDataYAML(e.top, e.classes, e.images, prefix), now); err != nil {
		return err
	}

	ids := make(map[string]int, len(e.classes))
	for i, name := range e.classes {
		ids[name] = i
	}
	taken := map[string]bool{}
	for _, img := range e.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := prefix + img.Path
		ok, err := e.copyImage(zw, img, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		a := e.readAnnotation(img)
		if a == nil {
			continue
		}
		label := ultralyticsLabelPath(name)
		if taken[label] {
			e.log.Warn("export: two images share a label file name; the second keeps none", "image", img.Path, "file", label)
			continue
		}
		data, ok := yoloLabels(a, img, ids)
		if !ok {
			e.log.Info("export: image size unknown, no YOLO labels", "image", img.Path)
			continue
		}
		taken[label] = true
		if err := e.addFile(zw, label, data, img.ModTime); err != nil {
			return err
		}
		e.annotations++
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
// used; otherwise train and val both point to all images.
func yoloDataYAML(name string, classes []string, images []exportImage, prefix string) []byte {
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

	var b strings.Builder
	fmt.Fprintf(&b, "# trainMaster 导出的 YOLO 数据集“%s”。\n# 路径相对于本文件所在的文件夹；类别编号与 classes.txt 一致。\n", name)
	if split {
		fmt.Fprintf(&b, "train: images/train\nval: images/%s\n", val)
		if dirs["test"] {
			b.WriteString("test: images/test\n")
		}
	} else {
		b.WriteString("# 图片没有按 train / val 子文件夹划分，train 与 val 都指向全部图片；正式训练前请先划分验证集。\n")
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
