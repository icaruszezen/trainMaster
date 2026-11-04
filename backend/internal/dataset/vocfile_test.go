package dataset

import (
	"archive/zip"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestEncodeDecodeVOC(t *testing.T) {
	objects := []AnnotationObject{
		{Name: "划痕", Xmin: 1, Ymin: 2, Xmax: 30, Ymax: 40, Difficult: true},
		{Name: "a<b&c", Xmin: 0, Ymin: 0, Xmax: 64, Ymax: 48, Truncated: true},
	}
	data, err := EncodeVOC("NG", "0001.jpg", 64, 48, objects)
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes as an import writes (LabelImg layout).
	want := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<annotation>\n\t<folder>NG</folder>\n\t<filename>0001.jpg</filename>\n" +
		"\t<source>\n\t\t<database>Unknown</database>\n\t</source>\n" +
		"\t<size>\n\t\t<width>64</width>\n\t\t<height>48</height>\n\t\t<depth>3</depth>\n\t</size>\n\t<segmented>0</segmented>\n"
	if !strings.HasPrefix(string(data), want) || !strings.Contains(string(data), "<name>a&lt;b&amp;c</name>") ||
		!strings.HasSuffix(string(data), "</object>\n</annotation>\n") {
		t.Errorf("EncodeVOC =\n%s", data)
	}
	f, err := DecodeVOC(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f, VOCFile{Filename: "0001.jpg", Width: 64, Height: 48, Objects: objects}) {
		t.Errorf("DecodeVOC = %+v", f)
	}

	// The top level of a dataset has no <folder>; an unknown size no <size>.
	data, _ = EncodeVOC(".", "a.png", 0, 0, nil)
	if strings.Contains(string(data), "<folder>") || strings.Contains(string(data), "<size>") {
		t.Errorf("EncodeVOC without folder and size =\n%s", data)
	}
	if f, err := DecodeVOC(data); err != nil || f.Objects == nil || len(f.Objects) != 0 || f.Width != 0 {
		t.Errorf("DecodeVOC of an empty annotation = %+v, %v", f, err)
	}

	// Files of other tools: GBK, unsupported objects.
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String(`<?xml version="1.0" encoding="GBK"?><annotation>` +
		`<filename>图片.jpg</filename><object><name>划痕</name><bndbox><xmin>1.4</xmin><ymin>2</ymin><xmax>3</xmax>` +
		`<ymax>4</ymax></bndbox></object><object><name>poly</name></object></annotation>`)
	f, err = DecodeVOC([]byte(gbk))
	if err != nil || f.Filename != "图片.jpg" || len(f.Objects) != 1 || f.Objects[0].Name != "划痕" || f.Unsupported != 1 {
		t.Errorf("DecodeVOC(GBK) = %+v, %v", f, err)
	}

	// Not VOC, not XML.
	_, err = DecodeVOC([]byte("<config/>"))
	if !errors.Is(err, errNotVOC) || !strings.Contains(AnnotationErrorMessage(err), "不是 Pascal VOC 格式") {
		t.Errorf("DecodeVOC(<config/>) = %v (%s)", err, AnnotationErrorMessage(err))
	}
	if _, err := DecodeVOC([]byte("<annotation><object>")); err == nil || !strings.Contains(AnnotationErrorMessage(err), "无法解析") {
		t.Errorf("DecodeVOC(truncated) = %v", err)
	}
}

func TestAnnotationVersionAndNormalize(t *testing.T) {
	a, b := AnnotationVersion([]byte("x")), AnnotationVersion([]byte("y"))
	if a == "" || a == b || a != annotationVersion([]byte("x")) || len(a) != 32 {
		t.Errorf("AnnotationVersion = %q, %q", a, b)
	}
	in := []AnnotationObject{{Name: " dent ", Xmin: -5, Ymin: 2, Xmax: 500, Ymax: 40}}
	got, err := NormalizeObjects(in, 100, 80)
	if err != nil || !reflect.DeepEqual(got, []AnnotationObject{{Name: "dent", Xmin: 0, Ymin: 2, Xmax: 100, Ymax: 40}}) {
		t.Errorf("NormalizeObjects = %+v, %v", got, err)
	}
	if in[0].Name != " dent " || in[0].Xmin != -5 {
		t.Errorf("NormalizeObjects modified its input: %+v", in)
	}
	_, err = NormalizeObjects([]AnnotationObject{{Name: "a", Xmin: 100, Ymin: 0, Xmax: 120, Ymax: 5}}, 100, 80)
	var de *Error
	if !errors.As(err, &de) || de.Kind != ErrInvalid || !strings.Contains(de.Message, "宽或高为 0") {
		t.Errorf("NormalizeObjects(outside) = %v", err)
	}
}

func TestZipMethod(t *testing.T) {
	for name, want := range map[string]uint16{
		"a.jpg": zip.Store, "NG/b.JPEG": zip.Store, "c.png": zip.Store, "d.gif": zip.Store, "e.webp": zip.Store,
		"f.avif": zip.Store, "g.bmp": zip.Deflate, "h.tif": zip.Deflate, "i.xml": zip.Deflate, "j": zip.Deflate,
	} {
		if got := ZipMethod(name); got != want {
			t.Errorf("ZipMethod(%q) = %d, want %d", name, got, want)
		}
	}
}
