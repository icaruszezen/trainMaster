package dataset

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// Editing an existing VOC file. Other tools put more into a VOC file than
// the platform models: LabelImg marks reviewed files with
// <annotation verified="yes"> and writes <path>, CVAT adds <occluded> and
// <attributes> to objects, X-AnyLabeling may write polygons. A save from
// the annotation workspace rewrites only the boxes and keeps the rest:
//
//   - the root element's start tag (attributes, namespace declarations),
//     the other top-level elements (path, source, owner, custom fields),
//     comments, processing instructions and text, in their order;
//   - <object> elements the platform cannot show (no name or no numeric
//     <bndbox>), where they were;
//   - each box that is unchanged (same name, coordinates and flags) keeps
//     its original element, including <pose>, extra fields and the
//     coordinate format, so an untouched box never drifts;
//   - <size> is replaced only when it disagrees with the image.
//
// What is kept is copied byte for byte from the file (after decoding it to
// UTF-8), not re-encoded, so namespaces, entity references, CDATA sections
// and the formatting inside elements survive. Only the whitespace between
// the top-level children is rewritten: one child per line, indented with a
// tab, with the file's line ends. Changed and new boxes are written like
// writeVOC does; the new boxes go where the first box was. The file is
// written as UTF-8 whatever its original encoding, and anything after the
// root element is dropped.

// vocNode is a top-level child of <annotation>: an element, a comment, a
// processing instruction, a document type declaration or a piece of text,
// with its bytes.
type vocNode struct {
	raw  []byte
	name string // the local name of an element, "" for anything else
	// box is set for an <object> element the platform can show.
	box *AnnotationObject
	// width and height are those of a <size> element (0 when not a
	// positive number).
	width, height int
}

// vocDocument is a VOC file split up for patchVOC. The byte slices point
// into the file's UTF-8 text.
type vocDocument struct {
	// prolog holds the comments, processing instructions, document type
	// and stray text before the root element (not the XML declaration).
	prolog [][]byte
	start  []byte // the root element's start tag (never self-closing)
	end    []byte // its end tag
	nodes  []vocNode
	// newline is the line end of the file: "\r\n" when its first line ends
	// so, otherwise "\n".
	newline string
}

type vocSizeXML struct {
	Width  string `xml:"width"`
	Height string `xml:"height"`
}

// errNotPatchable is returned for files patchVOC cannot edit.
var errNotPatchable = errors.New("annotation cannot be patched")

// errEncodingFound stops the decoder of vocUTF8 at the XML declaration.
var errEncodingFound = errors.New("encoding declared")

// xmlSpace holds the white space characters of XML.
const xmlSpace = " \t\r\n"

// vocUTF8 returns the text of a VOC file in UTF-8, decoded as parseVOC
// reads it (see newVOCDecoder): UTF-16 by its byte order mark or its
// declaration, other encodings as the XML declaration names them. A UTF-8
// byte order mark is dropped; the declaration itself is left in the text.
func vocUTF8(data []byte) ([]byte, error) {
	src, transcoded := utf16ToUTF8(bufio.NewReader(bytes.NewReader(data)))
	text, err := io.ReadAll(src)
	if err != nil || transcoded {
		return text, err
	}
	// Let encoding/xml find the encoding, so the label is the one parseVOC
	// decoded the file with.
	label := ""
	dec := xml.NewDecoder(bytes.NewReader(text))
	dec.CharsetReader = func(l string, _ io.Reader) (io.Reader, error) {
		label = l
		return nil, errEncodingFound
	}
	for label == "" {
		tok, err := dec.RawToken()
		if err != nil {
			break // a malformed file fails again in splitVOC
		}
		if _, ok := tok.(xml.StartElement); ok {
			break
		}
	}
	if label == "" {
		return text, nil
	}
	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	// The encodings encoding/xml asks for are ASCII-compatible, so the
	// declaration decodes to itself.
	return enc.NewDecoder().Bytes(text)
}

