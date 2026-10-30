package dataset

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// maxAnnotationBytes bounds how much of a VOC file is read. Real files are
// a few KB; anything larger is not an annotation.
const maxAnnotationBytes = 16 << 20

// AnnotationObject is one labelled bounding box. The JSON field names are
// part of the API contract with the frontend.
type AnnotationObject struct {
	Name      string `json:"name"`
	Xmin      int    `json:"xmin"`
	Ymin      int    `json:"ymin"`
	Xmax      int    `json:"xmax"`
	Ymax      int    `json:"ymax"`
	Difficult bool   `json:"difficult"`
	Truncated bool   `json:"truncated"`
}

// vocAnnotation is a parsed Pascal VOC file.
type vocAnnotation struct {
	Filename string // <filename>, base name only
	Width    int    // <size><width>, 0 when missing
	Height   int
	Objects  []AnnotationObject
}

// labelCounts returns the number of objects per label.
func (a *vocAnnotation) labelCounts() map[string]int {
	counts := make(map[string]int)
	for _, o := range a.Objects {
		counts[o.Name]++
	}
	return counts
}

// errNotVOC is returned for well-formed XML whose root is not <annotation>.
var errNotVOC = errors.New("root element is not <annotation>")

type vocXML struct {
	XMLName  xml.Name `xml:"annotation"`
	Filename string   `xml:"filename"`
	Size     struct {
		Width  string `xml:"width"`
		Height string `xml:"height"`
	} `xml:"size"`
	Objects []struct {
		Name      string `xml:"name"`
		Difficult string `xml:"difficult"`
		Truncated string `xml:"truncated"`
		BndBox    *struct {
			Xmin string `xml:"xmin"`
			Ymin string `xml:"ymin"`
			Xmax string `xml:"xmax"`
			Ymax string `xml:"ymax"`
		} `xml:"bndbox"`
	} `xml:"object"`
}

// parseVOC parses a Pascal VOC annotation (spec §4.5). It returns an error
// for malformed XML and for any root element other than <annotation>.
// Objects without a name or without a numeric bounding box are skipped.
// Encodings other than UTF-8 (GBK, GB18030, Big5, UTF-16, ...) are decoded
// as declared, which matters for files written by Chinese Windows tools.
func parseVOC(r io.Reader) (*vocAnnotation, error) {
	// A longer file is cut off and then fails to parse.
	br := bufio.NewReader(io.LimitReader(r, maxAnnotationBytes))
	src, transcoded := utf16ToUTF8(br)

	dec := xml.NewDecoder(src)
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if transcoded {
			return input, nil // already UTF-8; the declaration names the original encoding
		}
		enc, err := htmlindex.Get(label)
		if err != nil {
			return nil, fmt.Errorf("unsupported encoding %q", label)
		}
		return transform.NewReader(input, enc.NewDecoder()), nil
	}

	// Check the root element first so other XML files (e.g. config files
	// in the same folder) are reported as "not VOC" rather than decoded.
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse annotation: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}
	if root.Name.Local != "annotation" {
		return nil, errNotVOC
	}
	var doc vocXML
	if err := dec.DecodeElement(&doc, &root); err != nil {
		return nil, fmt.Errorf("parse annotation: %w", err)
	}
	a := &vocAnnotation{
		Filename: path.Base(strings.ReplaceAll(strings.TrimSpace(doc.Filename), `\`, "/")),
		Width:    positiveInt(doc.Size.Width),
		Height:   positiveInt(doc.Size.Height),
	}
	if a.Filename == "." || a.Filename == "/" {
		a.Filename = ""
	}
	for _, o := range doc.Objects {
		name := strings.TrimSpace(o.Name)
		if name == "" || o.BndBox == nil {
			continue
		}
		var box [4]int
		valid := true
		for i, s := range []string{o.BndBox.Xmin, o.BndBox.Ymin, o.BndBox.Xmax, o.BndBox.Ymax} {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > math.MaxInt32 {
				valid = false
				break
			}
			box[i] = int(math.Round(f))
		}
		if !valid {
			continue
		}
		a.Objects = append(a.Objects, AnnotationObject{
			Name: name, Xmin: box[0], Ymin: box[1], Xmax: box[2], Ymax: box[3],
			Difficult: flag(o.Difficult), Truncated: flag(o.Truncated),
		})
	}
	return a, nil
}

// utf16ToUTF8 detects UTF-16 input (by BOM, or by the "<?" of the XML
// declaration) and converts it to UTF-8, because encoding/xml cannot read
// the declaration of a UTF-16 document by itself. A UTF-8 BOM is skipped.
func utf16ToUTF8(br *bufio.Reader) (io.Reader, bool) {
	head, _ := br.Peek(4)
	var endian unicode.Endianness
	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		_, _ = br.Discard(3)
		return br, false
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}), bytes.HasPrefix(head, []byte{'<', 0, '?', 0}):
		endian = unicode.LittleEndian
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}), bytes.HasPrefix(head, []byte{0, '<', 0, '?'}):
		endian = unicode.BigEndian
	default:
		return br, false
	}
	return transform.NewReader(br, unicode.UTF16(endian, unicode.UseBOM).NewDecoder()), true
}

// positiveInt parses a dimension such as "1920" or "1920.0"; 0 if invalid.
func positiveInt(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 1 || f > math.MaxInt32 {
		return 0
	}
	return int(math.Round(f))
}

// flag parses VOC's optional 0/1 fields.
func flag(s string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil && n != 0
}
