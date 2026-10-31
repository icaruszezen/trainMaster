package dataset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeObjects(t *testing.T) {
	box := func(name string, x0, y0, x1, y1 int) AnnotationObject {
		return AnnotationObject{Name: name, Xmin: x0, Ymin: y0, Xmax: x1, Ymax: y1}
	}
	got, err := normalizeObjects([]AnnotationObject{
		box("  划痕 ", 1, 2, 30, 40),
		{Name: "dent", Xmin: -5, Ymin: -1, Xmax: 120, Ymax: 90, Difficult: true, Truncated: true}, // clamped to 100×80
		box("edge", 99, 79, 100, 80),
	}, 100, 80)
	want := []AnnotationObject{
		box("划痕", 1, 2, 30, 40),
		{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 100, Ymax: 80, Difficult: true, Truncated: true},
		box("edge", 99, 79, 100, 80),
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeObjects = %+v, %v; want %+v", got, err, want)
	}
	if got, err := normalizeObjects(nil, 0, 0); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no objects = %#v, %v; want empty, nil", got, err)
	}
	// Unknown image size: only negative coordinates are clamped.
	if got, err := normalizeObjects([]AnnotationObject{box("a", -3, 0, 5000, 4000)}, 0, 0); err != nil ||
		got[0].Xmin != 0 || got[0].Xmax != 5000 || got[0].Ymax != 4000 {
		t.Errorf("unknown size = %+v, %v", got, err)
	}
	// ... but they must stay within what a VOC file is read back with.
	if got, err := normalizeObjects([]AnnotationObject{box("max", 0, 0, math.MaxInt32, math.MaxInt32)}, 0, 0); err != nil ||
		got[0].Xmax != math.MaxInt32 {
		t.Errorf("MaxInt32 = %+v, %v", got, err)
	}
	_, err = normalizeObjects([]AnnotationObject{box("a", 1, 1, 2, 2), box("huge", 1, 1, 3_000_000_000, 5)}, 0, 0)
	wantKind(t, err, ErrInvalid, "第 2 个目标（huge）的坐标超出范围（不能大于 2147483647）")
	_, err = normalizeObjects([]AnnotationObject{box("huge", 1, math.MaxInt32+1, 3, math.MaxInt32+2)}, 0, 0)
	wantKind(t, err, ErrInvalid, "坐标超出范围")
	// A known size clamps them first.
	if got, err := normalizeObjects([]AnnotationObject{box("huge", 1, 1, 3_000_000_000, 5)}, 100, 80); err != nil || got[0].Xmax != 100 {
		t.Errorf("clamped huge box = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		objects []AnnotationObject
		msg     string
	}{
		{[]AnnotationObject{box(" ", 1, 1, 2, 2)}, "第 1 个目标没有类别"},
		{[]AnnotationObject{box("a", 1, 1, 2, 2), box("a\nb", 1, 1, 2, 2)}, "第 2 个目标的类别名称包含换行或控制字符"},
		{[]AnnotationObject{box("a\u0085b", 1, 1, 2, 2)}, "控制字符"},
		{[]AnnotationObject{box(strings.Repeat("长", 256), 1, 1, 2, 2)}, "不能超过 255 个字符"},
		{[]AnnotationObject{box("flat", 5, 5, 9, 5)}, "第 1 个目标（flat）的框在图片范围内的宽或高为 0"},
		{[]AnnotationObject{box("inverted", 9, 5, 5, 9)}, "宽或高为 0"},
		{[]AnnotationObject{box("outside", 120, 10, 150, 20)}, "宽或高为 0"}, // entirely right of a 100 px image
		{make([]AnnotationObject, maxSavedObjects+1), "最多保存 10000 个目标"},
	} {
		_, err := normalizeObjects(tc.objects, 100, 80)
		wantKind(t, err, ErrInvalid, tc.msg)
	}
	if _, err := normalizeObjects([]AnnotationObject{box(strings.Repeat("长", 255), 1, 1, 2, 2)}, 100, 80); err != nil {
		t.Errorf("255-character label: %v", err)
	}
}

