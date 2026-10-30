package dataset

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

const labelImgXML = `<annotation verified="yes">
	<folder>JPEGImages</folder>
	<filename>%s</filename>
	<path>C:\data\JPEGImages\0001.jpg</path>
	<size><width>1920</width><height>1080.0</height><depth>3</depth></size>
	<segmented>0</segmented>
	<object>
		<name> %s </name>
		<pose>Unspecified</pose>
		<truncated>1</truncated>
		<difficult>0</difficult>
		<bndbox><xmin>10</xmin><ymin>20.4</ymin><xmax>100.5</xmax><ymax>200.6</ymax></bndbox>
	</object>
	<object>
		<name>dent</name>
		<difficult>1</difficult>
		<bndbox><xmin> 1 </xmin><ymin>2</ymin><xmax>3</xmax><ymax>4</ymax></bndbox>
	</object>
	<object><name></name><bndbox><xmin>1</xmin><ymin>2</ymin><xmax>3</xmax><ymax>4</ymax></bndbox></object>
	<object><name>no-box</name></object>
	<object><name>bad-box</name><bndbox><xmin>a</xmin><ymin>2</ymin><xmax>3</xmax><ymax>4</ymax></bndbox></object>
</annotation>`

func vocDoc(decl, filename, label string) string {
	return decl + strings.Replace(strings.Replace(labelImgXML, "%s", filename, 1), "%s", label, 1)
}

func TestParseVOC(t *testing.T) {
	a, err := parseVOC(strings.NewReader(vocDoc(`<?xml version="1.0"?>`, "0001.jpg", "划痕")))
	if err != nil {
		t.Fatalf("parseVOC: %v", err)
	}
	want := &vocAnnotation{
		Filename: "0001.jpg", Width: 1920, Height: 1080,
		Objects: []AnnotationObject{
			{Name: "划痕", Xmin: 10, Ymin: 20, Xmax: 101, Ymax: 201, Truncated: true},
			{Name: "dent", Xmin: 1, Ymin: 2, Xmax: 3, Ymax: 4, Difficult: true},
		},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("got %+v\nwant %+v", a, want)
	}
	if c := a.labelCounts(); c["划痕"] != 1 || c["dent"] != 1 || len(c) != 2 {
		t.Errorf("labelCounts = %v", c)
	}
}

func TestParseVOCEncodings(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="GBK"?>`, "图片.jpg", "划痕"))
	if err != nil {
		t.Fatal(err)
	}
	gb2312, _ := simplifiedchinese.GBK.NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="gb2312"?>`, "图片.jpg", "划痕"))
	gb18030, _ := simplifiedchinese.GB18030.NewEncoder().String(vocDoc(`<?xml version='1.0' encoding='GB18030'?>`, "图片.jpg", "划痕"))
	big5, _ := traditionalchinese.Big5.NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="big5"?>`, "圖片.jpg", "劃痕"))
	utf16le, _ := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="UTF-16"?>`, "图片.jpg", "划痕"))
	utf16be, _ := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="utf-16"?>`, "图片.jpg", "划痕"))
	utf16NoBOM, _ := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().String(vocDoc(`<?xml version="1.0" encoding="utf-16"?>`, "图片.jpg", "划痕"))
	utf8BOM := "\xef\xbb\xbf" + vocDoc(`<?xml version="1.0" encoding="UTF-8"?>`, "图片.jpg", "划痕")

	tests := map[string]struct {
		data             string
		filename, label0 string
	}{
		"gbk":           {gbk, "图片.jpg", "划痕"},
		"gb2312":        {gb2312, "图片.jpg", "划痕"},
		"gb18030":       {gb18030, "图片.jpg", "划痕"},
		"big5":          {big5, "圖片.jpg", "劃痕"},
		"utf-16le BOM":  {utf16le, "图片.jpg", "划痕"},
		"utf-16be BOM":  {utf16be, "图片.jpg", "划痕"},
		"utf-16 no BOM": {utf16NoBOM, "图片.jpg", "划痕"},
		"utf-8 BOM":     {utf8BOM, "图片.jpg", "划痕"},
	}
	for name, tt := range tests {
		a, err := parseVOC(bytes.NewReader([]byte(tt.data)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if a.Filename != tt.filename || len(a.Objects) != 2 || a.Objects[0].Name != tt.label0 {
			t.Errorf("%s: got %+v", name, a)
		}
	}
}

func TestParseVOCInvalid(t *testing.T) {
	for name, doc := range map[string]string{
		"other root":       `<?xml version="1.0"?><config><name>x</name></config>`,
		"html":             `<html><body>not voc</body></html>`,
		"malformed":        `<annotation><object><name>a</name>`,
		"not xml":          "\x89PNG\r\n\x1a\n",
		"empty":            "",
		"unknown encoding": `<?xml version="1.0" encoding="x-klingon"?><annotation/>`,
	} {
		if _, err := parseVOC(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := parseVOC(strings.NewReader(`<config/>`)); !errors.Is(err, errNotVOC) {
		t.Errorf("other root: err = %v, want errNotVOC", err)
	}
}

func TestParseVOCEmpty(t *testing.T) {
	a, err := parseVOC(strings.NewReader(`<annotation><filename>dir\neg.jpg</filename><size><width>-1</width></size></annotation>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Objects) != 0 || a.Width != 0 || a.Height != 0 || a.Filename != "neg.jpg" {
		t.Errorf("got %+v", a)
	}
}
