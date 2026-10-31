package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestNormalizeClasses(t *testing.T) {
	got, err := NormalizeClasses([]string{" scratch ", "划痕", "Scratch"})
	if err != nil || !slices.Equal(got, []string{"scratch", "划痕", "Scratch"}) {
		t.Errorf("NormalizeClasses = %q, %v", got, err)
	}
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, "需要提供类别顺序"},
		{[]string{"a", " "}, "第 2 个类别名称为空"},
		{[]string{"a", "b", "a"}, "类别“a”重复（第 1 个和第 3 个）"},
		{[]string{"a\tb"}, "第 1 个类别名称包含换行或控制字符"},
		{[]string{"a\nb"}, "控制字符"},
		{[]string{"\xff"}, "控制字符"},
		{[]string{"a\u009fb"}, "控制字符"},
		{[]string{strings.Repeat("长", 256)}, "不能超过 255 个字符"},
		{make([]string, 1001), "不能超过 1000 个"},
	} {
		_, err := NormalizeClasses(c.in)
		wantKind(t, err, ErrInvalid, c.want)
	}
	if _, err := NormalizeClasses([]string{strings.Repeat("长", 255)}); err != nil {
		t.Errorf("255 characters: %v", err)
	}
}

func TestNormalizeFormat(t *testing.T) {
	if f, c, err := normalizeFormat("", []string{"x"}, "标注"); f != FormatVOC || c != nil || err != nil {
		t.Errorf(`"" = %q %q %v`, f, c, err)
	}
	if f, c, err := normalizeFormat(FormatXAnyLabeling, []string{"x"}, "标注"); f != FormatXAnyLabeling || c != nil || err != nil {
		t.Errorf("xanylabeling = %q %q %v", f, c, err)
	}
	if f, c, err := normalizeFormat(FormatYOLO, []string{"x "}, "标注"); f != FormatYOLO || !slices.Equal(c, []string{"x"}) || err != nil {
		t.Errorf("yolo = %q %q %v", f, c, err)
	}
	_, _, err := normalizeFormat("coco", nil, "导出")
	wantKind(t, err, ErrInvalid, "导出格式必须是 voc")
	_, _, err = normalizeFormat(FormatYOLO, nil, "导出")
	wantKind(t, err, ErrInvalid, "需要提供类别顺序")
}

func TestClassifyFor(t *testing.T) {
	cases := []struct {
		rel, format string
		want        fileKind
	}{
		{"a.JPG", FormatYOLO, kindImage},
		{"a.xml", FormatVOC, kindAnnotation},
		{"a.xml", FormatYOLO, kindIgnored},
		{"a.xml", FormatXAnyLabeling, kindIgnored},
		{"a.json", FormatXAnyLabeling, kindAnnotation},
		{"a.JSON", FormatXAnyLabeling, kindAnnotation},
		{"a.json", FormatVOC, kindIgnored},
		{"labels/a.txt", FormatYOLO, kindAnnotation},
		{"a.txt", FormatXAnyLabeling, kindIgnored},
		{"labels/classes.txt", FormatYOLO, kindIgnored},
		{"Classes.TXT", FormatYOLO, kindIgnored},
		{"__MACOSX/a.txt", FormatYOLO, kindIgnored},
		{"._a.json", FormatXAnyLabeling, kindIgnored},
		{"data.yaml", FormatYOLO, kindIgnored},
	}
	for _, c := range cases {
		if got := classifyFor(c.rel, c.format); got != c.want {
			t.Errorf("classifyFor(%q, %s) = %v, want %v", c.rel, c.format, got, c.want)
		}
	}
}

func TestYOLOLabelDir(t *testing.T) {
	for dir, want := range map[string]string{
		"images":                 "labels",
		"ds/images/train":        "ds/labels/train",
		"Images/val":             "labels/val",
		"images/x/images":        "images/x/labels",
		"JPEGImages":             "",
		"my_images/train/images": "my_images/train/labels",
		"":                       "",
	} {
		got, ok := yoloLabelDir(dir)
		if got != want || ok != (want != "") {
			t.Errorf("yoloLabelDir(%q) = %q, %v; want %q", dir, got, ok, want)
		}
	}
}

