package dataset

import "bytes"

// Exported VOC helpers for other packages that keep VOC files of their
// own: the model review (internal/review, internal/reviewfile) writes one
// file per reviewed image with the same writer and reads it with the same
// parser as datasets, so the files look and behave exactly like dataset
// annotations (LabelImg can open them, merging them into a dataset needs no
// conversion). They are pure functions: no database, no files.

// MaxAnnotationBytes bounds how much of a VOC file is read (16 MiB): a
// longer file is cut off and then fails to parse.
const MaxAnnotationBytes = maxAnnotationBytes

// VOCFile is a parsed Pascal VOC annotation.
type VOCFile struct {
	Filename string // <filename>, base name only ("" if missing)
	// Width and Height are <size>, 0 when missing or invalid.
	Width, Height int
	Objects       []AnnotationObject // the boxes, never nil
	// Unsupported counts the <object> elements that are not boxes the
	// platform can show (no name or no numeric <bndbox>, e.g. polygons).
	Unsupported int
}

// DecodeVOC parses a Pascal VOC file as dataset scans and the annotation
// workspace do: UTF-8 with or without BOM, UTF-16 and the encodings a
// declaration names (GBK, Big5, ...); coordinates are rounded to whole
// pixels and objects without a name or a numeric box are counted as
// unsupported. AnnotationErrorMessage explains an error in Chinese.
func DecodeVOC(data []byte) (VOCFile, error) {
	a, err := parseVOC(bytes.NewReader(data))
	if err != nil {
		return VOCFile{}, err
	}
	f := VOCFile{Filename: a.Filename, Width: a.Width, Height: a.Height, Objects: a.Objects, Unsupported: a.Unsupported}
	if f.Objects == nil {
		f.Objects = []AnnotationObject{}
	}
	return f, nil
}

// AnnotationErrorMessage explains in Chinese why a VOC file could not be
// read or parsed (an error of DecodeVOC, or of reading the file).
func AnnotationErrorMessage(err error) string { return annotationErrorMessage(err) }

// EncodeVOC writes a complete Pascal VOC file in the LabelImg layout that
// dataset imports write: <folder> is the name of the image's folder ("." or
// "" for the top level of a dataset, which is left out), <filename> the
// image's file name, <size> the image size with depth 3 (left out when
// width or height is not positive), then the objects in order. The error
// is always nil; it is there for writers that may fail later.
func EncodeVOC(folder, fileName string, width, height int, objects []AnnotationObject) ([]byte, error) {
	var b bytes.Buffer
	info := imageInfo{Width: width, Height: height, Depth: 3}
	if err := writeVOC(&b, folder, fileName, info, objects); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// AnnotationVersion identifies the content of an annotation file: the
// annotation_version of dataset images. Clients treat it as opaque; ""
// stands for "no file".
func AnnotationVersion(data []byte) string { return annotationVersion(data) }

// NormalizeObjects validates boxes to save as dataset annotation saves do:
// labels are trimmed and must be non-empty, at most 255 characters and
// free of control characters; coordinates are clamped to the image (when
// width and height are positive), must then fit in an int32 and span at
// least one pixel; at most 10,000 boxes. A problem is an *Error of kind
// ErrInvalid naming the box. The input is not modified.
func NormalizeObjects(objects []AnnotationObject, width, height int) ([]AnnotationObject, error) {
	return normalizeObjects(objects, width, height)
}
