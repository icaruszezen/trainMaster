package dataset

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Editing an existing VOC file. Other tools put more into a VOC file than
// the platform models: LabelImg marks reviewed files with
// <annotation verified="yes"> and writes <path>, CVAT adds <occluded> and
// <attributes> to objects, X-AnyLabeling may write polygons. A save from
// the annotation workspace rewrites only the boxes and keeps the rest:
//
//   - the root element's attributes, the other top-level elements (path,
//     source, owner, custom fields, comments), in their order;
//   - <object> elements the platform cannot show (no name or no numeric
//     <bndbox>), where they were;
//   - each box that is unchanged (same name, coordinates and flags) keeps
//     its original element, including <pose>, extra fields and the
//     coordinate format, so an untouched box never drifts;
//   - <size> is replaced only when it disagrees with the image.
//
// Changed and new boxes are written like writeVOC does. The new boxes go
// where the first box was. The file is written as UTF-8 whatever its
// original encoding.

// vocNode is a top-level child of <annotation>: an element with all its
// tokens, or a comment / processing instruction.
type vocNode struct {
	tokens []xml.Token
	name   string // element name, "" for a comment or processing instruction
	// box is set for an <object> element the platform can show.
	box *AnnotationObject
}

// errNotPatchable is returned for files patchVOC leaves to writeVOC.
var errNotPatchable = errors.New("annotation cannot be patched")

// parseVOCNodes splits a VOC file into the tokens before the root element,
// the root element and its top-level children. It fails for files that are
// not well-formed, use XML namespaces (which encoding/xml cannot write back
// faithfully) or have text directly inside <annotation>.
func parseVOCNodes(data []byte) (prolog []xml.Token, root xml.StartElement, nodes []vocNode, err error) {
	dec := newVOCDecoder(bytes.NewReader(data))
	depth := 0
	var current *vocNode
	for {
		tok, terr := dec.Token()
		if terr == io.EOF || (terr == nil && root.Name.Local != "" && depth == 0) {
			break // anything after the root element is dropped
		}
		if terr != nil {
			return nil, root, nil, terr
		}
		tok = xml.CopyToken(tok)
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != "" {
				return nil, root, nil, errNotPatchable
			}
			for _, a := range t.Attr {
				if a.Name.Space != "" || a.Name.Local == "xmlns" {
					return nil, root, nil, errNotPatchable
				}
			}
			depth++
			switch depth {
			case 1:
				if t.Name.Local != "annotation" {
					return nil, root, nil, errNotVOC
				}
				root = t
				continue
			case 2:
				nodes = append(nodes, vocNode{name: t.Name.Local})
				current = &nodes[len(nodes)-1]
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				continue
			}
		case xml.CharData:
			if depth == 1 {
				if len(bytes.TrimSpace(t)) > 0 {
					return nil, root, nil, errNotPatchable
				}
				continue // indentation between children is rewritten
			}
		case xml.ProcInst:
			if depth == 0 {
				if t.Target != "xml" {
					prolog = append(prolog, t)
				}
				continue
			}
		}
		switch {
		case depth == 0:
			if _, ok := tok.(xml.CharData); !ok {
				prolog = append(prolog, tok) // comments, <!DOCTYPE>
			}
		case depth == 1 && current == nil:
			// A comment or processing instruction among the children.
			nodes = append(nodes, vocNode{tokens: []xml.Token{tok}})
		default:
			current.tokens = append(current.tokens, tok)
			if _, ok := tok.(xml.EndElement); ok && depth == 1 {
				current = nil
			}
		}
	}
	if root.Name.Local == "" {
		return nil, root, nil, errNotVOC
	}
	for i := range nodes {
		if nodes[i].name != "object" {
			continue
		}
		var o vocObjectXML
		if err := xml.Unmarshal(encodeTokens(nodes[i].tokens), &o); err != nil {
			return nil, root, nil, err
		}
		if obj, ok := o.object(); ok {
			nodes[i].box = &obj
		}
	}
	return prolog, root, nodes, nil
}

// encodeTokens writes tokens as XML; they come from a decoder, so they are
// balanced and cannot fail to encode.
func encodeTokens(tokens []xml.Token) []byte {
	var b bytes.Buffer
	enc := xml.NewEncoder(&b)
	for _, t := range tokens {
		_ = enc.EncodeToken(t)
	}
	_ = enc.Flush()
	return b.Bytes()
}

// patchVOC rewrites the VOC file data with objects (already normalized),
// keeping what the platform does not edit (see the comment at the top of
// this file). It fails when the file cannot be patched; the caller then
// writes a new file.
func patchVOC(data []byte, info imageInfo, objects []AnnotationObject) ([]byte, error) {
	prolog, root, nodes, err := parseVOCNodes(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}

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

	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n")
	enc := xml.NewEncoder(&b)
	raw := func(s string) {
		_ = enc.Flush()
		b.WriteString(s)
	}
	// Encoder errors cannot happen for decoded tokens and a bytes.Buffer.
	emit := func(tokens ...xml.Token) {
		for _, t := range tokens {
			if c, ok := t.(xml.CharData); ok {
				// The encoder escapes tabs and quotes in text; keep the
				// original indentation readable instead.
				raw(textEscaper.Replace(string(c)))
				continue
			}
			_ = enc.EncodeToken(t)
		}
	}
	for _, t := range prolog {
		emit(t)
		raw("\n")
	}
	emit(root)

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
				raw("\n\t")
				emit(nodes[i].tokens...)
				continue
			}
			raw("\n\t" + vocObjectText(o))
		}
	}
	for i, n := range nodes {
		if i == insertAt {
			writeBoxes()
		}
		if n.box != nil {
			continue // replaced by the new boxes
		}
		if n.name == "size" && knownSize {
			if w, h := sizeOf(n.tokens); w != info.Width || h != info.Height {
				raw("\n\t" + vocSizeText(info))
				continue
			}
		}
		raw("\n\t")
		emit(n.tokens...)
		if n.name == "filename" && addSize {
			raw("\n\t" + vocSizeText(info))
			addSize = false
		}
	}
	if insertAt == len(nodes) {
		writeBoxes()
	}
	if addSize {
		raw("\n\t" + vocSizeText(info))
	}
	raw("\n")
	emit(root.End())
	raw("\n")
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// textEscaper escapes character data, keeping tabs, newlines and quotes
// as they are. \r is escaped so that it survives line-end normalization.
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")

// sizeOf reads <width> and <height> from the tokens of a <size> element.
func sizeOf(tokens []xml.Token) (width, height int) {
	var s struct {
		Width  string `xml:"width"`
		Height string `xml:"height"`
	}
	if xml.Unmarshal(encodeTokens(tokens), &s) != nil {
		return 0, 0
	}
	return positiveInt(s.Width), positiveInt(s.Height)
}