func openTestRoot(t *testing.T, files map[string][]byte, dirs ...string) *os.Root {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestDefaultAnnotationPath(t *testing.T) {
	root := openTestRoot(t, map[string][]byte{"voc/annotations.txt": nil, "voc2/Annotations": []byte("a file, not a folder")},
		"VOC2007/JPEGImages", "VOC2007/Annotations", "lower/images", "lower/annotations", "voc/JPEGImages", "voc2/JPEGImages",
		"JPEGImages", "Annotations", "NG")
	for rel, want := range map[string]string{
		"a.jpg":                       "a.xml",
		"NG/0001.JPG":                 "NG/0001.xml",
		"NG/a.b.png":                  "NG/a.b.xml",
		"VOC2007/JPEGImages/0001.jpg": "VOC2007/Annotations/0001.xml",
		"lower/images/x.png":          "lower/annotations/x.xml",
		"JPEGImages/top.jpg":          "Annotations/top.xml",      // the dataset folder itself is the VOC root
		"voc/JPEGImages/a.jpg":        "voc/JPEGImages/a.xml",     // no Annotations folder next to it
		"voc2/JPEGImages/a.jpg":       "voc2/JPEGImages/a.xml",    // "Annotations" is a file
		"missing/JPEGImages/a.jpg":    "missing/JPEGImages/a.xml", // the parent does not exist
		"NG/JPEGImagesX/a.jpg":        "NG/JPEGImagesX/a.xml",     // not a VOC image folder
		"VOC2007/Annotations/odd.jpg": "VOC2007/Annotations/odd.xml",
	} {
		if got := defaultAnnotationPath(root, rel); got != want {
			t.Errorf("defaultAnnotationPath(%q) = %q, want %q", rel, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		// A symbolic link named Annotations is not a folder to write into.
		linked := openTestRoot(t, nil, "set/JPEGImages", "elsewhere")
		if err := os.Symlink(filepath.Join(linked.Name(), "elsewhere"), filepath.Join(linked.Name(), "set", "Annotations")); err != nil {
			t.Fatal(err)
		}
		if got := defaultAnnotationPath(linked, "set/JPEGImages/a.jpg"); got != "set/JPEGImages/a.xml" {
			t.Errorf("Annotations symlink: %q", got)
		}
	}
}

func TestAnnotationCandidate(t *testing.T) {
	root := openTestRoot(t, map[string][]byte{
		"a.xml": nil, "NG/c.xml": nil,
		"VOC/JPEGImages/d.xml": nil, "VOC/Annotations/d.xml": nil, // the image's folder before Annotations
		"VOC/images/f.png": nil, "VOC/Annotations/f.xml": nil,
		"other/JPEGImagesX/g.jpg": nil, "other/Annotations/g.xml": nil, // not a VOC image folder
		"dir/h.xml/x": nil, // a folder named like the file does not count
	}, "VOC/JPEGImages", "empty")
	check := func(root *os.Root, cases map[string]string) {
		t.Helper()
		for rel, want := range cases {
			if got := annotationCandidate(root, rel); got != want {
				t.Errorf("annotationCandidate(%q) = %q, want %q", rel, got, want)
			}
		}
	}
	check(root, map[string]string{
		"a.jpg":                   "a.xml",
		"NG/c.png":                "NG/c.xml",
		"VOC/JPEGImages/d.jpg":    "VOC/JPEGImages/d.xml",
		"VOC/images/f.png":        "VOC/Annotations/f.xml",
		"other/JPEGImagesX/g.jpg": "",
		"dir/h.png":               "",
		"empty/x.png":             "",
		"missing/x.png":           "",
	})

	if caseSensitive(t) {
		// Names that differ in case only: the exact name first, then the
		// first in byte order, as pairAnnotations does.
		check(openTestRoot(t, map[string][]byte{
			"a.xml": nil, "b.XML": nil, "B.xml": nil,
			"NG/c.xml": nil, "NG/C.Xml": nil,
			"VOC/Annotations/E.xml": nil, "VOC/annotations/e.xml": nil,
			"dir/h.xml/x": nil, "dir/H.xml": nil,
		}, "VOC/JPEGImages"), map[string]string{
			"A.jpg":                "a.xml",
			"b.jpg":                "B.xml",
			"NG/c.png":             "NG/c.xml",
			"NG/C.png":             "NG/C.Xml",
			"VOC/JPEGImages/e.jpg": "VOC/Annotations/E.xml",
			"dir/h.png":            "dir/H.xml",
		})
	}
	if runtime.GOOS != "windows" {
		// A scan does not follow symbolic links.
		linked := openTestRoot(t, map[string][]byte{"set/real/a.xml": nil, "set/JPEGImages/b.xml": nil}, "set/JPEGImages")
		if err := os.Symlink(filepath.Join(linked.Name(), "set", "real"), filepath.Join(linked.Name(), "set", "Annotations")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(linked.Name(), "set", "JPEGImages", "b.xml"), filepath.Join(linked.Name(), "set", "JPEGImages", "c.xml")); err != nil {
			t.Fatal(err)
		}
		for rel, want := range map[string]string{"set/JPEGImages/a.jpg": "", "set/JPEGImages/c.jpg": ""} {
			if got := annotationCandidate(linked, rel); got != want {
				t.Errorf("annotationCandidate(%q) = %q, want %q", rel, got, want)
			}
		}
	}
}

// caseSensitive reports whether the file system of the temporary folders
// tells names apart that differ in case only.
func caseSensitive(t *testing.T) bool {
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{"a": nil})
	_, err := os.Lstat(filepath.Join(dir, "A"))
	return errors.Is(err, fs.ErrNotExist)
}

func TestAnnotationFileChangeUndo(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	root := openTestRoot(t, map[string][]byte{"a.xml": []byte("old"), "d.xml": []byte("doomed")})
	dir := root.Name()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, "d.xml"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if errors.Is(err, fs.ErrNotExist) {
			return "<none>"
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	hidden := func() []string {
		t.Helper()
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		return names
	}

	// A replacement is staged, applied, then undone: the old bytes are back.
	c := &annotationFileChange{root: root, rel: "a.xml", data: []byte("new"), existed: true, previous: []byte("old")}
	fi, err := c.stage()
	if err != nil || fi.Size() != 3 || read("a.xml") != "old" {
		t.Fatalf("stage = %v, %v; a.xml = %q", fi, err, read("a.xml"))
	}
	if err := c.apply(); err != nil || read("a.xml") != "new" {
		t.Fatalf("apply = %v; a.xml = %q", err, read("a.xml"))
	}
	if after, err := os.Lstat(filepath.Join(dir, "a.xml")); err != nil || !after.ModTime().Equal(fi.ModTime()) {
		t.Errorf("modification time %v after the rename, %v staged (%v)", after.ModTime(), fi.ModTime(), err)
	}
	c.undo(log)
	if read("a.xml") != "old" || len(hidden()) != 0 {
		t.Errorf("after undo: a.xml = %q, hidden files %v", read("a.xml"), hidden())
	}

	// Undone before it was applied: only the staged file goes.
	c = &annotationFileChange{root: root, rel: "a.xml", data: []byte("new"), existed: true, previous: []byte("old")}
	if _, err := c.stage(); err != nil {
		t.Fatal(err)
	}
	c.undo(log)
	if read("a.xml") != "old" || len(hidden()) != 0 {
		t.Errorf("undo of a staged change: a.xml = %q, hidden files %v", read("a.xml"), hidden())
	}

	// A new file is removed again; one changed meanwhile stays.
	c = &annotationFileChange{root: root, rel: "n.xml", data: []byte("new")}
	if _, err := c.stage(); err != nil || c.apply() != nil {
		t.Fatal("new file not written")
	}
	c.undo(log)
	if read("n.xml") != "<none>" {
		t.Errorf("new file after undo = %q", read("n.xml"))
	}
	c = &annotationFileChange{root: root, rel: "n.xml", data: []byte("new")}
	if _, err := c.stage(); err != nil || c.apply() != nil {
		t.Fatal("new file not written")
	}
	writeFiles(t, dir, map[string][]byte{"n.xml": []byte("someone else")})
	c.undo(log)
	if read("n.xml") != "someone else" {
		t.Errorf("a file changed meanwhile was reverted: %q", read("n.xml"))
	}

	// A removal moves the file aside; undo moves it back as it was.
	before, _ := os.Lstat(filepath.Join(dir, "d.xml"))
	c = &annotationFileChange{root: root, rel: "d.xml", existed: true, previous: []byte("doomed")}
	if err := c.apply(); err != nil || read("d.xml") != "<none>" {
		t.Fatalf("apply removal = %v; d.xml = %q", err, read("d.xml"))
	}
	c.undo(log)
	after, err := os.Lstat(filepath.Join(dir, "d.xml"))
	if err != nil || read("d.xml") != "doomed" || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() || len(hidden()) != 0 {
		t.Errorf("after undoing the removal: %v %v, %v; hidden files %v", after, err, before, hidden())
	}
	// Committed: the file is gone for good.
	c = &annotationFileChange{root: root, rel: "d.xml", existed: true, previous: []byte("doomed")}
	if err := c.apply(); err != nil {
		t.Fatal(err)
	}
	c.finish(log)
	if read("d.xml") != "<none>" || len(hidden()) != 0 {
		t.Errorf("after the removal: d.xml = %q, hidden files %v", read("d.xml"), hidden())
	}
}

func TestWriteAnnotationFile(t *testing.T) {
	root := openTestRoot(t, map[string][]byte{"NG/a.xml": []byte("old")}, "OK")
	dir := root.Name()
	if err := os.Chmod(filepath.Join(dir, "NG", "a.xml"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := writeAnnotationFile(root, "NG/a.xml", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := writeAnnotationFile(root, "OK/b.xml", []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	// Missing folders are created (e.g. a paired Annotations/ file whose folder was removed).
	if err := writeAnnotationFile(root, "Annotations/c.xml", []byte("c")); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"NG/a.xml": "new", "OK/b.xml": "fresh", "Annotations/c.xml": "c"} {
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", rel, data, err, want)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "NG", "a.xml")); fi.Mode().Perm() != 0o664 {
			t.Errorf("replaced file mode = %v, want the old 0664", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Join(dir, "OK", "b.xml")); fi.Mode().Perm() != 0o644 {
			t.Errorf("new file mode = %v, want 0644", fi.Mode().Perm())
		}
	}
	// No temporary file is left behind.
	for _, sub := range []string{"NG", "OK", "Annotations"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				t.Errorf("left %s/%s behind", sub, e.Name())
			}
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	wantKind(t, writeAnnotationFile(root, "link/a.xml", []byte("x")), ErrConflict, "不是普通文件夹")
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote through a symbolic link: %v", entries)
	}
}

func TestReadVersionedAnnotation(t *testing.T) {
	root := openTestRoot(t, map[string][]byte{
		"a.xml":      vocFile("a.png", "dent"),
		"same.xml":   vocFile("a.png", "dent"),
		"other.xml":  vocFile("a.png", "scratch"),
		"broken.xml": []byte("<annotation><object>"),
	})
	a, v, data, err := readVersionedAnnotation(root, "a.xml")
	if err != nil || len(a.Objects) != 1 || len(v) != 32 || !bytes.Equal(data, vocFile("a.png", "dent")) {
		t.Fatalf("a.xml = %+v, %q, %q, %v", a, v, data, err)
	}
	if _, same, _, _ := readVersionedAnnotation(root, "same.xml"); same != v {
		t.Errorf("identical content has version %q, want %q", same, v)
	}
	if _, other, _, _ := readVersionedAnnotation(root, "other.xml"); other == v {
		t.Error("different content has the same version")
	}
	if a, broken, _, err := readVersionedAnnotation(root, "broken.xml"); err == nil || a != nil || broken == "" {
		t.Errorf("broken.xml = %+v, %q, %v; want a version and a parse error", a, broken, err)
	}
	if _, missing, _, err := readVersionedAnnotation(root, "missing.xml"); !errors.Is(err, fs.ErrNotExist) || missing != "" {
		t.Errorf("missing.xml = %q, %v", missing, err)
	}
}

func TestNormalizeClassList(t *testing.T) {
	got, err := NormalizeClassList([]Class{{Name: " 划痕 ", Color: "#EF4444"}, {Name: "dent"}, {Name: "Dent", Color: " #00ff00 "}})
	want := []Class{{Name: "划痕", Color: "#ef4444"}, {Name: "dent"}, {Name: "Dent", Color: "#00ff00"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeClassList = %+v, %v; want %+v", got, err, want)
	}
	if got, err := NormalizeClassList(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty list = %#v, %v", got, err)
	}
	for _, tc := range []struct {
		classes []Class
		msg     string
	}{
		{[]Class{{Name: "a"}, {Name: " a"}}, "类别“a”重复"},
		{[]Class{{Name: ""}}, "第 1 个类别名称为空"},
		{[]Class{{Name: "a\tb"}}, "控制字符"},
		{[]Class{{Name: "a", Color: "red"}}, "类别“a”的颜色必须是 #rrggbb 格式"},
		{[]Class{{Name: "a", Color: "#12345"}}, "#rrggbb"},
		{make([]Class, maxClasses+1), "类别数量不能超过 1000 个"},
	} {
		_, err := NormalizeClassList(tc.classes)
		wantKind(t, err, ErrInvalid, tc.msg)
	}
}

// ---- integration ----

// annotationEnv is a scanned dataset to annotate.
type annotationEnv struct {
	*testEnv
	d   Dataset
	dir string
}

func newAnnotationEnv(t *testing.T, files map[string][]byte) *annotationEnv {
	t.Helper()
	e := newTestEnv(t, true)
	dir := filepath.Join(e.datasetsDir(), "annotate")
	writeFiles(t, dir, files)
	dirName := "annotate"
	d, err := e.svc.CreateDataset(context.Background(), CreateDatasetInput{Name: "标注", Directory: &dirName})
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, d.ID, d.ActiveImport.ID); job.Status != StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	return &annotationEnv{testEnv: e, d: d, dir: dir}
}

// image returns the indexed image at rel.
func (e *annotationEnv) image(t *testing.T, rel string) Image {
	t.Helper()
	items, _, err := e.svc.ListImages(context.Background(), e.d.ID, ImageQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range items {
		if img.Path == rel {
			return img
		}
	}
	t.Fatalf("no image %s", rel)
	return Image{}
}

func (e *annotationEnv) detail(t *testing.T, img Image) ImageDetail {
	t.Helper()
	got, err := e.svc.GetImage(context.Background(), e.d.ID, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (e *annotationEnv) dataset(t *testing.T) Dataset {
	t.Helper()
	d, err := e.svc.GetDataset(context.Background(), e.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *annotationEnv) readFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIntegrationSaveAnnotation(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"NG/a.png": pngFile(t, 64, 48),
		"NG/b.png": pngFile(t, 64, 48), "NG/b.xml": vocFile("b.png", "dent", "dent"),
		"OK/c.png": pngFile(t, 64, 48),
	})
	ctx := context.Background()
	a := e.image(t, "NG/a.png")
	if detail := e.detail(t, a); detail.AnnotationVersion != "" || detail.AnnotationPath != nil || detail.AnnotationState != annotationNone {
		t.Fatalf("unannotated detail = %+v", detail)
	}
	before := e.dataset(t)

	objects := []AnnotationObject{
		{Name: " 划痕 ", Xmin: 1, Ymin: 2, Xmax: 30, Ymax: 40, Difficult: true},
		{Name: "dent", Xmin: 50, Ymin: 10, Xmax: 100, Ymax: 60}, // beyond 64×48: clamped
	}
	saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{Objects: objects, BaseVersion: ""})
	if err != nil {
		t.Fatalf("SaveAnnotation: %v", err)
	}
	wantObjects := []AnnotationObject{
		{Name: "划痕", Xmin: 1, Ymin: 2, Xmax: 30, Ymax: 40, Difficult: true},
		{Name: "dent", Xmin: 50, Ymin: 10, Xmax: 64, Ymax: 48},
	}
	if saved.AnnotationState != annotationAnnotated || saved.AnnotationPath == nil || *saved.AnnotationPath != "NG/a.xml" ||
		saved.AnnotationVersion == "" || saved.AnnotationError != "" || !reflect.DeepEqual(saved.Objects, wantObjects) ||
		saved.Annotation == nil || saved.Annotation.ObjectCount != 2 {
		t.Errorf("saved = %+v", saved)
	}
	// The file is a plain labelImg-style VOC file.
	xml := e.readFile(t, "NG/a.xml")
	parsed, err := parseVOC(bytes.NewReader(xml))
	if err != nil || parsed.Filename != "a.png" || parsed.Width != 64 || parsed.Height != 48 || !reflect.DeepEqual(parsed.Objects, wantObjects) {
		t.Errorf("written VOC = %+v, %v\n%s", parsed, err, xml)
	}
	if !bytes.Contains(xml, []byte("<folder>NG</folder>")) || !bytes.Contains(xml, []byte("<depth>3</depth>")) {
		t.Errorf("written VOC:\n%s", xml)
	}
	if detail := e.detail(t, a); detail.AnnotationVersion != saved.AnnotationVersion || !reflect.DeepEqual(detail.Objects, wantObjects) {
		t.Errorf("detail after save = %+v", detail)
	}

	// The index and the counters follow without a scan.
	if img := e.image(t, "NG/a.png"); img.AnnotationState != annotationAnnotated || img.Annotation == nil ||
		img.Annotation.ObjectCount != 2 || *img.AnnotationPath != "NG/a.xml" {
		t.Errorf("indexed image = %+v", img)
	}
	after := e.dataset(t)
	if after.AnnotatedCount != before.AnnotatedCount+1 || after.ObjectCount != before.ObjectCount+2 ||
		after.LabelCount != 2 || !after.UpdatedAt.After(before.UpdatedAt) || after.ScannedAt == nil || !after.ScannedAt.Equal(*before.ScannedAt) {
		t.Errorf("counters %+v after save, %+v before", after, before)
	}
	labels, _ := e.svc.ListLabels(ctx, e.d.ID)
	if want := []Label{{Name: "dent", ObjectCount: 3, ImageCount: 2}, {Name: "划痕", ObjectCount: 1, ImageCount: 1}}; !reflect.DeepEqual(labels, want) {
		t.Errorf("labels = %+v, want %+v", labels, want)
	}
	if got, _, _ := e.svc.ListImages(ctx, e.d.ID, ImageQuery{Limit: 10, Label: "划痕"}); len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("label filter = %+v", got)
	}

	// A rescan finds nothing to change: the index matches the file.
	job, err := e.svc.StartScan(ctx, e.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job = e.waitJob(t, e.d.ID, job.ID); job.Summary[sumImagesUnchanged] != 3 || job.Summary[sumImagesUpdated] != 0 {
		t.Errorf("rescan after save = %+v", job.Summary)
	}

	// A save from an outdated version is refused; force overwrites.
	b := e.image(t, "NG/b.png")
	bDetail := e.detail(t, b)
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, b.ID, SaveAnnotationInput{Objects: []AnnotationObject{}, BaseVersion: "stale"})
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{Objects: []AnnotationObject{}, BaseVersion: ""})
	wantKind(t, err, ErrAnnotationChanged, "已被修改") // a.xml exists now
	if data := e.readFile(t, "NG/b.xml"); !bytes.Equal(data, vocFile("b.png", "dent", "dent")) {
		t.Errorf("refused save changed the file:\n%s", data)
	}
	// An empty list makes an empty annotation (a negative sample).
	empty, err := e.svc.SaveAnnotation(ctx, e.d.ID, b.ID, SaveAnnotationInput{Objects: []AnnotationObject{}, BaseVersion: bDetail.AnnotationVersion})
	if err != nil || empty.AnnotationState != annotationEmpty || len(empty.Objects) != 0 || empty.AnnotationVersion == bDetail.AnnotationVersion {
		t.Errorf("empty save = %+v, %v", empty, err)
	}
	forced, err := e.svc.SaveAnnotation(ctx, e.d.ID, b.ID, SaveAnnotationInput{
		Objects: []AnnotationObject{{Name: "x", Xmin: 0, Ymin: 0, Xmax: 1, Ymax: 1}}, BaseVersion: "stale", Force: true})
	if err != nil || forced.AnnotationState != annotationAnnotated {
		t.Errorf("forced save = %+v, %v", forced, err)
	}

	// Deleting the annotation removes the file; the image is unannotated.
	_, err = e.svc.DeleteAnnotation(ctx, e.d.ID, b.ID, "stale", false)
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	gone, err := e.svc.DeleteAnnotation(ctx, e.d.ID, b.ID, forced.AnnotationVersion, false)
	if err != nil || gone.AnnotationState != annotationNone || gone.AnnotationPath != nil || gone.AnnotationVersion != "" || gone.AnnotationError != "" {
		t.Errorf("deleted = %+v, %v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "NG", "b.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("b.xml still there: %v", err)
	}
	if d := e.dataset(t); d.AnnotatedCount != 1 || d.EmptyCount != 0 || d.ObjectCount != 2 || d.LabelCount != 2 {
		t.Errorf("counters after delete = %+v", d)
	}
	// Deleting again (no file) is fine.
	if again, err := e.svc.DeleteAnnotation(ctx, e.d.ID, b.ID, "", false); err != nil || again.AnnotationState != annotationNone {
		t.Errorf("delete again = %+v, %v", again, err)
	}

	// Validation and missing things.
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{Objects: []AnnotationObject{{Name: "", Xmax: 1, Ymax: 1}}, Force: true})
	wantKind(t, err, ErrInvalid, "没有类别")
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, 1<<40, SaveAnnotationInput{Force: true})
	wantKind(t, err, ErrNotFound, "图片不存在")
	_, err = e.svc.SaveAnnotation(ctx, 1<<40, a.ID, SaveAnnotationInput{Force: true})
	wantKind(t, err, ErrNotFound, "数据集不存在")
	c := e.image(t, "OK/c.png")
	if err := os.Remove(filepath.Join(e.dir, "OK", "c.png")); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, c.ID, SaveAnnotationInput{Objects: []AnnotationObject{}, BaseVersion: ""})
	wantKind(t, err, ErrNotFound, "图片文件不存在")
	if _, err := os.Stat(filepath.Join(e.dir, "OK", "c.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("wrote an annotation for a missing image")
	}
}

func TestIntegrationAnnotationTargets(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/1.jpg":  encodeImage(t, "jpeg", gradient(32, 32)),
		"VOC/Annotations/.keep": nil,
		"pair/a.jpg":            pngFile(t, 16, 16), // a PNG named .jpg: fine
		"pair/a.png":            pngFile(t, 16, 16),
		"pair/a.xml":            vocFile("a.jpg", "dent"),
		"late/x.png":            pngFile(t, 16, 16),
		"late/y.png":            pngFile(t, 16, 16),
		"late/y.jpg":            pngFile(t, 16, 16),
		"late/w.png":            pngFile(t, 16, 16),
		"late/w.jpg":            pngFile(t, 16, 16),
		"late/q.png":            pngFile(t, 16, 16),
		"late/r.png":            pngFile(t, 16, 16),
		"VOC/JPEGImages/2.jpg":  pngFile(t, 16, 16),
	})
	ctx := context.Background()
	box := []AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}

	// A Pascal VOC folder layout keeps its layout.
	voc := e.image(t, "VOC/JPEGImages/1.jpg")
	saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, voc.ID, SaveAnnotationInput{Objects: box})
	if err != nil || *saved.AnnotationPath != "VOC/Annotations/1.xml" {
		t.Fatalf("VOC layout save = %+v, %v", saved, err)
	}
	if bytes.Contains(e.readFile(t, "VOC/Annotations/1.xml"), []byte("<folder>")) == false {
		t.Error("no <folder> in the VOC file")
	}

	// a.xml belongs to a.jpg (its <filename>): a.png cannot have its own.
	png := e.image(t, "pair/a.png")
	if got := e.detail(t, png); got.AnnotationState != annotationNone || got.AnnotationPath != nil || len(got.Objects) != 0 ||
		!strings.Contains(got.AnnotationBlocked, "属于同一文件夹中的另一张图片 pair/a.jpg") {
		t.Errorf("a.png detail = %+v", got)
	}
	if got := e.detail(t, e.image(t, "pair/a.jpg")); got.AnnotationBlocked != "" {
		t.Errorf("a.jpg is blocked: %+v", got)
	}
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, png.ID, SaveAnnotationInput{Objects: box})
	wantKind(t, err, ErrConflict, "标注文件 pair/a.xml 属于同一文件夹中的另一张图片 pair/a.jpg")

	// A file written by another tool after the last scan is this image's
	// annotation: shown, and saved over only from its version.
	writeFiles(t, e.dir, map[string][]byte{"late/x.xml": vocFile("x.png", "scratch")})
	x := e.image(t, "late/x.png")
	got := e.detail(t, x)
	if got.AnnotationState != annotationAnnotated || got.AnnotationPath == nil || *got.AnnotationPath != "late/x.xml" ||
		got.AnnotationVersion == "" || len(got.Objects) != 1 || got.Objects[0].Name != "scratch" {
		t.Errorf("late file detail = %+v", got)
	}
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, x.ID, SaveAnnotationInput{Objects: box, BaseVersion: ""})
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, x.ID, SaveAnnotationInput{Objects: box, BaseVersion: got.AnnotationVersion}); err != nil {
		t.Errorf("save over the late file: %v", err)
	}
	// ... unless a scan would give it to another image that wants it too:
	// y.xml names y.jpg, so y.png can neither show nor save (not even
	// forced) nor delete it.
	yFile := vocFile("y.jpg", "scratch")
	writeFiles(t, e.dir, map[string][]byte{"late/y.xml": yFile})
	y := e.image(t, "late/y.png")
	if got := e.detail(t, y); got.AnnotationState != annotationNone || got.AnnotationVersion != "" || len(got.Objects) != 0 ||
		got.AnnotationPath != nil || !strings.Contains(got.AnnotationBlocked, "标注文件 late/y.xml 属于同一文件夹中的另一张图片 late/y.jpg") {
		t.Errorf("y.png sees y.jpg's late file: %+v", got)
	}
	for _, force := range []bool{false, true} {
		_, err = e.svc.SaveAnnotation(ctx, e.d.ID, y.ID, SaveAnnotationInput{Objects: box, Force: force})
		wantKind(t, err, ErrConflict, "属于同一文件夹中的另一张图片 late/y.jpg")
		_, err = e.svc.DeleteAnnotation(ctx, e.d.ID, y.ID, "", force)
		wantKind(t, err, ErrConflict, "属于同一文件夹中的另一张图片 late/y.jpg")
	}
	if data := e.readFile(t, "late/y.xml"); !bytes.Equal(data, yFile) {
		t.Errorf("y.png changed y.jpg's file:\n%s", data)
	}
	yjpg := e.image(t, "late/y.jpg")
	yDetail := e.detail(t, yjpg)
	if yDetail.AnnotationState != annotationAnnotated || len(yDetail.Objects) != 1 || yDetail.AnnotationBlocked != "" {
		t.Errorf("y.jpg does not see its late file: %+v", yDetail)
	}
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, yjpg.ID, SaveAnnotationInput{Objects: box, BaseVersion: yDetail.AnnotationVersion}); err != nil {
		t.Errorf("y.jpg save: %v", err)
	}
	// Without a <filename> that decides, the first image in sorted order
	// gets the file, as in a scan.
	writeFiles(t, e.dir, map[string][]byte{"late/w.xml": []byte("<annotation><object><name>w</name><bndbox><xmin>1</xmin><ymin>1</ymin><xmax>2</xmax><ymax>2</ymax></bndbox></object></annotation>")})
	if got := e.detail(t, e.image(t, "late/w.png")); !strings.Contains(got.AnnotationBlocked, "另一张图片 late/w.jpg") {
		t.Errorf("w.png detail = %+v", got)
	}
	if got := e.detail(t, e.image(t, "late/w.jpg")); got.AnnotationBlocked != "" || len(got.Objects) != 1 {
		t.Errorf("w.jpg detail = %+v", got)
	}
	// A <filename> naming an image that does not want the file (another
	// stem) does not take it away: a scan pairs q.xml with q.png.
	writeFiles(t, e.dir, map[string][]byte{"late/q.xml": vocFile("r.png", "scratch")})
	if got := e.detail(t, e.image(t, "late/q.png")); got.AnnotationBlocked != "" || got.AnnotationState != annotationAnnotated {
		t.Errorf("q.png detail = %+v", got)
	}

	// The file a scan would pick is the one shown and saved: in a VOC
	// layout, a.xml written next to the image (LabelImg's default) comes
	// before Annotations/.
	writeFiles(t, e.dir, map[string][]byte{"VOC/JPEGImages/2.xml": vocFile("2.jpg", "labelimg")})
	two := e.image(t, "VOC/JPEGImages/2.jpg")
	got = e.detail(t, two)
	if got.AnnotationPath == nil || *got.AnnotationPath != "VOC/JPEGImages/2.xml" || len(got.Objects) != 1 || got.Objects[0].Name != "labelimg" {
		t.Fatalf("VOC image with a file next to it: %+v", got)
	}
	saved, err = e.svc.SaveAnnotation(ctx, e.d.ID, two.ID, SaveAnnotationInput{Objects: box, BaseVersion: got.AnnotationVersion})
	if err != nil || *saved.AnnotationPath != "VOC/JPEGImages/2.xml" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "VOC", "Annotations", "2.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote a second file: %v", err)
	}
	job, err := e.svc.StartScan(ctx, e.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(t, e.d.ID, job.ID)
	if after := e.detail(t, e.image(t, "VOC/JPEGImages/2.jpg")); after.AnnotationPath == nil || *after.AnnotationPath != "VOC/JPEGImages/2.xml" ||
		after.AnnotationVersion != saved.AnnotationVersion || !reflect.DeepEqual(after.Objects, box) {
		t.Errorf("after a rescan: %+v", after)
	}
	// ... and the rescan agrees with the owners above.
	if img := e.image(t, "late/y.jpg"); img.AnnotationPath == nil || *img.AnnotationPath != "late/y.xml" {
		t.Errorf("rescan paired y.xml with %+v", img)
	}
	if img := e.image(t, "late/w.jpg"); img.AnnotationPath == nil || *img.AnnotationPath != "late/w.xml" {
		t.Errorf("rescan paired w.xml with %+v", img)
	}
	if img := e.image(t, "late/q.png"); img.AnnotationPath == nil || *img.AnnotationPath != "late/q.xml" {
		t.Errorf("rescan paired q.xml with %+v", img)
	}

	if runtime.GOOS != "windows" {
		// An annotation file replaced by a symbolic link is not written through.
		outside := filepath.Join(t.TempDir(), "outside.xml")
		writeFiles(t, filepath.Dir(outside), map[string][]byte{"outside.xml": vocFile("1.jpg")})
		target := filepath.Join(e.dir, "VOC", "Annotations", "1.xml")
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Fatal(err)
		}
		_, err := e.svc.SaveAnnotation(ctx, e.d.ID, voc.ID, SaveAnnotationInput{Objects: box, Force: true})
		wantKind(t, err, ErrConflict, "符号链接")
		if data, _ := os.ReadFile(outside); !bytes.Equal(data, vocFile("1.jpg")) {
			t.Error("wrote through a symbolic link")
		}
	}
}