// splitVOC splits the UTF-8 text of a VOC file into the parts patchVOC
// works with. It fails for text that is not well-formed up to the end of
// the root element and for a root element other than <annotation>.
func splitVOC(text []byte) (*vocDocument, error) {
	doc := &vocDocument{newline: "\n"}
	if i := bytes.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		doc.newline = "\r\n"
	}
	dec := xml.NewDecoder(bytes.NewReader(text))
	// The text is UTF-8 already; its declaration may still name the
	// original encoding.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }

	// Up to the root element. InputOffset is where the next token starts.
	for doc.start == nil {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, errNotVOC
		}
		if err != nil {
			return nil, err
		}
		raw := text[off:dec.InputOffset()]
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "annotation" {
				return nil, errNotVOC
			}
			doc.start = raw
		case xml.ProcInst:
			if t.Target != "xml" { // the declaration is written anew
				doc.prolog = append(doc.prolog, raw)
			}
		case xml.CharData:
			if raw = bytes.Trim(raw, xmlSpace); len(raw) > 0 {
				doc.prolog = append(doc.prolog, raw)
			}
		default: // comments, <!DOCTYPE ...>
			doc.prolog = append(doc.prolog, raw)
		}
	}
	if bytes.HasSuffix(doc.start, []byte("/>")) {
		// <annotation .../> gets children: make it a start tag and end tag.
		open := bytes.TrimRight(doc.start[:len(doc.start)-2], xmlSpace)
		qname := open[1:]
		if i := bytes.IndexAny(qname, xmlSpace); i >= 0 {
			qname = qname[:i]
		}
		doc.start = append(append([]byte{}, open...), '>')
		doc.end = []byte("</" + string(qname) + ">")
	}

	for {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, err // an unclosed root is an unexpected EOF
		}
		n := vocNode{}
		switch t := tok.(type) {
		case xml.StartElement:
			n.name = t.Name.Local
			switch n.name {
			case "object":
				var o vocObjectXML
				if err := dec.DecodeElement(&o, &t); err != nil {
					return nil, err
				}
				if obj, ok := o.object(); ok {
					n.box = &obj
				}
			case "size":
				var s vocSizeXML
				if err := dec.DecodeElement(&s, &t); err != nil {
					return nil, err
				}
				n.width, n.height = positiveInt(s.Width), positiveInt(s.Height)
			default:
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement: // of the root
			if doc.end == nil {
				doc.end = text[off:dec.InputOffset()]
			}
			return doc, nil
		case xml.CharData:
			// The white space between children is written anew; text
			// directly in the root is kept without it.
			raw := bytes.Trim(text[off:dec.InputOffset()], xmlSpace)
			if len(raw) == 0 {
				continue
			}
			n.raw = raw
		}
		if n.raw == nil {
			n.raw = text[off:dec.InputOffset()]
		}
		doc.nodes = append(doc.nodes, n)
	}
}

// patchVOC rewrites the VOC file data with objects (already normalized),
// keeping what the platform does not edit (see the comment at the top of
// this file). It fails with errNotPatchable when the file cannot be read
// as a VOC file, or the result would not read back as the file with the
// new boxes.
func patchVOC(data []byte, info imageInfo, objects []AnnotationObject) ([]byte, error) {
	text, err := vocUTF8(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	doc, err := splitVOC(text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	nodes := doc.nodes

	used := make([]bool, len(nodes))
	reuse := func(o AnnotationObject) int {
		for i, n := range nodes {
			if !used[i] && n.box != nil && *n.box == o {
				used[i] = true
				return i
			}
		}
		return -1
	}
	// Where the boxes go: at the first box, else after the last object
	// the platform cannot show, else at the end.
	insertAt := len(nodes)
	for i, n := range nodes {
		if n.box != nil {
			insertAt = i
			break
		}
	}
	if insertAt == len(nodes) {
		for i := len(nodes) - 1; i >= 0; i-- {
			if nodes[i].name == "object" {
				insertAt = i + 1
				break
			}
		}
	}

	nl := doc.newline
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + nl)
	for _, p := range doc.prolog {
		b.Write(p)
		b.WriteString(nl)
	}
	b.Write(doc.start)
	child := func(raw []byte) {
		b.WriteString(nl + "\t")
		b.Write(raw)
	}
	// newChild writes an element made by vocObjectText or vocSizeText.
	newChild := func(s string) {
		b.WriteString(nl + "\t" + strings.ReplaceAll(s, "\n", nl))
	}

	knownSize := info.Width > 0 && info.Height > 0
	addSize := knownSize
	for _, n := range nodes {
		if n.name == "size" {
			addSize = false
		}
	}
	writeBoxes := func() {
		for _, o := range objects {
			if i := reuse(o); i >= 0 {
				child(nodes[i].raw)
				continue
			}
			newChild(vocObjectText(o))
		}
	}
	for i, n := range nodes {
		if i == insertAt {
			writeBoxes()
		}
		if n.box != nil {
			continue // replaced by the new boxes
		}
		if n.name == "size" && knownSize && (n.width != info.Width || n.height != info.Height) {
			newChild(vocSizeText(info))
			continue
		}
		child(n.raw)
		if n.name == "filename" && addSize {
			newChild(vocSizeText(info))
			addSize = false
		}
	}
	if insertAt == len(nodes) {
		writeBoxes()
	}
	if addSize {
		newChild(vocSizeText(info))
	}
	b.WriteString(nl)
	b.Write(doc.end)
	b.WriteString(nl)

	// The result must read back like the file, with the new boxes; if not,
	// patchVOC understood the file differently from parseVOC (such as an
	// encoding declared inside the root).
	before, err := parseVOC(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	after, err := parseVOC(bytes.NewReader(b.Bytes()))
	if err != nil || after.Filename != before.Filename || after.Unsupported != before.Unsupported || !slices.Equal(after.Objects, objects) {
		return nil, fmt.Errorf("%w: the edited file reads differently", errNotPatchable)
	}
	return b.Bytes(), nil
}
