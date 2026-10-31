package dataset

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// labelImgFile is a VOC file as LabelImg writes it after a review, with
// things the platform does not edit: a root attribute, <path>, comments,
// a polygon object, an extra field and float coordinates.
const labelImgFile = `<?xml version="1.0" encoding="utf-8"?>
<!-- exported by LabelImg -->
<annotation verified="yes">
	<folder>images</folder>
	<filename>a.png</filename>
	<path>C:\data\images\a.png</path>
	<source>
		<database>Unknown</database>
	</source>
	<size>
		<width>100</width>
		<height>80</height>
		<depth>3</depth>
	</size>
	<segmented>0</segmented>
	<!-- reviewed by "qa" -->
	<object>
		<name>dent</name>
		<pose>Left</pose>
		<truncated>0</truncated>
		<difficult>0</difficult>
		<occluded>1</occluded>
		<bndbox>
			<xmin>10.0</xmin>
			<ymin>20.0</ymin>
			<xmax>30.0</xmax>
			<ymax>40.0</ymax>
		</bndbox>
	</object>
	<object>
		<name>scratch</name>
		<bndbox><xmin>1</xmin><ymin>1</ymin><xmax>5</xmax><ymax>5</ymax></bndbox>
	</object>
	<object>
		<name>hole</name>
		<polygon><x1>1</x1><y1>1</y1><x2>9</x2><y2>1</y2><x3>5</x3><y3>9</y3></polygon>
	</object>
	<custom a="1 &amp; 2">x &amp; y &lt; z</custom>
</annotation>
`

// section returns the lines of labelImgFile from the one containing from
// up to (not including) the one containing to.
func section(t *testing.T, from, to string) string {
	t.Helper()
	start := strings.Index(labelImgFile, from)
	end := strings.Index(labelImgFile, to)
	if start < 0 || end < start {
		t.Fatalf("no section %q..%q", from, to)
	}
	start = strings.LastIndex(labelImgFile[:start], "\n") + 1
	end = strings.LastIndex(labelImgFile[:end], "\n") + 1
	return labelImgFile[start:end]
}

func TestPatchVOCKeepsUnknownContent(t *testing.T) {
	info := imageInfo{Width: 100, Height: 80, Depth: 3}
	dent := AnnotationObject{Name: "dent", Xmin: 10, Ymin: 20, Xmax: 30, Ymax: 40}
	scratch := AnnotationObject{Name: "scratch", Xmin: 2, Ymin: 2, Xmax: 6, Ymax: 6}
	added := AnnotationObject{Name: "划痕&", Xmin: 50, Ymin: 50, Xmax: 60, Ymax: 60, Difficult: true}

	got, err := patchVOC([]byte(labelImgFile), info, []AnnotationObject{dent, scratch, added})
	if err != nil {
		t.Fatal(err)
	}
	// The unchanged box keeps its element; the changed and the new box are
	// written like writeVOC does, where the boxes were. Everything else
	// stays, in its order.
	want := section(t, "<?xml", "\t<object>\n\t\t<name>dent") +
		section(t, "\t<object>\n\t\t<name>dent", "\t<object>\n\t\t<name>scratch") +
		"\t" + vocObjectText(scratch) + "\n" +
		"\t" + vocObjectText(added) + "\n" +
		section(t, "\t<object>\n\t\t<name>hole", "</annotation>") +
		"</annotation>\n"
	if string(got) != want {
		t.Errorf("patched:\n%s\nwant:\n%s", got, want)
	}

	parsed, err := parseVOC(bytes.NewReader(got))
	if err != nil || !reflect.DeepEqual(parsed.Objects, []AnnotationObject{dent, scratch, added}) || parsed.Unsupported != 1 {
		t.Errorf("parsed = %+v, %v", parsed, err)
	}

	// Saving the same boxes again changes nothing.
	again, err := patchVOC(got, info, []AnnotationObject{dent, scratch, added})
	if err != nil || !bytes.Equal(again, got) {
		t.Errorf("second patch:\n%s\n%v", again, err)
	}
	// An untouched file stays as it is.
	same, err := patchVOC([]byte(labelImgFile), info, []AnnotationObject{dent, {Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}})
	if err != nil || string(same) != labelImgFile {
		t.Errorf("unchanged boxes:\n%s\n%v", same, err)
	}
}