// namespacedFile is labelImgFile with namespace declarations on the root
// and a namespaced element in an object.
var namespacedFile = strings.Replace(strings.Replace(labelImgFile, `<annotation verified="yes">`,
	`<annotation verified="yes" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xml:lang="zh">`, 1),
	"<occluded>1</occluded>", `<occluded>1</occluded><ext xmlns="urn:x"/>`, 1)

// unpatchable parses, but the declaration inside the root switches parseVOC
// to GBK there, so a new label written in UTF-8 would not read back.
var unpatchable = []byte("<annotation><?xml version=\"1.0\" encoding=\"GBK\"?><filename>\xcd\xbc.png</filename>" +
	"<object><name>a</name><bndbox><xmin>1</xmin><ymin>1</ymin><xmax>5</xmax><ymax>5</ymax></bndbox></object></annotation>")

func TestIntegrationSaveKeepsVOCContent(t *testing.T) {
	broken := []byte("<annotation><object>")
	e := newAnnotationEnv(t, map[string][]byte{
		"images/a.png": pngFile(t, 100, 80), "images/a.xml": []byte(labelImgFile),
		"images/b.png": pngFile(t, 16, 16), "images/b.xml": broken,
		"images/c.png": pngFile(t, 100, 80), "images/c.xml": []byte(namespacedFile),
		"images/d.png": pngFile(t, 16, 16), "images/d.xml": unpatchable,
	})
	ctx := context.Background()
	a := e.image(t, "images/a.png")
	detail := e.detail(t, a)
	dent := AnnotationObject{Name: "dent", Xmin: 10, Ymin: 20, Xmax: 30, Ymax: 40}
	if detail.AnnotationUnsupported != 1 || len(detail.Objects) != 2 || detail.Objects[0] != dent {
		t.Fatalf("detail = %+v", detail)
	}
	added := AnnotationObject{Name: "划痕", Xmin: 50, Ymin: 50, Xmax: 60, Ymax: 60}
	saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{
		Objects: []AnnotationObject{dent, added}, BaseVersion: detail.AnnotationVersion})
	if err != nil || saved.AnnotationUnsupported != 1 || !reflect.DeepEqual(saved.Objects, []AnnotationObject{dent, added}) {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	data := string(e.readFile(t, "images/a.xml"))
	for _, keep := range []string{`<annotation verified="yes">`, `<path>C:\data\images\a.png</path>`, "<occluded>1</occluded>",
		"<xmin>10.0</xmin>", "<polygon>", "<!-- reviewed by", `<custom a="1 &amp; 2">`} {
		if !strings.Contains(data, keep) {
			t.Errorf("save dropped %s:\n%s", keep, data)
		}
	}
	if strings.Contains(data, "<name>scratch</name>") || !strings.Contains(data, "<name>划痕</name>") {
		t.Errorf("boxes not replaced:\n%s", data)
	}
	// The index counts the boxes only.
	if img := e.image(t, "images/a.png"); img.Annotation == nil || img.Annotation.ObjectCount != 2 {
		t.Errorf("indexed image = %+v", img)
	}
	// The version returned is the one of the bytes written.
	if saved.AnnotationVersion != annotationVersion([]byte(data)) {
		t.Errorf("saved version %q, file version %q", saved.AnnotationVersion, annotationVersion([]byte(data)))
	}

	// Namespaces do not stop the file from being edited in place; saving
	// the same boxes leaves the file as it is.
	c := e.image(t, "images/c.png")
	detail = e.detail(t, c)
	if detail.AnnotationUnsupported != 1 || len(detail.Objects) != 2 {
		t.Fatalf("namespaced detail = %+v", detail)
	}
	saved, err = e.svc.SaveAnnotation(ctx, e.d.ID, c.ID, SaveAnnotationInput{Objects: detail.Objects, BaseVersion: detail.AnnotationVersion})
	if err != nil || saved.AnnotationUnsupported != 1 || saved.AnnotationVersion != detail.AnnotationVersion {
		t.Errorf("namespaced save = %+v, %v", saved, err)
	}
	if got := e.readFile(t, "images/c.xml"); string(got) != namespacedFile {
		t.Errorf("namespaced file changed:\n%s", got)
	}

	// A file that parses but cannot be edited in place is regenerated only
	// when forced.
	d := e.image(t, "images/d.png")
	detail = e.detail(t, d)
	if detail.AnnotationError != "" || len(detail.Objects) != 1 {
		t.Fatalf("unpatchable detail = %+v", detail)
	}
	renamed := []AnnotationObject{{Name: "划痕", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}
	_, err = e.svc.SaveAnnotation(ctx, e.d.ID, d.ID, SaveAnnotationInput{Objects: renamed, BaseVersion: detail.AnnotationVersion})
	wantKind(t, err, ErrAnnotationUnpatchable, "标注文件 images/d.xml 无法在保留其中其它内容的情况下修改")
	if got := e.readFile(t, "images/d.xml"); !bytes.Equal(got, unpatchable) {
		t.Errorf("refused save changed the file:\n%s", got)
	}
	saved, err = e.svc.SaveAnnotation(ctx, e.d.ID, d.ID, SaveAnnotationInput{Objects: renamed, BaseVersion: detail.AnnotationVersion, Force: true})
	if err != nil || !reflect.DeepEqual(saved.Objects, renamed) || saved.AnnotationVersion != annotationVersion(e.readFile(t, "images/d.xml")) {
		t.Errorf("forced save = %+v, %v", saved, err)
	}

	// A file that is not valid XML is replaced by a new one when forced.
	b := e.image(t, "images/b.png")
	if got := e.detail(t, b); got.AnnotationError == "" || got.AnnotationVersion == "" {
		t.Fatalf("broken detail = %+v", got)
	}
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, b.ID, SaveAnnotationInput{
		Objects: []AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}, Force: true}); err != nil {
		t.Fatal(err)
	}
	if parsed, err := parseVOC(bytes.NewReader(e.readFile(t, "images/b.xml"))); err != nil || len(parsed.Objects) != 1 || parsed.Filename != "b.png" {
		t.Errorf("rewritten b.xml = %+v, %v", parsed, err)
	}
}

