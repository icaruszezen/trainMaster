package augment

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

// allGeometric is a method set whose representations cover all of D4.
var allGeometric = []Method{HFlip, VFlip, Rotate90}

// orientPlan is the plan of element o alone.
func orientPlan(o orient) Plan { return Plan{Steps: represent(allGeometric)[o].steps()} }

// uniqueNRGBA is a w×h image whose pixels all differ.
func uniqueNRGBA(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x*7 + y*13), 0xff})
		}
	}
	return img
}

// TestApplyGeometry checks every element of D4 against the matrices on
// centered coordinates: source pixel (x, y) must land on (x', y') with
// (2x'+1-W', 2y'+1-H') = M·(2x+1-W, 2y+1-H).
func TestApplyGeometry(t *testing.T) {
	const w, h = 7, 4
	src := uniqueNRGBA(w, h)
	for o := range orient(8) {
		out, _ := Apply(src, nil, orientPlan(o))
		dst := out.(*image.NRGBA)
		m := orientMats[o]
		ow, oh := w, h
		if m[0] == 0 {
			ow, oh = h, w
		}
		if dst.Rect != image.Rect(0, 0, ow, oh) {
			t.Fatalf("orient %d: bounds %v, want %dx%d", o, dst.Rect, ow, oh)
		}
		for y := range h {
			for x := range w {
				cx, cy := 2*x+1-w, 2*y+1-h
				dx, dy := m[0]*cx+m[1]*cy, m[2]*cx+m[3]*cy
				x2, y2 := (dx+ow-1)/2, (dy+oh-1)/2
				if got, want := dst.NRGBAAt(x2, y2), src.NRGBAAt(x, y); got != want {
					t.Fatalf("orient %d: (%d,%d) → (%d,%d) is %v, want %v", o, x, y, x2, y2, got, want)
				}
			}
		}
	}
}

// TestApplyBoxes draws a colored rectangle with its box and checks for
// every element of D4 that the moved box covers exactly the colored
// pixels of the result.
func TestApplyBoxes(t *testing.T) {
	const w, h = 9, 6
	red := color.NRGBA{200, 10, 10, 0xff}
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	box := Box{Label: "scratch", Xmin: 1, Ymin: 2, Xmax: 4, Ymax: 6, Difficult: true}
	for y := box.Ymin; y < box.Ymax; y++ {
		for x := box.Xmin; x < box.Xmax; x++ {
			src.SetNRGBA(x, y, red)
		}
	}
	whole := Box{Label: "all", Xmin: 0, Ymin: 0, Xmax: w, Ymax: h, Truncated: true}
	for o := range orient(8) {
		boxes := []Box{box, whole}
		out, got := Apply(src, boxes, orientPlan(o))
		if !reflect.DeepEqual(boxes, []Box{box, whole}) {
			t.Fatalf("input boxes modified: %v", boxes)
		}
		dst := out.(*image.NRGBA)
		b := dst.Bounds()
		if len(got) != 2 {
			t.Fatalf("orient %d: boxes %v", o, got)
		}
		if got[1] != (Box{Label: "all", Xmin: 0, Ymin: 0, Xmax: b.Dx(), Ymax: b.Dy(), Truncated: true}) {
			t.Errorf("orient %d: whole-image box %+v in %v", o, got[1], b)
		}
		nb := got[0]
		if nb.Label != "scratch" || !nb.Difficult || nb.Truncated {
			t.Errorf("orient %d: box fields %+v", o, nb)
		}
		if (nb.Xmax-nb.Xmin)*(nb.Ymax-nb.Ymin) != 12 {
			t.Errorf("orient %d: box %+v has the wrong area", o, nb)
		}
		for y := range b.Dy() {
			for x := range b.Dx() {
				inBox := x >= nb.Xmin && x < nb.Xmax && y >= nb.Ymin && y < nb.Ymax
				if colored := dst.NRGBAAt(x, y) == red; colored != inBox {
					t.Fatalf("orient %d: pixel (%d,%d) colored %v, in box %+v %v", o, x, y, colored, nb, inBox)
				}
			}
		}
	}

	// Known values: hflip maps xmin, xmax to W-xmax, W-xmin; a quarter turn
	// maps (x, y) to (H-1-y, x).
	_, got := Apply(src, []Box{box}, Plan{Steps: []Step{{Method: HFlip}}})
	if want := (Box{Label: "scratch", Xmin: w - 4, Ymin: 2, Xmax: w - 1, Ymax: 6, Difficult: true}); got[0] != want {
		t.Errorf("hflip: %+v, want %+v", got[0], want)
	}
	_, got = Apply(src, []Box{box}, Plan{Steps: []Step{{Method: Rotate90, Params: []float64{1}}}})
	if want := (Box{Label: "scratch", Xmin: 0, Ymin: 1, Xmax: 4, Ymax: 4, Difficult: true}); got[0] != want {
		t.Errorf("rotate90: %+v, want %+v", got[0], want)
	}
}

