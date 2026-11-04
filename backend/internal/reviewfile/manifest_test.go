package reviewfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

func intPtr(v int) *int { return &v }

func testManifest() *Manifest {
	reviewed := time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)
	return &Manifest{
		Format: ManifestFormat, Version: ManifestVersion, Kind: KindPackage, PackageID: "0123456789abcdef0123456789abcdef",
		Platform: "trainMaster 0.1.0-dev (d62132f)", ExportedAt: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
		Review: ManifestReview{ID: 7, Name: "A 线 v3 审图", Description: "<夜班> & 白班", CreatedAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)},
		Model: ModelRef{ID: 3, ProjectID: 1, ProjectName: "A 线", Version: 3, YOLOVersion: "yolo11", Task: "detect",
			FileName: "best.pt", ClassNames: []string{"scratch", "dent"}},
		Options:  Options{Conf: 0.25, IoU: 0.7, ImgSize: [2]int{640, 640}, MaxDet: 300},
		Classes:  []Class{{Name: "scratch", Color: "#ff0000"}, {Name: "dent"}},
		Datasets: []ManifestDataset{{ID: 4, Name: "line-a", Folder: "line-a"}, {ID: 5, Name: "LINE-A", Folder: "LINE-A~5"}},
		Images: []ManifestImage{
			{ID: 11, DatasetID: 4, ImageID: 101, Path: "line-a/NG/a.jpg", SourcePath: "NG/a.jpg", Annotation: "line-a/NG/a.xml",
				HasAnnotation: true, Width: intPtr(64), Height: intPtr(48),
				Predictions: []Prediction{{Name: "scratch", Confidence: 0.91, Xmin: 1, Ymin: 2, Xmax: 30, Ymax: 40}},
				BaseStatus:  StatusPending, BaseRevision: 1, BaseAnnotationVersion: "abc", Status: StatusApproved,
				ReviewedAt: &reviewed, Edited: true},
			{ID: 12, DatasetID: 5, ImageID: 102, Path: "LINE-A~5/b.png", SourcePath: "b.png", Annotation: "LINE-A~5/b.xml",
				InferenceError: "无法读取图片", BaseStatus: StatusNeedsSample, Status: StatusNeedsSample},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := testManifest()
	var b bytes.Buffer
	if err := WriteManifest(&b, m); err != nil {
		t.Fatal(err)
	}
	text := b.String()
	// Indented, one image per line, nothing escaped.
	if !strings.HasPrefix(text, "{\n  \"format\": \"trainmaster-review\",\n") ||
		strings.Count(text, "\n    {\"id\":") != 2 || !strings.Contains(text, `"<夜班> & 白班"`) ||
		!strings.HasSuffix(text, "}\n  ]\n}\n") {
		t.Errorf("review.json =\n%s", text)
	}
	// The second image has no predictions: [] rather than null.
	if !strings.Contains(text, `"predictions":[],"inference_error":"无法读取图片"`) {
		t.Errorf("review.json =\n%s", text)
	}
	var generic map[string]any
	if err := json.Unmarshal(b.Bytes(), &generic); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	got, err := ReadManifest(bytes.NewReader(b.Bytes()), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	m.Images[1].Predictions = []Prediction{}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("ReadManifest =\n%+v\nwant\n%+v", got, m)
	}

	// Empty lists.
	b.Reset()
	if err := WriteManifest(&b, &Manifest{Format: ManifestFormat, Version: 1, Kind: KindResults, Review: ManifestReview{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.String(), "\"images\": []\n}\n") || !strings.Contains(b.String(), `"datasets": []`) {
		t.Errorf("empty review.json =\n%s", b.String())
	}
	if m, err := ReadManifest(&b, 1<<20); err != nil || m.Kind != KindResults || len(m.Images) != 0 {
		t.Errorf("ReadManifest(empty) = %+v, %v", m, err)
	}
}

func TestReadManifestRejects(t *testing.T) {
	encode := func(m *Manifest) []byte {
		var b bytes.Buffer
		if err := WriteManifest(&b, m); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	for _, tt := range []struct {
		name   string
		change func(m *Manifest)
		msg    string
	}{
		{"format", func(m *Manifest) { m.Format = "labelme" }, "不是 trainMaster 的审图包"},
		{"newer version", func(m *Manifest) { m.Version = 2 }, "比这个程序支持的版本（1）新"},
		{"version 0", func(m *Manifest) { m.Version = 0 }, "格式版本 0 无效"},
		{"kind", func(m *Manifest) { m.Kind = "backup" }, "kind 必须是"},
		{"review id", func(m *Manifest) { m.Review.ID = 0 }, "缺少审图任务 ID"},
		{"dataset id", func(m *Manifest) { m.Datasets[0].ID = -1 }, "第 1 个数据集的 ID 无效"},
		{"dataset twice", func(m *Manifest) { m.Datasets[1].ID = 4 }, "数据集 4 出现了多次"},
		{"folder with slash", func(m *Manifest) { m.Datasets[0].Folder = "a/b" }, "文件夹名称无效"},
		{"reserved folder", func(m *Manifest) { m.Datasets[0].Folder = "Review.json" }, "文件夹名称无效"},
		{"folder case", func(m *Manifest) { m.Datasets[1].Folder = "Line-A" }, "出现了多次"},
		{"image id", func(m *Manifest) { m.Images[0].ID = 0 }, "第 1 张图片的 ID 无效"},
		{"image twice", func(m *Manifest) { m.Images[1].ID = 11 }, "图片 11 出现了多次"},
		{"unknown dataset", func(m *Manifest) { m.Images[1].DatasetID = 9 }, "数据集 9 不在 datasets 中"},
		{"status", func(m *Manifest) { m.Images[0].Status = "done" }, "审核状态无效"},
		{"base status", func(m *Manifest) { m.Images[0].BaseStatus = "" }, "审核状态无效"},
		{"revision", func(m *Manifest) { m.Images[0].BaseRevision = -1 }, "base_revision 无效"},
		{"size", func(m *Manifest) { m.Images[0].Width = intPtr(0) }, "尺寸无效"},
		{"source", func(m *Manifest) { m.Images[0].SourcePath = "" }, "缺少 source_path"},
		{"escaping path", func(m *Manifest) { m.Images[0].Path = "line-a/../../etc/passwd" }, "路径无效"},
		{"absolute path", func(m *Manifest) { m.Images[0].Annotation = "/etc/a.xml" }, "路径无效"},
		{"other folder", func(m *Manifest) { m.Images[0].Path = "LINE-A~5/a.jpg" }, "路径无效"},
		{"windows path", func(m *Manifest) { m.Images[0].Path = `line-a\a.jpg` }, "路径无效"},
		{"same file", func(m *Manifest) { m.Images[1].Path = "line-a~5/B.PNG"; m.Images[1].Annotation = "line-a~5/B.PNG" }, "出现了多次"},
	} {
		m := testManifest()
		tt.change(m)
		_, err := ReadManifest(bytes.NewReader(encode(m)), 1<<20)
		var me *ManifestError
		if !errors.As(err, &me) || !strings.Contains(me.Message, tt.msg) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.msg)
		}
	}
	for name, data := range map[string]string{
		"not JSON": "PK\x03\x04",
		"array":    "[]",
		"empty":    "",
	} {
		if _, err := ReadManifest(strings.NewReader(data), 1<<20); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	data := encode(testManifest())
	_, err := ReadManifest(bytes.NewReader(data), int64(len(data)-1))
	if err == nil || !strings.Contains(err.Error(), "过大") {
		t.Errorf("oversized: %v", err)
	}
	// Fields of newer writers are ignored.
	extra := bytes.Replace(data, []byte(`"kind"`), []byte(`"future": {"x": 1}, "kind"`), 1)
	if _, err := ReadManifest(bytes.NewReader(extra), 1<<20); err != nil {
		t.Errorf("unknown field: %v", err)
	}
}

func TestAnnotationHelpers(t *testing.T) {
	preds := []Prediction{
		{Name: "dent", Confidence: 0.5, Xmin: 5, Ymin: 5, Xmax: 9, Ymax: 9},
		{Name: "scratch", Confidence: 0.9, Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4},
		{Name: "dent", Confidence: 0.4, Xmin: 5, Ymin: 5, Xmax: 9, Ymax: 9},
	}
	objects := PredictionObjects(preds)
	if !SameBoxes(objects, preds) {
		t.Error("the predictions are not their own boxes")
	}
	reordered := []dataset.AnnotationObject{objects[2], objects[1], objects[0]}
	reordered[0].Difficult, reordered[1].Truncated = true, true
	if !SameBoxes(reordered, preds) {
		t.Error("order and flags should not count")
	}
	for name, change := range map[string]func(o []dataset.AnnotationObject) []dataset.AnnotationObject{
		"moved":   func(o []dataset.AnnotationObject) []dataset.AnnotationObject { o[0].Xmax++; return o },
		"renamed": func(o []dataset.AnnotationObject) []dataset.AnnotationObject { o[1].Name = "Scratch"; return o },
		"removed": func(o []dataset.AnnotationObject) []dataset.AnnotationObject { return o[:2] },
		"duplicate swapped": func(o []dataset.AnnotationObject) []dataset.AnnotationObject {
			o[2] = o[1]
			return o
		},
	} {
		if SameBoxes(change(PredictionObjects(preds)), preds) {
			t.Errorf("%s: SameBoxes = true", name)
		}
	}
	if !SameBoxes(nil, nil) || !SameBoxes([]dataset.AnnotationObject{}, nil) {
		t.Error("no boxes are the same as no predictions")
	}
	if c := LabelCounts(objects); !reflect.DeepEqual(c, map[string]int{"dent": 2, "scratch": 1}) {
		t.Errorf("LabelCounts = %v", c)
	}

	data := EncodeAnnotation("NG/0001.jpg", intPtr(64), intPtr(48), objects)
	f, err := DecodeAnnotation(data)
	if err != nil || f.Filename != "0001.jpg" || f.Width != 64 || !reflect.DeepEqual(f.Objects, objects) ||
		!strings.Contains(string(data), "<folder>NG</folder>") {
		t.Errorf("EncodeAnnotation =\n%s\n%+v, %v", data, f, err)
	}
	top := EncodeAnnotation("a.png", nil, nil, nil)
	if strings.Contains(string(top), "<folder>") || strings.Contains(string(top), "<size>") {
		t.Errorf("EncodeAnnotation(top level, no size) =\n%s", top)
	}
	if AnnotationVersion(data) == AnnotationVersion(top) || AnnotationVersion(data) != dataset.AnnotationVersion(data) {
		t.Error("AnnotationVersion")
	}
	for _, s := range []string{StatusPending, StatusApproved, StatusNeedsSample} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false", s)
		}
	}
	if ValidStatus("") || ValidStatus("reviewed") {
		t.Error("ValidStatus accepts other values")
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := WriteFile(root, "ds/NG/a.xml", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "ds/NG/a.xml", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadAnnotation(root, "ds/NG/a.xml"); err != nil || string(data) != "two" {
		t.Errorf("ReadAnnotation = %q, %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "ds", "NG"))
	if len(entries) != 1 {
		t.Errorf("leftover temporary files: %v", entries)
	}

	tmp, err := StageFile(root, "ds/b.xml", []byte("staged"))
	if err != nil || !IsTempName(filepath.Base(tmp)) || !strings.HasPrefix(tmp, "ds/") {
		t.Fatalf("StageFile = %q, %v", tmp, err)
	}
	if _, err := ReadAnnotation(root, "ds/b.xml"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged file visible: %v", err)
	}
	if err := Rename(root, tmp, "ds/b.xml"); err != nil {
		t.Fatal(err)
	}
	if data, _ := ReadAnnotation(root, "ds/b.xml"); string(data) != "staged" {
		t.Errorf("after Rename: %q", data)
	}

	// Oversized files are cut one byte after the limit.
	big := bytes.Repeat([]byte("x"), dataset.MaxAnnotationBytes+10)
	if err := WriteFile(root, "big.xml", big); err != nil {
		t.Fatal(err)
	}
	if data, _ := ReadAnnotation(root, "big.xml"); len(data) != dataset.MaxAnnotationBytes+1 {
		t.Errorf("ReadAnnotation(big) = %d bytes", len(data))
	}

	// A file or a symbolic link in the way of a folder.
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "file/a.xml", []byte("x")); !errors.Is(err, ErrNotFolder) {
		t.Errorf("WriteFile under a file: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err == nil {
		if err := WriteFile(root, "link/a.xml", []byte("x")); !errors.Is(err, ErrNotFolder) {
			t.Errorf("WriteFile through a link: %v", err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("wrote outside of the root: %v", entries)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAnnotation(root, "folder.xml"); err == nil {
		t.Error("ReadAnnotation of a folder succeeded")
	}
}

func TestManifestPrefix(t *testing.T) {
	for _, tt := range []struct {
		names  []string
		prefix string
		err    string
	}{
		{[]string{"review.json", "a/b.png"}, "", ""},
		{[]string{"审图-1-x/", "审图-1-x/review.json", "审图-1-x/a/b.xml"}, "审图-1-x/", ""},
		// Zipped again, with the folder it was extracted to.
		{[]string{"out/审图-1-x/review.json", "out/审图-1-x/a/b.xml"}, "out/审图-1-x/", ""},
		// The shallowest wins; macOS metadata and folders do not count.
		{[]string{"__MACOSX/review.json", "x/review.json/", "a/review.json", "a/b/review.json"}, "a/", ""},
		{[]string{"a/b/c/review.json"}, "a/b/c/", ""},
		{[]string{"a/b/c/d/review.json"}, "", "没有 review.json"},
		{[]string{"a/review-progress.json", "a.zip"}, "", "没有 review.json"},
		{nil, "", "没有 review.json"},
		{[]string{"b/review.json", "a/review.json", "c/d/review.json"}, "", "a/review.json 和 b/review.json"},
	} {
		prefix, err := ManifestPrefix(tt.names)
		var me *ManifestError
		switch {
		case tt.err == "" && (err != nil || prefix != tt.prefix):
			t.Errorf("ManifestPrefix(%q) = %q, %v; want %q", tt.names, prefix, err, tt.prefix)
		case tt.err != "" && (!errors.As(err, &me) || !strings.Contains(me.Message, tt.err)):
			t.Errorf("ManifestPrefix(%q) error = %v, want %q", tt.names, err, tt.err)
		}
	}
}

func TestManifestWriter(t *testing.T) {
	// Written one image at a time, review.json is what WriteManifest writes.
	m := testManifest()
	var whole, streamed bytes.Buffer
	if err := WriteManifest(&whole, m); err != nil {
		t.Fatal(err)
	}
	head := *m
	head.Images = []ManifestImage{{ID: 999}} // ignored
	mw, err := NewManifestWriter(&streamed, &head)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Images {
		if err := mw.Add(&m.Images[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	if whole.String() != streamed.String() {
		t.Errorf("streamed review.json =\n%s\nwant\n%s", streamed.String(), whole.String())
	}
	if m.Images[1].Predictions != nil {
		t.Error("Add changed the image")
	}
	if err := mw.Add(&m.Images[0]); err == nil {
		t.Error("Add after Close succeeded")
	}
}

func TestSameObjectsAndPackageFolder(t *testing.T) {
	a := []dataset.AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}, {Name: "scratch", Xmax: 9, Ymax: 9, Difficult: true}}
	b := []dataset.AnnotationObject{a[1], a[0]}
	if !SameObjects(a, b) || !SameObjects(nil, []dataset.AnnotationObject{}) {
		t.Error("SameObjects is false for the same objects in another order")
	}
	c := slices.Clone(b)
	c[0].Difficult = false
	d := slices.Clone(b)
	d[1].Xmax = 6
	if SameObjects(a, c) || SameObjects(a, d) || SameObjects(a, a[:1]) || SameObjects(a, []dataset.AnnotationObject{a[0], a[0]}) {
		t.Error("SameObjects is true for different objects")
	}
	if b[0].Name != "scratch" {
		t.Error("SameObjects reordered its input")
	}
	for _, tt := range []struct {
		id   int64
		name string
		want string
	}{
		{12, "外观检测 v3 审图", "审图-12-外观检测 v3 审图"},
		{3, `a/b:c*d?`, "审图-3-a_b_c_d_"},
		{4, "夜班.", "审图-4-夜班_"},
	} {
		if got := PackageFolder(tt.id, tt.name); got != tt.want || !ValidRelPath(got) {
			t.Errorf("PackageFolder(%d, %q) = %q, want %q", tt.id, tt.name, got, tt.want)
		}
	}
}