func TestIntegrationConcurrentAnnotationSaves(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{"a.png": pngFile(t, 32, 32), "b.png": pngFile(t, 32, 32)})
	ctx := context.Background()
	a := e.image(t, "a.png")
	// Everyone starts from "no annotation": exactly one save wins.
	const editors = 8
	var wg sync.WaitGroup
	errs := make([]error, editors)
	for i := range editors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{
				Objects: []AnnotationObject{{Name: fmt.Sprintf("editor-%d", i), Xmin: 0, Ymin: 0, Xmax: 4, Ymax: 4}}})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrAnnotationChanged):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d saves succeeded, want 1", ok)
	}
	// Saves of different images of one dataset keep the counters right.
	b := e.image(t, "b.png")
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, b.ID, SaveAnnotationInput{
		Objects: []AnnotationObject{{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 4, Ymax: 4}, {Name: "dent", Xmin: 5, Ymin: 5, Xmax: 9, Ymax: 9}}}); err != nil {
		t.Fatal(err)
	}
	if d := e.dataset(t); d.AnnotatedCount != 2 || d.ObjectCount != 3 || d.LabelCount != 2 {
		t.Errorf("counters = %+v", d)
	}
}

func TestIntegrationImageNeighbors(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"img1", "img2", "img3", "img10", "img11"} {
		files[name+".png"] = pngFile(t, 8, 8)
	}
	files["img2.xml"] = vocFile("img2.png", "dent")
	files["img10.xml"] = vocFile("img10.png")
	e := newAnnotationEnv(t, files)
	ctx := context.Background()
	ids := map[string]int64{}
	for _, name := range []string{"img1", "img2", "img3", "img10", "img11"} {
		ids[name] = e.image(t, name+".png").ID
	}
	path := func(img *Image) string {
		if img == nil {
			return "-"
		}
		return img.Path
	}
	check := func(name string, q ImageQuery, prev, next string, position int, matches bool, total int) {
		t.Helper()
		n, err := e.svc.ImageNeighbors(ctx, e.d.ID, ids[name], q)
		if err != nil {
			t.Fatalf("ImageNeighbors(%s, %+v): %v", name, q, err)
		}
		if path(n.Prev) != prev || path(n.Next) != next || n.Position != position || n.Matches != matches || n.Total != total {
			t.Errorf("ImageNeighbors(%s, %+v) = prev %s, next %s, position %d, matches %v, total %d; want %s, %s, %d, %v, %d",
				name, q, path(n.Prev), path(n.Next), n.Position, n.Matches, n.Total, prev, next, position, matches, total)
		}
	}
	// Image order: img1, img2, img3, img10, img11.
	check("img1", ImageQuery{}, "-", "img2.png", 0, true, 5)
	check("img3", ImageQuery{}, "img2.png", "img10.png", 2, true, 5)
	check("img11", ImageQuery{}, "img10.png", "-", 4, true, 5)
	// Unannotated: img1, img3, img11.
	check("img1", ImageQuery{Status: FilterUnannotated}, "-", "img3.png", 0, true, 3)
	check("img2", ImageQuery{Status: FilterUnannotated}, "img1.png", "img3.png", 1, false, 3) // not unannotated itself
	check("img10", ImageQuery{Status: FilterUnannotated}, "img3.png", "img11.png", 2, false, 3)
	check("img3", ImageQuery{Label: "dent"}, "img2.png", "-", 1, false, 1)
	check("img3", ImageQuery{Search: "IMG1"}, "img1.png", "img10.png", 1, false, 3)

	// After annotating img3, "next unannotated" from it is still img11.
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, ids["img3"], SaveAnnotationInput{
		Objects: []AnnotationObject{{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 2, Ymax: 2}}}); err != nil {
		t.Fatal(err)
	}
	check("img3", ImageQuery{Status: FilterUnannotated}, "img1.png", "img11.png", 1, false, 2)
	check("img3", ImageQuery{Label: "dent"}, "img2.png", "-", 1, true, 2)

	if _, err := e.svc.ImageNeighbors(ctx, e.d.ID, 1<<40, ImageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown image: %v", err)
	}
	if _, err := e.svc.ImageNeighbors(ctx, 1<<40, ids["img1"], ImageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown dataset: %v", err)
	}
}

