package dataset

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func encodeImage(t *testing.T, format string, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "png":
		err = png.Encode(&buf, img)
	case "bmp":
		err = bmp.Encode(&buf, img)
	case "tiff":
		err = tiff.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return img
}

func TestThumbnailGenerate(t *testing.T) {
	gray16 := image.NewGray16(image.Rect(0, 0, 640, 640))
	for i := range gray16.Pix {
		gray16.Pix[i] = uint8(i)
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 100, 50)) // all pixels fully transparent

	tests := []struct {
		name         string
		data         []byte
		side         int
		wantW, wantH int
	}{
		{"jpeg landscape", encodeImage(t, "jpeg", gradient(1000, 500)), 320, 320, 160},
		{"jpeg portrait large", encodeImage(t, "jpeg", gradient(900, 1800)), 1600, 800, 1600},
		{"png not upscaled", encodeImage(t, "png", transparent), 320, 100, 50},
		{"png 16-bit gray", encodeImage(t, "png", gray16), 320, 320, 320},
		{"bmp", encodeImage(t, "bmp", gradient(400, 300)), 320, 320, 240},
		{"tiff", encodeImage(t, "tiff", gradient(330, 100)), 320, 320, 97},
		{"png tall and thin", encodeImage(t, "png", gradient(40, 4000)), 320, 3, 320},
	}
	th := newThumbnailer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "img"), tt.data, 0o644); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(t.TempDir(), "cache", "1-sm-v.jpg")
			if err := th.generate(dir, "img", tt.side, dst); err != nil {
				t.Fatalf("generate: %v", err)
			}
			f, err := os.Open(dst)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := jpeg.Decode(f)
			if err != nil {
				t.Fatalf("thumbnail is not a JPEG: %v", err)
			}
			if b := got.Bounds(); b.Dx() != tt.wantW || b.Dy() != tt.wantH {
				t.Errorf("thumbnail is %dx%d, want %dx%d", b.Dx(), b.Dy(), tt.wantW, tt.wantH)
			}
			if tt.name == "png not upscaled" {
				if r, g, b, _ := got.At(10, 10).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
					t.Errorf("transparency should become white, got %v", got.At(10, 10))
				}
			}
			// No temporary files are left next to the result.
			entries, _ := os.ReadDir(filepath.Dir(dst))
			if len(entries) != 1 {
				t.Errorf("cache folder holds %v", entries)
			}
		})
	}
}

// grayPNGHeader returns a 1×1 gray PNG whose IHDR claims w×h pixels; the
// size checks must happen before anything is decoded.
func grayPNGHeader(t *testing.T, w, h uint32) []byte {
	data := encodeImage(t, "png", image.NewGray(image.Rect(0, 0, 1, 1)))
	binary.BigEndian.PutUint32(data[16:], w)
	binary.BigEndian.PutUint32(data[20:], h)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return data
}

