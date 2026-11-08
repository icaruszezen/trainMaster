package augment

import "io"

// Orientation is the orientation tag (EXIF / TIFF tag 274) of an image
// file.
type Orientation struct {
	Value  uint16 // 1..8; 0: the file has no tag
	Offset int64  // of the tag's 2-byte value in the file
	Big    bool   // the value is big-endian
}

// FindOrientation reads the orientation tag of a JPEG (APP1 Exif, IFD0)
// or TIFF (IFD0) file of size bytes; other formats, files without the tag
// and malformed metadata give Value 0 and no error. Only I/O errors are
// returned.
func FindOrientation(r io.ReaderAt, size int64) (Orientation, error) {
	panic("augment: not implemented")
}

// NeutralizeOrientation sets the tag found by FindOrientation to 1 (top
// left) in w, a copy of the file being written in place: only the two
// bytes of the value change, the pixels stay as stored (annotations are
// in the stored pixel grid, but training tools apply the tag).
func NeutralizeOrientation(w io.WriterAt, o Orientation) error {
	panic("augment: not implemented")
}
