package augment

import (
	"image"
	"math"
	"testing"
)

func TestLimitedRange(t *testing.T) {
	// Black and white in limited range (Y 16 and 235), 4:2:0 like VP8.
	ycc := image.NewYCbCr(image.Rect(0, 0, 4, 2), image.YCbCrSubsampleRatio420)
	for y := range 2 {
		for x := range 4 {
			ycc.Y[ycc.YOffset(x, y)] = []uint8{16, 16, 235, 235}[x]
		}
	}
	for i := range ycc.Cb {
		ycc.Cb[i], ycc.Cr[i] = 128, 128
	}
	for _, src := range []image.Image{ycc, &image.NYCbCrA{YCbCr: *ycc, A: make([]uint8, 8)}} {
		got, _ := Apply(LimitedRange(src), nil, Plan{Steps: []Step{{Method: HFlip}}})
		n := got.(*image.NRGBA)
		for x, want := range []uint8{255, 255, 0, 0} {
			if c := n.NRGBAAt(x, 1); c.R != want || c.G != want || c.B != want || c.A != 0xff {
				t.Errorf("%T pixel %d = %v, want %d", src, x, c, want)
			}
		}
	}

	// The fixed point matches the BT.601 formula within one level, and the
	// generic path (At) gives the same as the row converter.
	for _, v := range [][3]int{{16, 128, 128}, {120, 90, 200}, {200, 30, 60}, {81, 240, 110}} {
		r, g, b := limitedYCbCrToRGB(uint8(v[0]), uint8(v[1]), uint8(v[2]))
		y, cb, cr := 1.164*float64(v[0]-16), float64(v[1]-128), float64(v[2]-128)
		want := [3]float64{y + 1.596*cr, y - 0.391*cb - 0.813*cr, y + 2.018*cb}
		for i, got := range []uint8{r, g, b} {
			w := math.Round(min(max(want[i], 0), 255))
			if math.Abs(float64(got)-w) > 1 {
				t.Errorf("%v channel %d = %d, want %v", v, i, got, w)
			}
		}
	}
	got, _ := Apply(LimitedRange(ycc), nil, Plan{})
	want, _ := Apply(hide{LimitedRange(ycc)}, nil, Plan{})
	if !imagesClose(got, want, 0) {
		t.Error("the row converter differs from At")
	}
	if g := image.NewGray(image.Rect(0, 0, 1, 1)); LimitedRange(g) != image.Image(g) {
		t.Error("a gray image must be returned unchanged")
	}
}
