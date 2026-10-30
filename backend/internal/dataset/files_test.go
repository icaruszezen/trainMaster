package dataset

import (
	"maps"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := map[string]fileKind{
		"a.jpg":                 kindImage,
		"OK/b.JPEG":             kindImage,
		"x/y/c.PNG":             kindImage,
		"d.bmp":                 kindImage,
		"e.gif":                 kindImage,
		"f.webp":                kindImage,
		"g.tif":                 kindImage,
		"h.TIFF":                kindImage,
		"i.avif":                kindImage,
		"Annotations/a.xml":     kindAnnotation,
		"a.XML":                 kindAnnotation,
		"notes.txt":             kindIgnored,
		"labels":                kindIgnored,
		"a.jpg.bak":             kindIgnored,
		".hidden.jpg":           kindIgnored,
		"._a.jpg":               kindIgnored, // AppleDouble
		"__MACOSX/a.jpg":        kindIgnored,
		"set/__MACOSX/x/a.xml":  kindIgnored,
		".git/objects/a.png":    kindIgnored,
		"set/.cache/a.png":      kindIgnored,
		"Thumbs.db":             kindIgnored,
		"sub/thumbs.db":         kindIgnored,
		"desktop.ini":           kindIgnored,
		"set/Desktop.INI":       kindIgnored,
		"datasets.v2/b.jpg":     kindImage, // only leading dots hide a segment
		"MACOSX/a.jpg":          kindImage,
		"thumbs.db.d/image.png": kindImage,
	}
	for rel, want := range tests {
		if got := classify(rel); got != want {
			t.Errorf("classify(%q) = %d, want %d", rel, got, want)
		}
	}
}

func TestCleanEntryPath(t *testing.T) {
	ok := map[string]string{
		"a.jpg":             "a.jpg",
		"set/a.jpg":         "set/a.jpg",
		`set\sub\a.jpg`:     "set/sub/a.jpg",
		"./set//a.jpg":      "set/a.jpg",
		"set/../a.jpg":      "a.jpg",
		"划痕/样本 1.png":       "划痕/样本 1.png",
		"set/a:b.jpg":       "set/a:b.jpg", // only a leading drive letter is special
		"set/x/../../b.jpg": "b.jpg",
	}
	for in, want := range ok {
		got, valid := cleanEntryPath(in)
		if !valid || got != want {
			t.Errorf("cleanEntryPath(%q) = %q, %v; want %q", in, got, valid, want)
		}
	}
	for _, in := range []string{
		"", ".", "./", "..", "../a.jpg", "set/../../a.jpg", `..\a.jpg`, "/etc/passwd", `\\server\share\a.jpg`,
		"//server/a.jpg", "C:/a.jpg", `c:\a.jpg`, "C:a.jpg", "a:b.jpg", "a\x00.jpg",
		"\xbb\xae.jpg", "ok/\xbb\xae/a.jpg", // GBK bytes, not UTF-8
	} {
		if got, valid := cleanEntryPath(in); valid {
			t.Errorf("cleanEntryPath(%q) = %q, should be rejected", in, got)
		}
	}
}

func TestPairAnnotations(t *testing.T) {
	tests := []struct {
		name     string
		images   []string
		xmls     []string
		filename map[string]string // <filename> recorded in each xml
		want     map[string]string
	}{
		{
			name:   "same directory and stem",
			images: []string{"a.jpg", "OK/b.png", "NG/c.bmp", "d.jpg"},
			xmls:   []string{"a.xml", "OK/b.xml", "NG/c.xml", "NG/d.xml", "orphan.xml"},
			want:   map[string]string{"a.jpg": "a.xml", "OK/b.png": "OK/b.xml", "NG/c.bmp": "NG/c.xml"},
		},
		{
			name:   "exact match wins over case-insensitive",
			images: []string{"Img.JPG"},
			xmls:   []string{"img.xml", "Img.xml"},
			want:   map[string]string{"Img.JPG": "Img.xml"},
		},
		{
			name:   "case-insensitive stem and extension",
			images: []string{"set/IMG_01.JPG", "set/b.png"},
			xmls:   []string{"set/img_01.XML", "set/B.xml"},
			want:   map[string]string{"set/IMG_01.JPG": "set/img_01.XML", "set/b.png": "set/B.xml"},
		},
		{
			name:   "directories compare exactly",
			images: []string{"Set/a.jpg"},
			xmls:   []string{"set/a.xml"},
			want:   map[string]string{},
		},
		{
			name:   "stem with dots",
			images: []string{"a.b.c.jpg"},
			xmls:   []string{"a.b.c.xml", "a.xml"},
			want:   map[string]string{"a.b.c.jpg": "a.b.c.xml"},
		},
		{
			name:   "Pascal VOC layout",
			images: []string{"VOC2007/JPEGImages/000001.jpg", "images/2.png", "train/IMAGES/3.jpg", "other/4.jpg"},
			xmls:   []string{"VOC2007/Annotations/000001.xml", "annotations/2.xml", "train/ANNOTATIONS/3.xml", "Annotations/4.xml"},
			want: map[string]string{
				"VOC2007/JPEGImages/000001.jpg": "VOC2007/Annotations/000001.xml",
				"images/2.png":                  "annotations/2.xml",
				"train/IMAGES/3.jpg":            "train/ANNOTATIONS/3.xml",
			},
		},
		{
			name:   "same directory beats the VOC layout",
			images: []string{"JPEGImages/a.jpg"},
			xmls:   []string{"JPEGImages/a.xml", "Annotations/a.xml"},
			want:   map[string]string{"JPEGImages/a.jpg": "JPEGImages/a.xml"},
		},
		{
			name:     "shared annotation: <filename> decides",
			images:   []string{"a.jpg", "a.png"},
			xmls:     []string{"a.xml"},
			filename: map[string]string{"a.xml": "a.png"},
			want:     map[string]string{"a.png": "a.xml"},
		},
		{
			name:     "shared annotation: <filename> ignoring case",
			images:   []string{"a.jpg", "a.png"},
			xmls:     []string{"a.xml"},
			filename: map[string]string{"a.xml": "A.PNG"},
			want:     map[string]string{"a.png": "a.xml"},
		},
		{
			name:     "shared annotation: first image when <filename> does not help",
			images:   []string{"a.png", "a.jpg"},
			xmls:     []string{"a.xml"},
			filename: map[string]string{"a.xml": "other.jpg"},
			want:     map[string]string{"a.jpg": "a.xml"},
		},
		{
			name:   "nothing to pair",
			images: []string{"a.jpg"},
			want:   map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pairAnnotations(tt.images, tt.xmls, func(x string) string { return tt.filename[x] })
			if !maps.Equal(got, tt.want) {
				t.Errorf("pairs = %v, want %v", got, tt.want)
			}
		})
	}
}