func TestIntegrationClasses(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "classes"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := e.svc.ListClasses(ctx, d.ID)
	if err != nil || empty.Classes == nil || len(empty.Classes) != 0 || len(empty.Version) != 32 {
		t.Errorf("new dataset classes = %#v, %v", empty, err)
	}
	time.Sleep(2 * time.Millisecond)
	set, err := e.svc.SetClasses(ctx, d.ID, []Class{{Name: " scratch ", Color: "#EF4444"}, {Name: "凹坑"}}, &empty.Version)
	want := []Class{{Name: "scratch", Color: "#ef4444"}, {Name: "凹坑"}}
	if err != nil || !reflect.DeepEqual(set.Classes, want) || set.Version == empty.Version {
		t.Errorf("SetClasses = %+v, %v", set, err)
	}
	if got, _ := e.svc.ListClasses(ctx, d.ID); !reflect.DeepEqual(got, set) {
		t.Errorf("ListClasses = %+v, want %+v", got, set)
	}
	if after, _ := e.svc.GetDataset(ctx, d.ID); !after.UpdatedAt.After(d.UpdatedAt) {
		t.Errorf("updated_at not touched: %v, was %v", after.UpdatedAt, d.UpdatedAt)
	}
	// A replacement from an older version is refused and changes nothing.
	_, err = e.svc.SetClasses(ctx, d.ID, []Class{{Name: "crack"}}, &empty.Version)
	wantKind(t, err, ErrConflict, "类别列表已被其他人修改，请载入最新的列表后再保存")
	_, err = e.svc.SetClasses(ctx, d.ID, []Class{{Name: "a"}, {Name: "a"}}, nil)
	wantKind(t, err, ErrInvalid, "重复")
	if got, _ := e.svc.ListClasses(ctx, d.ID); !reflect.DeepEqual(got, set) {
		t.Errorf("a refused list changed the classes: %+v", got)
	}

	// Adding a class appends it; adding it again changes nothing.
	added, err := e.svc.AddClass(ctx, d.ID, Class{Name: " 孔洞 ", Color: "#00FF00"})
	want = append(want, Class{Name: "孔洞", Color: "#00ff00"})
	if err != nil || !reflect.DeepEqual(added.Classes, want) || added.Version == set.Version {
		t.Errorf("AddClass = %+v, %v", added, err)
	}
	before, _ := e.svc.GetDataset(ctx, d.ID)
	if again, err := e.svc.AddClass(ctx, d.ID, Class{Name: "孔洞", Color: "#123456"}); err != nil || !reflect.DeepEqual(again, added) {
		t.Errorf("AddClass again = %+v, %v", again, err)
	}
	if after, _ := e.svc.GetDataset(ctx, d.ID); !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("adding an existing class touched the dataset")
	}
	for _, tc := range []struct {
		class Class
		msg   string
	}{
		{Class{Name: " "}, "类别名称为空"},
		{Class{Name: "a\nb"}, "类别名称包含换行或控制字符"},
		{Class{Name: strings.Repeat("长", 256)}, "类别名称不能超过 255 个字符"},
		{Class{Name: "a", Color: "red"}, "类别“a”的颜色必须是 #rrggbb 格式"},
	} {
		_, err := e.svc.AddClass(ctx, d.ID, tc.class)
		wantKind(t, err, ErrInvalid, tc.msg)
	}

	// Concurrent additions all arrive.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.AddClass(ctx, d.ID, Class{Name: fmt.Sprintf("c%d", i)}); err != nil {
				t.Errorf("AddClass c%d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if got, _ := e.svc.ListClasses(ctx, d.ID); len(got.Classes) != 3+8 {
		t.Errorf("after concurrent additions: %+v", got.Classes)
	}

	// At most maxClasses.
	full := make([]Class, maxClasses)
	for i := range full {
		full[i] = Class{Name: fmt.Sprintf("k%d", i)}
	}
	if _, err := e.svc.SetClasses(ctx, d.ID, full, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.AddClass(ctx, d.ID, Class{Name: "one more"})
	wantKind(t, err, ErrInvalid, "类别数量不能超过 1000 个")
	if _, err := e.svc.AddClass(ctx, d.ID, Class{Name: "k0"}); err != nil {
		t.Errorf("adding an existing class to a full list: %v", err)
	}

	if got, err := e.svc.SetClasses(ctx, d.ID, []Class{}, nil); err != nil || len(got.Classes) != 0 || got.Version != empty.Version {
		t.Errorf("clear = %+v, %v", got, err)
	}
	if _, err := e.svc.ListClasses(ctx, 1<<40); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListClasses(unknown) = %v", err)
	}
	if _, err := e.svc.SetClasses(ctx, 1<<40, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetClasses(unknown) = %v", err)
	}
	if _, err := e.svc.AddClass(ctx, 1<<40, Class{Name: "a"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddClass(unknown) = %v", err)
	}
}
