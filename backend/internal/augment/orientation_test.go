package augment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/image/tiff"
)

// ifdEntry is a classic TIFF directory entry.
type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    [4]byte
}

func shortEntry(order binary.ByteOrder, tag, v uint16) ifdEntry {
	e := ifdEntry{tag: tag, typ: 3, count: 1}
	order.PutUint16(e.value[:], v)
	return e
}

func longEntry(order binary.ByteOrder, tag uint16, v uint32) ifdEntry {
	e := ifdEntry{tag: tag, typ: 4, count: 1}
	order.PutUint32(e.value[:], v)
	return e
}

// tiffHeader is the byte order mark and magic of classic TIFF.
func tiffHeader(order binary.ByteOrder) []byte {
	if order == binary.BigEndian {
		return []byte("MM\x00*")
	}
	return []byte("II*\x00")
}

// buildIFD writes a classic TIFF header pointing to IFD0 at offset ifd,
// filler up to it, IFD0 and its next-IFD offset 0.
func buildIFD(order binary.ByteOrder, ifd uint32, filler []byte, entries []ifdEntry) []byte {
	var b bytes.Buffer
	b.Write(tiffHeader(order))
	binary.Write(&b, order, ifd)
	b.Write(filler)
	for b.Len() < int(ifd) {
		b.WriteByte(0)
	}
	binary.Write(&b, order, uint16(len(entries)))
	for _, e := range entries {
		binary.Write(&b, order, e.tag)
		binary.Write(&b, order, e.typ)
		binary.Write(&b, order, e.count)
		b.Write(e.value[:])
	}
	binary.Write(&b, order, uint32(0))
	return b.Bytes()
}

