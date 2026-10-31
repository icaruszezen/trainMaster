package dataset

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image/color"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Conversion of X-AnyLabeling / LabelMe JSON and YOLO txt annotations to
// Pascal VOC. An upload in one of these formats is converted in its staging
// folder: every annotation file paired with an image becomes <stem>.xml next
// to the image, and the source files are removed, so the rest of the import
// (moveStaged, syncIndex) handles it exactly like a VOC upload.

// maxJSONAnnotationBytes bounds how much of a JSON file is read. LabelMe can
// embed the image (imageData, base64), so these are larger than VOC files.
const maxJSONAnnotationBytes = 64 << 20

// errClassOutOfRange fails a YOLO import whose labels use a class id the
// class list does not have: every file would be converted with wrong or
// missing names, so nothing is imported.
type classRangeError struct {
	file      string
	line, id  int
	numLabels int
}

func (e *classRangeError) Error() string {
	return fmt.Sprintf("标注文件 %s 第 %d 行的类别编号为 %d，但只提供了 %d 个类别（编号 0–%d），请检查类别顺序后重新导入",
		e.file, e.line, e.id, e.numLabels, e.numLabels-1)
}

// convertedAnnotation is one annotation file read in another format.
type convertedAnnotation struct {
	Objects []AnnotationObject
	Skipped int // shapes that are not boxes
	// Width and Height are the image size recorded in the file, 0 if none.
	Width, Height int
}

// imageInfo is what conversion needs to know about an image.
type imageInfo struct {
	Width, Height int // 0 when unknown
	Depth         int // channels for <size><depth>
}

// stagedImageInfo reads the dimensions and color depth of a staged image.
func stagedImageInfo(root *os.Root, rel string) imageInfo {
	info := imageInfo{Depth: 3}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return info
	}
	defer f.Close()
	cfg, _, err := decodeImageConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return info
	}
	info.Width, info.Height = cfg.Width, cfg.Height
	switch cfg.ColorModel {
	case color.GrayModel, color.Gray16Model:
		info.Depth = 1
	}
	return info
}

// convertStaged converts the annotations of a JSON or YOLO upload below
// staged to VOC (see the comment at the top of this file).
func (s *Service) convertStaged(ctx context.Context, staged string, job Import, sum *importSummary, p *progressTracker) error {
	root, err := os.OpenRoot(staged)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing was staged
	}
	if err != nil {
		return err
	}
	defer root.Close()

	images, sources, err := listStaged(ctx, staged, job.AnnotationFormat)
	if err != nil {
		return fmt.Errorf("list staged files: %w", err)
	}
	c := &converter{root: root, format: job.AnnotationFormat, classes: job.Classes, imagePaths: map[string]string{}}
	pairs := c.pair(images, sources)
	sum.Orphaned += len(sources) - len(pairs)

	paired := make([]string, 0, len(pairs))
	for img := range pairs {
		paired = append(paired, img)
	}
	slices.Sort(paired)
	p.setStage(ctx, stageConverting, unitFiles, int64(len(paired)))
	for i, img := range paired {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := pairs[img]
		dir, base := path.Split(img)
		info := stagedImageInfo(root, img)
		a, err := c.read(src, info)
		var rangeErr *classRangeError
		if errors.As(err, &rangeErr) {
			return &Error{Kind: ErrInvalid, Message: rangeErr.Error()}
		}
		if err == nil {
			if info.Width == 0 {
				info.Width, info.Height = a.Width, a.Height
			}
			// Fails for the second of a.jpg and a.png when both are paired
			// (JSON imagePath): VOC keeps one annotation per stem.
			err = writeStagedVOC(root, dir+strings.TrimSuffix(base, path.Ext(base))+".xml", base, info, a.Objects)
		}
		switch {
		case err == nil:
			sum.Converted++
			sum.ObjectsSkipped += a.Skipped
		case isAnnotationError(err):
			sum.ConvertFailed++
			s.log.Info("annotation not converted", "import_id", job.ID, "path", src, "image", img, "err", err)
		default:
			return err
		}
		p.set(ctx, int64(i+1), int64(len(paired)))
	}
	// The sources stay out of the dataset; moveStaged would count them as
	// ignored files.
	for _, src := range sources {
		if err := root.Remove(filepath.FromSlash(src)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove converted annotation: %w", err)
		}
	}
	return nil
}

