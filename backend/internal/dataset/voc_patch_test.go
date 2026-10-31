package dataset

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
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

func TestPatchVOCRefuses(t *testing.T) {
	box := []AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4}}
	for name, in := range map[string]string{
		"namespace":          `<annotation xmlns="urn:x"><filename>a.png</filename></annotation>`,
		"prefixed element":   `<annotation xmlns:x="urn:x"><x:extra/></annotation>`,
		"prefixed attribute": `<annotation xmlns:x="urn:x" x:a="1"></annotation>`,
		"xml attribute":      `<annotation xml:lang="zh"></annotation>`,
		"text in root":       `<annotation>text<filename>a.png</filename></annotation>`,
		"not well-formed":    `<annotation><object><name>a</name></annotation>`,
		"unclosed root":      `<annotation><filename>a.png</filename>`,
		"other root":         `<doc><filename>a.png</filename></doc>`,
		"empty":              ``,
	} {
		got, err := patchVOC([]byte(in), imageInfo{}, box)
		if !errors.Is(err, errNotPatchable) || got != nil {
			t.Errorf("%s: patched = %q, %v; want errNotPatchable", name, got, err)
		}
	}
}
