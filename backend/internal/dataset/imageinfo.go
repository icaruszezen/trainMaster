package dataset

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"

	// Decoders for the formats image.DecodeConfig/Decode recognize.
	// AVIF has no pure-Go decoder: such images get no dimensions from the
	// file (the VOC <size> is used if present) and no thumbnail.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// RandomAccessReader is an image file stream supporting sequential reads,
// seeking, and random range reads (implemented by *os.File and storagenode.File).
type RandomAccessReader interface {
	io.Reader
	io.Seeker
	io.ReaderAt
}

// isTIFF checks the TIFF byte-order mark. TIFF decoding needs random
// access; image.DecodeConfig/Decode would buffer the whole file in memory
// because they wrap the reader, so TIFF files are decoded from the
// RandomAccessReader directly.
func isTIFF(f RandomAccessReader) bool {
	var head [4]byte
	if _, err := f.ReadAt(head[:], 0); err != nil {
		return false
	}
	return bytes.Equal(head[:], []byte("II*\x00")) || bytes.Equal(head[:], []byte("MM\x00*"))
}

// errDecoderPanic reports a decoder that panicked on a malformed file.
var errDecoderPanic = errors.New("image decoder panicked")

// recoverDecoder turns a panic of an image decoder, which a crafted file
// could trigger, into an error for that one file. It must be deferred
// directly.
func recoverDecoder(err *error) {
	if v := recover(); v != nil {
		*err = fmt.Errorf("%w: %v", errDecoderPanic, v)
	}
}

// decodeImageConfig reads the dimensions and format (as image.DecodeConfig
// names it) of the image in f.
func decodeImageConfig(f RandomAccessReader) (_ image.Config, format string, err error) {
	defer recoverDecoder(&err)
	if isTIFF(f) {
		cfg, err := tiff.DecodeConfig(f)
		return cfg, "tiff", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return image.Config{}, "", err
	}
	return image.DecodeConfig(f)
}

// decodeImage decodes the whole image in f.
func decodeImage(f RandomAccessReader) (_ image.Image, err error) {
	defer recoverDecoder(&err)
	if isTIFF(f) {
		return tiff.Decode(f)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	return img, err
}

// imageDimensions returns the dimensions of the image file, or nil when
// they cannot be determined.
func imageDimensions(f RandomAccessReader) (width, height *int) {
	cfg, _, err := decodeImageConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, nil
	}
	return &cfg.Width, &cfg.Height
}