func TestApplyBoxClipping(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 10, 8))
	boxes := []Box{
		{Label: "a", Xmin: -5, Ymin: 2, Xmax: 3, Ymax: 20}, // clipped
		{Label: "b", Xmin: 6, Ymin: 5, Xmax: 2, Ymax: 1},   // swapped
		{Label: "c", Xmin: 10, Ymin: 0, Xmax: 15, Ymax: 4}, // outside
		{Label: "d", Xmin: 3, Ymin: 3, Xmax: 3, Ymax: 5},   // no width
	}
	_, got := Apply(src, boxes, Plan{})
	want := []Box{{Label: "a", Xmin: 0, Ymin: 2, Xmax: 3, Ymax: 8}, {Label: "b", Xmin: 2, Ymin: 1, Xmax: 6, Ymax: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("boxes = %+v, want %+v", got, want)
	}
}

// TestApplyRotations: non-canonical steps compose too, and rotate90's
// parameter is taken modulo 4.
func TestApplyRotations(t *testing.T) {
	src := uniqueNRGBA(5, 3)
	rot := func(turns ...float64) Plan {
		var p Plan
		for _, n := range turns {
			p.Steps = append(p.Steps, Step{Method: Rotate90, Params: []float64{n}})
		}
		return p
	}
	same := func(a, b Plan) bool {
		x, _ := Apply(src, nil, a)
		y, _ := Apply(src, nil, b)
		return reflect.DeepEqual(x, y)
	}
	if !same(rot(1, 2), rot(3)) || !same(rot(5), rot(1)) || !same(rot(-1), rot(3)) || !same(rot(4), Plan{}) {
		t.Error("rotations do not compose")
	}
	if !same(Plan{Steps: []Step{{Method: Rotate90}}}, rot(1)) {
		t.Error("rotate90 without parameters is not a quarter turn")
	}
	if !same(Plan{Steps: []Step{{Method: HFlip}, {Method: VFlip}}}, rot(2)) {
		t.Error("hflip and vflip are not a half turn")
	}
}

func TestApplyKeepsSource(t *testing.T) {
	src := uniqueNRGBA(12, 9)
	src.Pix[3] = 0 // a transparent pixel
	before := slices.Clone(src.Pix)
	p := Plan{Steps: []Step{
		{Method: Rotate90, Params: []float64{1}}, {Method: HFlip},
		{Method: BrightnessContrast, Params: []float64{0.1, 1.1}},
		{Method: Blur, Params: []float64{1}}, {Method: Noise, Params: []float64{5, 9}},
	}}
	out, _ := Apply(src, nil, p)
	if !bytes.Equal(src.Pix, before) {
		t.Fatal("source modified")
	}
	dst := out.(*image.NRGBA)
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] != 0xff {
			t.Fatalf("alpha %d at %d", dst.Pix[i], i)
		}
	}
}

