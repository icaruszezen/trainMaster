package augment

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

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
//
// A tag that is not a single SHORT, or whose value is not 1..8, counts as
// none (readers ignore it too). In a JPEG file only the first Exif
// segment before the image data is read, as Pillow and OpenCV do. A file
// that ends early is malformed, not an I/O error.
func FindOrientation(r io.ReaderAt, size int64) (Orientation, error) {
	sr := io.NewSectionReader(r, 0, max(size, 0))
	var head [4]byte
	if err := readAt(sr, head[:], 0); err != nil {
		return malformed(err)
	}
	var (
		o   Orientation
		err error
	)
	switch {
	case head[0] == 0xff && head[1] == 0xd8: // SOI
		o, err = jpegOrientation(sr)
	case string(head[:2]) == "II" || string(head[:2]) == "MM":
		o, err = tiffOrientation(sr, sr.Size(), true)
	}
	if err != nil {
		return malformed(err)
	}
	return o, nil
}

// NeutralizeOrientation sets the tag found by FindOrientation to 1 (top
// left) in w, a copy of the file being written in place: only the two
// bytes of the value change, the pixels stay as stored (annotations are
// in the stored pixel grid, but training tools apply the tag).
func NeutralizeOrientation(w io.WriterAt, o Orientation) error {
	if o.Value == 0 || o.Value == 1 {
		return nil
	}
	v := []byte{1, 0}
	if o.Big {
		v = []byte{0, 1}
	}
	n, err := w.WriteAt(v, o.Offset)
	if err == nil && n < len(v) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("reset the orientation tag: %w", err)
	}
	return nil
}

// errMalformed stops the parsing of metadata that is not as expected.
var errMalformed = errors.New("malformed metadata")

// malformed turns the end of the file and malformed metadata into "no
// tag"; other errors are I/O errors.
func malformed(err error) (Orientation, error) {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errMalformed) {
		return Orientation{}, nil
	}
	return Orientation{}, err
}

// readAt reads len(p) bytes at off (io.ErrUnexpectedEOF when the input
// ends first).
func readAt(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// jpegScanLimit bounds how far into a JPEG file the Exif segment is
// looked for (it precedes the image data, normally right after SOI).
const jpegScanLimit = 16 << 20

// jpegOrientation walks the segments of a JPEG file from SOI to the first
// SOS or EOI and reads the first APP1 Exif segment.
func jpegOrientation(r *io.SectionReader) (Orientation, error) {
	br := bufio.NewReader(io.NewSectionReader(r, 2, min(r.Size(), jpegScanLimit)-2))
	pos := int64(2) // file offset of the next byte of br
	readByte := func() (byte, error) {
		c, err := br.ReadByte()
		if err == nil {
			pos++
		}
		return c, err
	}
	for {
		c, err := readByte()
		for err == nil && c != 0xff { // extraneous bytes before a marker
			c, err = readByte()
		}
		for err == nil && c == 0xff { // fill bytes
			c, err = readByte()
		}
		if err != nil {
			return Orientation{}, err
		}
		marker := c
		switch {
		case marker == 0xda || marker == 0xd9: // SOS, EOI: no Exif before the image data
			return Orientation{}, nil
		case marker == 0x00, marker == 0x01, marker >= 0xd0 && marker <= 0xd8: // no length
			continue
		}
		var length [2]byte
		if _, err := io.ReadFull(br, length[:]); err != nil {
			return Orientation{}, err
		}
		pos += 2
		n := int(binary.BigEndian.Uint16(length[:])) - 2
		if n < 0 {
			return Orientation{}, errMalformed
		}
		const exifHeader = "Exif\x00\x00"
		if marker != 0xe1 { // APP1
			if _, err := br.Discard(n); err != nil {
				return Orientation{}, err
			}
			pos += int64(n)
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return Orientation{}, err
		}
		start := pos
		pos += int64(n)
		if !bytes.HasPrefix(payload, []byte(exifHeader)) {
			continue // e.g. XMP
		}
		tiff := payload[len(exifHeader):]
		o, err := tiffOrientation(bytes.NewReader(tiff), int64(len(tiff)), false)
		if err == nil && o.Value != 0 {
			o.Offset += start + int64(len(exifHeader))
		}
		return o, err
	}
}

// maxIFDEntries bounds the entries of IFD0 that are read.
const maxIFDEntries = 4096

// tiffOrientation reads tag 274 of IFD0 of the TIFF structure in the first
// size bytes of r (classic TIFF; BigTIFF too when big). Offsets are
// relative to r.
func tiffOrientation(r io.ReaderAt, size int64, big bool) (Orientation, error) {
	var head [16]byte
	if err := readAt(r, head[:8], 0); err != nil {
		return Orientation{}, err
	}
	var order binary.ByteOrder
	switch string(head[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return Orientation{}, errMalformed
	}
	var ifd, countSize, entrySize, valueAt int64
	switch magic := order.Uint16(head[2:]); {
	case magic == 42:
		ifd, countSize, entrySize, valueAt = int64(order.Uint32(head[4:])), 2, 12, 8
	case magic == 43 && big: // BigTIFF: 8-byte offsets and counts
		if err := readAt(r, head[8:16], 8); err != nil {
			return Orientation{}, err
		}
		if order.Uint16(head[4:]) != 8 || order.Uint16(head[6:]) != 0 {
			return Orientation{}, errMalformed
		}
		off := order.Uint64(head[8:])
		if off > uint64(size) {
			return Orientation{}, errMalformed
		}
		ifd, countSize, entrySize, valueAt = int64(off), 8, 20, 12
	default:
		return Orientation{}, errMalformed
	}
	if ifd < 8 || ifd > size-countSize {
		return Orientation{}, errMalformed
	}
	var cb [8]byte
	if err := readAt(r, cb[:countSize], ifd); err != nil {
		return Orientation{}, err
	}
	count := uint64(order.Uint16(cb[:]))
	if countSize == 8 {
		count = order.Uint64(cb[:])
	}
	first := ifd + countSize
	// Entries beyond the end of the input are not read: a tag among them
	// counts as none.
	n := int64(min(count, maxIFDEntries, uint64((size-first)/entrySize)))
	entries := make([]byte, n*entrySize)
	if err := readAt(r, entries, first); err != nil {
		return Orientation{}, err
	}
	for i := range n {
		e := entries[i*entrySize : (i+1)*entrySize]
		if order.Uint16(e) != 274 {
			continue
		}
		typ := order.Uint16(e[2:])
		var values uint64
		if countSize == 8 {
			values = order.Uint64(e[4:])
		} else {
			values = uint64(order.Uint32(e[4:]))
		}
		v := order.Uint16(e[valueAt:])
		if typ != 3 || values != 1 || v < 1 || v > 8 { // not a single SHORT 1..8
			return Orientation{}, nil
		}
		return Orientation{Value: v, Offset: first + i*entrySize + valueAt, Big: head[0] == 'M'}, nil
	}
	return Orientation{}, nil
}