func TestPairSidecars(t *testing.T) {
	images := []string{"a.jpg", "B.png", "sub/c.jpg", "images/train/d.jpg", "Images/val/e.jpg", "f.jpg", "f.png", "x/images/g.jpg"}
	labels := []string{"a.txt", "b.TXT", "sub/c.txt", "labels/train/d.txt", "labels/val/E.txt", "f.txt",
		"x/labels/g.txt", "x/g.txt", "orphan.txt"}
	got := pairSidecars(images, labels, ".txt", yoloLabelDir, nil)
	want := map[string]string{
		"a.jpg":              "a.txt",
		"B.png":              "b.TXT", // file name ignoring case
		"sub/c.jpg":          "sub/c.txt",
		"images/train/d.jpg": "labels/train/d.txt",
		"Images/val/e.jpg":   "labels/val/E.txt", // the whole path ignoring case
		"f.jpg":              "f.txt",            // f.png gets nothing: the first image in order wins
		"x/images/g.jpg":     "x/labels/g.txt",
	}
	if !maps.Equal(got, want) {
		t.Errorf("pairs = %v\nwant    %v", got, want)
	}

	// The same folder wins over the Ultralytics folder; without alt there is
	// no second rule.
	got = pairSidecars([]string{"images/a.jpg"}, []string{"images/a.txt", "labels/a.txt"}, ".txt", yoloLabelDir, nil)
	if got["images/a.jpg"] != "images/a.txt" {
		t.Errorf("same folder: %v", got)
	}
	if got = pairSidecars([]string{"images/a.jpg"}, []string{"labels/a.txt"}, ".txt", nil, nil); len(got) != 0 {
		t.Errorf("no alt: %v", got)
	}

	// Two images claiming one file: the recorded file name decides.
	got = pairSidecars([]string{"a.jpg", "a.png"}, []string{"a.json"}, ".json", nil,
		func(string) string { return "a.png" })
	if !maps.Equal(got, map[string]string{"a.png": "a.json"}) {
		t.Errorf("claims: %v", got)
	}
}

func TestParseXAnyLabeling(t *testing.T) {
	data := []byte(`{
	  "version": "4.2.3", "flags": {}, "imagePath": "..\\images\\a.jpg", "imageData": "aGVsbG8=",
	  "imageWidth": 100, "imageHeight": 50,
	  "shapes": [
	    {"label": "scratch", "points": [[10.4, 5], [30, 5], [30, 20.6], [10.4, 20.6]], "shape_type": "rectangle", "difficult": true},
	    {"label": "dent", "points": [[30, 40], [10, 10]], "shape_type": "rectangle"},
	    {"label": "poly", "points": [[50, 10], [60, 30], [40, 25]]},
	    {"label": "rot", "points": [[90, 0], [110, 10], [100, 30], [80, 20]], "shape_type": "rotation"},
	    {"label": "ring", "points": [[20, 20], [25, 20]], "shape_type": "circle"},
	    {"label": "pt", "points": [[1, 1]], "shape_type": "point"},
	    {"label": "ln", "points": [[1, 1], [5, 5]], "shape_type": "line"},
	    {"label": " ", "points": [[1, 1], [5, 5]], "shape_type": "rectangle"},
	    {"label": "bad", "points": [["x", 1], [5, 5]], "shape_type": "rectangle"},
	    {"label": "tiny", "points": [[1, 1], [1.2, 1.2]], "shape_type": "rectangle"},
	    {"label": "outside", "points": [[150, 1], [300, 5]], "shape_type": "rectangle"}
	  ]}`)
	f, err := parseXAnyLabeling(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.ImagePath != `..\images\a.jpg` || f.Width != 100 || f.Height != 50 || len(f.Shapes) != 11 {
		t.Errorf("file = %+v", f)
	}
	a := f.convert(imageInfo{}) // size from the file
	want := []AnnotationObject{
		{Name: "scratch", Xmin: 10, Ymin: 5, Xmax: 30, Ymax: 21, Difficult: true},
		{Name: "dent", Xmin: 10, Ymin: 10, Xmax: 30, Ymax: 40},
		{Name: "poly", Xmin: 40, Ymin: 10, Xmax: 60, Ymax: 30},
		{Name: "rot", Xmin: 80, Ymin: 0, Xmax: 100, Ymax: 30}, // clipped to the 100 px width
		{Name: "ring", Xmin: 15, Ymin: 15, Xmax: 25, Ymax: 25},
	}
	if !slices.Equal(a.Objects, want) || a.Skipped != 6 {
		t.Errorf("objects = %+v (skipped %d)\nwant      %+v", a.Objects, a.Skipped, want)
	}

	// The image's real size wins over the recorded one.
	a = f.convert(imageInfo{Width: 200, Height: 100})
	if a.Objects[3].Xmax != 110 || len(a.Objects) != 6 {
		t.Errorf("with image size: %+v", a.Objects)
	}

	// GBK text, a BOM, LabelMe's null shape_type (polygon).
	gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(`{"shapes": [{"label": "划痕", "points": [[1, 2], [3, 4], [1, 4]], "shape_type": null}], "imagePath": "a.jpg"}`))
	f, err = parseXAnyLabeling(gbk)
	if err != nil || len(f.Shapes) != 1 {
		t.Fatalf("GBK: %+v, %v", f, err)
	}
	if obj, ok := shapeBox(f.Shapes[0], 0, 0); !ok || obj.Name != "划痕" || obj.Xmax != 3 {
		t.Errorf("GBK shape = %+v, %v", obj, ok)
	}
	if _, err := parseXAnyLabeling([]byte("\xEF\xBB\xBF{\"shapes\": []}")); err != nil {
		t.Errorf("BOM: %v", err)
	}

	for _, bad := range []string{`[]`, `{"imagePath": "a.jpg"}`, `{"shapes": {}}`, `not json`, ``} {
		_, err := parseXAnyLabeling([]byte(bad))
		if !isAnnotationError(err) {
			t.Errorf("parseXAnyLabeling(%q) = %v, want an annotation error", bad, err)
		}
	}
}

