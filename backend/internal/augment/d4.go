package augment

import "slices"

// The geometric methods act on the pixel grid through the dihedral group
// D4: the 8 symmetries of a rectangle under flips and right-angle turns.
// An element is written orient(rot | flip<<2): turn clockwise rot quarter
// turns, then flip horizontally when flip is set. Every element has
// exactly one such form, but a plan may only use the methods the user
// selected, so its steps use the representation of the element in those
// methods (see represent).
type orient uint8

const identity orient = 0

// mat2 is the matrix of an element on centered pixel coordinates (y
// pointing down): x' = m[0]·x + m[1]·y, y' = m[2]·x + m[3]·y.
type mat2 [4]int

var (
	matI = mat2{1, 0, 0, 1}
	matR = mat2{0, -1, 1, 0} // a clockwise quarter turn
	matH = mat2{-1, 0, 0, 1} // horizontal flip (mirror left / right)
	matV = mat2{1, 0, 0, -1} // vertical flip (mirror top / bottom)
)

// mul returns a·b: b applied first, then a.
func (a mat2) mul(b mat2) mat2 {
	return mat2{
		a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3],
	}
}

// orientMats are the matrices of the 8 elements, by orient value.
var orientMats = func() (ms [8]mat2) {
	for o := range ms {
		m := matI
		for range o & 3 {
			m = matR.mul(m)
		}
		if o&4 != 0 {
			m = matH.mul(m)
		}
		ms[o] = m
	}
	return ms
}()

// orientOf is the element whose matrix is m (a product of the matrices
// above).
func orientOf(m mat2) orient {
	for o, om := range orientMats {
		if om == m {
			return orient(o)
		}
	}
	panic("augment: not an element of D4")
}

// then returns the element "o, then p".
func (o orient) then(p orient) orient { return orientOf(orientMats[p].mul(orientMats[o])) }

// generator is the element a geometric method applies (with rotate90:
// one quarter turn); ok is false for photometric methods.
func generator(m Method) (o orient, ok bool) {
	switch m {
	case HFlip:
		return orientOf(matH), true
	case VFlip:
		return orientOf(matV), true
	case Rotate90:
		return orientOf(matR), true
	}
	return 0, false
}

// subgroup is the subgroup of D4 the geometric methods of methods
// generate, in ascending order (so identity first).
func subgroup(methods []Method) []orient {
	var gens []orient
	for _, m := range methods {
		if g, ok := generator(m); ok {
			gens = append(gens, g)
		}
	}
	in := [8]bool{identity: true}
	group := []orient{identity}
	for i := 0; i < len(group); i++ { // closure under the generators
		for _, g := range gens {
			if p := group[i].then(g); !in[p] {
				in[p] = true
				group = append(group, p)
			}
		}
	}
	slices.Sort(group)
	return group
}

// geoSteps is an element written in the geometric methods: turns
// clockwise quarter turns (rotate90), then hflip, then vflip.
type geoSteps struct {
	turns int
	h, v  bool
}

func (g geoSteps) count() int {
	n := 0
	if g.turns > 0 {
		n++
	}
	if g.h {
		n++
	}
	if g.v {
		n++
	}
	return n
}

func (g geoSteps) orient() orient {
	m := matI
	for range g.turns {
		m = matR.mul(m)
	}
	if g.h {
		m = matH.mul(m)
	}
	if g.v {
		m = matV.mul(m)
	}
	return orientOf(m)
}

// represent writes every element of the subgroup methods generate in
// those methods: the representation with the fewest steps, then the
// fewest quarter turns (each element has one such, so equal elements
// always give equal steps; e.g. 180° is "rotate90 ×2" when rotate90 is
// selected and "hflip, vflip" otherwise).
func represent(methods []Method) map[orient]geoSteps {
	turns, hs, vs := []int{0}, []bool{false}, []bool{false}
	for _, m := range methods {
		switch m {
		case Rotate90:
			turns = []int{0, 1, 2, 3}
		case HFlip:
			hs = []bool{false, true}
		case VFlip:
			vs = []bool{false, true}
		}
	}
	reps := map[orient]geoSteps{}
	for _, t := range turns {
		for _, h := range hs {
			for _, v := range vs {
				g := geoSteps{t, h, v}
				o := g.orient()
				if old, ok := reps[o]; !ok || g.count() < old.count() ||
					g.count() == old.count() && g.turns < old.turns {
					reps[o] = g
				}
			}
		}
	}
	return reps
}

// steps returns the plan steps of g.
func (g geoSteps) steps() []Step {
	var steps []Step
	if g.turns > 0 {
		steps = append(steps, Step{Method: Rotate90, Params: []float64{float64(g.turns)}})
	}
	if g.h {
		steps = append(steps, Step{Method: HFlip})
	}
	if g.v {
		steps = append(steps, Step{Method: VFlip})
	}
	return steps
}

// pixelMap maps the pixels of a w0×h0 source to the result of a sequence
// of geometric steps: source pixel (x, y) (relative to the source's top
// left) goes to (m[0]·x + m[1]·y + t[0], m[2]·x + m[3]·y + t[1]) of a
// w×h result.
type pixelMap struct {
	m    mat2
	t    [2]int
	w, h int
}

func identityMap(w, h int) pixelMap { return pixelMap{m: matI, w: w, h: h} }

// rotate appends a clockwise quarter turn: (x, y) → (h-1-y, x), and the
// sides swap.
func (p pixelMap) rotate() pixelMap {
	return pixelMap{
		m: mat2{-p.m[2], -p.m[3], p.m[0], p.m[1]},
		t: [2]int{p.h - 1 - p.t[1], p.t[0]},
		w: p.h, h: p.w,
	}
}

// hflip appends a horizontal flip: (x, y) → (w-1-x, y).
func (p pixelMap) hflip() pixelMap {
	p.m[0], p.m[1] = -p.m[0], -p.m[1]
	p.t[0] = p.w - 1 - p.t[0]
	return p
}

// vflip appends a vertical flip: (x, y) → (x, h-1-y).
func (p pixelMap) vflip() pixelMap {
	p.m[2], p.m[3] = -p.m[2], -p.m[3]
	p.t[1] = p.h - 1 - p.t[1]
	return p
}

func (p pixelMap) point(x, y int) (int, int) {
	return p.m[0]*x + p.m[1]*y + p.t[0], p.m[2]*x + p.m[3]*y + p.t[1]
}

// box maps a box in edge coordinates (it covers the pixels xmin..xmax-1,
// ymin..ymax-1) by mapping its first and last pixel; b must have area.
func (p pixelMap) box(b Box) Box {
	x1, y1 := p.point(b.Xmin, b.Ymin)
	x2, y2 := p.point(b.Xmax-1, b.Ymax-1)
	b.Xmin, b.Xmax = min(x1, x2), max(x1, x2)+1
	b.Ymin, b.Ymax = min(y1, y2), max(y1, y2)+1
	return b
}
