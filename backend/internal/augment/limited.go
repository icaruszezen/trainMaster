package augment

import (
	"image"
	"image/color"
)

// LimitedRange returns img with its YCbCr samples read as BT.601 limited
// range (Y 16..235), the way libwebp, and with it the training tools,
// reads lossy WebP. golang.org/x/image/webp returns such an image as an
// *image.YCbCr (or *image.NYCbCrA), whose own conversion (image/color) is
// full range (JFIF, as in JPEG): copies made from it would have lifted
// blacks and dimmed whites. Other images are returned unchanged; the alpha
// plane of an NYCbCrA is dropped, as Apply drops it anyway.
func LimitedRange(img image.Image) image.Image {
	switch s := img.(type) {
	case *image.YCbCr:
		return &limitedYCbCr{s}
	case *image.NYCbCrA:
		return &limitedYCbCr{&s.YCbCr}
	}
	return img
}

// limitedYCbCr is an *image.YCbCr read as limited range.
type limitedYCbCr struct{ m *image.YCbCr }

func (l *limitedYCbCr) ColorModel() color.Model { return color.RGBAModel }
func (l *limitedYCbCr) Bounds() image.Rectangle { return l.m.Rect }

func (l *limitedYCbCr) At(x, y int) color.Color {
	if !(image.Point{X: x, Y: y}.In(l.m.Rect)) {
		return color.RGBA{}
	}
	yi, ci := l.m.YOffset(x, y), l.m.COffset(x, y)
	r, g, b := limitedYCbCrToRGB(l.m.Y[yi], l.m.Cb[ci], l.m.Cr[ci])
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

// limitedYCbCrToRGB is libwebp's VP8YuvToRgb (14-bit fixed point,
// R = 1.164 (Y-16) + 1.596 (Cr-128), ...).
func limitedYCbCrToRGB(y, cb, cr uint8) (r, g, b uint8) {
	mult := func(v uint8, c int) int { return int(v) * c >> 8 }
	clip := func(v int) uint8 {
		switch {
		case v < 0:
			return 0
		case v >= 256<<6:
			return 0xff
		}
		return uint8(v >> 6)
	}
	yy := mult(y, 19077)
	return clip(yy + mult(cr, 26149) - 14234),
		clip(yy - mult(cb, 6419) - mult(cr, 13320) + 8708),
		clip(yy + mult(cb, 33050) - 17685)
}
