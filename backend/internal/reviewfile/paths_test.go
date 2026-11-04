package reviewfile

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSafeSegment(t *testing.T) {
	for in, want := range map[string]string{
		"a.jpg":          "a.jpg",
		"划痕 01.png":      "划痕 01.png",
		"":               "_",
		".":              "_",
		"..":             "__",
		"a.":             "a_",
		"a. . ":          "a____",
		" a ":            " a_",
		`a<b>c:d"e|f?g*`: "a_b_c_d_e_f_g_",
		`a\b`:            "a_b",
		"a/b":            "a_b",
		"tab\there\x00":  "tab_here_",
		"del\x7f":        "del_",
		"bad\xffutf8":    "bad_utf8",
		"CON":            "_CON",
		"con.txt":        "_con.txt",
		"Nul.tar.gz":     "_Nul.tar.gz",
		"aux .jpg":       "_aux .jpg",
		"COM1":           "_COM1",
		"lpt9.png":       "_lpt9.png",
		"COM0":           "_COM0",
		"com²":           "_com²",
		"CONIN$":         "_CONIN$",
		"COM10":          "COM10",
		"CONSOLE":        "CONSOLE",
		"xcon":           "xcon",
		"con_1.jpg":      "con_1.jpg",
		"LPT":            "LPT",
		".hidden":        ".hidden",
		"~1.jpg":         "~1.jpg",
	} {
		got := SafeSegment(in)
		if got != want {
			t.Errorf("SafeSegment(%q) = %q, want %q", in, got, want)
		}
		if again := SafeSegment(got); again != got {
			t.Errorf("SafeSegment(%q) = %q is not stable: %q", in, got, again)
		}
	}

	// Long names keep their extension within NTFS's 255 UTF-16 units.
	long := strings.Repeat("长", 300) + ".jpeg"
	got := SafeSegment(long)
	if n := len(utf16.Encode([]rune(got))); n != 255 || !strings.HasSuffix(got, "长.jpeg") {
		t.Errorf("SafeSegment(long) has %d units: %q", n, got)
	}
	emoji := strings.Repeat("😀", 200) + ".png" // 2 units each
	if n := len(utf16.Encode([]rune(SafeSegment(emoji)))); n > 255 {
		t.Errorf("SafeSegment(emoji) has %d units", n)
	}
}

