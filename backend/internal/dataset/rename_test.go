package dataset

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// planFor runs the planner over new images given by content (path →
// bytes), with existing images of the dataset and XML files, and returns
// the plans as "from>to" (renamed) or "from=same_name_as" (identical).
func planFor(t *testing.T, existing, incoming map[string]string, xmls []string, pairs map[string]string, onConflict string) []string {
	t.Helper()
	files := func(m map[string]string) map[string]diskFile {
		out := map[string]diskFile{}
		for rel, data := range m {
			out[rel] = diskFile{Size: int64(len(data))}
		}
		return out
	}
	compared := 0
	same := func(_ context.Context, a, b nameRef) (bool, error) {
		compared++
		content := func(r nameRef) string {
			if r.staged {
				return incoming[r.rel]
			}
			return existing[r.rel]
		}
		return a.size == b.size && content(a) == content(b), nil
	}
	pl := newNamePlanner(context.Background(), files(existing), nil, files(incoming), xmls, onConflict, same)
	plans, err := planRenames(pl, files(incoming), pairs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range plans {
		switch {
		case p.Identical:
			out = append(out, p.From+"="+p.SameNameAs)
		case p.XMLFrom != "":
			out = append(out, p.From+">"+p.To+" "+p.XMLFrom+">"+p.XMLTo)
		default:
			out = append(out, p.From+">"+p.To)
		}
	}
	return out
}

func TestPlanRenames(t *testing.T) {
	tests := []struct {
		name       string
		existing   map[string]string
		incoming   map[string]string
		xmls       []string
		pairs      map[string]string
		onConflict string
		want       []string
	}{
		{
			name:     "different content in two folders: the later one is renamed with its annotation",
			incoming: map[string]string{"cam2/0001.jpg": "B", "cam1/0001.jpg": "A", "cam1/0002.jpg": "C"},
			xmls:     []string{"cam1/0001.xml", "cam2/0001.xml"},
			pairs:    map[string]string{"cam1/0001.jpg": "cam1/0001.xml", "cam2/0001.jpg": "cam2/0001.xml"},
			want:     []string{"cam2/0001.jpg>cam2/0001_2.jpg cam2/0001.xml>cam2/0001_2.xml"},
		},
		{
			name:     "identical content keeps the name; three folders count up",
			incoming: map[string]string{"a/x.png": "1", "b/x.png": "2", "c/x.png": "1", "d/x.png": "3"},
			want:     []string{"b/x.png>b/x_2.png", "c/x.png=a/x.png", "d/x.png>d/x_3.png"},
		},
		{
			name:     "names compare without extension and case, also at the top level",
			incoming: map[string]string{"A/Img.JPG": "1", "B/img.png": "2", "img.bmp": "3"},
			want:     []string{"B/img.png>B/img_2.png", "img.bmp>img_3.bmp"},
		},
		{
			name:     "a numbered name another file has is skipped",
			incoming: map[string]string{"a/p.jpg": "1", "b/p.jpg": "2", "c/p_2.jpg": "3"},
			xmls:     []string{"z/P_3.xml"},
			want:     []string{"b/p.jpg>b/p_4.jpg"},
		},
		{
			name:     "images of the dataset keep their names",
			existing: map[string]string{"old/0001.jpg": "A"},
			incoming: map[string]string{"new/0001.jpg": "B", "new/0002.jpg": "C"},
			want:     []string{"new/0001.jpg>new/0001_2.jpg"},
		},
		{
			name:     "a new image identical to the dataset's keeps its name",
			existing: map[string]string{"old/0001.jpg": "A"},
			incoming: map[string]string{"new/0001.jpg": "A"},
			want:     []string{"new/0001.jpg=old/0001.jpg"},
		},
		{
			name:     "the same path is the import's conflict, not a rename",
			existing: map[string]string{"a/0001.jpg": "A"},
			incoming: map[string]string{"a/0001.jpg": "B"},
		},
		{
			name:     "imported again: the renamed copy is found by its content",
			existing: map[string]string{"a/0001.jpg": "A", "b/0001_2.jpg": "B"},
			incoming: map[string]string{"a/0001.jpg": "A", "b/0001.jpg": "B"},
			want:     []string{"b/0001.jpg>b/0001_2.jpg"},
		},
		{
			name:     "a numbered copy with other content is not reused",
			existing: map[string]string{"a/0001.jpg": "A", "b/0001_2.jpg": "X"},
			incoming: map[string]string{"b/0001.jpg": "B"},
			want:     []string{"b/0001.jpg>b/0001_3.jpg"},
		},
		{
			name:     "dataset images with two contents: a new image cannot equal both",
			existing: map[string]string{"a/q.jpg": "A", "b/q.jpg": "B"},
			incoming: map[string]string{"c/q.jpg": "A"},
			want:     []string{"c/q.jpg>c/q_2.jpg"},
		},
		{
			name:     "dataset images with one content: an identical new image keeps its name",
			existing: map[string]string{"a/q.jpg": "A", "b/q.jpg": "A"},
			incoming: map[string]string{"c/q.jpg": "A"},
			want:     []string{"c/q.jpg=a/q.jpg"},
		},
		{
			name:       "overwrite: the replacing image stands for the path",
			existing:   map[string]string{"a/q.jpg": "A"},
			incoming:   map[string]string{"a/q.jpg": "B", "b/q.jpg": "B"},
			onConflict: ConflictOverwrite,
			want:       []string{"b/q.jpg=a/q.jpg"},
		},
		{
			name:       "skip: the image of the dataset stands for the path",
			existing:   map[string]string{"a/q.jpg": "A"},
			incoming:   map[string]string{"a/q.jpg": "B", "b/q.jpg": "B"},
			onConflict: ConflictSkip,
			want:       []string{"b/q.jpg>b/q_2.jpg"},
		},
		{
			name:     "VOC layout: the annotation is renamed in Annotations/",
			incoming: map[string]string{"s1/JPEGImages/1.jpg": "A", "s2/JPEGImages/1.jpg": "B"},
			xmls:     []string{"s1/Annotations/1.xml", "s2/Annotations/1.xml"},
			pairs:    map[string]string{"s1/JPEGImages/1.jpg": "s1/Annotations/1.xml", "s2/JPEGImages/1.jpg": "s2/Annotations/1.xml"},
			want:     []string{"s2/JPEGImages/1.jpg>s2/JPEGImages/1_2.jpg s2/Annotations/1.xml>s2/Annotations/1_2.xml"},
		},
		{
			name:     "same folder, other extension",
			incoming: map[string]string{"a/k.jpg": "1", "a/k.png": "2"},
			xmls:     []string{"a/k.xml"},
			pairs:    map[string]string{"a/k.png": "a/k.xml"},
			want:     []string{"a/k.png>a/k_2.png a/k.xml>a/k_2.xml"},
		},
		{
			name:     "first in image order keeps the name (numbers by value)",
			incoming: map[string]string{"batch10/a.jpg": "1", "batch9/a.jpg": "2"},
			want:     []string{"batch10/a.jpg>batch10/a_2.jpg"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planFor(t, tt.existing, tt.incoming, tt.xmls, tt.pairs, tt.onConflict)
			if !slices.Equal(got, tt.want) {
				t.Errorf("plans = %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestPlanRenamesComparesLazily(t *testing.T) {
	// Many images of the dataset share a name with different content: a
	// new image is compared until two contents are known, not with all.
	existing := map[string]string{}
	for _, d := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		existing[d+"/1.jpg"] = d
	}
	compared := 0
	same := func(_ context.Context, a, b nameRef) (bool, error) {
		compared++
		return false, nil
	}
	files := map[string]diskFile{}
	for rel := range existing {
		files[rel] = diskFile{Size: 1}
	}
	pl := newNamePlanner(context.Background(), files, nil, map[string]diskFile{"z/1.jpg": {Size: 1}}, nil, ConflictSkip, same)
	plans, err := planRenames(pl, map[string]diskFile{"z/1.jpg": {Size: 1}}, nil, nil)
	if err != nil || len(plans) != 1 || plans[0].To != "z/1_2.jpg" || plans[0].SameNameAs != "a/1.jpg" {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	if compared > 2 {
		t.Errorf("compared %d times, want at most 2", compared)
	}
}

func TestNumberedStem(t *testing.T) {
	if got := numberedStem("0001", ".jpg", 2); got != "0001_2" {
		t.Errorf("numberedStem = %q", got)
	}
	long := strings.Repeat("划", 90) // 270 bytes
	got := numberedStem(long, ".jpeg", 12)
	if !utf8.ValidString(got) || len(got)+len(".jpeg") > maxFileNameBytes || !strings.HasSuffix(got, "_12") {
		t.Errorf("numberedStem(long) = %q (%d bytes)", got, len(got))
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789"), 20000) // more than one buffer
	other := slices.Clone(big)
	other[len(other)-1] = 'x'
	writeFiles(t, dir, map[string][]byte{"a": big, "b": big, "c": other, "d": big[:100]})
	p := func(n string) string { return filepath.Join(dir, n) }
	ctx := context.Background()
	for _, tt := range []struct {
		a, b string
		sa   int64
		want bool
	}{
		{"a", "b", int64(len(big)), true},
		{"a", "c", int64(len(big)), false},
		{"a", "missing", int64(len(big)), false},
	} {
		if got, err := sameContent(ctx, p(tt.a), p(tt.b), tt.sa, tt.sa); got != tt.want || err != nil {
			t.Errorf("sameContent(%s, %s) = %v, %v", tt.a, tt.b, got, err)
		}
	}
	// A size difference answers without reading.
	if got, _ := sameContent(ctx, p("a"), p("d"), int64(len(big)), 100); got {
		t.Error("different sizes compared equal")
	}
	// The file being shorter than its recorded size is not "the same".
	if got, _ := sameContent(ctx, p("a"), p("d"), 100, 100); got {
		t.Error("files of different lengths compared equal")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sameContent(canceled, p("a"), p("b"), int64(len(big)), int64(len(big))); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled compare: %v", err)
	}
}

func TestRenameVOC(t *testing.T) {
	labelImg := "<annotation verified=\"yes\">\r\n" +
		"\t<folder>cam2</folder>\r\n" +
		"\t<filename>0001.jpg</filename>\r\n" +
		"\t<path>C:\\data\\cam2\\0001.jpg</path>\r\n" +
		"\t<source><database>Unknown</database></source>\r\n" +
		"\t<size><width>64</width><height>48</height><depth>3</depth></size>\r\n" +
		"\t<object><name>划痕</name><pose>Unspecified</pose><bndbox><xmin>1.5</xmin><ymin>2</ymin><xmax>10</xmax><ymax>12</ymax></bndbox><extra>k</extra></object>\r\n" +
		"\t<object><name>poly</name><polygon><x1>1</x1></polygon></object>\r\n" +
		"</annotation>\r\n"
	out, err := renameVOC([]byte(labelImg), "0001_2.jpg")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		`<annotation verified="yes">`, "<folder>cam2</folder>", "<filename>0001_2.jpg</filename>",
		`<path>C:\data\cam2\0001_2.jpg</path>`, "<extra>k</extra>", "<xmin>1.5</xmin>", "<polygon>", "\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renamed file lacks %q:\n%s", want, text)
		}
	}
	a, err := parseVOC(bytes.NewReader(out))
	if err != nil || a.Filename != "0001_2.jpg" || len(a.Objects) != 1 || a.Unsupported != 1 || a.Width != 64 {
		t.Errorf("renamed file reads as %+v, %v", a, err)
	}

	// GBK files are rewritten as UTF-8; a path with "/" and a name with
	// characters XML escapes.
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String(`<?xml version="1.0" encoding="GBK"?>` +
		"<annotation><filename>划痕.jpg</filename><path>/data/缺陷/划痕.jpg</path></annotation>")
	out, err = renameVOC([]byte(gbk), "划痕 & 污_2.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `encoding="utf-8"`) || !strings.Contains(string(out), "<path>/data/缺陷/划痕 &amp; 污_2.jpg</path>") {
		t.Errorf("GBK file renamed to:\n%s", out)
	}
	if a, err := parseVOC(bytes.NewReader(out)); err != nil || a.Filename != "划痕 & 污_2.jpg" {
		t.Errorf("renamed GBK file reads as %+v, %v", a, err)
	}

	// A file without <filename> keeps having none; CDATA and an empty
	// element are rewritten.
	out, err = renameVOC([]byte("<annotation><path><![CDATA[a/0001.jpg]]></path><object><name>x</name><bndbox><xmin>1</xmin><ymin>1</ymin><xmax>2</xmax><ymax>2</ymax></bndbox></object></annotation>"), "0001_2.jpg")
	if err != nil || !strings.Contains(string(out), "<path>a/0001_2.jpg</path>") || strings.Contains(string(out), "<filename>") {
		t.Errorf("renameVOC = %s, %v", out, err)
	}
	out, err = renameVOC([]byte("<annotation><filename/></annotation>"), "b.jpg")
	if err != nil || !strings.Contains(string(out), "<filename>b.jpg</filename>") {
		t.Errorf("renameVOC(<filename/>) = %s, %v", out, err)
	}

	for _, bad := range []string{"<annotation><object>", "<other/>", "not xml"} {
		if _, err := renameVOC([]byte(bad), "x.jpg"); !errors.Is(err, errNotPatchable) {
			t.Errorf("renameVOC(%q) = %v, want errNotPatchable", bad, err)
		}
	}
}

func TestRenameImageFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{
		"b/0001.jpg":   []byte("img"),
		"b/0001.xml":   vocFile("0001.jpg", "dent"),
		"c/k.png":      []byte("img"),
		"c/k.xml":      []byte("<annotation><object>"), // not readable as VOC: renamed as is
		"d/x.jpg":      []byte("img"),
		"d/x_2.jpg":    []byte("taken"),
		"e/y.jpg":      []byte("img"),
		"e/y.xml":      vocFile("y.jpg"),
		"e/y_2.xml":    []byte("taken"),
		"f/plain.tiff": []byte("img"),
	})
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}

	if err := renameImageFiles(root, &renamePlan{From: "b/0001.jpg", To: "b/0001_2.jpg", XMLFrom: "b/0001.xml", XMLTo: "b/0001_2.xml"}); err != nil {
		t.Fatal(err)
	}
	if read("b/0001.jpg") != "<missing>" || read("b/0001.xml") != "<missing>" || read("b/0001_2.jpg") != "img" ||
		!strings.Contains(read("b/0001_2.xml"), "<filename>0001_2.jpg</filename>") {
		t.Errorf("b after rename: %v", datasetFiles(t, filepath.Join(dir, "b")))
	}

	if err := renameImageFiles(root, &renamePlan{From: "c/k.png", To: "c/k_2.png", XMLFrom: "c/k.xml", XMLTo: "c/k_2.xml"}); err != nil {
		t.Fatal(err)
	}
	if read("c/k_2.png") != "img" || read("c/k_2.xml") != "<annotation><object>" || read("c/k.xml") != "<missing>" {
		t.Errorf("c after rename: %v", datasetFiles(t, filepath.Join(dir, "c")))
	}

	// Targets that exist are never replaced, and nothing changes.
	err = renameImageFiles(root, &renamePlan{From: "d/x.jpg", To: "d/x_2.jpg"})
	if !errors.Is(err, os.ErrExist) || read("d/x.jpg") != "img" || read("d/x_2.jpg") != "taken" {
		t.Errorf("rename onto an image: %v", err)
	}
	err = renameImageFiles(root, &renamePlan{From: "e/y.jpg", To: "e/y_2.jpg", XMLFrom: "e/y.xml", XMLTo: "e/y_2.xml"})
	if !errors.Is(err, os.ErrExist) || read("e/y.jpg") != "img" || read("e/y_2.jpg") != "<missing>" || read("e/y_2.xml") != "taken" {
		t.Errorf("rename onto an annotation: %v", err)
	}

	if err := renameImageFiles(root, &renamePlan{From: "f/plain.tiff", To: "f/plain_2.tiff"}); err != nil || read("f/plain_2.tiff") != "img" {
		t.Errorf("rename without annotation: %v", err)
	}
}