// identityOf applies the empty plan and checks the result type.
func identityOf[T image.Image](t *testing.T, src image.Image) T {
	t.Helper()
	out, _ := Apply(src, nil, Plan{})
	dst, ok := out.(T)
	if !ok {
		t.Fatalf("%T gives %T", src, out)
	}
	if out.Bounds() != image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()) {
		t.Fatalf("%T: bounds %v", src, out.Bounds())
	}
	return dst
}

func TestApplyGray(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(src.Pix, []uint8{1, 2, 3, 4, 5, 6})
	if got := identityOf[*image.Gray](t, src); !bytes.Equal(got.Pix, src.Pix) {
		t.Errorf("gray: %v", got.Pix)
	}
	out, _ := Apply(src, nil, Plan{Steps: []Step{{Method: Rotate90, Params: []float64{1}}}})
	if got := out.(*image.Gray); !bytes.Equal(got.Pix, []uint8{4, 1, 5, 2, 6, 3}) || got.Stride != 2 {
		t.Errorf("rotated gray: %v stride %d", got.Pix, got.Stride)
	}
	out, _ = Apply(src, nil, Plan{Steps: []Step{{Method: Noise, Params: []float64{4, 1}}, {Method: Blur, Params: []float64{0.5}}}})
	if _, ok := out.(*image.Gray); !ok {
		t.Errorf("gray with photometric steps gives %T", out)
	}

	g16 := image.NewGray16(image.Rect(0, 0, 2, 1))
	g16.SetGray16(0, 0, color.Gray16{0x12ff})
	g16.SetGray16(1, 0, color.Gray16{0xff00})
	if got := identityOf[*image.Gray](t, g16); !bytes.Equal(got.Pix, []uint8{0x12, 0xff}) {
		t.Errorf("gray16: %v", got.Pix)
	}
}

// TestApplyColorTypes checks the straight 8-bit color of each source
// type, transparent pixels included.
func TestApplyColorTypes(t *testing.T) {
	type px = [3]uint8
	check := func(name string, src image.Image, want []px) {
		t.Helper()
		got := identityOf[*image.NRGBA](t, src)
		tol := 0
		if name == "generic rgba" { // color.NRGBAModel truncates
			tol = 1
		}
		for i, w := range want {
			p := got.Pix[4*i : 4*i+4]
			off := false
			for c := range 3 {
				off = off || int(p[c]) < int(w[c])-tol || int(p[c]) > int(w[c])+tol
			}
			if off || p[3] != 0xff {
				t.Errorf("%s pixel %d: %v, want %v", name, i, p, w)
			}
		}
	}

	nrgba := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	nrgba.SetNRGBA(0, 0, color.NRGBA{10, 20, 30, 0})
	nrgba.SetNRGBA(1, 0, color.NRGBA{100, 150, 200, 128})
	nrgba.SetNRGBA(2, 0, color.NRGBA{1, 2, 3, 255})
	check("nrgba", nrgba, []px{{10, 20, 30}, {100, 150, 200}, {1, 2, 3}})

	n64 := image.NewNRGBA64(image.Rect(0, 0, 2, 1))
	n64.SetNRGBA64(0, 0, color.NRGBA64{0x1234, 0x5678, 0x9abc, 0})
	n64.SetNRGBA64(1, 0, color.NRGBA64{0xffff, 0x00ff, 0x8000, 0x4000})
	check("nrgba64", n64, []px{{0x12, 0x56, 0x9a}, {0xff, 0x00, 0x80}})

	rgba := image.NewRGBA(image.Rect(0, 0, 3, 1))
	rgba.SetRGBA(0, 0, color.RGBA{50, 25, 0, 128}) // straight 100, 50, 0
	rgba.SetRGBA(1, 0, color.RGBA{0, 0, 0, 0})
	rgba.SetRGBA(2, 0, color.RGBA{7, 8, 9, 255})
	check("rgba", rgba, []px{{100, 50, 0}, {0, 0, 0}, {7, 8, 9}})

	r64 := image.NewRGBA64(image.Rect(0, 0, 2, 1))
	r64.SetRGBA64(0, 0, color.RGBA64{0x4000, 0x2000, 0, 0x8000}) // straight 0x7fff, 0x3fff, 0
	r64.SetRGBA64(1, 0, color.RGBA64{0xabcd, 0x1234, 0xffff, 0xffff})
	check("rgba64", r64, []px{{0x7f, 0x3f, 0}, {0xab, 0x12, 0xff}})

	pal := image.NewPaletted(image.Rect(0, 0, 4, 1), color.Palette{
		color.NRGBA{1, 2, 3, 0}, color.RGBA{10, 20, 30, 255}, color.Gray{77},
	})
	copy(pal.Pix, []uint8{0, 1, 2, 5}) // 5: beyond the palette
	check("paletted", pal, []px{{1, 2, 3}, {10, 20, 30}, {77, 77, 77}, {0, 0, 0}})

	cmyk := image.NewCMYK(image.Rect(0, 0, 2, 1))
	cmyk.SetCMYK(0, 0, color.CMYK{0, 255, 255, 0})
	cmyk.SetCMYK(1, 0, color.CMYK{10, 20, 30, 40})
	r, g, b := color.CMYKToRGB(10, 20, 30, 40)
	check("cmyk", cmyk, []px{{255, 0, 0}, {r, g, b}})

	nycc := image.NewNYCbCrA(image.Rect(0, 0, 1, 1), image.YCbCrSubsampleRatio444)
	nycc.Y[0], nycc.Cb[0], nycc.Cr[0], nycc.A[0] = 120, 90, 200, 0
	r, g, b = color.YCbCrToRGB(120, 90, 200)
	check("nycbcra", nycc, []px{{r, g, b}})

	// Other types go through At and keep straight colors too.
	check("generic nrgba", hide{nrgba}, []px{{10, 20, 30}, {100, 150, 200}, {1, 2, 3}})
	check("generic rgba", hide{rgba}, []px{{100, 50, 0}, {0, 0, 0}, {7, 8, 9}})
	if got := identityOf[*image.Gray](t, hide{image.NewGray16(image.Rect(0, 0, 1, 1))}); len(got.Pix) != 1 {
		t.Errorf("generic gray16: %v", got.Pix)
	}
}

