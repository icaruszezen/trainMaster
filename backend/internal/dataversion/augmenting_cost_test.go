package dataversion

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestAugmentCost(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 100, 50))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "gray.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// A gray PNG may decode as NRGBA (a transparent color): 4 + 8 per pixel.
	if got := augmentCost(f, "png", image.Config{ColorModel: color.GrayModel, Width: 100, Height: 50}); got < 12*5000 {
		t.Errorf("gray PNG cost = %d", got)
	}
	for _, size := range [][2]int{{1 << 15, 1 << 15}, {math.MaxInt32, math.MaxInt32}} {
		cfg := image.Config{ColorModel: color.NRGBA64Model, Width: size[0], Height: size[1]}
		if got := augmentCost(f, "png", cfg); got <= maxAugmentMemory {
			t.Errorf("%v: cost = %d", size, got)
		}
	}
}