// segment is a JPEG marker segment.
func segment(marker byte, payload []byte) []byte {
	return append([]byte{0xff, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
}

func exifSegment(order binary.ByteOrder, entries ...ifdEntry) []byte {
	return segment(0xe1, append([]byte("Exif\x00\x00"), buildIFD(order, 8, nil, entries)...))
}

// testJPEG is a small JPEG file written by image/jpeg (SOI, DQT, ...).
func testJPEG(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, uniqueNRGBA(16, 8), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withSegments inserts segments right after SOI.
func withSegments(file []byte, segs ...[]byte) []byte {
	out := append([]byte{}, file[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, file[2:]...)
}

// testTIFF is a 3×2 8-bit gray TIFF file with an orientation tag of value
// v (none when 0) and the pixels 10, 20, …, 60.
func testTIFF(order binary.ByteOrder, v uint16) []byte {
	entries := []ifdEntry{
		shortEntry(order, 256, 3),    // ImageWidth
		shortEntry(order, 257, 2),    // ImageLength
		shortEntry(order, 258, 8),    // BitsPerSample
		shortEntry(order, 259, 1),    // Compression: none
		shortEntry(order, 262, 1),    // PhotometricInterpretation: BlackIsZero
		longEntry(order, 273, 8),     // StripOffsets
		shortEntry(order, 277, 1),    // SamplesPerPixel
		shortEntry(order, 278, 2),    // RowsPerStrip
		longEntry(order, 279, 6),     // StripByteCounts
		shortEntry(order, 296, 1),    // ResolutionUnit
		shortEntry(order, 0x8000, 0), // private
	}
	if v != 0 {
		entries = append(entries[:6:6], append([]ifdEntry{shortEntry(order, 274, v)}, entries[6:]...)...)
	}
	return buildIFD(order, 16, []byte{10, 20, 30, 40, 50, 60}, entries)
}

func find(t *testing.T, data []byte) Orientation {
	t.Helper()
	o, err := FindOrientation(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("FindOrientation: %v", err)
	}
	if o.Value != 0 {
		if o.Offset < 0 || o.Offset+2 > int64(len(data)) {
			t.Fatalf("offset %d outside %d bytes", o.Offset, len(data))
		}
		order := binary.ByteOrder(binary.LittleEndian)
		if o.Big {
			order = binary.BigEndian
		}
		if got := order.Uint16(data[o.Offset:]); got != o.Value {
			t.Fatalf("value at %d is %d, reported %d", o.Offset, got, o.Value)
		}
	}
	return o
}

// sliceWriter writes into a byte slice in place.
type sliceWriter []byte

func (s sliceWriter) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(s)) {
		return 0, errors.New("write outside the slice")
	}
	return copy(s[off:], p), nil
}

// neutralized returns a copy of data with the tag set to 1, and checks
// that only the value changed.
func neutralized(t *testing.T, data []byte, o Orientation) []byte {
	t.Helper()
	out := bytes.Clone(data)
	if err := NeutralizeOrientation(sliceWriter(out), o); err != nil {
		t.Fatal(err)
	}
	for i := range out {
		if out[i] != data[i] && (int64(i) < o.Offset || int64(i) >= o.Offset+2) {
			t.Fatalf("byte %d changed (tag at %d)", i, o.Offset)
		}
	}
	return out
}

func TestFindOrientationJPEG(t *testing.T) {
	base := testJPEG(t)
	II, MM := binary.ByteOrder(binary.LittleEndian), binary.ByteOrder(binary.BigEndian)
	width := func(o binary.ByteOrder) ifdEntry { return longEntry(o, 256, 16) }
	jfif := segment(0xe0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	xmp := segment(0xe1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"))
	many := []ifdEntry{}
	for i := range 100 {
		many = append(many, shortEntry(II, uint16(300+i), 0))
	}
	many = append(many, shortEntry(II, 274, 5))
	beforeEOI := append(append(bytes.Clone(base[:len(base)-2]), exifSegment(II, shortEntry(II, 274, 6))...), 0xff, 0xd9)
	tests := []struct {
		name string
		data []byte
		want uint16
		big  bool
	}{
		{"little-endian", withSegments(base, exifSegment(II, width(II), shortEntry(II, 274, 6))), 6, false},
		{"big-endian", withSegments(base, exifSegment(MM, width(MM), shortEntry(MM, 274, 8))), 8, true},
		{"value 1", withSegments(base, exifSegment(MM, shortEntry(MM, 274, 1))), 1, true},
		{"after JFIF", withSegments(base, jfif, exifSegment(II, shortEntry(II, 274, 3))), 3, false},
		{"after XMP", withSegments(base, xmp, exifSegment(MM, shortEntry(MM, 274, 2))), 2, true},
		{"fill bytes and comment", withSegments(base, segment(0xfe, []byte("hi")), []byte{0xff, 0xff}, exifSegment(II, shortEntry(II, 274, 7))), 7, false},
		{"last of many entries", withSegments(base, exifSegment(II, many...)), 5, false},
		{"no Exif", base, 0, false},
		{"only JFIF", withSegments(base, jfif), 0, false},
		{"no tag", withSegments(base, exifSegment(II, width(II))), 0, false},
		{"LONG tag", withSegments(base, exifSegment(II, longEntry(II, 274, 6))), 0, false},
		{"two values", withSegments(base, exifSegment(II, ifdEntry{tag: 274, typ: 3, count: 2, value: [4]byte{6, 0, 6, 0}})), 0, false},
		{"value 0", withSegments(base, exifSegment(II, shortEntry(II, 274, 0))), 0, false},
		{"value 9", withSegments(base, exifSegment(II, shortEntry(II, 274, 9))), 0, false},
		{"first Exif wins", withSegments(base, exifSegment(II, width(II)), exifSegment(II, shortEntry(II, 274, 6))), 0, false},
		{"after the image data", beforeEOI, 0, false},
		{"IFD outside the segment", withSegments(base, segment(0xe1, append([]byte("Exif\x00\x00II*\x00"), 0xff, 0, 0, 0))), 0, false},
		{"BigTIFF in Exif", withSegments(base, segment(0xe1, []byte("Exif\x00\x00II+\x00\x08\x00\x00\x00\x10\x00\x00\x00\x00\x00\x00\x00"))), 0, false},
		{"bad length", withSegments(base, []byte{0xff, 0xe1, 0x00, 0x01}), 0, false},
	}
	for _, tt := range tests {
		o := find(t, tt.data)
		if o.Value != tt.want || o.Value != 0 && o.Big != tt.big {
			t.Errorf("%s: %+v, want value %d big %v", tt.name, o, tt.want, tt.big)
			continue
		}
		if o.Value == 0 {
			continue
		}
		fixed := neutralized(t, tt.data, o)
		if got := find(t, fixed); got != (Orientation{Value: 1, Offset: o.Offset, Big: o.Big}) {
			t.Errorf("%s: after neutralizing %+v", tt.name, got)
		}
		a, err1 := jpeg.Decode(bytes.NewReader(tt.data))
		b, err2 := jpeg.Decode(bytes.NewReader(fixed))
		if err1 != nil || err2 != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: decodes differently after neutralizing (%v, %v)", tt.name, err1, err2)
		}
	}
}

func TestFindOrientationTIFF(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		plain, err := tiff.Decode(bytes.NewReader(testTIFF(order, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if g, ok := plain.(*image.Gray); !ok || !bytes.Equal(g.Pix, []byte{10, 20, 30, 40, 50, 60}) {
			t.Fatalf("test TIFF decodes as %#v", plain)
		}
		if o := find(t, testTIFF(order, 0)); o.Value != 0 {
			t.Errorf("%v without tag: %+v", order, o)
		}
		data := testTIFF(order, 6)
		o := find(t, data)
		if o.Value != 6 || o.Big != (order == binary.BigEndian) {
			t.Fatalf("%v: %+v", order, o)
		}
		fixed := neutralized(t, data, o)
		if got := find(t, fixed); got.Value != 1 || got.Offset != o.Offset {
			t.Errorf("%v after neutralizing: %+v", order, got)
		}
		a, err1 := tiff.Decode(bytes.NewReader(data))
		b, err2 := tiff.Decode(bytes.NewReader(fixed))
		if err1 != nil || err2 != nil || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, plain) {
			t.Errorf("%v: decodes differently (%v, %v)", order, err1, err2)
		}
	}

	// A TIFF file written by x/image/tiff has no tag.
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, uniqueNRGBA(5, 4), nil); err != nil {
		t.Fatal(err)
	}
	if o := find(t, buf.Bytes()); o.Value != 0 {
		t.Errorf("x/image/tiff file: %+v", o)
	}

	// BigTIFF: 8-byte offsets and counts, 20-byte entries.
	var big bytes.Buffer
	big.WriteString("II+\x00")
	binary.Write(&big, binary.LittleEndian, []uint16{8, 0})
	binary.Write(&big, binary.LittleEndian, uint64(16))
	binary.Write(&big, binary.LittleEndian, uint64(1))
	binary.Write(&big, binary.LittleEndian, []uint16{274, 3})
	binary.Write(&big, binary.LittleEndian, uint64(1))
	binary.Write(&big, binary.LittleEndian, []uint16{8, 0, 0, 0})
	binary.Write(&big, binary.LittleEndian, uint64(0))
	if o := find(t, big.Bytes()); o != (Orientation{Value: 8, Offset: 16 + 8 + 12}) {
		t.Errorf("BigTIFF: %+v", o)
	}

	II := binary.LittleEndian
	for name, data := range map[string][]byte{
		"IFD at 0":           buildIFD(II, 0, nil, []ifdEntry{shortEntry(II, 274, 6)})[:8],
		"IFD beyond the end": append(tiffHeader(II), 0, 0, 1, 0),
		"truncated entries":  append(tiffHeader(II), 8, 0, 0, 0, 0xff, 0xff, 1, 1, 3, 0),
		"bad magic":          []byte("II\x2b\x01\x08\x00\x00\x00\x00\x00"),
		"header only":        tiffHeader(II),
	} {
		if o := find(t, data); o.Value != 0 {
			t.Errorf("%s: %+v", name, o)
		}
	}
}

func TestFindOrientationOther(t *testing.T) {
	var buf bytes.Buffer
	img, _ := Apply(uniqueNRGBA(4, 4), nil, Plan{})
	if err := Encode(&buf, img, "png"); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"png": buf.Bytes(), "empty": nil, "one byte": {0xff}, "SOI only": {0xff, 0xd8},
		"garbage": bytes.Repeat([]byte{0xff}, 100), "webp": riff("VP8L", []byte{0}),
	} {
		if o := find(t, data); o.Value != 0 {
			t.Errorf("%s: %+v", name, o)
		}
	}
}

// TestFindOrientationTruncated: every prefix of a file with a tag is
// malformed or still has the tag.
func TestFindOrientationTruncated(t *testing.T) {
	II := binary.LittleEndian
	for _, data := range [][]byte{
		withSegments(testJPEG(t), exifSegment(II, longEntry(II, 256, 16), shortEntry(II, 274, 6))),
		testTIFF(binary.BigEndian, 6),
	} {
		for n := range len(data) {
			o, err := FindOrientation(bytes.NewReader(data[:n]), int64(n))
			if err != nil || o.Value != 0 && (o.Value != 6 || o.Offset+2 > int64(n)) {
				t.Fatalf("prefix of %d bytes: %+v, %v", n, o, err)
			}
			// size smaller than the data: the rest is not read.
			if o2, err := FindOrientation(bytes.NewReader(data), int64(n)); err != nil || o2 != o {
				t.Fatalf("size %d: %+v, %v; want %+v", n, o2, err, o)
			}
		}
	}
}

// failingReader fails every read that reaches past ok bytes.
type failingReader struct {
	data []byte
	ok   int64
}

var errDisk = errors.New("disk on fire")

func (f failingReader) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.ok {
		return 0, errDisk
	}
	return copy(p, f.data[off:]), nil
}

func TestFindOrientationIOError(t *testing.T) {
	II := binary.LittleEndian
	for _, data := range [][]byte{
		withSegments(testJPEG(t), exifSegment(II, shortEntry(II, 274, 6))),
		testTIFF(II, 6),
	} {
		for _, ok := range []int64{0, 3, 10, 20} {
			_, err := FindOrientation(failingReader{data, ok}, int64(len(data)))
			if !errors.Is(err, errDisk) {
				t.Errorf("reads fail after %d bytes: %v", ok, err)
			}
		}
	}
}

// errorWriter fails every write.
type errorWriter struct{}

func (errorWriter) WriteAt([]byte, int64) (int, error) { return 0, errDisk }

func TestNeutralizeOrientation(t *testing.T) {
	for _, o := range []Orientation{{}, {Value: 1, Offset: 5}} {
		if err := NeutralizeOrientation(errorWriter{}, o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if err := NeutralizeOrientation(errorWriter{}, Orientation{Value: 6, Offset: 5}); !errors.Is(err, errDisk) {
		t.Errorf("write error: %v", err)
	}
	for _, big := range []bool{false, true} {
		b := []byte{9, 9, 6, 0, 9}
		want := []byte{9, 9, 1, 0, 9}
		if big {
			b[2], b[3] = 0, 6
			want[2], want[3] = 0, 1
		}
		if err := NeutralizeOrientation(sliceWriter(b), Orientation{Value: 6, Offset: 2, Big: big}); err != nil || !bytes.Equal(b, want) {
			t.Errorf("big %v: %v, %v", big, b, err)
		}
	}

	// On a file, as the data version builder uses it.
	II := binary.LittleEndian
	data := withSegments(testJPEG(t), exifSegment(II, shortEntry(II, 274, 8)))
	path := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o, err := FindOrientation(f, int64(len(data)))
	if err != nil || o.Value != 8 {
		t.Fatalf("file: %+v, %v", o, err)
	}
	if err := NeutralizeOrientation(f, o); err != nil {
		t.Fatal(err)
	}
	if o, err := FindOrientation(f, int64(len(data))); err != nil || o.Value != 1 {
		t.Errorf("file after neutralizing: %+v, %v", o, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(f); err != nil {
		t.Errorf("decode after neutralizing: %v", err)
	}
}

func FuzzFindOrientation(f *testing.F) {
	II, MM := binary.LittleEndian, binary.BigEndian
	base := testJPEG(f)
	f.Add(withSegments(base, exifSegment(II, longEntry(II, 256, 16), shortEntry(II, 274, 6))))
	f.Add(withSegments(base, exifSegment(MM, shortEntry(MM, 274, 3))))
	f.Add(withSegments(base, segment(0xe0, []byte("JFIF\x00")), exifSegment(II)))
	f.Add(testTIFF(II, 6))
	f.Add(testTIFF(MM, 0))
	f.Add([]byte("II+\x00\x08\x00\x00\x00\x10\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		o, err := FindOrientation(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("error from a bytes.Reader: %v", err)
		}
		if o.Value == 0 {
			return
		}
		if o.Value > 8 || o.Offset < 0 || o.Offset+2 > int64(len(data)) {
			t.Fatalf("bad result %+v for %d bytes", o, len(data))
		}
		fixed := bytes.Clone(data)
		if err := NeutralizeOrientation(sliceWriter(fixed), o); err != nil {
			t.Fatal(err)
		}
		if got, err := FindOrientation(bytes.NewReader(fixed), int64(len(fixed))); err != nil ||
			got != (Orientation{Value: 1, Offset: o.Offset, Big: o.Big}) {
			t.Fatalf("after neutralizing %+v: %+v, %v", o, got, err)
		}
	})
}
