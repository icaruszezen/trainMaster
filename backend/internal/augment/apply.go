package augment

import (
	"image"
	"image/color"
	"math"
)

// apply implements Apply. The geometric steps are combined into one pixel
// map and applied first, together with the conversion to 8 bits, in one
// pass over the source; the photometric steps then work in place on the
// result, in plan order. Box coordinates are edges (a box covers the
// pixels xmin..xmax-1, its width is xmax-xmin, as in the VOC files and
// the YOLO labels the platform writes), so e.g. a horizontal flip maps
// xmin, xmax to W-xmax, W-xmin.
func apply(src image.Image, boxes []Box, p Plan) (image.Image, []Box) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	g := identityMap(w, h)
	for _, s := range p.Steps {
		switch s.Method {
		case Rotate90:
			for range quarterTurns(s.Params) {
				g = g.rotate()
			}
		case HFlip:
			g = g.hflip()
		case VFlip:
			g = g.vflip()
		}
	}

	gray := isGray(src)
	channels := 4
	if gray {
		channels = 1
	}
	stride := g.w * channels
	pix := make([]uint8, stride*g.h)
	remap(src, g, pix, stride, channels, gray)

	for _, s := range p.Steps {
		switch s.Method {
		case BrightnessContrast:
			if len(s.Params) >= 2 && finite(s.Params[0]) && finite(s.Params[1]) {
				brightnessContrast(pix, channels, s.Params[0], s.Params[1])
			}
		case Blur:
			if len(s.Params) >= 1 && s.Params[0] > 0 && s.Params[0] <= maxSigma {
				gaussianBlur(pix, g.w, g.h, channels, s.Params[0])
			}
		case Noise:
			if len(s.Params) >= 2 && s.Params[0] > 0 && s.Params[0] <= 255 {
				addNoise(pix, channels, s.Params[0], uint32(s.Params[1]))
			}
		}
	}

	out := make([]Box, 0, len(boxes))
	for _, bx := range boxes {
		bx, ok := clipBox(bx, w, h)
		if !ok {
			continue
		}
		if bx, ok = clipBox(g.box(bx), g.w, g.h); ok {
			out = append(out, bx)
		}
	}

	rect := image.Rect(0, 0, g.w, g.h)
	if gray {
		return &image.Gray{Pix: pix, Stride: stride, Rect: rect}, out
	}
	return &image.NRGBA{Pix: pix, Stride: stride, Rect: rect}, out
}

// maxSigma bounds the blur sigma Apply accepts (NewPlan draws at most
// maxBlurSigma); it keeps the kernel small for a plan from elsewhere.
const maxSigma = 20

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// quarterTurns is the number of clockwise quarter turns (0..3) of a
// rotate90 step; one without parameters turns once.
func quarterTurns(params []float64) int {
	if len(params) == 0 {
		return 1
	}
	if !finite(params[0]) {
		return 0
	}
	return int(math.Mod(math.Mod(math.Round(params[0]), 4)+4, 4))
}

// clipBox orders the coordinates of b and clips it to a w×h image; ok is
// false when nothing is left.
func clipBox(b Box, w, h int) (Box, bool) {
	b.Xmin, b.Xmax = min(b.Xmin, b.Xmax), max(b.Xmin, b.Xmax)
	b.Ymin, b.Ymax = min(b.Ymin, b.Ymax), max(b.Ymin, b.Ymax)
	b.Xmin, b.Xmax = min(max(b.Xmin, 0), w), min(max(b.Xmax, 0), w)
	b.Ymin, b.Ymax = min(max(b.Ymin, 0), h), min(max(b.Ymax, 0), h)
	return b, b.Xmin < b.Xmax && b.Ymin < b.Ymax
}

// isGray reports whether the result of src is grayscale.
func isGray(src image.Image) bool {
	m := src.ColorModel()
	return m == color.GrayModel || m == color.Gray16Model
}

// remap writes src, converted to 8 bits (1 channel when gray, else 4 with
// A = 255) and moved by g, into pix. Rows are converted one at a time; a
// source row lands on a line of the result whose pixels are a fixed
// number of bytes apart.
func remap(src image.Image, g pixelMap, pix []uint8, stride, channels int, gray bool) {
	b := src.Bounds()
	w := b.Dx()
	if w == 0 || b.Dy() == 0 {
		return
	}
	conv := rowConverter(src, gray)
	row := make([]uint8, w*channels)
	step := g.m[2]*stride + g.m[0]*channels
	for j := range b.Dy() {
		x0, y0 := g.point(0, j)
		off := y0*stride + x0*channels
		if step == channels { // a row of the result, in order
			conv(b.Min.Y+j, pix[off:off+len(row)])
			continue
		}
		conv(b.Min.Y+j, row)
		if channels == 1 {
			for _, v := range row {
				pix[off] = v
				off += step
			}
			continue
		}
		for i := 0; i < len(row); i += 4 {
			copy(pix[off:off+4], row[i:i+4])
			off += step
		}
	}
}