func TestFoldKey(t *testing.T) {
	same := [][2]string{
		{"A.JPG", "a.jpg"},
		{"Straße", "STRAßE"},
		{"\u212a", "k"}, // Kelvin sign
		{"ǅ", "ǆ"},
		{"Ω", "ω"},
		{"ΣΑΣ", "σας"},
	}
	for _, p := range same {
		if FoldKey(p[0]) != FoldKey(p[1]) {
			t.Errorf("FoldKey(%q) != FoldKey(%q)", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{"a.jpg", "a.png"}, {"划痕", "划伤"}, {"a", "á"}} {
		if FoldKey(p[0]) == FoldKey(p[1]) {
			t.Errorf("FoldKey(%q) == FoldKey(%q)", p[0], p[1])
		}
	}
}

func TestValidRelPath(t *testing.T) {
	for _, p := range []string{"a.jpg", "ds/a.jpg", "ds/NG/划痕 1.png", "ds/a.png.xml", "ds/~2/a~2.jpg", "ds/.hidden"} {
		if !ValidRelPath(p) {
			t.Errorf("ValidRelPath(%q) = false", p)
		}
	}
	for _, p := range []string{"", "/a.jpg", "a/", "a//b", "./a", "a/../b", "..", `a\b`, "C:/a.jpg", "c:a", "a/con.jpg",
		"a/b.", "a/b ", "a\x00", "a/\xff", strings.Repeat("a/", 2049) + "b"} {
		if ValidRelPath(p) {
			t.Errorf("ValidRelPath(%q) = true", p)
		}
	}
}

func TestFolder(t *testing.T) {
	used := map[string]bool{}
	folder := func(name string, id int64) string {
		f := Folder(name, id, func(key string) bool { return used[key] })
		used[FoldKey(f)] = true
		return f
	}
	got := []string{
		folder("line-a", 1),
		folder("LINE-A", 2),      // same name ignoring case
		folder("line-a~2", 3),    // what dataset 2 got
		folder("Line-A", 2),      // impossible in practice (same ID), still unique
		folder("review.json", 5), // the manifest's name is reserved
		folder("README.TXT", 6),
		folder("con", 7),
		folder("夜班", 8),
		folder("a:b", 9),
	}
	want := []string{"line-a", "LINE-A~2", "line-a~2~3", "Line-A~2~2", "review.json~5", "README.TXT~6", "_con", "夜班", "a_b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %q, want %q", got, want)
	}
	// A suffix is kept on names at the length limit.
	long := strings.Repeat("x", 255)
	used[FoldKey(long)] = true
	if f := Folder(long, 12, func(key string) bool { return used[key] }); !strings.HasSuffix(f, "x~12") || len(f) != 255 {
		t.Errorf("Folder(long) = %q", f)
	}
}

func TestLayout(t *testing.T) {
	images := []LayoutImage{
		{"ds", "a.jpg"},
		{"ds", "a.png"},           // a.xml is taken by a.jpg
		{"ds", "A.JPG"},           // the same file as a.jpg on Windows
		{"ds", "a~2.jpg"},         // the name A.JPG got
		{"ds", "NG/b.jpg"},        // folders ...
		{"ds", "ng/B.jpg"},        // ... that differ in case are one folder
		{"ds", "ng/b.xml.jpg"},    // its VOC file b.xml.xml is fine
		{"ds", "x.xml/c.jpg"},     // a folder named like a VOC file ...
		{"ds", "x.jpg"},           // ... so x.jpg's VOC file is x.jpg.xml
		{"ds", "aux/con.jpg"},     // device names
		{"ds", "q?/r*.jpg"},       // invalid characters
		{"ds", "q_/r_.jpg"},       // ... what they become
		{"other", "a.jpg"},        // another dataset folder
		{"ds", "dot./e.jpg"},      // trailing dots
		{"ds", "dot_/e.jpg"},      // ... clash with what they become
		{"ds", "p.png"},           // p.xml is the name of a folder below,
		{"ds", "p.xml/z.png"},     // which is known before any file is placed
		{"ds", "img.tar.gz.jpeg"}, // the stem keeps its inner dots
	}
	got := Layout(images)
	want := []Placement{
		{"ds/a.jpg", "ds/a.xml"},
		{"ds/a.png", "ds/a.png.xml"},
		{"ds/A~2.JPG", "ds/A~2.xml"},
		{"ds/a~2~2.jpg", "ds/a~2~2.xml"},
		{"ds/NG/b.jpg", "ds/NG/b.xml"},
		{"ds/ng/B~2.jpg", "ds/ng/B~2.xml"},
		{"ds/ng/b.xml.jpg", "ds/ng/b.xml.xml"},
		{"ds/x.xml/c.jpg", "ds/x.xml/c.xml"},
		{"ds/x.jpg", "ds/x.jpg.xml"},
		{"ds/_aux/_con.jpg", "ds/_aux/_con.xml"},
		{"ds/q_/r_.jpg", "ds/q_/r_.xml"},
		{"ds/q_/r_~2.jpg", "ds/q_/r_~2.xml"},
		{"other/a.jpg", "other/a.xml"},
		{"ds/dot_/e.jpg", "ds/dot_/e.xml"},
		{"ds/dot_/e~2.jpg", "ds/dot_/e~2.xml"},
		{"ds/p.png", "ds/p.png.xml"},
		{"ds/p.xml/z.png", "ds/p.xml/z.xml"},
		{"ds/img.tar.gz.jpeg", "ds/img.tar.gz.xml"},
	}
	if !reflect.DeepEqual(got, want) {
		for i := range max(len(got), len(want)) {
			var g, w Placement
			if i < len(got) {
				g = got[i]
			}
			if i < len(want) {
				w = want[i]
			}
			if g != w {
				t.Errorf("%d (%s): got %+v, want %+v", i, images[i%len(images)].RelPath, g, w)
			}
		}
	}
	// All paths are valid and unique ignoring case; none is a folder.
	seen := map[string]bool{}
	for _, p := range got {
		for _, s := range []string{p.Image, p.Annotation} {
			if !ValidRelPath(s) || seen[FoldKey(s)] {
				t.Errorf("path %q invalid or repeated", s)
			}
			seen[FoldKey(s)] = true
		}
	}
	// Deterministic.
	if again := Layout(images); !reflect.DeepEqual(again, got) {
		t.Error("Layout is not deterministic")
	}
	if len(Layout(nil)) != 0 {
		t.Error("Layout(nil) is not empty")
	}
}

func TestLayoutManyClashes(t *testing.T) {
	var images []LayoutImage
	for i := range 30 {
		name := "a.jpg"
		if i%2 == 1 {
			name = "A.jpg"
		}
		images = append(images, LayoutImage{"ds", name})
	}
	long := strings.Repeat("长", 251) + ".jpg" // 255 units
	images = append(images, LayoutImage{"ds", long}, LayoutImage{"ds", strings.ToUpper(long)}, LayoutImage{"ds", long})
	seen := map[string]bool{}
	for i, p := range Layout(images) {
		for _, s := range []string{p.Image, p.Annotation} {
			if !ValidRelPath(s) || seen[FoldKey(s)] {
				t.Errorf("%d: path %q invalid or repeated", i, s)
			}
			seen[FoldKey(s)] = true
		}
		if i == 29 && p.Image != "ds/A~30.jpg" {
			t.Errorf("30th image = %q", p.Image)
		}
		if i >= 30 {
			if !strings.HasSuffix(strings.ToLower(p.Image), ".jpg") || len(utf16.Encode([]rune(p.Image[3:]))) > 255 {
				t.Errorf("long name %d = %q", i, p.Image)
			}
			if i == 32 && !strings.HasSuffix(p.Image, fmt.Sprintf("~%d.jpg", 3)) {
				t.Errorf("third long name = %q", p.Image)
			}
		}
	}
}
