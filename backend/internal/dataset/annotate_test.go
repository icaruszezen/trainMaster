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
	root := openTestRoot(t, map[string][]byte{"voc/annotations.txt": nil, "voc2/Annotations": []byte("a file, not a folder"),
		"VOC2007/JPEGImages/twin.jpg": nil, "VOC2007/images/twin.jpg": nil, "VOC2007/images/k.png": nil, "VOC2007/images/dir.jpg/x": nil,
		"VOC2007/Annotations/inner.jpg": nil},
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
		// An image of the same name in the other VOC image folder would take
		// Annotations/twin.xml too: each gets a file next to it.
		"VOC2007/JPEGImages/twin.jpg":  "VOC2007/JPEGImages/twin.xml",
		"VOC2007/images/twin.jpg":      "VOC2007/images/twin.xml",
		"VOC2007/JPEGImages/inner.jpg": "VOC2007/JPEGImages/inner.xml", // so does one in Annotations/
		"VOC2007/JPEGImages/k.jpg":     "VOC2007/Annotations/k.xml",    // only the stem is the same
		"VOC2007/JPEGImages/dir.jpg":   "VOC2007/Annotations/dir.xml",  // a folder is no image
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

// rescan scans the dataset folder again and waits for the scan.
func (e *annotationEnv) rescan(t *testing.T) {
	t.Helper()
	job, err := e.svc.StartScan(context.Background(), e.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job = e.waitJob(t, e.d.ID, job.ID); job.Status != StatusSucceeded {
		t.Fatalf("rescan = %+v", job)
	}
}

func objectNames(objects []AnnotationObject) []string {
	names := []string{}
	for _, o := range objects {
		names = append(names, o.Name)
	}
	return names
}

func boxNamed(name string) []AnnotationObject {
	return []AnnotationObject{{Name: name, Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}
}

// A paired image is not edited in a file the next scan would drop: when
// another tool writes a file a scan prefers (LabelImg saves next to the
// image), that file is shown and saved, and the image is paired with it.
func TestIntegrationPairedAnnotationSuperseded(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/a.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/c.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/c.png":  pngFile(t, 16, 16),
		"VOC/Annotations/.keep": nil,
		"flat/e.png":            pngFile(t, 16, 16),
		"flat/e.xml":            vocFile("e.png", "dent"),
	})
	ctx := context.Background()
	save := func(rel, base, label string) (ImageDetail, error) {
		return e.svc.SaveAnnotation(ctx, e.d.ID, e.image(t, rel).ID, SaveAnnotationInput{Objects: boxNamed(label), BaseVersion: base})
	}

	first, err := save("VOC/JPEGImages/a.jpg", "", "platform1")
	if err != nil || *first.AnnotationPath != "VOC/Annotations/a.xml" {
		t.Fatalf("first save = %+v, %v", first, err)
	}
	platformFile := e.readFile(t, "VOC/Annotations/a.xml")
	labelImg := vocFile("a.jpg", "labelimg")
	writeFiles(t, e.dir, map[string][]byte{"VOC/JPEGImages/a.xml": labelImg})
	got := e.detail(t, e.image(t, "VOC/JPEGImages/a.jpg"))
	if got.AnnotationPath == nil || *got.AnnotationPath != "VOC/JPEGImages/a.xml" || got.AnnotationVersion != annotationVersion(labelImg) ||
		!reflect.DeepEqual(objectNames(got.Objects), []string{"labelimg"}) || got.AnnotationBlocked != "" {
		t.Fatalf("detail after LabelImg wrote a.xml = %+v", got)
	}
	// An edit started from the platform's file meets LabelImg's.
	_, err = save("VOC/JPEGImages/a.jpg", first.AnnotationVersion, "platform2")
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	saved, err := save("VOC/JPEGImages/a.jpg", got.AnnotationVersion, "platform2")
	if err != nil || *saved.AnnotationPath != "VOC/JPEGImages/a.xml" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if img := e.image(t, "VOC/JPEGImages/a.jpg"); img.AnnotationPath == nil || *img.AnnotationPath != "VOC/JPEGImages/a.xml" {
		t.Errorf("not paired with the file it saved: %+v", img)
	}
	if data := e.readFile(t, "VOC/Annotations/a.xml"); !bytes.Equal(data, platformFile) {
		t.Errorf("the superseded file changed:\n%s", data)
	}

	// c.xml next to the images names c.png, so a scan gives it to c.png and
	// leaves c.jpg without annotation: c.jpg can no longer edit its file.
	if _, err := save("VOC/JPEGImages/c.jpg", "", "platform"); err != nil {
		t.Fatal(err)
	}
	cPlatform := e.readFile(t, "VOC/Annotations/c.xml")
	writeFiles(t, e.dir, map[string][]byte{"VOC/JPEGImages/c.xml": vocFile("c.png", "labelimg")})
	c := e.image(t, "VOC/JPEGImages/c.jpg")
	if got := e.detail(t, c); len(got.Objects) != 0 || got.AnnotationPath != nil ||
		!strings.Contains(got.AnnotationBlocked, "标注文件 VOC/JPEGImages/c.xml 属于同一文件夹中的另一张图片 VOC/JPEGImages/c.png") {
		t.Errorf("c.jpg detail = %+v", got)
	}
	for _, force := range []bool{false, true} {
		_, err = e.svc.SaveAnnotation(ctx, e.d.ID, c.ID, SaveAnnotationInput{Objects: boxNamed("x"), Force: force})
		wantKind(t, err, ErrConflict, "另一张图片 VOC/JPEGImages/c.png")
	}
	if data := e.readFile(t, "VOC/Annotations/c.xml"); !bytes.Equal(data, cPlatform) {
		t.Errorf("c.jpg's old file changed:\n%s", data)
	}

	// A preferred file in another case is not looked for when viewing (that
	// would list the folder), but saving and deleting stop until a rescan.
	sensitive := caseSensitive(t)
	if sensitive {
		bFirst, err := save("VOC/JPEGImages/b.jpg", "", "platform")
		if err != nil || *bFirst.AnnotationPath != "VOC/Annotations/b.xml" {
			t.Fatalf("b save = %+v, %v", bFirst, err)
		}
		writeFiles(t, e.dir, map[string][]byte{"VOC/JPEGImages/B.XML": vocFile("b.jpg", "labelimg")})
		_, err = save("VOC/JPEGImages/b.jpg", bFirst.AnnotationVersion, "platform2")
		wantKind(t, err, ErrConflict, "另一个标注文件 VOC/JPEGImages/B.XML，请先重新扫描")
		_, err = e.svc.DeleteAnnotation(ctx, e.d.ID, e.image(t, "VOC/JPEGImages/b.jpg").ID, bFirst.AnnotationVersion, true)
		wantKind(t, err, ErrConflict, "请先重新扫描")
		if data := e.readFile(t, "VOC/Annotations/b.xml"); !strings.Contains(string(data), "<name>platform</name>") {
			t.Errorf("b.jpg's old file changed:\n%s", data)
		}
		// So does a file found instead of a paired file that is gone.
		if err := os.Rename(filepath.Join(e.dir, "flat", "e.xml"), filepath.Join(e.dir, "flat", "E.XML")); err != nil {
			t.Fatal(err)
		}
		if got := e.detail(t, e.image(t, "flat/e.png")); got.AnnotationError != "标注文件已不存在，请重新扫描" {
			t.Errorf("e.png detail = %+v", got)
		}
		_, err = save("flat/e.png", "", "platform")
		wantKind(t, err, ErrConflict, "另一个标注文件 flat/E.XML，请先重新扫描")
		if _, err := os.Stat(filepath.Join(e.dir, "flat", "e.xml")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("wrote the paired file again: %v", err)
		}
	}

	// The rescan agrees.
	e.rescan(t)
	if after := e.detail(t, e.image(t, "VOC/JPEGImages/a.jpg")); after.AnnotationPath == nil || *after.AnnotationPath != "VOC/JPEGImages/a.xml" ||
		after.AnnotationVersion != saved.AnnotationVersion || !reflect.DeepEqual(objectNames(after.Objects), []string{"platform2"}) {
		t.Errorf("a.jpg after a rescan = %+v", after)
	}
	if img := e.image(t, "VOC/JPEGImages/c.png"); img.AnnotationPath == nil || *img.AnnotationPath != "VOC/JPEGImages/c.xml" {
		t.Errorf("c.png after a rescan = %+v", img)
	}
	if img := e.image(t, "VOC/JPEGImages/c.jpg"); img.AnnotationPath != nil {
		t.Errorf("c.jpg after a rescan = %+v", img)
	}
	if sensitive {
		b := e.detail(t, e.image(t, "VOC/JPEGImages/b.jpg"))
		if b.AnnotationPath == nil || *b.AnnotationPath != "VOC/JPEGImages/B.XML" {
			t.Fatalf("b.jpg after a rescan = %+v", b)
		}
		if _, err := save("VOC/JPEGImages/b.jpg", b.AnnotationVersion, "platform2"); err != nil {
			t.Errorf("b.jpg save after a rescan: %v", err)
		}
	}
}

// A new annotation file is created where a scan gives it to the image that
// created it, whatever happens to the other files later.
func TestIntegrationNewAnnotationFileOwner(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/a.jpg":  pngFile(t, 16, 16),
		"VOC/images/a.jpg":      pngFile(t, 16, 16),
		"VOC/JPEGImages/k.jpg":  pngFile(t, 16, 16),
		"VOC/images/k.png":      pngFile(t, 16, 16),
		"VOC/Annotations/.keep": nil,
		"same/a.jpg":            pngFile(t, 16, 16),
		"same/a.png":            pngFile(t, 16, 16),
	})
	ctx := context.Background()
	save := func(rel, label string) ImageDetail {
		t.Helper()
		saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, e.image(t, rel).ID, SaveAnnotationInput{Objects: boxNamed(label)})
		if err != nil {
			t.Fatalf("save %s: %v", rel, err)
		}
		return saved
	}

	// JPEGImages/a.jpg and images/a.jpg would both take Annotations/a.xml,
	// and its <filename> could not tell them apart: each image gets a file
	// next to it instead.
	for _, rel := range []string{"VOC/images/a.jpg", "VOC/JPEGImages/a.jpg"} {
		if got := e.detail(t, e.image(t, rel)); got.AnnotationBlocked != "" {
			t.Errorf("%s detail = %+v", rel, got)
		}
	}
	if saved := save("VOC/images/a.jpg", "for-images"); *saved.AnnotationPath != "VOC/images/a.xml" {
		t.Errorf("images/a.jpg saved = %+v", saved)
	}
	if saved := save("VOC/JPEGImages/a.jpg", "for-jpeg"); *saved.AnnotationPath != "VOC/JPEGImages/a.xml" {
		t.Errorf("JPEGImages/a.jpg saved = %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "VOC", "Annotations", "a.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("created a shared file: %v", err)
	}
	// Images of other names share Annotations/: the <filename> of the new
	// file decides.
	if saved := save("VOC/JPEGImages/k.jpg", "for-k"); *saved.AnnotationPath != "VOC/Annotations/k.xml" {
		t.Errorf("JPEGImages/k.jpg saved = %+v", saved)
	}
	if got := e.detail(t, e.image(t, "VOC/images/k.png")); !strings.Contains(got.AnnotationBlocked, "属于另一张图片 VOC/JPEGImages/k.jpg（VOC 按文件名配对）") ||
		strings.Contains(got.AnnotationBlocked, "同一文件夹") {
		t.Errorf("images/k.png detail = %+v", got)
	}

	// In one folder the <filename> of the new file decides: a.png may create
	// a.xml.
	png := e.image(t, "same/a.png")
	if got := e.detail(t, png); got.AnnotationBlocked != "" {
		t.Errorf("same/a.png detail = %+v", got)
	}
	save("same/a.png", "for-png")

	e.rescan(t)
	for rel, want := range map[string]string{
		"VOC/JPEGImages/a.jpg": "for-jpeg", "VOC/images/a.jpg": "for-images", "VOC/JPEGImages/k.jpg": "for-k", "same/a.png": "for-png",
	} {
		if got := e.detail(t, e.image(t, rel)); !reflect.DeepEqual(objectNames(got.Objects), []string{want}) || got.AnnotationBlocked != "" {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
	if got := e.detail(t, e.image(t, "VOC/images/k.png")); !strings.Contains(got.AnnotationBlocked, "属于另一张图片 VOC/JPEGImages/k.jpg") {
		t.Errorf("images/k.png after a rescan = %+v", got)
	}
	if got := e.detail(t, e.image(t, "same/a.jpg")); !strings.Contains(got.AnnotationBlocked, "属于同一文件夹中的另一张图片 same/a.png") {
		t.Errorf("same/a.jpg after a rescan = %+v", got)
	}
}

// The boxes saved for an image stay with it: viewing and saving use the file
// the next scan pairs with the image, also after the platform deletes the
// file of another image or another tool rewrites a file.
func TestIntegrationAnnotationOwnershipKept(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		// JPEGImages/b.jpg has a file of its own (LabelImg's).
		"VOC/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/b.xml":  vocFile("b.jpg", "theirs"),
		"VOC/images/b.jpg":      pngFile(t, 16, 16),
		"VOC/Annotations/.keep": nil,
		// The scan pairs images/c.jpg with Annotations/c.xml: JPEGImages/c.jpg
		// has a file of its own.
		"VOC2/JPEGImages/c.jpg":  pngFile(t, 16, 16),
		"VOC2/JPEGImages/c.xml":  vocFile("c.jpg", "labelimg"),
		"VOC2/images/c.jpg":      pngFile(t, 16, 16),
		"VOC2/Annotations/c.xml": vocFile("c.jpg", "shared"),
		"f/a.jpg":                pngFile(t, 16, 16),
		"f/a.png":                pngFile(t, 16, 16),
	})
	ctx := context.Background()
	save := func(rel, base, label string) (ImageDetail, error) {
		return e.svc.SaveAnnotation(ctx, e.d.ID, e.image(t, rel).ID, SaveAnnotationInput{Objects: boxNamed(label), BaseVersion: base})
	}
	remove := func(rel string, force bool) (ImageDetail, error) {
		img := e.image(t, rel)
		return e.svc.DeleteAnnotation(ctx, e.d.ID, img.ID, e.detail(t, img).AnnotationVersion, force)
	}

	// images/b.jpg creates its file; deleting the file of JPEGImages/b.jpg
	// then makes that image look for Annotations/b.xml.
	mine, err := save("VOC/images/b.jpg", "", "mine")
	if err != nil || *mine.AnnotationPath != "VOC/images/b.xml" {
		t.Fatalf("images/b.jpg save = %+v, %v", mine, err)
	}
	if _, err := remove("VOC/JPEGImages/b.jpg", false); err != nil {
		t.Fatalf("delete JPEGImages/b.jpg: %v", err)
	}
	got := e.detail(t, e.image(t, "VOC/images/b.jpg"))
	if !reflect.DeepEqual(objectNames(got.Objects), []string{"mine"}) || got.AnnotationBlocked != "" {
		t.Fatalf("images/b.jpg detail = %+v", got)
	}
	if _, err := save("VOC/images/b.jpg", got.AnnotationVersion, "mine-2"); err != nil {
		t.Fatalf("images/b.jpg second save: %v", err)
	}

	// Deleting the file of JPEGImages/c.jpg would make it take
	// Annotations/c.xml from images/c.jpg at the next scan: refused, even
	// forced. Saving it empty keeps it off that file.
	for _, force := range []bool{false, true} {
		_, err := remove("VOC2/JPEGImages/c.jpg", force)
		wantKind(t, err, ErrConflict, "重新扫描会把另一张图片 VOC2/images/c.jpg 的标注文件 VOC2/Annotations/c.xml 配对给这张图片")
	}
	if data := e.readFile(t, "VOC2/JPEGImages/c.xml"); !bytes.Equal(data, vocFile("c.jpg", "labelimg")) {
		t.Errorf("refused delete changed the file:\n%s", data)
	}
	c := e.image(t, "VOC2/JPEGImages/c.jpg")
	empty, err := e.svc.SaveAnnotation(ctx, e.d.ID, c.ID, SaveAnnotationInput{Objects: []AnnotationObject{}, BaseVersion: e.detail(t, c).AnnotationVersion})
	if err != nil || empty.AnnotationState != annotationEmpty {
		t.Errorf("empty save = %+v, %v", empty, err)
	}

	// Another tool rewrites a.xml for a.png: a scan gives it to a.png now,
	// so a.jpg can no longer save into it or delete it.
	if _, err := save("f/a.jpg", "", "jpgbox"); err != nil {
		t.Fatal(err)
	}
	pngXML := vocFile("a.png", "pngbox")
	writeFiles(t, e.dir, map[string][]byte{"f/a.xml": pngXML})
	jpg := e.image(t, "f/a.jpg")
	_, err = save("f/a.jpg", annotationVersion(pngXML), "jpgbox2")
	wantKind(t, err, ErrConflict, "标注文件 f/a.xml 按 VOC 的配对规则现在属于同一文件夹中的另一张图片 f/a.png")
	_, err = e.svc.DeleteAnnotation(ctx, e.d.ID, jpg.ID, annotationVersion(pngXML), true)
	wantKind(t, err, ErrConflict, "请先重新扫描数据集")
	if data := e.readFile(t, "f/a.xml"); !bytes.Equal(data, pngXML) {
		t.Errorf("a.jpg changed a.png's file:\n%s", data)
	}

	e.rescan(t)
	for rel, want := range map[string][]string{
		"VOC/images/b.jpg": {"mine-2"}, "VOC/JPEGImages/b.jpg": {},
		"VOC2/images/c.jpg": {"shared"}, "VOC2/JPEGImages/c.jpg": {},
		"f/a.png": {"pngbox"},
	} {
		if got := e.detail(t, e.image(t, rel)); !reflect.DeepEqual(objectNames(got.Objects), want) || got.AnnotationBlocked != "" {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
	if img := e.image(t, "VOC2/JPEGImages/c.jpg"); img.AnnotationState != annotationEmpty {
		t.Errorf("JPEGImages/c.jpg after a rescan = %+v", img)
	}
	if got := e.detail(t, e.image(t, "f/a.jpg")); !strings.Contains(got.AnnotationBlocked, "属于同一文件夹中的另一张图片 f/a.png") {
		t.Errorf("a.jpg after a rescan = %+v", got)
	}
}

// A paired image whose file is gone gets a new file where an image without
// one would, so its owner does not depend on the files of other images.
func TestIntegrationPairedAnnotationGone(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/b.xml":  vocFile("b.jpg", "theirs"),
		"VOC/images/b.jpg":      pngFile(t, 16, 16),
		"VOC/Annotations/b.xml": vocFile("b.jpg", "shared"),
		"flat/e.png":            pngFile(t, 16, 16),
		"flat/e.xml":            vocFile("e.png", "dent"),
	})
	ctx := context.Background()
	for _, rel := range []string{"VOC/Annotations/b.xml", "flat/e.xml"} {
		if err := os.Remove(filepath.Join(e.dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, e.image(t, "VOC/images/b.jpg").ID, SaveAnnotationInput{Objects: boxNamed("mine")})
	if err != nil || *saved.AnnotationPath != "VOC/images/b.xml" {
		t.Fatalf("images/b.jpg save = %+v, %v", saved, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "VOC", "Annotations", "b.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote the shared file again: %v", err)
	}
	saved, err = e.svc.SaveAnnotation(ctx, e.d.ID, e.image(t, "flat/e.png").ID, SaveAnnotationInput{Objects: boxNamed("again")})
	if err != nil || *saved.AnnotationPath != "flat/e.xml" {
		t.Fatalf("e.png save = %+v, %v", saved, err)
	}
	jpeg := e.image(t, "VOC/JPEGImages/b.jpg")
	if _, err := e.svc.DeleteAnnotation(ctx, e.d.ID, jpeg.ID, e.detail(t, jpeg).AnnotationVersion, false); err != nil {
		t.Fatalf("delete JPEGImages/b.jpg: %v", err)
	}
	e.rescan(t)
	if got := e.detail(t, e.image(t, "VOC/images/b.jpg")); !reflect.DeepEqual(objectNames(got.Objects), []string{"mine"}) {
		t.Errorf("images/b.jpg after a rescan = %+v", got)
	}
	if got := e.detail(t, e.image(t, "VOC/JPEGImages/b.jpg")); got.AnnotationState != annotationNone || got.AnnotationBlocked != "" {
		t.Errorf("JPEGImages/b.jpg after a rescan = %+v", got)
	}
}

// Deleting the file of an image that has another candidate pairs the image
// with that file, as a scan would.
func TestIntegrationDeleteAnnotationFallsBack(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/a.jpg":  pngFile(t, 16, 16),
		"VOC/Annotations/.keep": nil,
		"VOC/JPEGImages/h.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/h.xml":  vocFile("h.jpg", "own"),
		"VOC/images/h.png":      pngFile(t, 16, 16),
	})
	ctx := context.Background()
	a := e.image(t, "VOC/JPEGImages/a.jpg")
	first, err := e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{Objects: boxNamed("platform")})
	if err != nil || *first.AnnotationPath != "VOC/Annotations/a.xml" {
		t.Fatalf("first save = %+v, %v", first, err)
	}
	writeFiles(t, e.dir, map[string][]byte{"VOC/JPEGImages/a.xml": vocFile("a.jpg", "labelimg")})
	got := e.detail(t, a)
	if *got.AnnotationPath != "VOC/JPEGImages/a.xml" {
		t.Fatalf("detail = %+v", got)
	}
	gone, err := e.svc.DeleteAnnotation(ctx, e.d.ID, a.ID, got.AnnotationVersion, false)
	if err != nil || gone.AnnotationState != annotationAnnotated || gone.AnnotationPath == nil || *gone.AnnotationPath != "VOC/Annotations/a.xml" ||
		gone.AnnotationVersion != first.AnnotationVersion || !reflect.DeepEqual(objectNames(gone.Objects), []string{"platform"}) ||
		gone.Annotation == nil || gone.Annotation.ObjectCount != 1 || gone.AnnotationError != "" {
		t.Fatalf("deleted = %+v, %v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "VOC", "JPEGImages", "a.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a.xml still there: %v", err)
	}
	if img := e.image(t, "VOC/JPEGImages/a.jpg"); img.AnnotationPath == nil || *img.AnnotationPath != "VOC/Annotations/a.xml" ||
		img.AnnotationState != annotationAnnotated {
		t.Errorf("indexed image = %+v", img)
	}
	if d := e.dataset(t); d.AnnotatedCount != 2 {
		t.Errorf("counters after delete = %+v", d)
	}
	if after := e.detail(t, a); after.AnnotationVersion != gone.AnnotationVersion || !reflect.DeepEqual(after.Objects, gone.Objects) {
		t.Errorf("detail after delete = %+v", after)
	}
	// The index is what a scan writes.
	job, err := e.svc.StartScan(ctx, e.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job = e.waitJob(t, e.d.ID, job.ID); job.Summary[sumImagesUpdated] != 0 {
		t.Errorf("rescan after delete = %+v", job.Summary)
	}
	// The next edit starts from that file.
	saved, err := e.svc.SaveAnnotation(ctx, e.d.ID, a.ID, SaveAnnotationInput{Objects: boxNamed("again"), BaseVersion: gone.AnnotationVersion})
	if err != nil || *saved.AnnotationPath != "VOC/Annotations/a.xml" {
		t.Errorf("save after delete = %+v, %v", saved, err)
	}

	// A remaining file that a scan gives to another image: the image is
	// unannotated, and the result says why it cannot be edited.
	writeFiles(t, e.dir, map[string][]byte{"VOC/Annotations/h.xml": vocFile("h.png", "for-png")})
	h := e.image(t, "VOC/JPEGImages/h.jpg")
	gone, err = e.svc.DeleteAnnotation(ctx, e.d.ID, h.ID, e.detail(t, h).AnnotationVersion, false)
	if err != nil || gone.AnnotationState != annotationNone || gone.AnnotationPath != nil || len(gone.Objects) != 0 ||
		!strings.Contains(gone.AnnotationBlocked, "标注文件 VOC/Annotations/h.xml 属于另一张图片 VOC/images/h.png") {
		t.Fatalf("deleted h.jpg = %+v, %v", gone, err)
	}
	if after := e.detail(t, h); after.AnnotationBlocked != gone.AnnotationBlocked {
		t.Errorf("h.jpg detail after delete = %+v", after)
	}
	if img := e.image(t, "VOC/JPEGImages/h.jpg"); img.AnnotationPath != nil || img.AnnotationState != annotationNone {
		t.Errorf("indexed h.jpg = %+v", img)
	}
	e.rescan(t)
	if img := e.image(t, "VOC/images/h.png"); img.AnnotationPath == nil || *img.AnnotationPath != "VOC/Annotations/h.xml" {
		t.Errorf("h.png after a rescan = %+v", img)
	}
	if img := e.image(t, "VOC/JPEGImages/h.jpg"); img.AnnotationPath != nil {
		t.Errorf("h.jpg after a rescan = %+v", img)
	}
}

// Deleting a file that other images claim too (and lose) makes them claim
// their next candidate: a delete that would hand them a file the index
// pairs with another image is refused, so the boxes saved for that image
// stay with it.
func TestIntegrationDeleteAnnotationFreesClaimants(t *testing.T) {
	files := map[string][]byte{
		// JPEGImages/b.jpg loses b.xml to b.png (LabelImg's file for it), and
		// would win Annotations/b.xml from images/b.jpg (first in byte order).
		"VOC/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"VOC/JPEGImages/b.png":  pngFile(t, 16, 16),
		"VOC/JPEGImages/b.xml":  vocFile("b.png", "labelimg"),
		"VOC/images/b.jpg":      pngFile(t, 16, 16),
		"VOC/Annotations/b.xml": vocFile("b.jpg", "tool"),
		// The same without namesakes: no <filename> matches.
		"VOC2/JPEGImages/b.jpeg": pngFile(t, 16, 16),
		"VOC2/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"VOC2/JPEGImages/b.xml":  vocFile("b.jpg", "labelimg"),
		"VOC2/images/b.png":      pngFile(t, 16, 16),
		"VOC2/Annotations/b.xml": vocFile("IMG_0001.png", "tool"),
		// Nobody has Annotations/d.xml: JPEGImages/d.jpg takes it.
		"VOC4/JPEGImages/d.jpg":  pngFile(t, 16, 16),
		"VOC4/JPEGImages/d.png":  pngFile(t, 16, 16),
		"VOC4/JPEGImages/d.xml":  vocFile("d.png", "labelimg"),
		"VOC4/Annotations/d.xml": vocFile("d.jpg", "orphan"),
		// images/h.png has Annotations/h.xml; another tool then rewrites it
		// for images/h.jpeg.
		"VOC5/JPEGImages/h.jpg":  pngFile(t, 16, 16),
		"VOC5/JPEGImages/h.xml":  vocFile("h.jpg", "own"),
		"VOC5/images/h.png":      pngFile(t, 16, 16),
		"VOC5/images/h.jpeg":     pngFile(t, 16, 16),
		"VOC5/Annotations/h.xml": vocFile("h.png", "for-png"),
	}
	sensitive := caseSensitive(t)
	if sensitive {
		// Annotations/a.jpg wins Annotations/a.xml from JPEGImages/a.png,
		// whose next candidate is annotations/a.xml of annotations/a.png;
		// JPEGImages/a.png would win that one (first in byte order).
		files["VOC3/Annotations/a.jpg"] = pngFile(t, 16, 16)
		files["VOC3/Annotations/a.xml"] = vocFile("a.jpg", "own")
		files["VOC3/JPEGImages/a.png"] = pngFile(t, 16, 16)
		files["VOC3/annotations/a.png"] = pngFile(t, 16, 16)
		files["VOC3/annotations/a.xml"] = vocFile("a.png", "tool")
	}
	e := newAnnotationEnv(t, files)
	ctx := context.Background()
	remove := func(rel string, force bool) (ImageDetail, error) {
		img := e.image(t, rel)
		return e.svc.DeleteAnnotation(ctx, e.d.ID, img.ID, e.detail(t, img).AnnotationVersion, force)
	}
	saveBox := func(rel, label string) {
		t.Helper()
		img := e.image(t, rel)
		if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, img.ID, SaveAnnotationInput{Objects: boxNamed(label), BaseVersion: e.detail(t, img).AnnotationVersion}); err != nil {
			t.Fatalf("save %s: %v", rel, err)
		}
	}

	refused := map[string]string{
		"VOC/JPEGImages/b.png": "删除 VOC/JPEGImages/b.xml 后，重新扫描会把图片 VOC/images/b.jpg 的标注文件 VOC/Annotations/b.xml " +
			"改为配对给同一文件夹中的另一张图片 VOC/JPEGImages/b.jpg",
		"VOC2/JPEGImages/b.jpg": "重新扫描会把图片 VOC2/images/b.png 的标注文件 VOC2/Annotations/b.xml 改为配对给同一文件夹中的另一张图片 VOC2/JPEGImages/b.jpeg",
	}
	kept := map[string][]string{"VOC/images/b.jpg": {"z-platform"}, "VOC2/images/b.png": {"z-platform"},
		"VOC/JPEGImages/b.png": {"labelimg"}, "VOC2/JPEGImages/b.jpg": {"labelimg"}}
	saveBox("VOC/images/b.jpg", "z-platform")
	saveBox("VOC2/images/b.png", "z-platform")
	if sensitive {
		refused["VOC3/Annotations/a.jpg"] = "重新扫描会把图片 VOC3/annotations/a.png 的标注文件 VOC3/annotations/a.xml 改为配对给另一张图片 VOC3/JPEGImages/a.png"
		kept["VOC3/annotations/a.png"], kept["VOC3/Annotations/a.jpg"] = []string{"o-platform"}, []string{"own"}
		saveBox("VOC3/annotations/a.png", "o-platform")
	}
	for rel, msg := range refused {
		for _, force := range []bool{false, true} {
			_, err := remove(rel, force)
			wantKind(t, err, ErrConflict, msg)
			wantKind(t, err, ErrConflict, "如需清空这张图片的框，请把它标为空标注")
		}
	}

	// A file nobody has goes to the image that wins it: the result names it.
	gone, err := remove("VOC4/JPEGImages/d.png", false)
	if err != nil || gone.AnnotationState != annotationNone ||
		!strings.Contains(gone.AnnotationBlocked, "标注文件 VOC4/Annotations/d.xml 属于同一文件夹中的另一张图片 VOC4/JPEGImages/d.jpg") {
		t.Fatalf("delete d.png = %+v, %v", gone, err)
	}
	if got := e.detail(t, e.image(t, "VOC4/JPEGImages/d.jpg")); !reflect.DeepEqual(objectNames(got.Objects), []string{"orphan"}) || got.AnnotationBlocked != "" {
		t.Errorf("d.jpg after the delete = %+v", got)
	}
	// The index pairs Annotations/h.xml with images/h.png, but a scan gives it
	// to images/h.jpeg already: the result names the image a scan gives it.
	writeFiles(t, e.dir, map[string][]byte{"VOC5/Annotations/h.xml": vocFile("h.jpeg", "for-jpeg")})
	gone, err = remove("VOC5/JPEGImages/h.jpg", false)
	if err != nil || !strings.Contains(gone.AnnotationBlocked, "标注文件 VOC5/Annotations/h.xml 属于另一张图片 VOC5/images/h.jpeg") {
		t.Fatalf("delete h.jpg = %+v, %v", gone, err)
	}

	e.rescan(t)
	kept["VOC4/JPEGImages/d.jpg"], kept["VOC5/images/h.jpeg"] = []string{"orphan"}, []string{"for-jpeg"}
	for rel, want := range kept {
		if got := e.detail(t, e.image(t, rel)); !reflect.DeepEqual(objectNames(got.Objects), want) || got.AnnotationBlocked != "" {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
	for rel, owner := range map[string]string{"VOC4/JPEGImages/d.png": "VOC4/JPEGImages/d.jpg", "VOC5/JPEGImages/h.jpg": "VOC5/images/h.jpeg"} {
		if got := e.detail(t, e.image(t, rel)); !strings.Contains(got.AnnotationBlocked, owner) {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
}

// Images inside an "Annotations" folder claim the files there too.
func TestIntegrationAnnotationsFolderImages(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		"VOC/JPEGImages/n.jpg":  pngFile(t, 16, 16),
		"VOC/Annotations/n.jpg": pngFile(t, 16, 16),
		// annotations/r.jpg comes after JPEGImages/r.jpg in byte order.
		"low/JPEGImages/r.jpg":  pngFile(t, 16, 16),
		"low/annotations/r.jpg": pngFile(t, 16, 16),
		// The scan gives Annotations/s.xml to JPEGImages/s.jpg by its <filename>.
		"pat/JPEGImages/s.jpg":  pngFile(t, 16, 16),
		"pat/Annotations/s.png": pngFile(t, 16, 16),
		"pat/Annotations/s.xml": vocFile("s.jpg", "shared"),
	})
	ctx := context.Background()
	save := func(rel, label string) (ImageDetail, error) {
		img := e.image(t, rel)
		return e.svc.SaveAnnotation(ctx, e.d.ID, img.ID, SaveAnnotationInput{Objects: boxNamed(label), BaseVersion: e.detail(t, img).AnnotationVersion})
	}

	// Annotations/n.jpg would take a new Annotations/n.xml (first in byte
	// order): JPEGImages/n.jpg gets a file next to it.
	if saved, err := save("VOC/JPEGImages/n.jpg", "jpeg"); err != nil || *saved.AnnotationPath != "VOC/JPEGImages/n.xml" {
		t.Fatalf("JPEGImages/n.jpg save = %+v, %v", saved, err)
	}
	if saved, err := save("VOC/JPEGImages/n.jpg", "jpeg-2"); err != nil || *saved.AnnotationPath != "VOC/JPEGImages/n.xml" {
		t.Fatalf("JPEGImages/n.jpg second save = %+v, %v", saved, err)
	}
	if saved, err := save("VOC/Annotations/n.jpg", "inner"); err != nil || *saved.AnnotationPath != "VOC/Annotations/n.xml" {
		t.Fatalf("Annotations/n.jpg save = %+v, %v", saved, err)
	}

	// JPEGImages/r.jpg, without a file of its own, would take a new
	// annotations/r.xml: annotations/r.jpg cannot create it.
	inner := e.image(t, "low/annotations/r.jpg")
	if got := e.detail(t, inner); !strings.Contains(got.AnnotationBlocked, "标注文件 low/annotations/r.xml 属于另一张图片 low/JPEGImages/r.jpg") {
		t.Errorf("annotations/r.jpg detail = %+v", got)
	}
	_, err := save("low/annotations/r.jpg", "inner")
	wantKind(t, err, ErrConflict, "属于另一张图片 low/JPEGImages/r.jpg")
	if saved, err := save("low/JPEGImages/r.jpg", "jpeg"); err != nil || *saved.AnnotationPath != "low/JPEGImages/r.xml" {
		t.Fatalf("JPEGImages/r.jpg save = %+v, %v", saved, err)
	}
	if saved, err := save("low/annotations/r.jpg", "inner"); err != nil || *saved.AnnotationPath != "low/annotations/r.xml" {
		t.Fatalf("annotations/r.jpg save = %+v, %v", saved, err)
	}
	jpeg := e.image(t, "low/JPEGImages/r.jpg")
	_, err = e.svc.DeleteAnnotation(ctx, e.d.ID, jpeg.ID, e.detail(t, jpeg).AnnotationVersion, false)
	wantKind(t, err, ErrConflict, "重新扫描会把另一张图片 low/annotations/r.jpg 的标注文件 low/annotations/r.xml 配对给这张图片")

	// Another tool rewrites Annotations/s.xml for Annotations/s.png: a scan
	// gives it to that image now.
	png := vocFile("s.png", "png")
	writeFiles(t, e.dir, map[string][]byte{"pat/Annotations/s.xml": png})
	_, err = save("pat/JPEGImages/s.jpg", "jpeg")
	wantKind(t, err, ErrConflict, "标注文件 pat/Annotations/s.xml 按 VOC 的配对规则现在属于另一张图片 pat/Annotations/s.png")
	if data := e.readFile(t, "pat/Annotations/s.xml"); !bytes.Equal(data, png) {
		t.Errorf("JPEGImages/s.jpg changed the file:\n%s", data)
	}

	e.rescan(t)
	for rel, want := range map[string][]string{
		"VOC/JPEGImages/n.jpg": {"jpeg-2"}, "VOC/Annotations/n.jpg": {"inner"},
		"low/JPEGImages/r.jpg": {"jpeg"}, "low/annotations/r.jpg": {"inner"}, "pat/Annotations/s.png": {"png"},
	} {
		if got := e.detail(t, e.image(t, rel)); !reflect.DeepEqual(objectNames(got.Objects), want) || got.AnnotationBlocked != "" {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
}

// On a file system that tells case apart, a new file in the "Annotations"
// folder that comes first in byte order (ANNOTATIONS/) would come before
// the file another image has in the next one (annotations/): the image
// that would create it is refused, so the other image keeps its boxes.
func TestIntegrationCaseVariantAnnotationsFolders(t *testing.T) {
	if !caseSensitive(t) {
		t.Skip("needs a file system that tells case apart")
	}
	e := newAnnotationEnv(t, map[string][]byte{
		// JPEGImages/a.jpg wins annotations/a.xml (first in byte order);
		// ANNOTATIONS/a.png would write ANNOTATIONS/a.xml next to it.
		"V/JPEGImages/a.jpg":  pngFile(t, 16, 16),
		"V/annotations/a.jpg": pngFile(t, 16, 16),
		"V/annotations/a.xml": vocFile("a.jpg", "tool"),
		"V/ANNOTATIONS/a.png": pngFile(t, 16, 16),
		// ANNOTATIONS/b.png has ANNOTATIONS/b.xml; JPEGImages/b.jpg claims it
		// first and loses it, so annotations/b.xml is nobody's until then.
		"W/JPEGImages/b.jpg":  pngFile(t, 16, 16),
		"W/annotations/b.xml": vocFile("b.jpg", "waiting"),
		"W/ANNOTATIONS/b.png": pngFile(t, 16, 16),
		"W/ANNOTATIONS/b.xml": vocFile("b.png", "png"),
	})
	ctx := context.Background()
	jpeg := e.image(t, "V/JPEGImages/a.jpg")
	if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, jpeg.ID, SaveAnnotationInput{Objects: boxNamed("platform"), BaseVersion: e.detail(t, jpeg).AnnotationVersion}); err != nil {
		t.Fatalf("save JPEGImages/a.jpg: %v", err)
	}
	png := e.image(t, "V/ANNOTATIONS/a.png")
	const blocked = "标注文件 V/ANNOTATIONS/a.xml 属于另一张图片 V/JPEGImages/a.jpg"
	if got := e.detail(t, png); !strings.Contains(got.AnnotationBlocked, blocked) {
		t.Errorf("ANNOTATIONS/a.png detail = %+v", got)
	}
	for _, force := range []bool{false, true} {
		_, err := e.svc.SaveAnnotation(ctx, e.d.ID, png.ID, SaveAnnotationInput{Objects: boxNamed("png"), Force: force})
		wantKind(t, err, ErrConflict, blocked)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "V", "ANNOTATIONS", "a.xml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ANNOTATIONS/a.xml written: %v", err)
	}

	// Once ANNOTATIONS/b.xml is deleted, JPEGImages/b.jpg gets
	// annotations/b.xml: the result says that b.png cannot have a file again,
	// as the next view does.
	bpng := e.image(t, "W/ANNOTATIONS/b.png")
	gone, err := e.svc.DeleteAnnotation(ctx, e.d.ID, bpng.ID, e.detail(t, bpng).AnnotationVersion, false)
	const bBlocked = "标注文件 W/ANNOTATIONS/b.xml 属于另一张图片 W/JPEGImages/b.jpg"
	if err != nil || gone.AnnotationState != annotationNone || !strings.Contains(gone.AnnotationBlocked, bBlocked) {
		t.Fatalf("delete ANNOTATIONS/b.xml = %+v, %v", gone, err)
	}
	if got := e.detail(t, bpng); got.AnnotationBlocked != gone.AnnotationBlocked {
		t.Errorf("ANNOTATIONS/b.png after the delete = %+v, the delete said %q", got, gone.AnnotationBlocked)
	}

	e.rescan(t)
	for rel, want := range map[string][]string{
		"V/JPEGImages/a.jpg": {"platform"}, "V/annotations/a.jpg": {}, "W/JPEGImages/b.jpg": {"waiting"},
	} {
		if got := e.detail(t, e.image(t, rel)); !reflect.DeepEqual(objectNames(got.Objects), want) {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
	}
}

// An image blocked by another image whose pairing in the index is out of
// date is told to rescan, not to rename an image.
func TestIntegrationStaleAnnotationOwner(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{
		// The scan gives Annotations/a.xml to JPEGImages/a.jpg (first in
		// byte order); LabelImg then writes JPEGImages/a.xml.
		"VOC/JPEGImages/a.jpg":  pngFile(t, 16, 16),
		"VOC/images/a.jpg":      pngFile(t, 16, 16),
		"VOC/Annotations/a.xml": vocFile("a.jpg", "platform"),
		"flat/c.jpg":            pngFile(t, 16, 16),
		"flat/c.png":            pngFile(t, 16, 16),
		"flat/c.xml":            vocFile("c.jpg", "jpg"),
		"flat/d.jpg":            pngFile(t, 16, 16),
		"flat/d.png":            pngFile(t, 16, 16),
		"flat/d.xml":            vocFile("d.jpg", "jpg"),
		"flat/g.jpg":            pngFile(t, 16, 16),
		"flat/g.png":            pngFile(t, 16, 16),
		"flat/g.xml":            vocFile("g.jpg", "jpg"),
	})
	ctx := context.Background()
	writeFiles(t, e.dir, map[string][]byte{
		"VOC/JPEGImages/a.xml": vocFile("a.jpg", "labelimg"),
		"flat/g.xml":           vocFile("g.png", "png"), // rewritten for g.png
	})
	for _, rel := range []string{"flat/c.xml", "flat/d.jpg"} {
		if err := os.Remove(filepath.Join(e.dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.detail(t, e.image(t, "VOC/JPEGImages/a.jpg")); *got.AnnotationPath != "VOC/JPEGImages/a.xml" {
		t.Fatalf("JPEGImages/a.jpg detail = %+v", got)
	}
	for rel, owner := range map[string]string{
		"VOC/images/a.jpg": "VOC/Annotations/a.xml 在索引中仍与图片 VOC/JPEGImages/a.jpg 配对",
		"flat/c.png":       "flat/c.xml 在索引中仍与图片 flat/c.jpg 配对", // c.xml is gone
		"flat/d.png":       "flat/d.xml 在索引中仍与图片 flat/d.jpg 配对", // d.jpg is gone
		"flat/g.png":       "flat/g.xml 在索引中仍与图片 flat/g.jpg 配对", // g.xml names g.png
	} {
		img := e.image(t, rel)
		got := e.detail(t, img)
		if !strings.Contains(got.AnnotationBlocked, owner) || !strings.Contains(got.AnnotationBlocked, "配对已过期") ||
			!strings.Contains(got.AnnotationBlocked, "请先重新扫描数据集") || strings.Contains(got.AnnotationBlocked, "重命名") {
			t.Errorf("%s detail = %+v", rel, got)
		}
		_, err := e.svc.SaveAnnotation(ctx, e.d.ID, img.ID, SaveAnnotationInput{Objects: boxNamed("x"), Force: true})
		wantKind(t, err, ErrConflict, "配对已过期")
	}
	if data := e.readFile(t, "VOC/Annotations/a.xml"); !bytes.Equal(data, vocFile("a.jpg", "platform")) {
		t.Errorf("a blocked save changed the file:\n%s", data)
	}

	e.rescan(t)
	for rel, want := range map[string][]string{
		"VOC/images/a.jpg": {"platform"}, "flat/c.png": {}, "flat/d.png": {"jpg"}, "flat/g.png": {"png"},
	} {
		img := e.image(t, rel)
		got := e.detail(t, img)
		if got.AnnotationBlocked != "" || !reflect.DeepEqual(objectNames(got.Objects), want) {
			t.Errorf("%s after a rescan = %+v", rel, got)
		}
		if _, err := e.svc.SaveAnnotation(ctx, e.d.ID, img.ID, SaveAnnotationInput{Objects: boxNamed("x"), BaseVersion: got.AnnotationVersion}); err != nil {
			t.Errorf("%s save after a rescan: %v", rel, err)
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
	// on purpose (see TestIntegrationRegenerateAnnotation).
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
	saved, err = e.svc.SaveAnnotation(ctx, e.d.ID, d.ID, SaveAnnotationInput{Objects: renamed, BaseVersion: detail.AnnotationVersion, Regenerate: true})
	if err != nil || !reflect.DeepEqual(saved.Objects, renamed) || saved.AnnotationVersion != annotationVersion(e.readFile(t, "images/d.xml")) {
		t.Errorf("regenerating save = %+v, %v", saved, err)
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

// Regenerating a file that cannot be edited in place and overwriting a
// concurrent change are separate consents.
func TestIntegrationRegenerateAnnotation(t *testing.T) {
	e := newAnnotationEnv(t, map[string][]byte{"images/d.png": pngFile(t, 16, 16), "images/d.xml": unpatchable})
	ctx := context.Background()
	d := e.image(t, "images/d.png")
	v0 := e.detail(t, d).AnnotationVersion
	mine := boxNamed("划痕") // not ASCII: cannot be written into the GBK part
	save := func(base string, force, regenerate bool) (ImageDetail, error) {
		return e.svc.SaveAnnotation(ctx, e.d.ID, d.ID, SaveAnnotationInput{Objects: mine, BaseVersion: base, Force: force, Regenerate: regenerate})
	}
	wantFile := func(want []byte) {
		t.Helper()
		if got := e.readFile(t, "images/d.xml"); !bytes.Equal(got, want) {
			t.Errorf("d.xml =\n%s\nwant\n%s", got, want)
		}
	}

	_, err := save(v0, false, false)
	wantKind(t, err, ErrAnnotationUnpatchable, "无法在保留其中其它内容的情况下修改")
	// Overwriting a newer version does not regenerate the file either.
	_, err = save("stale", true, false)
	wantKind(t, err, ErrAnnotationUnpatchable, "无法在保留其中其它内容的情况下修改")
	wantFile(unpatchable)

	// Regenerating still checks the version: a change made while the user
	// was asked is not lost.
	theirs := vocFile("d.png", "b-box1", "b-box2")
	writeFiles(t, e.dir, map[string][]byte{"images/d.xml": theirs})
	_, err = save(v0, false, true)
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	wantFile(theirs)

	// Both consents: the newer unpatchable file is replaced.
	writeFiles(t, e.dir, map[string][]byte{"images/d.xml": unpatchable})
	saved, err := save("stale", true, true)
	if err != nil || !reflect.DeepEqual(saved.Objects, mine) || saved.AnnotationVersion != annotationVersion(e.readFile(t, "images/d.xml")) {
		t.Errorf("forced regenerating save = %+v, %v", saved, err)
	}
	// A file that can be edited in place still is, also with regenerate.
	writeFiles(t, e.dir, map[string][]byte{"images/d.xml": []byte(labelImgFile)})
	v1 := e.detail(t, d).AnnotationVersion
	if _, err = save(v1, false, true); err != nil {
		t.Fatal(err)
	}
	if got := string(e.readFile(t, "images/d.xml")); !strings.Contains(got, `verified="yes"`) || !strings.Contains(got, "<polygon>") {
		t.Errorf("regenerate rewrote a file it could edit:\n%s", got)
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