// hide hides the concrete type of an image.
type hide struct{ image.Image }

// TestApplySubImage reads sources that do not start at (0, 0).
func TestApplySubImage(t *testing.T) {
	full := uniqueNRGBA(10, 8)
	r := image.Rect(3, 2, 8, 7)
	subs := []image.Image{
		full.SubImage(r),
		toRGBA(full).SubImage(r),
		toGray(full).SubImage(r),
		toYCbCr(full, image.YCbCrSubsampleRatio420).SubImage(r),
	}
	for _, sub := range subs {
		for o := range orient(8) {
			got, _ := Apply(sub, nil, orientPlan(o))
			want, _ := Apply(hide{sub}, nil, orientPlan(o)) // the generic path
			if !imagesClose(got, want, 0) {
				t.Errorf("%T sub-image, orient %d differs from the generic path", sub, o)
			}
		}
	}
}

func TestApplyYCbCr(t *testing.T) {
	src := uniqueNRGBA(13, 9)
	for _, ratio := range []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420,
		image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410,
	} {
		ycc := toYCbCr(src, ratio)
		for o := range orient(8) {
			got, _ := Apply(ycc, nil, orientPlan(o))
			want, _ := Apply(hide{ycc}, nil, orientPlan(o))
			// The generic path converts from 16-bit RGBA: off by one at most.
			if !imagesClose(got, want, 1) {
				t.Errorf("%v orient %d differs from the generic path", ratio, o)
			}
		}
	}

	// A decoded JPEG.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	ycc, ok := dec.(*image.YCbCr)
	if !ok {
		t.Fatalf("decoded %T", dec)
	}
	out, _ := Apply(ycc, nil, orientPlan(orientOf(matR)))
	dst := out.(*image.NRGBA)
	b := ycc.Bounds()
	for y := range b.Dy() {
		for x := range b.Dx() {
			c := ycc.YCbCrAt(x, y)
			r, g, bl := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			if got := dst.NRGBAAt(b.Dy()-1-y, x); got != (color.NRGBA{r, g, bl, 0xff}) {
				t.Fatalf("pixel (%d,%d): %v, want %v", x, y, got, color.NRGBA{r, g, bl, 0xff})
			}
		}
	}
}