// annotationFileError marks problems of one annotation file, which only
// leave its image unannotated, as opposed to I/O errors of the import.
type annotationFileError struct{ err error }

func (e *annotationFileError) Error() string { return e.err.Error() }
func (e *annotationFileError) Unwrap() error { return e.err }

func badAnnotation(format string, args ...any) error {
	return &annotationFileError{fmt.Errorf(format, args...)}
}

func isAnnotationError(err error) bool {
	var ae *annotationFileError
	return errors.As(err, &ae)
}

// listStaged lists the images and the annotation files of format below
// staged (slash-separated, relative). Staged files come from cleaned
// archive entries or uploaded paths: there are no symbolic links and every
// name is valid UTF-8.
func listStaged(ctx context.Context, staged, format string) (images, sources []string, err error) {
	err = filepath.WalkDir(staged, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(staged, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch classifyFor(rel, format) {
		case kindImage:
			images = append(images, rel)
		case kindAnnotation:
			sources = append(sources, rel)
		}
		return nil
	})
	return images, sources, err
}

// converter reads the annotation files of one upload.
type converter struct {
	root    *os.Root
	format  string
	classes []string
	// imagePaths caches the imagePath of the JSON files pairing looked at.
	// Only that is kept: read parses a file again, so a large upload does
	// not hold every file's shapes in memory.
	imagePaths map[string]string
}