func TestParseYOLO(t *testing.T) {
	classes := []string{"scratch", "dent", "划痕"}
	info := imageInfo{Width: 200, Height: 100}
	data := "0 0.5 0.5 0.2 0.4\n\n  1 0.1 0.1 0.2 0.2 0.93\r\n2 0.1 0.2 0.3 0.2 0.2 0.6\n1.0 0.995 0.5 0.02 0.1\n0 0.5 0.5 0 0.1\n"
	a, err := parseYOLO([]byte(data), "a.txt", classes, info)
	if err != nil {
		t.Fatal(err)
	}
	want := []AnnotationObject{
		{Name: "scratch", Xmin: 80, Ymin: 30, Xmax: 120, Ymax: 70},
		{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 40, Ymax: 20}, // clipped at 0; the confidence is ignored
		{Name: "划痕", Xmin: 20, Ymin: 20, Xmax: 60, Ymax: 60},
		{Name: "dent", Xmin: 197, Ymin: 45, Xmax: 200, Ymax: 55}, // clipped to the width
	}
	if !slices.Equal(a.Objects, want) || a.Skipped != 1 {
		t.Errorf("objects = %+v (skipped %d)\nwant      %+v", a.Objects, a.Skipped, want)
	}

	if a, err := parseYOLO(nil, "empty.txt", classes, imageInfo{}); err != nil || len(a.Objects) != 0 {
		t.Errorf("empty file = %+v, %v", a, err)
	}

	for _, bad := range []string{
		"0 0.5 0.5 0.2\n",                 // too few values
		"0 0.5 0.5 0.2 0.2 0.1 0.1 0.1\n", // 7 values: neither a box nor a polygon
		"x 0.5 0.5 0.2 0.2\n",
		"0.5 0.5 0.5 0.2 0.2\n",
		"-1 0.5 0.5 0.2 0.2\n",
		"0 0.5 0.5 nan 0.2\n",
		"0 120 50 30 20\n", // pixels, not normalized
		"0 0.5 0.5 -0.1 0.2\n",
	} {
		if _, err := parseYOLO([]byte(bad), "a.txt", classes, info); !isAnnotationError(err) {
			t.Errorf("parseYOLO(%q) = %v, want an annotation error", bad, err)
		}
	}
	if _, err := parseYOLO([]byte("0 0.5 0.5 0.2 0.2\n"), "a.txt", classes, imageInfo{}); !isAnnotationError(err) {
		t.Errorf("unknown image size: %v", err)
	}

	_, err = parseYOLO([]byte("0 0.5 0.5 0.2 0.2\n\n3 0.5 0.5 0.2 0.2\n"), "labels/a.txt", classes, info)
	var re *classRangeError
	if !errors.As(err, &re) || re.Error() != "标注文件 labels/a.txt 第 3 行的类别编号为 3，但只提供了 3 个类别（编号 0–2），请检查类别顺序后重新导入" {
		t.Errorf("class out of range: %v", err)
	}
	// The class id is checked before anything else can reject the file.
	for _, tc := range []struct {
		data string
		info imageInfo
	}{
		{"0 0.5 0.5 0.2 0.2\n7 0.5 0.5 0.1 0.1\n", imageInfo{}}, // size unknown
		{"0 0.5\n9 0.5 0.5 0.1 0.1\n", info},                    // earlier bad line
		{"5 0.5\n", info},
	} {
		if _, err := parseYOLO([]byte(tc.data), "a.txt", classes, tc.info); !errors.As(err, &re) {
			t.Errorf("parseYOLO(%q) = %v, want a class range error", tc.data, err)
		}
	}
}

