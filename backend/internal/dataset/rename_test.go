package dataset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// planFor runs the planner over new images given by content (path →
// bytes), with existing images and XML files of the dataset and new XML
// files, and returns the plans as "from>to" (renamed) or
// "from=same_name_as" (identical).
func planFor(t *testing.T, existing, incoming map[string]string, existingXMLs, xmls []string, pairs map[string]string, onConflict string) []string {
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
	content := func(r nameRef) string {
		if r.staged {
			return incoming[r.rel]
		}
		return existing[r.rel]
	}
	digest := func(_ context.Context, r nameRef) (string, error) { return content(r), nil }
	pl := newNamePlanner(context.Background(), files(existing), existingXMLs, files(incoming), xmls, onConflict, same, digest)
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
		oldXMLs    []string // of the dataset
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
			name:     "a copy of a renamed image gets the same number in its folder",
			incoming: map[string]string{"cam1/0001.jpg": "A", "cam2/0001.jpg": "B", "cam3/0001.jpg": "B", "cam4/0001.jpg": "C"},
			xmls:     []string{"cam3/0001.xml"},
			pairs:    map[string]string{"cam3/0001.jpg": "cam3/0001.xml"},
			want: []string{"cam2/0001.jpg>cam2/0001_2.jpg", "cam3/0001.jpg>cam3/0001_2.jpg cam3/0001.xml>cam3/0001_2.xml",
				"cam4/0001.jpg>cam4/0001_3.jpg"},
		},
		{
			name:     "a copy of an image renamed by an earlier import",
			existing: map[string]string{"cam1/0001.jpg": "A", "cam2/0001_2.jpg": "B"},
			incoming: map[string]string{"cam3/0001.jpg": "B"},
			want:     []string{"cam3/0001.jpg>cam3/0001_2.jpg"},
		},
		{
			name:     "a copy does not take a number its folder already has",
			existing: map[string]string{"a/n.jpg": "A", "b/n_2.jpg": "B"},
			incoming: map[string]string{"c/n.jpg": "B"},
			xmls:     []string{"c/n_2.xml"},
			want:     []string{"c/n.jpg>c/n_3.jpg"},
		},
		{
			name:     "a copy does not take a number its folder has (name with a dot)",
			existing: map[string]string{"a/x.v1.jpg": "A", "b/x.v1_2.jpg": "B"},
			oldXMLs:  []string{"c/x.v1_2.xml"},
			incoming: map[string]string{"c/x.v1.jpg": "B"},
			want:     []string{"c/x.v1.jpg>c/x.v1_3.jpg"},
		},
		{
			name:     "a copy does not take a number its annotation folder has",
			existing: map[string]string{"a/x.jpg": "A", "b/x_2.jpg": "B"},
			oldXMLs:  []string{"c/Annotations/x_2.xml"},
			incoming: map[string]string{"c/JPEGImages/x.jpg": "B"},
			xmls:     []string{"c/Annotations/x.xml"},
			pairs:    map[string]string{"c/JPEGImages/x.jpg": "c/Annotations/x.xml"},
			want:     []string{"c/JPEGImages/x.jpg>c/JPEGImages/x_3.jpg c/Annotations/x.xml>c/Annotations/x_3.xml"},
		},
		{
			name:     "a copy in JPEGImages/ without annotation does not take a number Annotations/ has",
			existing: map[string]string{"a/JPEGImages/n.jpg": "A", "b/JPEGImages/n_2.jpg": "B"},
			oldXMLs:  []string{"c/Annotations/n_2.xml"},
			incoming: map[string]string{"c/JPEGImages/n.jpg": "B"},
			want:     []string{"c/JPEGImages/n.jpg>c/JPEGImages/n_3.jpg"},
		},
		{
			// The same picture twice in one folder: VOC's <filename> decides
			// which one the annotation belongs to, as before.
			name:     "identical images in one folder keep their names",
			incoming: map[string]string{"a/k.jpg": "1", "a/k.JPG": "1"},
			want:     []string{"a/k.jpg=a/k.JPG"},
		},
		{
			name:     "a copy does not take a number held by images of two contents",
			existing: map[string]string{"a/n.jpg": "A", "b/n_2.jpg": "B", "d/n_2.jpg": "C"},
			incoming: map[string]string{"c/n.jpg": "B"},
			want:     []string{"c/n.jpg>c/n_3.jpg"},
		},
		{
			name: "copies among many numbers of one size",
			incoming: map[string]string{"x1/f.bmp": "A", "x2/f.bmp": "B", "x3/f.bmp": "C", "x4/f.bmp": "D",
				"x5/f.bmp": "C", "x6/f.bmp": "B", "x7/f.bmp": "E"},
			want: []string{"x2/f.bmp>x2/f_2.bmp", "x3/f.bmp>x3/f_3.bmp", "x4/f.bmp>x4/f_4.bmp",
				"x5/f.bmp>x5/f_3.bmp", "x6/f.bmp>x6/f_2.bmp", "x7/f.bmp>x7/f_5.bmp"},
		},
		{
			name:     "first in image order keeps the name (numbers by value)",
			incoming: map[string]string{"batch10/a.jpg": "1", "batch9/a.jpg": "2"},
			want:     []string{"batch10/a.jpg>batch10/a_2.jpg"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planFor(t, tt.existing, tt.incoming, tt.oldXMLs, tt.xmls, tt.pairs, tt.onConflict)
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
	digest := func(_ context.Context, r nameRef) (string, error) {
		t.Errorf("digest(%s): no image should be hashed", r.rel)
		return r.rel, nil
	}
	pl := newNamePlanner(context.Background(), files, nil, map[string]diskFile{"z/1.jpg": {Size: 1}}, nil, ConflictSkip, same, digest)
	plans, err := planRenames(pl, map[string]diskFile{"z/1.jpg": {Size: 1}}, nil, nil)
	if err != nil || len(plans) != 1 || plans[0].To != "z/1_2.jpg" || plans[0].SameNameAs != "a/1.jpg" {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	if compared > 2 {
		t.Errorf("compared %d times, want at most 2", compared)
	}
}

func TestPlanRenamesManyImagesOfOneName(t *testing.T) {
	// One name in many folders (each folder numbers its images from 0001,
	// all of one size like BMP images): numbering and comparing stay linear.
	const n = 20000
	images := make(map[string]diskFile, n)
	for i := range n {
		images[fmt.Sprintf("clip%05d/0001.bmp", i)] = diskFile{Size: 7}
	}
	digests := 0
	same := func(_ context.Context, a, b nameRef) (bool, error) { return a.rel == b.rel, nil }
	digest := func(_ context.Context, r nameRef) (string, error) {
		digests++
		return r.rel, nil
	}
	start := time.Now()
	pl := newNamePlanner(context.Background(), nil, nil, images, nil, ConflictSkip, same, digest)
	plans, err := planRenames(pl, images, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("planning %d images took %v", n, elapsed)
	}
	if len(plans) != n-1 || plans[n-2].To != fmt.Sprintf("clip%05d/0001_%d.bmp", n-1, n) {
		t.Fatalf("%d plans, last %+v", len(plans), plans[len(plans)-1])
	}
	if digests > n {
		t.Errorf("hashed %d times for %d images", digests, n)
	}
}

func TestPlanRenamesLongNames(t *testing.T) {
	// Names so long that the number replaces their end (numberedStem):
	// importing again finds the numbered copy, and copies with extensions
	// of other lengths are compared by content, not by their index entry.
	s := strings.Repeat("a", 250)
	n2 := numberedStem(s, ".jpg", 2)
	first := planFor(t, nil, map[string]string{"a/" + s + ".jpg": "A", "b/" + s + ".jpg": "B"}, nil, nil, nil, "")
	if want := []string{"b/" + s + ".jpg>b/" + n2 + ".jpg"}; !slices.Equal(first, want) {
		t.Fatalf("first import = %q\nwant %q", first, want)
	}
	again := planFor(t, map[string]string{"a/" + s + ".jpg": "A", "b/" + n2 + ".jpg": "B"},
		map[string]string{"a/" + s + ".jpg": "A", "b/" + s + ".jpg": "B"}, nil, nil, nil, "")
	if want := []string{"b/" + s + ".jpg>b/" + n2 + ".jpg"}; !slices.Equal(again, want) {
		t.Errorf("imported again = %q\nwant %q", again, want)
	}

	incoming := map[string]string{}
	for i, c := range []string{"A", "B", "C", "D", "E", "B"} {
		ext := ".jpg"
		if i == 3 || i == 5 {
			ext = ".jpeg"
		}
		incoming[fmt.Sprintf("f%d/%s%s", i, s, ext)] = c
	}
	checkPlanInvariants(t, nil, incoming, nil, nil, nil)
}

// checkPlanInvariants plans the new images incoming (path → content) for a
// dataset with images existing and XML files existingXMLs, and checks that
// no numbered name it gives out holds two contents, and that no new image
// ends up with the path (without extension) of another image or of an XML
// file it was not paired with.
func checkPlanInvariants(t *testing.T, existing, incoming map[string]string, existingXMLs, xmls []string, pairs map[string]string) {
	t.Helper()
	files := func(m map[string]string) map[string]diskFile {
		out := map[string]diskFile{}
		for rel, data := range m {
			out[rel] = diskFile{Size: int64(len(data))}
		}
		return out
	}
	content := func(r nameRef) string {
		if r.staged {
			return incoming[r.rel]
		}
		return existing[r.rel]
	}
	same := func(_ context.Context, a, b nameRef) (bool, error) {
		return a.size == b.size && content(a) == content(b), nil
	}
	hashed := map[nameRef]bool{}
	digest := func(_ context.Context, r nameRef) (string, error) {
		if hashed[r] {
			t.Errorf("hashed %s twice", r.rel)
		}
		hashed[r] = true
		return content(r), nil
	}
	pl := newNamePlanner(context.Background(), files(existing), existingXMLs, files(incoming), xmls, ConflictSkip, same, digest)
	plans, err := planRenames(pl, files(incoming), pairs, nil)
	if err != nil {
		t.Fatal(err)
	}

	final := map[string]string{} // image path → content, after the import
	maps.Copy(final, existing)
	renamed := map[string]*renamePlan{}
	for _, p := range plans {
		renamed[p.From] = p
	}
	for rel, c := range incoming {
		if _, ok := existing[rel]; ok {
			continue // skipped
		}
		if p := renamed[rel]; p != nil {
			rel = p.To
		}
		if _, ok := final[rel]; ok && existing[rel] != c {
			t.Errorf("%s holds two images", rel)
		}
		final[rel] = c
	}
	byName := map[string]map[string]bool{} // lower stem → contents
	for rel, c := range final {
		s := nameStem(rel)
		if byName[s] == nil {
			byName[s] = map[string]bool{}
		}
		byName[s][c] = true
	}
	before := map[string]bool{} // path stems of the files before the import
	for _, rel := range slices.Concat(slices.Collect(maps.Keys(existing)), slices.Collect(maps.Keys(incoming)), existingXMLs, xmls) {
		before[pathStem(rel)] = true
	}
	for _, p := range plans {
		if p.Identical {
			continue
		}
		if _, reused := existing[p.To]; reused {
			continue
		}
		if len(byName[nameStem(p.To)]) != 1 {
			t.Errorf("%s: the name %s holds %d contents", p.From, nameStem(p.To), len(byName[nameStem(p.To)]))
		}
		if before[pathStem(p.To)] {
			t.Errorf("%s: renamed to %s, whose name a file there has", p.From, p.To)
		}
		if p.XMLTo != "" && before[pathStem(p.XMLTo)] {
			t.Errorf("%s: annotation renamed to %s, whose name a file there has", p.From, p.XMLTo)
		}
	}
	stems := map[string]string{}
	for rel := range final {
		ps := pathStem(rel)
		if other, ok := stems[ps]; ok {
			_, a := existing[rel]
			_, b := existing[other]
			if (!a || !b) && final[rel] != final[other] { // the same picture twice in one folder keeps its names
				t.Errorf("%s and %s share a path stem", rel, other)
			}
		}
		stems[ps] = rel
	}
}

func TestPlanRenamesInvariants(t *testing.T) {
	// Random datasets and uploads of a few names, folders, extensions and
	// contents (of one size and of several).
	rng := rand.New(rand.NewPCG(1, 2))
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	for range 3000 {
		existing, incoming := map[string]string{}, map[string]string{}
		var oldXMLs, xmls []string
		pairs := map[string]string{}
		for range rng.IntN(5) {
			rel := pick("a/", "b/", "c/", "") + pick("x", "X", "x_2", "x_3", "y.v1", "y.v1_2") + pick(".jpg", ".png", ".jpeg")
			existing[rel] = pick("A", "B", "C", "DD")
		}
		for range rng.IntN(4) {
			oldXMLs = append(oldXMLs, pick("a/", "b/", "d/", "d/Annotations/")+pick("x_2", "x_3", "y.v1_2", "y.v1_3")+".xml")
		}
		for range 1 + rng.IntN(7) {
			dir := pick("a/", "b/", "c/", "d/", "e/", "d/JPEGImages/", "")
			stem := pick("x", "X", "x_2", "y.v1", "z")
			rel := dir + stem + pick(".jpg", ".png", ".jpeg")
			if _, ok := incoming[rel]; ok {
				continue
			}
			incoming[rel] = pick("A", "B", "C", "DD", "EEE")
			if rng.IntN(2) == 0 {
				x := dir + stem + ".xml"
				if dir == "d/JPEGImages/" {
					x = "d/Annotations/" + stem + ".xml"
				}
				if !slices.Contains(xmls, x) {
					xmls = append(xmls, x)
					pairs[rel] = x
				}
			}
		}
		checkPlanInvariants(t, existing, incoming, oldXMLs, xmls, pairs)
		if t.Failed() {
			t.Fatalf("existing %v\nincoming %v\nold XMLs %v\nXMLs %v", existing, incoming, oldXMLs, xmls)
		}
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
		"g/1.jpg":      []byte("img"),
		"g/1.xml":      vocFile("1.jpg"),
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

	if err := renameImageFiles(root, &renamePlan{From: "b/0001.jpg", To: "b/0001_2.jpg", XMLFrom: "b/0001.xml", XMLTo: "b/0001_2.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if read("b/0001.jpg") != "<missing>" || read("b/0001.xml") != "<missing>" || read("b/0001_2.jpg") != "img" ||
		!strings.Contains(read("b/0001_2.xml"), "<filename>0001_2.jpg</filename>") {
		t.Errorf("b after rename: %v", datasetFiles(t, filepath.Join(dir, "b")))
	}

	if err := renameImageFiles(root, &renamePlan{From: "c/k.png", To: "c/k_2.png", XMLFrom: "c/k.xml", XMLTo: "c/k_2.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if read("c/k_2.png") != "img" || read("c/k_2.xml") != "<annotation><object>" || read("c/k.xml") != "<missing>" {
		t.Errorf("c after rename: %v", datasetFiles(t, filepath.Join(dir, "c")))
	}

	// Targets that exist are never replaced, and nothing changes.
	err = renameImageFiles(root, &renamePlan{From: "d/x.jpg", To: "d/x_2.jpg"}, false)
	if !errors.Is(err, os.ErrExist) || read("d/x.jpg") != "img" || read("d/x_2.jpg") != "taken" {
		t.Errorf("rename onto an image: %v", err)
	}
	err = renameImageFiles(root, &renamePlan{From: "e/y.jpg", To: "e/y_2.jpg", XMLFrom: "e/y.xml", XMLTo: "e/y_2.xml"}, true)
	if !errors.Is(err, os.ErrExist) || read("e/y.jpg") != "img" || read("e/y_2.jpg") != "<missing>" || read("e/y_2.xml") != "taken" {
		t.Errorf("rename onto an annotation: %v", err)
	}

	if err := renameImageFiles(root, &renamePlan{From: "f/plain.tiff", To: "f/plain_2.tiff"}, true); err != nil || read("f/plain_2.tiff") != "img" {
		t.Errorf("rename without annotation: %v", err)
	}

	// In place, the renamed VOC file keeps the permissions of the old one
	// and no temporary file is left.
	if err := os.Chmod(filepath.Join(dir, "g", "1.xml"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := renameImageFiles(root, &renamePlan{From: "g/1.jpg", To: "g/1_2.jpg", XMLFrom: "g/1.xml", XMLTo: "g/1_2.xml"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "g", "1_2.xml"))
	if err != nil || fi.Mode().Perm() != 0o664 || !strings.Contains(read("g/1_2.xml"), "<filename>1_2.jpg</filename>") {
		t.Errorf("g/1_2.xml: %v, %v", fi, err)
	}
	if got := datasetFiles(t, filepath.Join(dir, "g")); !slices.Equal(got, []string{"1_2.jpg", "1_2.xml"}) {
		t.Errorf("g after rename: %v", got)
	}
}