func TestPatchVOCRemovesBoxes(t *testing.T) {
	info := imageInfo{Width: 100, Height: 80, Depth: 3}
	got, err := patchVOC([]byte(labelImgFile), info, []AnnotationObject{})
	if err != nil {
		t.Fatal(err)
	}
	want := section(t, "<?xml", "\t<object>\n\t\t<name>dent") + section(t, "\t<object>\n\t\t<name>hole", "</annotation>") + "</annotation>\n"
	if string(got) != want {
		t.Errorf("patched:\n%s\nwant:\n%s", got, want)
	}
	// With only the polygon left, new boxes go after it.
	box := AnnotationObject{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}
	got, err = patchVOC(got, info, []AnnotationObject{box})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "</polygon>\n\t</object>\n\t"+vocObjectText(box)+"\n\t<custom") {
		t.Errorf("box not after the polygon:\n%s", got)
	}
	// Two equal boxes: one is kept, the other removed.
	two := strings.Replace(labelImgFile, "<name>scratch</name>\n\t\t<bndbox><xmin>1</xmin><ymin>1</ymin><xmax>5</xmax><ymax>5</ymax></bndbox>",
		"<name>dent</name>\n\t\t<bndbox><xmin>10</xmin><ymin>20</ymin><xmax>30</xmax><ymax>40</ymax></bndbox>", 1)
	got, err = patchVOC([]byte(two), info, []AnnotationObject{{Name: "dent", Xmin: 10, Ymin: 20, Xmax: 30, Ymax: 40}})
	if err != nil || strings.Count(string(got), "<name>dent</name>") != 1 || !strings.Contains(string(got), "<pose>Left</pose>") {
		t.Errorf("equal boxes:\n%s\n%v", got, err)
	}
}

func TestPatchVOCSize(t *testing.T) {
	box := AnnotationObject{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}
	info := imageInfo{Width: 64, Height: 48, Depth: 1}
	header := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n"
	tests := []struct {
		name, in, want string
		info           imageInfo
	}{
		{
			name: "wrong size replaced",
			in:   "<annotation>\n\t<filename>a.png</filename>\n\t<size><width>10</width><height>10</height></size>\n</annotation>",
			want: header + "<annotation>\n\t<filename>a.png</filename>\n\t" + vocSizeText(info) + "\n\t" + vocObjectText(box) + "\n</annotation>\n",
			info: info,
		},
		{
			name: "right size kept",
			in:   "<annotation>\n\t<filename>a.png</filename>\n\t<size><width>64</width><height>48</height><depth>3</depth></size>\n</annotation>",
			want: header + "<annotation>\n\t<filename>a.png</filename>\n\t<size><width>64</width><height>48</height><depth>3</depth></size>\n\t" + vocObjectText(box) + "\n</annotation>\n",
			info: info,
		},
		{
			name: "missing size added after filename",
			in:   "<annotation><folder>x</folder><filename>a.png</filename><segmented>0</segmented></annotation>",
			want: header + "<annotation>\n\t<folder>x</folder>\n\t<filename>a.png</filename>\n\t" + vocSizeText(info) + "\n\t<segmented>0</segmented>\n\t" + vocObjectText(box) + "\n</annotation>\n",
			info: info,
		},
		{
			name: "missing size and filename added at the end",
			in:   "<annotation></annotation>",
			want: header + "<annotation>\n\t" + vocObjectText(box) + "\n\t" + vocSizeText(info) + "\n</annotation>\n",
			info: info,
		},
		{
			name: "unknown size leaves size alone",
			in:   "<annotation><size><width>10</width><height>10</height></size></annotation>",
			want: header + "<annotation>\n\t<size><width>10</width><height>10</height></size>\n\t" + vocObjectText(box) + "\n</annotation>\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := patchVOC([]byte(tt.in), tt.info, []AnnotationObject{box})
			if err != nil || string(got) != tt.want {
				t.Errorf("patched (%v):\n%s\nwant:\n%s", err, got, tt.want)
			}
		})
	}
}