func TestWriteVOCRoundTrip(t *testing.T) {
	objects := []AnnotationObject{
		{Name: "a&b <c>", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4, Difficult: true},
		{Name: "划痕", Xmin: 5, Ymin: 6, Xmax: 7, Ymax: 8, Truncated: true},
	}
	var b bytes.Buffer
	if err := writeVOC(&b, "train", "图 1.jpg", imageInfo{Width: 640, Height: 480, Depth: 1}, objects); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"<folder>train</folder>", "<depth>1</depth>", "a&amp;b &lt;c&gt;"} {
		if !strings.Contains(b.String(), part) {
			t.Errorf("missing %q in\n%s", part, b.String())
		}
	}
	a, err := parseVOC(&b)
	if err != nil {
		t.Fatal(err)
	}
	if a.Filename != "图 1.jpg" || a.Width != 640 || a.Height != 480 || !slices.Equal(a.Objects, objects) {
		t.Errorf("parsed = %+v", a)
	}

	b.Reset()
	_ = writeVOC(&b, ".", "a.png", imageInfo{}, nil)
	if strings.Contains(b.String(), "<folder>") || strings.Contains(b.String(), "<size>") {
		t.Errorf("top-level image of unknown size:\n%s", b.String())
	}
	if a, err := parseVOC(&b); err != nil || len(a.Objects) != 0 || a.Width != 0 {
		t.Errorf("parsed = %+v, %v", a, err)
	}
}

