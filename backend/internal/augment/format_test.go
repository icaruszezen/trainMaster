package augment

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"sync"
	"testing"
)

func TestEncode(t *testing.T) {
	src := uniqueNRGBA(40, 30)
	color8, _ := Apply(src, nil, Plan{Steps: []Step{{Method: HFlip}}})
	gray8, _ := Apply(toGray(src), nil, Plan{})

	for _, img := range []image.Image{color8, gray8} {
		var buf bytes.Buffer
		if err := Encode(&buf, img, "png"); err != nil {
			t.Fatal(err)
		}
		dec, err := png.Decode(&buf)
		if err != nil {
			t.Fatal(err)
		}
		// png decodes an opaque NRGBA image as RGBA: compare the colors.
		for y := range 30 {
			for x := range 40 {
				r1, g1, b1, a1 := dec.At(x, y).RGBA()
				r2, g2, b2, a2 := img.At(x, y).RGBA()
				if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
					t.Fatalf("png %T: pixel (%d,%d) differs", img, x, y)
				}
			}
		}
		if _, gray := img.(*image.Gray); gray {
			if _, ok := dec.(*image.Gray); !ok {
				t.Errorf("gray png decodes as %T", dec)
			}
		}

		buf.Reset()
		if err := Encode(&buf, img, "jpeg"); err != nil {
			t.Fatal(err)
		}
		dec, err = jpeg.Decode(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if dec.Bounds() != img.Bounds() {
			t.Fatalf("jpeg bounds %v", dec.Bounds())
		}
		var diff, n float64
		for y := range 30 {
			for x := range 40 {
				r1, g1, b1, _ := dec.At(x, y).RGBA()
				r2, g2, b2, _ := img.At(x, y).RGBA()
				for _, d := range []float64{float64(r1) - float64(r2), float64(g1) - float64(g2), float64(b1) - float64(b2)} {
					diff += math.Abs(d) / 257
					n++
				}
			}
		}
		if diff/n > 6 {
			t.Errorf("jpeg %T: mean difference %.2f", img, diff/n)
		}
		if _, gray := img.(*image.Gray); gray {
			if _, ok := dec.(*image.Gray); !ok {
				t.Errorf("gray jpeg decodes as %T", dec)
			}
		}
	}

	// A translucent NRGBA image (not from Apply) still encodes.
	translucent := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := Encode(&buf, translucent, "jpeg"); err != nil {
		t.Error(err)
	}
	if err := Encode(&buf, color8, "webp"); err == nil {
		t.Error("unknown format accepted")
	}
}

// TestEncodeParallel shares the PNG buffers between goroutines (run with
// -race).
func TestEncodeParallel(t *testing.T) {
	img, _ := Apply(uniqueNRGBA(64, 48), nil, Plan{})
	var want bytes.Buffer
	if err := Encode(&want, img, "png"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				var buf bytes.Buffer
				if err := Encode(&buf, img, "png"); err != nil || !bytes.Equal(buf.Bytes(), want.Bytes()) {
					t.Error("parallel png encoding differs")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// riff builds a WebP file from chunks (fourcc and data).
func riff(chunks ...any) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for i := 0; i < len(chunks); i += 2 {
		data := chunks[i+1].([]byte)
		body.WriteString(chunks[i].(string))
		binary.Write(&body, binary.LittleEndian, uint32(len(data)))
		body.Write(data)
		if len(data)%2 == 1 {
			body.WriteByte(0)
		}
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestIsLosslessWebP(t *testing.T) {
	vp8x := make([]byte, 10)
	hugeChunk := append(riff("VP8X", vp8x), []byte("ICCP\xff\xff\xff\xff")...)
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"lossless", riff("VP8L", []byte{0x2f, 1, 2, 3, 4}), true},
		{"lossy", riff("VP8 ", []byte{1, 2, 3, 4, 5, 6}), false},
		{"extended lossless", riff("VP8X", vp8x, "ICCP", []byte{1, 2, 3}, "EXIF", []byte{4}, "VP8L", []byte{0x2f, 0}), true},
		{"extended lossy with alpha", riff("VP8X", vp8x, "ALPH", []byte{1, 2, 3}, "VP8 ", []byte{1, 2}, "VP8L", []byte{0}), false},
		{"extended without image", riff("VP8X", vp8x, "ICCP", []byte{1}), false},
		{"extended truncated", riff("VP8X", vp8x, "VP8L", []byte{0x2f, 0})[:30], false},
		{"huge chunk", hugeChunk, false},
		{"animation", riff("VP8X", vp8x, "ANIM", make([]byte, 6), "ANMF", append(make([]byte, 16), riff("VP8L", []byte{0})[12:]...)), false},
		{"not webp", []byte("RIFF\x10\x00\x00\x00WAVEfmt "), false},
		{"short", []byte("RIFF\x10\x00\x00\x00WEBPVP8"), false},
		{"empty", nil, false},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), false},
	}
	for _, tt := range tests {
		if got := IsLosslessWebP(bytes.NewReader(tt.data)); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestOutputFormat(t *testing.T) {
	tests := []struct {
		src      string
		lossless bool
		format   string
		ext      string
	}{
		{"jpeg", false, "jpeg", ".jpg"},
		{"webp", false, "jpeg", ".jpg"},
		{"webp", true, "png", ".png"},
		{"png", false, "png", ".png"},
		{"bmp", false, "png", ".png"},
		{"tiff", false, "png", ".png"},
		{"gif", false, "png", ".png"},
	}
	for _, tt := range tests {
		if f, e := OutputFormat(tt.src, tt.lossless); f != tt.format || e != tt.ext {
			t.Errorf("OutputFormat(%s, %v) = %s, %s", tt.src, tt.lossless, f, e)
		}
	}
}

func TestDecodedBytes(t *testing.T) {
	cfg := func(m color.Model, w, h int) image.Config { return image.Config{ColorModel: m, Width: w, Height: h} }
	tests := []struct {
		cfg  image.Config
		want int64
	}{
		{cfg(color.GrayModel, 10, 10), 100},
		{cfg(color.Gray16Model, 10, 10), 200},
		{cfg(color.Palette{color.Black}, 10, 10), 100},
		{cfg(color.YCbCrModel, 10, 10), 300},
		{cfg(color.CMYKModel, 10, 10), 400},
		{cfg(color.RGBAModel, 10, 10), 400},
		{cfg(color.NRGBAModel, 10, 10), 400},
		{cfg(color.RGBA64Model, 10, 10), 800},
		{cfg(color.NRGBA64Model, 10, 10), 800},
		{cfg(nil, 10, 10), 400},
		{cfg(color.NRGBAModel, 0, 10), 0},
		{cfg(color.NRGBAModel, -5, 10), 0},
		{cfg(color.GrayModel, 1<<30, 1<<30), 1 << 60},
		{cfg(color.NRGBA64Model, math.MaxInt32, math.MaxInt32), math.MaxInt64 / 4},
		{cfg(color.NRGBA64Model, math.MaxInt, math.MaxInt), math.MaxInt64 / 4},
		{cfg(color.NRGBA64Model, math.MaxInt/16, 2), math.MaxInt64 / 4},
	}
	for _, tt := range tests {
		got := DecodedBytes(tt.cfg)
		if got != tt.want {
			t.Errorf("DecodedBytes(%v %dx%d) = %d, want %d", tt.cfg.ColorModel, tt.cfg.Width, tt.cfg.Height, got, tt.want)
		}
		if got*3 < 0 {
			t.Errorf("3 × DecodedBytes overflows: %d", got)
		}
	}
}