func TestPatchVOCEncoding(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="GBK"?>`, "图片.jpg", "划痕"))
	if err != nil {
		t.Fatal(err)
	}
	box := AnnotationObject{Name: "凹坑", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}
	got, err := patchVOC([]byte(gbk), imageInfo{}, []AnnotationObject{box})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte(`<?xml version="1.0" encoding="utf-8"?>`)) || !bytes.Contains(got, []byte("<filename>图片.jpg</filename>")) {
		t.Errorf("not rewritten as UTF-8:\n%s", got)
	}
	parsed, err := parseVOC(bytes.NewReader(got))
	if err != nil || parsed.Filename != "图片.jpg" || !reflect.DeepEqual(parsed.Objects, []AnnotationObject{box}) {
		t.Errorf("parsed = %+v, %v", parsed, err)
	}
}

func TestPatchVOCKeepsBytes(t *testing.T) {
	info := imageInfo{Width: 100, Height: 80, Depth: 3}
	dent := AnnotationObject{Name: "dent", Xmin: 10, Ymin: 20, Xmax: 30, Ymax: 40}
	scratch := AnnotationObject{Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}
	added := AnnotationObject{Name: "凹坑", Xmin: 50, Ymin: 50, Xmax: 60, Ymax: 60}
	header := `<?xml version="1.0" encoding="utf-8"?>` + "\n"

	// Namespaces (declarations, prefixed elements and attributes), entity
	// and character references, CDATA and attribute quoting are copied as
	// they are.
	namespaced := header + `<annotation xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xml:lang='zh' xsi:noNamespaceSchemaLocation="voc.xsd">
	<filename>a.png</filename>
	<size><width>100</width><height>80</height></size>
	<x:meta xmlns:x="urn:x" x:by="qa">&#x6807;&amp;<![CDATA[<raw>]]></x:meta>
	<object>
		<name>dent</name>
		<ext xmlns="urn:ext"><score>0.9</score></ext>
		<bndbox><xmin>10</xmin><ymin>20</ymin><xmax>30</xmax><ymax>40</ymax></bndbox>
	</object>
	<object>
		<name>hole</name>
		<polygon xmlns="urn:poly"><x1>1</x1><y1>1</y1></polygon>
	</object>
</annotation>
`
	if got, err := patchVOC([]byte(namespaced), info, []AnnotationObject{dent}); err != nil || string(got) != namespaced {
		t.Errorf("unchanged namespaced file (%v):\n%s", err, got)
	}
	got, err := patchVOC([]byte(namespaced), info, []AnnotationObject{dent, added})
	if want := strings.Replace(namespaced, "\t<object>\n\t\t<name>hole", "\t"+vocObjectText(added)+"\n\t<object>\n\t\t<name>hole", 1); err != nil || string(got) != want {
		t.Errorf("added box (%v):\n%s\nwant:\n%s", err, got, want)
	}
	if parsed, err := parseVOC(bytes.NewReader(got)); err != nil || !reflect.DeepEqual(parsed.Objects, []AnnotationObject{dent, added}) || parsed.Unsupported != 1 {
		t.Errorf("parsed = %+v, %v", parsed, err)
	}

	// A prefixed root and prefixed objects: a changed box is written in the
	// default namespace, which parseVOC reads like the prefixed one.
	prefixed := header + "<v:annotation xmlns:v=\"urn:voc\">\n\t<v:object><v:name>scratch</v:name><v:bndbox><v:xmin>1</v:xmin><v:ymin>1</v:ymin>" +
		"<v:xmax>5</v:xmax><v:ymax>5</v:ymax></v:bndbox></v:object>\n</v:annotation>\n"
	if got, err := patchVOC([]byte(prefixed), imageInfo{}, []AnnotationObject{scratch}); err != nil || string(got) != prefixed {
		t.Errorf("unchanged prefixed file (%v):\n%s", err, got)
	}
	got, err = patchVOC([]byte(prefixed), imageInfo{}, []AnnotationObject{dent})
	if want := header + "<v:annotation xmlns:v=\"urn:voc\">\n\t" + vocObjectText(dent) + "\n</v:annotation>\n"; err != nil || string(got) != want {
		t.Errorf("changed prefixed box (%v):\n%s\nwant:\n%s", err, got, want)
	}

	// Windows line ends stay Windows line ends, also in new elements.
	crlf := strings.ReplaceAll(labelImgFile, "\n", "\r\n")
	if got, err := patchVOC([]byte(crlf), info, []AnnotationObject{dent, scratch}); err != nil || string(got) != crlf {
		t.Errorf("unchanged CRLF file (%v):\n%q", err, got)
	}
	got, err = patchVOC([]byte(crlf), info, []AnnotationObject{added})
	if err != nil || !strings.Contains(string(got), "\r\n\t"+strings.ReplaceAll(vocObjectText(added), "\n", "\r\n")+"\r\n") ||
		strings.Count(string(got), "\n") != strings.Count(string(got), "\r\n") {
		t.Errorf("CRLF file with a new box (%v):\n%q", err, got)
	}
}

func TestPatchVOCStructure(t *testing.T) {
	box := AnnotationObject{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}
	header := `<?xml version="1.0" encoding="utf-8"?>` + "\n"
	for _, tt := range []struct {
		name, in, want string
	}{
		{
			name: "self-closing root",
			in:   `<annotation verified="yes" />`,
			want: header + `<annotation verified="yes">` + "\n\t" + vocObjectText(box) + "\n</annotation>\n",
		},
		{
			name: "self-closing prefixed root",
			in:   `<v:annotation xmlns:v="urn:voc"/>`,
			want: header + `<v:annotation xmlns:v="urn:voc">` + "\n\t" + vocObjectText(box) + "\n</v:annotation>\n",
		},
		{
			name: "text in the root",
			in:   "<annotation>\n  some text &amp; more \n<filename>a.png</filename>tail</annotation>",
			want: header + "<annotation>\n\tsome text &amp; more\n\t<filename>a.png</filename>\n\ttail\n\t" + vocObjectText(box) + "\n</annotation>\n",
		},
		{
			name: "NUL padding and other bytes after the root",
			in:   "<annotation><filename>a.png</filename></annotation>\n<!-- after -->\x00\x00\x00garbage<",
			want: header + "<annotation>\n\t<filename>a.png</filename>\n\t" + vocObjectText(box) + "\n</annotation>\n",
		},
		{
			name: "prolog kept, declaration and BOM replaced",
			in:   "\xef\xbb\xbf<?xml version='1.0' standalone='yes'?>\n<!DOCTYPE annotation>\n<?style x?>\n<!-- c -->\n<annotation></annotation>",
			want: header + "<!DOCTYPE annotation>\n<?style x?>\n<!-- c -->\n<annotation>\n\t" + vocObjectText(box) + "\n</annotation>\n",
		},
		{
			name: "comments and processing instructions among the children",
			in:   "<annotation><!--a--><?pi b?><object><name>dent</name><bndbox><xmin>1</xmin><ymin>2</ymin><xmax>3</xmax><ymax>4</ymax></bndbox></object></annotation>",
			want: header + "<annotation>\n\t<!--a-->\n\t<?pi b?>\n\t<object><name>dent</name><bndbox><xmin>1</xmin><ymin>2</ymin><xmax>3</xmax><ymax>4</ymax></bndbox></object>\n</annotation>\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := patchVOC([]byte(tt.in), imageInfo{}, []AnnotationObject{box})
			if err != nil || string(got) != tt.want {
				t.Errorf("patched (%v):\n%q\nwant:\n%q", err, got, tt.want)
			}
		})
	}
}

func TestPatchVOCTranscodes(t *testing.T) {
	box := AnnotationObject{Name: "划痕", Xmin: 10, Ymin: 20, Xmax: 101, Ymax: 201, Truncated: true}
	doc := vocDoc(`<?xml version="1.0" encoding="GBK"?>`, "图片.jpg", "划痕")
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(doc)
	if err != nil {
		t.Fatal(err)
	}
	utf16, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().String(
		vocDoc(`<?xml version="1.0" encoding="UTF-16"?>`, "图片.jpg", "划痕"))
	if err != nil {
		t.Fatal(err)
	}
	// Unchanged boxes: the same file, in UTF-8.
	want := `<?xml version="1.0" encoding="utf-8"?>` + "\n" + strings.TrimPrefix(doc, `<?xml version="1.0" encoding="GBK"?>`) + "\n"
	for name, in := range map[string]string{"gbk": gbk, "utf-16": utf16} {
		got, err := patchVOC([]byte(in), imageInfo{}, []AnnotationObject{box, {Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4, Difficult: true}})
		if err != nil || string(got) != want {
			t.Errorf("%s (%v):\n%s\nwant:\n%s", name, err, got, want)
		}
	}
	if _, err := patchVOC([]byte(`<?xml version="1.0" encoding="x-unknown"?><annotation/>`), imageInfo{}, nil); !errors.Is(err, errNotPatchable) {
		t.Errorf("unknown encoding: %v", err)
	}
}

func TestPatchVOCRefuses(t *testing.T) {
	box := []AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}}
	for name, in := range map[string]string{
		"not well-formed": `<annotation><object><name>a</name></annotation>`,
		"unclosed root":   `<annotation><filename>a.png</filename>`,
		"other root":      `<doc><filename>a.png</filename></doc>`,
		"NUL in the root": "<annotation>\x00</annotation>",
		"empty":           ``,
		// parseVOC decodes the rest as GBK, patchVOC cannot.
		"invalid UTF-8 after a declaration in the root": "<annotation><?xml version=\"1.0\" encoding=\"GBK\"?><filename>\xb5\xb5</filename></annotation>",
	} {
		got, err := patchVOC([]byte(in), imageInfo{}, box)
		if !errors.Is(err, errNotPatchable) || got != nil {
			t.Errorf("%s: patched = %q, %v; want errNotPatchable", name, got, err)
		}
	}
	// The kept declaration in the root makes parseVOC read a new UTF-8 label
	// as GBK: the result would not read back as written.
	if got, err := patchVOC(unpatchable, imageInfo{}, []AnnotationObject{{Name: "a", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}); err != nil {
		t.Errorf("unchanged ASCII boxes after a declaration in the root: %q, %v", got, err)
	}
	if got, err := patchVOC(unpatchable, imageInfo{}, []AnnotationObject{{Name: "划痕", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}); !errors.Is(err, errNotPatchable) {
		t.Errorf("a new label after a declaration in the root: %q, %v", got, err)
	}
}

// TestPatchVOCParsesAsParseVOC checks that every file parseVOC accepts
// can be patched, and that the result parses to the new boxes with the
// unsupported objects kept.
func TestPatchVOCParsesAsParseVOC(t *testing.T) {
	box := AnnotationObject{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}
	for _, in := range []string{
		labelImgFile,
		vocDoc(`<?xml version="1.0"?>`, "0001.jpg", "划痕"),
		`<annotation xmlns="urn:x"><filename>a.png</filename><object><name>p</name><polygon/></object></annotation>`,
		`<annotation xmlns:x="urn:x"><x:extra/><x:object><x:name>a</x:name><x:bndbox><x:xmin>1</x:xmin><x:ymin>1</x:ymin><x:xmax>2</x:xmax><x:ymax>2</x:ymax></x:bndbox></x:object></annotation>`,
		`<annotation xml:lang="zh">text<filename>a.png</filename></annotation>` + "\x00\x00",
		`<!-- c --><annotation/>`,
	} {
		before, err := parseVOC(strings.NewReader(in))
		if err != nil {
			t.Fatalf("parseVOC(%q): %v", in, err)
		}
		got, err := patchVOC([]byte(in), imageInfo{}, []AnnotationObject{box})
		if err != nil {
			t.Errorf("patchVOC(%q): %v", in, err)
			continue
		}
		after, err := parseVOC(bytes.NewReader(got))
		if err != nil || !reflect.DeepEqual(after.Objects, []AnnotationObject{box}) || after.Unsupported != before.Unsupported || after.Filename != before.Filename {
			t.Errorf("patched %q = %+v, %v:\n%s", in, after, err, got)
		}
	}
}