func TestUltralyticsLabelPath(t *testing.T) {
	for in, want := range map[string]string{
		"images/a.jpg":          "labels/a.txt",
		"images/train/a.b.png":  "labels/train/a.b.txt",
		"images/x/images/a.jpg": "images/x/labels/a.txt",
		"images/Images/a.jpg":   "labels/Images/a.txt", // Ultralytics matches the name exactly
	} {
		if got := ultralyticsLabelPath(in); got != want {
			t.Errorf("ultralyticsLabelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestYOLOLabels(t *testing.T) {
	w, h := 200, 100
	a := &vocAnnotation{Width: 50, Height: 50, Objects: []AnnotationObject{
		{Name: "a", Xmin: 80, Ymin: 30, Xmax: 120, Ymax: 70},
		{Name: "skip", Xmin: 0, Ymin: 0, Xmax: 10, Ymax: 10},
		{Name: "b", Xmin: 220, Ymin: 50, Xmax: 150, Ymax: -10}, // inverted, partly outside
		{Name: "b", Xmin: 250, Ymin: 0, Xmax: 260, Ymax: 10},   // outside
	}}
	got, ok := yoloLabels(a, exportImage{Width: &w, Height: &h}, map[string]int{"a": 0, "b": 1})
	want := "0 0.500000 0.500000 0.200000 0.400000\n1 0.875000 0.250000 0.250000 0.500000\n"
	if !ok || string(got) != want {
		t.Errorf("labels = %q, %v\nwant     %q", got, ok, want)
	}
	// The VOC size is used when the index has none.
	if got, ok := yoloLabels(a, exportImage{}, map[string]int{"skip": 0}); !ok || string(got) != "0 0.100000 0.100000 0.200000 0.200000\n" {
		t.Errorf("VOC size: %q, %v", got, ok)
	}
	if _, ok := yoloLabels(&vocAnnotation{}, exportImage{}, nil); ok {
		t.Error("unknown size should not give labels")
	}
}

func TestYOLODataYAML(t *testing.T) {
	imgs := func(paths ...string) []exportImage {
		out := make([]exportImage, len(paths))
		for i, p := range paths {
			out[i] = exportImage{Path: p}
		}
		return out
	}
	classes := []string{"scratch", `say "hi"`}
	y := string(yoloDataYAML("ds", classes, imgs("images/train/a.jpg", "images/val/b.jpg", "images/test/c.jpg"), ""))
	for _, part := range []string{"train: images/train\n", "val: images/val\n", "test: images/test\n", "nc: 2\n",
		"  0: \"scratch\"\n", `  1: "say \"hi\""` + "\n"} {
		if !strings.Contains(y, part) {
			t.Errorf("missing %q in\n%s", part, y)
		}
	}
	y = string(yoloDataYAML("ds", classes, imgs("train/a.jpg", "valid/b.jpg"), "images/"))
	if !strings.Contains(y, "val: images/valid\n") || strings.Contains(y, "test:") {
		t.Errorf("valid split:\n%s", y)
	}
	for _, paths := range [][]string{{"train/a.jpg", "b.jpg", "val/c.jpg"}, {"train/a.jpg"}, {"train/a.jpg", "val/b.jpg", "extra/c.jpg"}} {
		y = string(yoloDataYAML("ds", classes, imgs(paths...), "images/"))
		if !strings.Contains(y, "train: images\nval: images\n") {
			t.Errorf("%v: want no split\n%s", paths, y)
		}
	}
}

func TestYOLODataYAMLFolderNameNewline(t *testing.T) {
	y := string(yoloDataYAML("ds\npath: /elsewhere", []string{"a"}, nil, ""))
	for _, line := range strings.Split(y, "\n") {
		if strings.HasPrefix(line, "path:") {
			t.Errorf("folder name starts a YAML key:\n%s", y)
		}
	}
}

// Names that differ only in "\\" and "_" map to the same entry; the archive
// must not hold it twice.
func TestExportUniqueEntryNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{`x\1.jpg`, "x_1.jpg", `x\1.xml`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ann := `x\1.xml`
	e := &Export{root: root, top: "ds", format: FormatVOC, entries: map[string]bool{},
		images: []exportImage{{Path: `x\1.jpg`, AnnPath: &ann}, {Path: "x_1.jpg"}},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil))}
	var b bytes.Buffer
	if err := e.Write(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if want := []string{"ds/x_1.jpg", "ds/x_1.xml"}; !slices.Equal(names, want) {
		t.Errorf("entries = %q, want %q", names, want)
	}
	if e.written != 1 || e.missing != 1 {
		t.Errorf("written %d, missing %d", e.written, e.missing)
	}
}

func TestXAnyLabelingJSON(t *testing.T) {
	w, h := 64, 48
	a := &vocAnnotation{Objects: []AnnotationObject{{Name: "<划痕>", Xmin: 30, Ymin: 2, Xmax: 10, Ymax: 20, Difficult: true}}}
	data, err := xAnyLabelingJSON("a.png", a, exportImage{Width: &w, Height: &h})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"checked", "description", "flags", "imageData", "imageHeight", "imagePath", "imageWidth", "shapes", "version"}
	if got := slices.Sorted(maps.Keys(doc)); !slices.Equal(got, wantKeys) {
		t.Errorf("keys = %v", got)
	}
	if doc["imageData"] != nil || doc["imagePath"] != "a.png" || doc["imageWidth"] != 64.0 || doc["imageHeight"] != 48.0 {
		t.Errorf("doc = %v", doc)
	}
	if !strings.Contains(string(data), `"label": "<划痕>"`) ||
		!strings.Contains(string(data), `"points": [
        [
          10,
          2
        ],`) {
		t.Errorf("json:\n%s", data)
	}
	shape := doc["shapes"].([]any)[0].(map[string]any)
	if shape["shape_type"] != "rectangle" || shape["difficult"] != true || len(shape["points"].([]any)) != 4 {
		t.Errorf("shape = %v", shape)
	}
	// The result reads back as the same box.
	f, err := parseXAnyLabeling(data)
	if err != nil {
		t.Fatal(err)
	}
	if obj, ok := shapeBox(f.Shapes[0], f.Width, f.Height); !ok || obj != (AnnotationObject{Name: "<划痕>", Xmin: 10, Ymin: 2, Xmax: 30, Ymax: 20, Difficult: true}) {
		t.Errorf("round trip = %+v", obj)
	}

	data, _ = xAnyLabelingJSON("b.avif", &vocAnnotation{}, exportImage{})
	if !strings.Contains(string(data), `"imageHeight": -1`) || !strings.Contains(string(data), `"shapes": []`) {
		t.Errorf("unknown size, no objects:\n%s", data)
	}
}