// rowConverter returns a function writing the source row y converted to 8
// bits into row: gray values when gray, else straight (not premultiplied)
// R, G, B and A = 255. The usual image types read their pixel buffers
// directly.
func rowConverter(src image.Image, gray bool) func(y int, row []uint8) {
	b := src.Bounds()
	minX, w := b.Min.X, b.Dx()
	if gray {
		switch s := src.(type) {
		case *image.Gray:
			return func(y int, row []uint8) {
				i := s.PixOffset(minX, y)
				copy(row, s.Pix[i:i+w])
			}
		case *image.Gray16:
			return func(y int, row []uint8) {
				p := s.Pix[s.PixOffset(minX, y):]
				for x := range row {
					row[x] = p[2*x] // high byte
				}
			}
		}
		return func(y int, row []uint8) {
			for x := range row {
				row[x] = grayOf(src.At(minX+x, y))
			}
		}
	}

	switch s := src.(type) {
	case *image.NRGBA:
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+1], row[i+2], row[i+3] = p[i], p[i+1], p[i+2], 0xff
			}
		}
	case *image.NRGBA64:
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+1], row[i+2], row[i+3] = p[2*i], p[2*i+2], p[2*i+4], 0xff
			}
		}
	case *image.RGBA:
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for i := 0; i < len(row); i += 4 {
				r, g, b, a := p[i], p[i+1], p[i+2], p[i+3]
				if a != 0xff && a != 0 {
					r, g, b = unpremultiply8(r, a), unpremultiply8(g, a), unpremultiply8(b, a)
				}
				row[i], row[i+1], row[i+2], row[i+3] = r, g, b, 0xff
			}
		}
	case *image.RGBA64:
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for i := 0; i < len(row); i += 4 {
				q := p[2*i : 2*i+8]
				a := uint32(q[6])<<8 | uint32(q[7])
				for c := range 3 {
					v := uint32(q[2*c])<<8 | uint32(q[2*c+1])
					if a != 0xffff && a != 0 {
						v = min(v*0xffff/a, 0xffff)
					}
					row[i+c] = uint8(v >> 8)
				}
				row[i+3] = 0xff
			}
		}
	case *image.YCbCr:
		return yCbCrConverter(s, minX, color.YCbCrToRGB)
	case *image.NYCbCrA: // non-premultiplied: the alpha plane is dropped
		return yCbCrConverter(&s.YCbCr, minX, color.YCbCrToRGB)
	case *limitedYCbCr:
		return yCbCrConverter(s.m, minX, limitedYCbCrToRGB)
	case *image.CMYK:
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+1], row[i+2] = color.CMYKToRGB(p[i], p[i+1], p[i+2], p[i+3])
				row[i+3] = 0xff
			}
		}
	case *image.Paletted:
		var table [256][4]uint8 // indexes beyond the palette are black
		for i := range table {
			table[i][3] = 0xff
		}
		for i, c := range s.Palette[:min(len(s.Palette), 256)] {
			table[i][0], table[i][1], table[i][2] = straightRGB(c)
		}
		return func(y int, row []uint8) {
			p := s.Pix[s.PixOffset(minX, y):]
			for x := range w {
				copy(row[4*x:4*x+4], table[p[x]][:])
			}
		}
	}
	return func(y int, row []uint8) {
		for x := range w {
			row[4*x], row[4*x+1], row[4*x+2] = straightRGB(src.At(minX+x, y))
			row[4*x+3] = 0xff
		}
	}
}

func yCbCrConverter(s *image.YCbCr, minX int, toRGB func(y, cb, cr uint8) (r, g, b uint8)) func(y int, row []uint8) {
	return func(y int, row []uint8) {
		for x := range len(row) / 4 {
			yi, ci := s.YOffset(minX+x, y), s.COffset(minX+x, y)
			row[4*x], row[4*x+1], row[4*x+2] = toRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
			row[4*x+3] = 0xff
		}
	}
}

// unpremultiply8 is the straight value of the 8-bit premultiplied value v
// with alpha a (0 < a < 255).
func unpremultiply8(v, a uint8) uint8 {
	return uint8(min((uint32(v)*0xff+uint32(a)/2)/uint32(a), 0xff))
}

// straightRGB is the 8-bit straight (not premultiplied) color of c; the
// non-premultiplied types keep their color where they are transparent.
func straightRGB(c color.Color) (r, g, b uint8) {
	switch c := c.(type) {
	case nil:
		return 0, 0, 0
	case color.NRGBA:
		return c.R, c.G, c.B
	case color.NRGBA64:
		return uint8(c.R >> 8), uint8(c.G >> 8), uint8(c.B >> 8)
	case color.NYCbCrA:
		return color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
	}
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return n.R, n.G, n.B
}

func grayOf(c color.Color) uint8 {
	if c == nil {
		return 0
	}
	return color.GrayModel.Convert(c).(color.Gray).Y
}