// pair matches images with annotation files: pairSidecars with the rules of
// the format, and for JSON files left over the image their imagePath
// names (X-AnyLabeling can save labels in a separate folder).
func (c *converter) pair(images, sources []string) map[string]string {
	if c.format == FormatYOLO {
		return pairSidecars(images, sources, ".txt", yoloLabelDir, nil)
	}
	pairs := pairSidecars(images, sources, ".json", nil, c.jsonImageName)

	used := make(map[string]bool, len(pairs))
	for _, src := range pairs {
		used[src] = true
	}
	exact := make(map[string]bool, len(images))
	folded := make(map[string]string, len(images))
	for _, img := range slices.Sorted(slices.Values(images)) {
		exact[img] = true
		if key := strings.ToLower(img); folded[key] == "" {
			folded[key] = img
		}
	}
	for _, src := range slices.Sorted(slices.Values(sources)) {
		if used[src] {
			continue
		}
		imagePath := c.jsonImagePath(src)
		if imagePath == "" {
			continue
		}
		target, ok := cleanEntryPath(path.Join(path.Dir(src), strings.ReplaceAll(imagePath, `\`, "/")))
		if !ok {
			continue
		}
		if !exact[target] {
			target = folded[strings.ToLower(target)]
		}
		if target != "" && pairs[target] == "" {
			pairs[target] = src
		}
	}
	return pairs
}

// jsonImageName is the image file name a JSON file records, for pairing.
func (c *converter) jsonImageName(rel string) string {
	imagePath := c.jsonImagePath(rel)
	if imagePath == "" {
		return ""
	}
	return path.Base(strings.ReplaceAll(imagePath, `\`, "/"))
}

// jsonImagePath is the imagePath a JSON file records, "" when it has none
// or the file cannot be read.
func (c *converter) jsonImagePath(rel string) string {
	if p, ok := c.imagePaths[rel]; ok {
		return p
	}
	var p string
	if f, err := c.jsonFile(rel); err == nil {
		p = f.ImagePath
	}
	c.imagePaths[rel] = p
	return p
}

func (c *converter) jsonFile(rel string) (*xalFile, error) {
	data, err := c.readFile(rel, maxJSONAnnotationBytes)
	if err != nil {
		return nil, err
	}
	return parseXAnyLabeling(data)
}

// readFile reads a staged annotation file of at most limit bytes.
func (c *converter) readFile(rel string, limit int64) ([]byte, error) {
	f, err := c.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, badAnnotation("annotation file larger than %d bytes", limit)
	}
	return data, nil
}

// read converts one annotation file for the image described by info.
func (c *converter) read(rel string, info imageInfo) (*convertedAnnotation, error) {
	if c.format == FormatYOLO {
		data, err := c.readFile(rel, maxAnnotationBytes)
		if err != nil {
			return nil, err
		}
		return parseYOLO(data, rel, c.classes, info)
	}
	f, err := c.jsonFile(rel)
	if err != nil {
		return nil, err
	}
	return f.convert(info), nil
}

// convert turns the shapes into boxes for the image described by info; the
// size the file records stands in when the image's is unknown.
func (f *xalFile) convert(info imageInfo) *convertedAnnotation {
	w, h := info.Width, info.Height
	if w == 0 {
		w, h = f.Width, f.Height
	}
	a := &convertedAnnotation{Width: f.Width, Height: f.Height}
	for _, raw := range f.Shapes {
		obj, ok := shapeBox(raw, w, h)
		if !ok {
			a.Skipped++
			continue
		}
		a.Objects = append(a.Objects, obj)
	}
	return a
}

// textUTF8 returns annotation text as UTF-8: a UTF-8 BOM is dropped, and
// text that is not valid UTF-8 is decoded as GB18030 (files saved by
// Chinese Windows tools).
func textUTF8(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return data
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
		return out
	}
	return data
}

// ---- X-AnyLabeling / LabelMe ----

// xalFile is the part of an X-AnyLabeling (or LabelMe) JSON file that
// conversion uses. imageData is not decoded.
type xalFile struct {
	Shapes        []json.RawMessage
	ImagePath     string
	Width, Height int
}

// parseXAnyLabeling parses an X-AnyLabeling / LabelMe annotation. The root
// must be an object with a "shapes" array.
func parseXAnyLabeling(data []byte) (*xalFile, error) {
	var doc struct {
		Shapes      *[]json.RawMessage `json:"shapes"`
		ImagePath   any                `json:"imagePath"`
		ImageWidth  any                `json:"imageWidth"`
		ImageHeight any                `json:"imageHeight"`
	}
	if err := json.Unmarshal(textUTF8(data), &doc); err != nil {
		return nil, badAnnotation("not a JSON object: %v", err)
	}
	if doc.Shapes == nil {
		return nil, badAnnotation(`no "shapes" array: not an X-AnyLabeling / LabelMe file`)
	}
	f := &xalFile{Shapes: *doc.Shapes}
	f.ImagePath, _ = doc.ImagePath.(string)
	w, wok := doc.ImageWidth.(float64)
	h, hok := doc.ImageHeight.(float64)
	if wok && hok && w >= 1 && h >= 1 && w <= math.MaxInt32 && h <= math.MaxInt32 {
		f.Width, f.Height = int(math.Round(w)), int(math.Round(h))
	}
	return f, nil
}

// shapeBox turns one shape into a box: rectangles, rotated boxes,
// quadrilaterals, polygons and cuboids by the bounding box of their points,
// circles (center and a point on the circle) by their bounding square.
// Points, lines and anything else are not boxes. The box is clipped to the
// image when its size (w, h) is known.
func shapeBox(raw json.RawMessage, w, h int) (AnnotationObject, bool) {
	var s struct {
		Label     any     `json:"label"`
		Points    [][]any `json:"points"`
		ShapeType any     `json:"shape_type"`
		Difficult any     `json:"difficult"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return AnnotationObject{}, false
	}
	label, _ := s.Label.(string)
	label = strings.TrimSpace(label)
	shapeType, _ := s.ShapeType.(string)
	if shapeType == "" {
		shapeType = "polygon" // LabelMe's default
	}
	if label == "" {
		return AnnotationObject{}, false
	}
	pts := make([][2]float64, 0, len(s.Points))
	for _, p := range s.Points {
		if len(p) < 2 {
			return AnnotationObject{}, false
		}
		x, xok := p[0].(float64)
		y, yok := p[1].(float64)
		if !xok || !yok || !finite(x) || !finite(y) {
			return AnnotationObject{}, false
		}
		pts = append(pts, [2]float64{x, y})
	}

	var box [4]float64 // xmin, ymin, xmax, ymax
	switch shapeType {
	case "rectangle", "rotation", "quadrilateral", "polygon", "cuboid":
		need := 3
		if shapeType == "rectangle" {
			need = 2 // two corners (LabelMe) or four (X-AnyLabeling)
		}
		if len(pts) < need {
			return AnnotationObject{}, false
		}
		box = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, p := range pts {
			box[0], box[1] = min(box[0], p[0]), min(box[1], p[1])
			box[2], box[3] = max(box[2], p[0]), max(box[3], p[1])
		}
	case "circle":
		if len(pts) < 2 {
			return AnnotationObject{}, false
		}
		r := math.Hypot(pts[1][0]-pts[0][0], pts[1][1]-pts[0][1])
		box = [4]float64{pts[0][0] - r, pts[0][1] - r, pts[0][0] + r, pts[0][1] + r}
	default:
		return AnnotationObject{}, false
	}
	obj, ok := pixelBox(label, box, w, h)
	obj.Difficult = truthy(s.Difficult)
	return obj, ok
}

// pixelBox clips a box to a w×h image (when known) and rounds it to whole
// pixels; ok is false when nothing of it is left.
func pixelBox(label string, box [4]float64, w, h int) (AnnotationObject, bool) {
	if w > 0 && h > 0 {
		box[0], box[2] = clamp(box[0], 0, float64(w)), clamp(box[2], 0, float64(w))
		box[1], box[3] = clamp(box[1], 0, float64(h)), clamp(box[3], 0, float64(h))
	}
	for _, v := range box {
		if math.Abs(v) > math.MaxInt32 {
			return AnnotationObject{}, false
		}
	}
	obj := AnnotationObject{Name: label,
		Xmin: int(math.Round(box[0])), Ymin: int(math.Round(box[1])),
		Xmax: int(math.Round(box[2])), Ymax: int(math.Round(box[3]))}
	return obj, obj.Xmax > obj.Xmin && obj.Ymax > obj.Ymin
}

func clamp(v, lo, hi float64) float64 { return max(lo, min(v, hi)) }

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// truthy reads flags such as "difficult" written as true, 1 or "1".
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return flag(x) || strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

// ---- YOLO ----

// yoloCoordSlack is how far outside 0–1 a YOLO coordinate may be (rounding
// in the tools that wrote it) before the file counts as not normalized.
const yoloCoordSlack = 0.01

// parseYOLO converts a YOLO label file: one object per line, either
// "class cx cy w h" (normalized; a 6th column, a confidence, is ignored) or
// a polygon "class x1 y1 x2 y2 x3 y3 ..." (segmentation, OBB) taken by its
// bounding box. Blank lines are skipped; an empty file is a negative
// sample. Any other line makes the file invalid, and a class id outside
// classes fails the whole import (classRangeError). rel names the file in
// that error.
func parseYOLO(data []byte, rel string, classes []string, info imageInfo) (*convertedAnnotation, error) {
	type line struct {
		n      int
		fields []string
	}
	var lines []line
	sc := bufio.NewScanner(bytes.NewReader(textUTF8(data)))
	sc.Buffer(make([]byte, 0, 64<<10), maxAnnotationBytes)
	for n := 1; sc.Scan(); n++ {
		if fields := strings.Fields(sc.Text()); len(fields) > 0 {
			lines = append(lines, line{n, fields})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, badAnnotation("read labels: %v", err)
	}
	// A class id past the list means the class order is wrong, whatever
	// else is wrong with the file: check every line before anything else.
	for _, l := range lines {
		id, err := strconv.ParseFloat(l.fields[0], 64)
		if err == nil && id >= float64(len(classes)) && id == math.Trunc(id) && id <= math.MaxInt32 {
			return nil, &classRangeError{file: rel, line: l.n, id: int(id), numLabels: len(classes)}
		}
	}

	a := &convertedAnnotation{}
	for _, l := range lines {
		n, fields := l.n, l.fields
		k := len(fields)
		if k != 5 && k != 6 && (k < 7 || (k-1)%2 != 0) {
			return nil, badAnnotation("line %d: %d values, want class cx cy w h or a polygon", n, k)
		}
		id, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || id < 0 || id != math.Trunc(id) || id > math.MaxInt32 {
			return nil, badAnnotation("line %d: class %q is not a class id", n, fields[0])
		}
		coords := fields[1:]
		if k == 6 {
			coords = coords[:4]
		}
		v := make([]float64, len(coords))
		for i, s := range coords {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil || !finite(f) {
				return nil, badAnnotation("line %d: %q is not a number", n, s)
			}
			if f < -yoloCoordSlack || f > 1+yoloCoordSlack {
				return nil, badAnnotation("line %d: %s is not a normalized coordinate (0–1)", n, s)
			}
			v[i] = f
		}
		if info.Width == 0 || info.Height == 0 {
			return nil, badAnnotation("image size unknown: YOLO coordinates cannot be converted to pixels")
		}
		W, H := float64(info.Width), float64(info.Height)
		var box [4]float64
		if len(v) == 4 {
			if v[2] < 0 || v[3] < 0 {
				return nil, badAnnotation("line %d: negative box size", n)
			}
			box = [4]float64{(v[0] - v[2]/2) * W, (v[1] - v[3]/2) * H, (v[0] + v[2]/2) * W, (v[1] + v[3]/2) * H}
		} else {
			box = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
			for i := 0; i+1 < len(v); i += 2 {
				x, y := v[i]*W, v[i+1]*H
				box[0], box[1] = min(box[0], x), min(box[1], y)
				box[2], box[3] = max(box[2], x), max(box[3], y)
			}
		}
		obj, ok := pixelBox(classes[int(id)], box, info.Width, info.Height)
		if !ok {
			a.Skipped++
			continue
		}
		a.Objects = append(a.Objects, obj)
	}
	return a, nil
}

// ---- VOC output ----

// writeStagedVOC writes a VOC file for the image named filename (with the
// size in info, when known) at rel in root. The file must not exist yet.
func writeStagedVOC(root *os.Root, rel, filename string, info imageInfo, objects []AnnotationObject) error {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) || isPathConflict(err) {
			return &annotationFileError{err}
		}
		return err
	}
	err = writeVOC(f, path.Base(path.Dir(rel)), filename, info, objects)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeVOC writes a Pascal VOC annotation in the layout labelImg uses.
// folder is the name of the image's folder ("." for the top level).
func writeVOC(w io.Writer, folder, filename string, info imageInfo, objects []AnnotationObject) error {
	var b bytes.Buffer
	text := func(s string) string {
		var t bytes.Buffer
		_ = xml.EscapeText(&t, []byte(s))
		return t.String()
	}
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<annotation>\n")
	if folder != "." && folder != "" {
		fmt.Fprintf(&b, "\t<folder>%s</folder>\n", text(folder))
	}
	fmt.Fprintf(&b, "\t<filename>%s</filename>\n", text(filename))
	b.WriteString("\t<source>\n\t\t<database>Unknown</database>\n\t</source>\n")
	if info.Width > 0 && info.Height > 0 {
		fmt.Fprintf(&b, "\t<size>\n\t\t<width>%d</width>\n\t\t<height>%d</height>\n\t\t<depth>%d</depth>\n\t</size>\n",
			info.Width, info.Height, max(info.Depth, 1))
	}
	b.WriteString("\t<segmented>0</segmented>\n")
	for _, o := range objects {
		fmt.Fprintf(&b, "\t<object>\n\t\t<name>%s</name>\n\t\t<pose>Unspecified</pose>\n"+
			"\t\t<truncated>%d</truncated>\n\t\t<difficult>%d</difficult>\n"+
			"\t\t<bndbox>\n\t\t\t<xmin>%d</xmin>\n\t\t\t<ymin>%d</ymin>\n\t\t\t<xmax>%d</xmax>\n\t\t\t<ymax>%d</ymax>\n\t\t</bndbox>\n\t</object>\n",
			text(o.Name), boolInt(o.Truncated), boolInt(o.Difficult), o.Xmin, o.Ymin, o.Xmax, o.Ymax)
	}
	b.WriteString("</annotation>\n")
	_, err := w.Write(b.Bytes())
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
