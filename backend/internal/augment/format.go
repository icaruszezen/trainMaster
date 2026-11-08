package augment

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sync"
)

// jpegQuality is the quality of the JPEG copies. (Go's encoder always
// subsamples the chroma 4:2:0.)
const jpegQuality = 95

// webpScanLimit bounds how far IsLosslessWebP looks for the image chunk
// of an extended (VP8X) file.
const webpScanLimit = 1 << 20

func isLosslessWebP(r io.ReaderAt) bool {
	var head [16]byte // "RIFF", size, "WEBP", the first chunk's fourcc
	if !readFullAt(r, head[:], 0) || string(head[:4]) != "RIFF" || string(head[8:12]) != "WEBP" {
		return false
	}
	switch string(head[12:16]) {
	case "VP8L":
		return true
	case "VP8X":
		// Chunks follow, each a fourcc, a little-endian size and the data
		// padded to an even size; the image is the first VP8L or "VP8 "
		// chunk (ALPH, ICCP, EXIF, ... come before or after it).
		var chunk [8]byte
		for off := int64(12); off < webpScanLimit; {
			if !readFullAt(r, chunk[:], off) {
				return false
			}
			switch string(chunk[:4]) {
			case "VP8L":
				return true
			case "VP8 ":
				return false
			}
			size := int64(binary.LittleEndian.Uint32(chunk[4:]))
			off += 8 + size + size&1
		}
	}
	return false
}

// readFullAt reads len(p) bytes at off; false on any error or a short
// read.
func readFullAt(r io.ReaderAt, p []byte, off int64) bool {
	n, err := r.ReadAt(p, off)
	return n == len(p) && (err == nil || err == io.EOF)
}

func encode(w io.Writer, img image.Image, format string) error {
	switch format {
	case "jpeg":
		// image/jpeg reads *image.RGBA directly and anything else pixel by
		// pixel through At; an opaque NRGBA image has the same bytes.
		if n, ok := img.(*image.NRGBA); ok && n.Opaque() {
			img = &image.RGBA{Pix: n.Pix, Stride: n.Stride, Rect: n.Rect}
		}
		return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
	case "png":
		enc := png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: pngBuffers}
		return enc.Encode(w, img)
	}
	return fmt.Errorf("augment: unknown output format %q", format)
}

// pngBuffers reuses the PNG encoders' buffers (and zlib writers) between
// copies; png.EncoderBuffer is not safe for concurrent use, sync.Pool
// hands each one to one encoder at a time.
var pngBuffers = &pngBufferPool{}

type pngBufferPool struct{ pool sync.Pool }

func (p *pngBufferPool) Get() *png.EncoderBuffer {
	if b, ok := p.pool.Get().(*png.EncoderBuffer); ok {
		return b
	}
	return nil // the encoder allocates one
}

func (p *pngBufferPool) Put(b *png.EncoderBuffer) { p.pool.Put(b) }