func toRGBA(src *image.NRGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	for y := range src.Bounds().Dy() {
		for x := range src.Bounds().Dx() {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

func toGray(src *image.NRGBA) *image.Gray {
	dst := image.NewGray(src.Bounds())
	for y := range src.Bounds().Dy() {
		for x := range src.Bounds().Dx() {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

func toYCbCr(src *image.NRGBA, ratio image.YCbCrSubsampleRatio) *image.YCbCr {
	b := src.Bounds()
	dst := image.NewYCbCr(b, ratio)
	for y := range b.Dy() {
		for x := range b.Dx() {
			c := src.NRGBAAt(x, y)
			yy, cb, cr := color.RGBToYCbCr(c.R, c.G, c.B)
			dst.Y[dst.YOffset(x, y)] = yy
			dst.Cb[dst.COffset(x, y)] = cb
			dst.Cr[dst.COffset(x, y)] = cr
		}
	}
	return dst
}

// imagesClose reports whether a and b have the same type, bounds and
// pixels within tol.
func imagesClose(a, b image.Image, tol int) bool {
	pa, pb := pixOf(a), pixOf(b)
	if reflect.TypeOf(a) != reflect.TypeOf(b) || a.Bounds() != b.Bounds() || len(pa) != len(pb) {
		return false
	}
	for i := range pa {
		if d := int(pa[i]) - int(pb[i]); d > tol || d < -tol {
			return false
		}
	}
	return true
}

func pixOf(img image.Image) []uint8 {
	switch m := img.(type) {
	case *image.Gray:
		return m.Pix
	case *image.NRGBA:
		return m.Pix
	}
	panic(fmt.Sprintf("pixOf %T", img))
}

func TestApplyEmptyImage(t *testing.T) {
	for _, src := range []image.Image{image.NewNRGBA(image.Rect(0, 0, 0, 0)), image.NewGray(image.Rect(0, 0, 5, 0))} {
		out, boxes := Apply(src, []Box{{Xmin: 0, Ymin: 0, Xmax: 1, Ymax: 1}}, Plan{Steps: []Step{
			{Method: Rotate90, Params: []float64{1}}, {Method: Blur, Params: []float64{1}}, {Method: Noise, Params: []float64{3, 1}},
		}})
		if !out.Bounds().Empty() || len(boxes) != 0 {
			t.Errorf("%v: %v, %v", src.Bounds(), out.Bounds(), boxes)
		}
	}
}

func TestBrightnessContrast(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	copy(src.Pix, []uint8{128, 0, 255, 7, 100, 200, 50, 0, 1, 2, 3, 255, 127, 129, 64, 9})
	out, _ := Apply(src, nil, Plan{Steps: []Step{{Method: BrightnessContrast, Params: []float64{0.1, 1.2}}}})
	f := func(v float64) uint8 { return uint8(min(max(math.Round((v-128)*1.2+128+25.5), 0), 255)) }
	got := out.(*image.NRGBA).Pix
	for i, v := range src.Pix {
		want := f(float64(v))
		if i%4 == 3 {
			want = 0xff
		}
		if got[i] != want {
			t.Errorf("value %d (%d): %d, want %d", i, v, got[i], want)
		}
	}
	if got[0] != 154 || got[1] != 0 || got[2] != 255 {
		t.Errorf("known values: %v", got[:3])
	}
}

// blurReference is a plain 2D gaussian blur with clamped edges in float64.
func blurReference(pix []uint8, w, h, channels int, sigma float64) []uint8 {
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	sum := 0.0
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		sum += k[i]
	}
	out := slices.Clone(pix)
	for y := range h {
		for x := range w {
			for ch := range min(channels, 3) {
				s := 0.0
				for dy := -r; dy <= r; dy++ {
					for dx := -r; dx <= r; dx++ {
						yy, xx := min(max(y+dy, 0), h-1), min(max(x+dx, 0), w-1)
						s += k[dy+r] * k[dx+r] * float64(pix[(yy*w+xx)*channels+ch])
					}
				}
				out[(y*w+x)*channels+ch] = uint8(math.Round(s / (sum * sum)))
			}
		}
	}
	return out
}

func TestBlur(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, size := range [][2]int{{1, 1}, {2, 3}, {3, 2}, {5, 1}, {1, 9}, {7, 7}, {16, 11}} {
		for _, gray := range []bool{true, false} {
			for _, sigma := range []float64{0.3, 0.6, 1.0, 2.5} {
				w, h := size[0], size[1]
				var src image.Image
				var pix []uint8
				channels := 1
				if gray {
					g := image.NewGray(image.Rect(0, 0, w, h))
					pix = g.Pix
					src = g
				} else {
					n := image.NewNRGBA(image.Rect(0, 0, w, h))
					pix = n.Pix
					src = n
					channels = 4
				}
				for i := range pix {
					pix[i] = uint8(rng.IntN(256))
					if channels == 4 && i%4 == 3 {
						pix[i] = 0xff
					}
				}
				want := blurReference(pix, w, h, channels, sigma)
				out, _ := Apply(src, nil, Plan{Steps: []Step{{Method: Blur, Params: []float64{sigma}}}})
				got := pixOf(out)
				for i := range want {
					if d := int(got[i]) - int(want[i]); d > 1 || d < -1 {
						t.Fatalf("%dx%d gray %v sigma %v: value %d is %d, want %d", w, h, gray, sigma, i, got[i], want[i])
					}
				}
			}
		}
	}

	// A uniform image stays uniform.
	g := image.NewGray(image.Rect(0, 0, 20, 20))
	for i := range g.Pix {
		g.Pix[i] = 99
	}
	out, _ := Apply(g, nil, Plan{Steps: []Step{{Method: Blur, Params: []float64{1}}}})
	for _, v := range out.(*image.Gray).Pix {
		if v != 99 {
			t.Fatalf("uniform image blurred to %d", v)
		}
	}
}

func TestNoise(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	for i := range src.Pix {
		src.Pix[i] = 128
	}
	plan := func(sigma, seed float64) Plan {
		return Plan{Steps: []Step{{Method: Noise, Params: []float64{sigma, seed}}}}
	}
	a, _ := Apply(src, nil, plan(4, 12345))
	b, _ := Apply(src, nil, plan(4, 12345))
	c, _ := Apply(src, nil, plan(4, 12346))
	if !reflect.DeepEqual(a, b) {
		t.Error("noise is not deterministic")
	}
	if reflect.DeepEqual(a, c) {
		t.Error("noise does not depend on the seed")
	}
	pix := a.(*image.NRGBA).Pix
	var sum, sq float64
	n := 0
	for i, v := range pix {
		if i%4 == 3 {
			if v != 0xff {
				t.Fatalf("alpha %d", v)
			}
			continue
		}
		d := float64(v) - 128
		sum += d
		sq += d * d
		n++
	}
	mean, std := sum/float64(n), math.Sqrt(sq/float64(n))
	if math.Abs(mean) > 0.1 || math.Abs(std-4) > 0.2 {
		t.Errorf("noise mean %.3f, std %.3f; want 0, 4", mean, std)
	}
	// The channels are independent.
	var rg float64
	for i := 0; i < len(pix); i += 4 {
		rg += (float64(pix[i]) - 128) * (float64(pix[i+1]) - 128)
	}
	if corr := rg / float64(len(pix)/4) / 16; math.Abs(corr) > 0.05 {
		t.Errorf("R and G noise correlate: %.3f", corr)
	}

	g := image.NewGray(image.Rect(0, 0, 10, 10))
	out, _ := Apply(g, nil, plan(6, 1))
	if _, ok := out.(*image.Gray); !ok {
		t.Errorf("gray with noise gives %T", out)
	}
}

// TestApplyPhotometric: photometric steps keep the size and the boxes;
// invalid parameters are ignored.
func TestApplyPhotometric(t *testing.T) {
	src := uniqueNRGBA(30, 20)
	boxes := []Box{{Label: "a", Xmin: 2, Ymin: 3, Xmax: 9, Ymax: 15}}
	for _, p := range []Plan{
		{Steps: []Step{{Method: BrightnessContrast, Params: []float64{-0.1, 0.9}}}},
		{Steps: []Step{{Method: Blur, Params: []float64{0.7}}}},
		{Steps: []Step{{Method: Noise, Params: []float64{3, 7}}}},
		NewPlan(5, "x", 3, []Method{BrightnessContrast, Blur, Noise}, 0),
	} {
		out, got := Apply(src, boxes, p)
		if out.Bounds() != src.Bounds() || !reflect.DeepEqual(got, boxes) {
			t.Errorf("%v: bounds %v, boxes %v", p, out.Bounds(), got)
		}
		if same, _ := Apply(src, nil, Plan{}); reflect.DeepEqual(out, same) {
			t.Errorf("%v: no change", p)
		}
	}
	identical, _ := Apply(src, nil, Plan{})
	for _, s := range []Step{
		{Method: BrightnessContrast}, {Method: BrightnessContrast, Params: []float64{math.NaN(), 1}},
		{Method: Blur}, {Method: Blur, Params: []float64{-1}}, {Method: Blur, Params: []float64{math.Inf(1)}},
		{Method: Noise, Params: []float64{3}}, {Method: Noise, Params: []float64{0, 1}},
		{Method: "unknown", Params: []float64{1}},
	} {
		if out, _ := Apply(src, nil, Plan{Steps: []Step{s}}); !reflect.DeepEqual(out, identical) {
			t.Errorf("step %v changed the image", s)
		}
	}
}

// TestApplyPlansDiffer: the copies NewPlan makes are different images.
func TestApplyPlansDiffer(t *testing.T) {
	src := uniqueNRGBA(6, 4)
	original, _ := Apply(src, nil, Plan{})
	for _, methods := range [][]Method{{HFlip, VFlip, Rotate90}, {HFlip, Noise}, {Rotate90, BrightnessContrast}} {
		images := []image.Image{original}
		for c := 1; c <= min(MaxCopies(methods), CopiesLimit); c++ {
			out, _ := Apply(src, nil, NewPlan(11, "k", c, methods, 0))
			for _, prev := range images {
				if reflect.DeepEqual(out, prev) {
					t.Errorf("%v copy %d repeats an image", methods, c)
				}
			}
			images = append(images, out)
		}
	}
}

func BenchmarkApply(b *testing.B) {
	const side = 2000
	src := uniqueNRGBA(side, side)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatal(err)
	}
	ycc, err := jpeg.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		b.Fatal(err)
	}
	gray := toGray(src)
	rot := Step{Method: Rotate90, Params: []float64{1}}
	for _, bc := range []struct {
		name string
		src  image.Image
		p    Plan
	}{
		{"nrgba/rotate", src, Plan{Steps: []Step{rot}}},
		{"ycbcr/rotate", ycc, Plan{Steps: []Step{rot}}},
		{"ycbcr/hflip", ycc, Plan{Steps: []Step{{Method: HFlip}}}},
		{"nrgba/brightness", src, Plan{Steps: []Step{{Method: BrightnessContrast, Params: []float64{0.1, 1.1}}}}},
		{"nrgba/blur1", src, Plan{Steps: []Step{{Method: Blur, Params: []float64{1}}}}},
		{"nrgba/noise", src, Plan{Steps: []Step{{Method: Noise, Params: []float64{4, 1}}}}},
		{"gray/rotate+blur", gray, Plan{Steps: []Step{rot, {Method: Blur, Params: []float64{1}}}}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			for b.Loop() {
				Apply(bc.src, nil, bc.p)
			}
		})
	}
}