func TestThumbnailUnsupported(t *testing.T) {
	jpegData := encodeImage(t, "jpeg", gradient(64, 64))

	th := newThumbnailer()
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"avif":      {[]byte("\x00\x00\x00\x20ftypavif\x00\x00\x00\x00avifmif1"), errUnsupportedImage},
		"truncated": {jpegData[:len(jpegData)/2], errUnsupportedImage},
		"not image": {[]byte("hello"), errUnsupportedImage},
		"too large": {grayPNGHeader(t, 40_000, 40_000), errImageTooLarge},
		// 100 MP, but the scaler would need 32 bytes per source row and
		// destination column: about 3 GB for a file of a few hundred KB.
		"1 pixel wide": {grayPNGHeader(t, 1, 100_000_000), errImageTooLarge},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "img"), tc.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := th.generate(dir, "img", 320, filepath.Join(dir, "out.jpg")); err != tc.want {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestImageDimensions(t *testing.T) {
	for name, data := range map[string][]byte{
		"jpeg": encodeImage(t, "jpeg", gradient(33, 21)),
		"png":  encodeImage(t, "png", gradient(33, 21)),
		"bmp":  encodeImage(t, "bmp", gradient(33, 21)),
		"tiff": encodeImage(t, "tiff", gradient(33, 21)),
	} {
		f, err := os.CreateTemp(t.TempDir(), "img")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(f, bytes.NewReader(data))
		w, h := imageDimensions(f)
		f.Close()
		if w == nil || h == nil || *w != 33 || *h != 21 {
			t.Errorf("%s: dimensions %v x %v", name, w, h)
		}
	}
}

func TestRecoverDecoder(t *testing.T) {
	err := func() (err error) {
		defer recoverDecoder(&err)
		var m map[string]int
		m["x"]++ // panics
		return nil
	}()
	if !errors.Is(err, errDecoderPanic) {
		t.Errorf("err = %v, want errDecoderPanic", err)
	}
}

func TestThumbnailCost(t *testing.T) {
	// Line-scan images stay within the limit for both sizes.
	for _, perPixel := range []int64{1, 3, 4} { // gray, JPEG YCbCr, RGBA
		for _, side := range thumbSides {
			tw, th := fitSize(4096, 30000, side)
			if cost := thumbnailCost(4096, 30000, tw, th, 4096*30000*perPixel); cost > maxThumbnailCost {
				t.Errorf("4096x30000 at %d bytes per pixel, side %d: cost %d MiB", perPixel, side, cost>>20)
			}
		}
	}
	tw, th := fitSize(1, 100_000_000, 320)
	if cost := thumbnailCost(1, 100_000_000, tw, th, 100_000_000); cost <= maxThumbnailCost {
		t.Errorf("1x100000000: cost %d MiB is within the limit", cost>>20)
	}
	// No resizing needed: only the decoded image and the copy.
	if cost := thumbnailCost(100, 50, 100, 50, 100*50*4); cost != 2*100*50*4 {
		t.Errorf("unscaled cost = %d", cost)
	}

	for i, tc := range []struct {
		model color.Model
		want  int64
	}{
		{color.GrayModel, 1}, {color.Palette{color.Black, color.White}, 1}, {color.Gray16Model, 2},
		{color.YCbCrModel, 3}, {color.NRGBAModel, 4}, {color.CMYKModel, 4}, {color.RGBA64Model, 8}, {color.NRGBA64Model, 8},
	} {
		if got := decodedBytesPerPixel(tc.model); got != tc.want {
			t.Errorf("model %d: decodedBytesPerPixel = %d, want %d", i, got, tc.want)
		}
	}
}

// decodeCostOf returns decodeCost for an image file with the given content.
func decodeCostOf(t *testing.T, data []byte) int64 {
	t.Helper()
	p := filepath.Join(t.TempDir(), "img")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := decodeImageConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return decodeCost(f, format, cfg)
}

func TestJPEGDecodeCost(t *testing.T) {
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	progressive := read("progressive-420.jpg")
	// image/jpeg skips extraneous bytes before a marker, so must the
	// frame header reader.
	extraneous := append([]byte{0xff, 0xd8, 0x00, 0x00}, progressive[2:]...)

	for name, data := range map[string][]byte{
		"baseline 4:2:0":    encodeImage(t, "jpeg", gradient(640, 480)),
		"progressive 4:2:0": progressive,
		"extraneous bytes":  extraneous,
		"progressive gray":  read("progressive-gray.jpg"),
		"cmyk":              read("cmyk.jpg"),
	} {
		cost := decodeCostOf(t, data)
		// The estimate covers what the decoder actually allocates, and not
		// much more.
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runtime.ReadMemStats(&after)
		allocated := int64(after.TotalAlloc - before.TotalAlloc)
		if cost < allocated-64<<10 || cost > 2*allocated {
			t.Errorf("%s: cost %d KiB, decoder allocated %d KiB", name, cost>>10, allocated>>10)
		}
	}

	// Without a frame header image/jpeg could decode, the image counts as
	// too large.
	for name, data := range map[string][]byte{
		"truncated":     progressive[:6],
		"sos first":     {0xff, 0xd8, 0xff, 0xda, 0x00, 0x02},
		"arithmetic":    {0xff, 0xd8, 0xff, 0xc9, 0x00, 0x0b, 8, 0, 1, 0, 1, 1, 1, 0x11, 0},
		"bad sampling":  {0xff, 0xd8, 0xff, 0xc2, 0x00, 0x0b, 8, 0, 1, 0, 1, 1, 1, 0x50, 0},
		"wrong length":  {0xff, 0xd8, 0xff, 0xc0, 0x00, 0x0e, 8, 0, 1, 0, 1, 1, 1, 0x11, 0, 0, 0, 0},
		"not jpeg data": encodeImage(t, "png", gradient(4, 4)),
	} {
		if cost := jpegDecodeCost(bytes.NewReader(data), image.Config{Width: 1, Height: 1}); cost <= maxThumbnailCost {
			t.Errorf("%s: cost %d is within the limit", name, cost)
		}
	}
}

func TestPNGDecodeCost(t *testing.T) {
	data := encodeImage(t, "png", gradient(64, 32)) // opaque: RGB, decoded as RGBA
	if cost := decodeCostOf(t, data); cost != 64*32*4 {
		t.Errorf("cost = %d, want %d", cost, 64*32*4)
	}
	data[28] = 1 // interlaced
	binary.BigEndian.PutUint32(data[29:], crc32.ChecksumIEEE(data[12:29]))
	if cost := decodeCostOf(t, data); cost != 2*64*32*4 {
		t.Errorf("interlaced: cost = %d, want %d", cost, 2*64*32*4)
	}
}
