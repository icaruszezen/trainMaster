package augment

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeMethods(t *testing.T) {
	tests := []struct {
		raw     []string
		want    []Method
		wantErr string
	}{
		{raw: nil, wantErr: "请至少选择一种增强方式"},
		{raw: []string{}, wantErr: "请至少选择一种增强方式"},
		{raw: []string{"hflip"}, want: []Method{HFlip}},
		{raw: []string{"noise", "hflip", "noise"}, want: []Method{HFlip, Noise}},
		{
			raw:  []string{"noise", "blur", "brightness_contrast", "rotate90", "vflip", "hflip"},
			want: Methods,
		},
		{raw: []string{"hflip", "crop"}, wantErr: "未知的增强方式“crop”"},
		{raw: []string{"HFlip"}, wantErr: "未知的增强方式“HFlip”"},
		{raw: []string{" hflip"}, wantErr: "未知的增强方式“ hflip”"},
		{raw: []string{""}, wantErr: "未知的增强方式“”"},
		{raw: []string{"a\nb"}, wantErr: "未知的增强方式“a b”"},
		{raw: []string{strings.Repeat("长", 50)}, wantErr: "未知的增强方式“" + strings.Repeat("长", 40) + "…”"},
	}
	for _, tt := range tests {
		got, err := NormalizeMethods(tt.raw)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr || !errors.Is(err, ErrInvalidMethods) {
				t.Errorf("NormalizeMethods(%q) = %v, %v; want error %q wrapping ErrInvalidMethods", tt.raw, got, err, tt.wantErr)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("NormalizeMethods(%q) = %v, %v; want %v", tt.raw, got, err, tt.want)
		}
	}
}

func TestMaxCopies(t *testing.T) {
	tests := []struct {
		methods []Method
		want    int
	}{
		{nil, 0},
		{[]Method{HFlip}, 1},
		{[]Method{VFlip}, 1},
		{[]Method{HFlip, VFlip}, 3},
		{[]Method{Rotate90}, 3},
		{[]Method{HFlip, Rotate90}, 7},
		{[]Method{VFlip, Rotate90}, 7},
		{[]Method{HFlip, VFlip, Rotate90}, 7},
		{[]Method{BrightnessContrast}, CopiesLimit},
		{[]Method{Blur}, CopiesLimit},
		{[]Method{Noise}, CopiesLimit},
		{[]Method{HFlip, Noise}, CopiesLimit},
		{Methods, CopiesLimit},
	}
	for _, tt := range tests {
		if got := MaxCopies(tt.methods); got != tt.want {
			t.Errorf("MaxCopies(%v) = %d, want %d", tt.methods, got, tt.want)
		}
	}
}

// orientOfSteps composes the geometric steps of a plan.
func orientOfSteps(steps []Step) orient {
	m := matI
	for _, s := range steps {
		switch s.Method {
		case Rotate90:
			for range quarterTurns(s.Params) {
				m = matR.mul(m)
			}
		case HFlip:
			m = matH.mul(m)
		case VFlip:
			m = matV.mul(m)
		}
	}
	return orientOf(m)
}

func TestGroup(t *testing.T) {
	// The 8 matrices are distinct and closed under composition.
	seen := map[mat2]bool{}
	for _, m := range orientMats {
		seen[m] = true
	}
	if len(seen) != 8 {
		t.Fatalf("orientMats has %d distinct matrices", len(seen))
	}
	for a := range orient(8) {
		for b := range orient(8) {
			_ = a.then(b) // panics when not closed
		}
		if a.then(identity) != a || identity.then(a) != a {
			t.Errorf("identity is not neutral for %d", a)
		}
	}
	r, h, v := orientOf(matR), orientOf(matH), orientOf(matV)
	r180 := r.then(r)
	if h.then(v) != r180 {
		t.Errorf("hflip then vflip is not a half turn")
	}
	if r.then(r).then(r).then(r) != identity || h.then(h) != identity {
		t.Errorf("wrong orders")
	}

	set := func(os ...orient) []orient { slices.Sort(os); return os }
	tests := []struct {
		methods []Method
		want    []orient
	}{
		{nil, set(identity)},
		{[]Method{Noise}, set(identity)},
		{[]Method{HFlip}, set(identity, h)},
		{[]Method{VFlip, Blur}, set(identity, v)},
		{[]Method{HFlip, VFlip}, set(identity, h, v, r180)},
		{[]Method{Rotate90}, set(identity, r, r180, r180.then(r))},
		{[]Method{HFlip, Rotate90}, set(0, 1, 2, 3, 4, 5, 6, 7)},
		{[]Method{VFlip, Rotate90}, set(0, 1, 2, 3, 4, 5, 6, 7)},
	}
	for _, tt := range tests {
		if got := subgroup(tt.methods); !slices.Equal(got, tt.want) {
			t.Errorf("subgroup(%v) = %v, want %v", tt.methods, got, tt.want)
		}
	}
}

// geometricSubsets are all non-empty sets of geometric methods.
var geometricSubsets = [][]Method{
	{HFlip}, {VFlip}, {Rotate90}, {HFlip, VFlip}, {HFlip, Rotate90}, {VFlip, Rotate90}, {HFlip, VFlip, Rotate90},
}

func TestRepresent(t *testing.T) {
	for _, methods := range geometricSubsets {
		group := subgroup(methods)
		reps := represent(methods)
		if len(reps) != len(group) {
			t.Errorf("%v: %d representations for %d elements", methods, len(reps), len(group))
		}
		for _, o := range group {
			steps := reps[o].steps()
			if got := orientOfSteps(steps); got != o {
				t.Errorf("%v: steps %v of %d give %d", methods, steps, o, got)
			}
			if (o == identity) != (len(steps) == 0) {
				t.Errorf("%v: steps of %d: %v", methods, o, steps)
			}
			for _, s := range steps {
				if !slices.Contains(methods, s.Method) {
					t.Errorf("%v: steps of %d use %s", methods, o, s.Method)
				}
			}
		}
	}
	r180 := orientOf(matR.mul(matR))
	if got := represent([]Method{HFlip, VFlip})[r180]; got != (geoSteps{h: true, v: true}) {
		t.Errorf("half turn with flips = %+v", got)
	}
	if got := represent([]Method{HFlip, VFlip, Rotate90})[r180]; got != (geoSteps{turns: 2}) {
		t.Errorf("half turn with rotate90 = %+v", got)
	}
}

// methodSubsets are all non-empty sets of methods, normalized.
func methodSubsets() [][]Method {
	var out [][]Method
	for mask := 1; mask < 1<<len(Methods); mask++ {
		var ms []Method
		for i, m := range Methods {
			if mask&(1<<i) != 0 {
				ms = append(ms, m)
			}
		}
		out = append(out, ms)
	}
	return out
}

func photometricSteps(p Plan) []Step {
	return slices.DeleteFunc(slices.Clone(p.Steps), func(s Step) bool { return Geometric(s.Method) })
}

func TestNewPlanDeterministic(t *testing.T) {
	for _, methods := range methodSubsets() {
		for _, key := range []string{"images/a/0001.jpg", "images/b/板材 2.png"} {
			for c := 1; c <= MaxCopies(methods); c++ {
				a, b := NewPlan(42, key, c, methods, 0), NewPlan(42, key, c, slices.Clone(methods), 0)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("%v %s %d: %v != %v", methods, key, c, a, b)
				}
			}
		}
	}
}

func TestNewPlanVaries(t *testing.T) {
	plans := map[string]bool{}
	for i := range 100 {
		plans[fmt.Sprint(NewPlan(42, fmt.Sprintf("images/%d.jpg", i), 1, Methods, 0))] = true
	}
	// A copy without photometric steps (1 in 8) has one of 7 orientations.
	if len(plans) < 85 {
		t.Errorf("only %d distinct plans for 100 keys", len(plans))
	}
	plans = map[string]bool{}
	for seed := range int64(100) {
		plans[fmt.Sprint(NewPlan(seed, "images/0.jpg", 1, Methods, 0))] = true
	}
	if len(plans) < 85 {
		t.Errorf("only %d distinct plans for 100 seeds", len(plans))
	}
	// Geometric only: the order of the orientations depends on the key.
	orders := map[string]bool{}
	for i := range 20 {
		var order []orient
		for c := 1; c <= 7; c++ {
			order = append(order, orientOfSteps(NewPlan(1, fmt.Sprint(i), c, []Method{HFlip, Rotate90}, 0).Steps))
		}
		orders[fmt.Sprint(order)] = true
	}
	if len(orders) < 15 {
		t.Errorf("only %d orders for 20 keys", len(orders))
	}
}

// TestNewPlanCopies checks for every method set and copy: the steps are
// canonical and of the selected methods, blur is left out with small
// boxes, a copy is never the original and the copies of one key differ.
func TestNewPlanCopies(t *testing.T) {
	for _, methods := range methodSubsets() {
		group := subgroup(methods)
		for _, minBox := range []int{0, 3, 7, 8, 100} {
			blurOK := minBox == 0 || minBox >= MinBlurBoxSide
			photoOK := slices.ContainsFunc(methods, func(m Method) bool {
				return m == BrightnessContrast || m == Noise || m == Blur && blurOK
			})
			for k := range 20 {
				key := fmt.Sprintf("images/%d.jpg", k)
				var plans []Plan
				seen := map[orient]int{}
				for c := 1; c <= MaxCopies(methods); c++ {
					p := NewPlan(7, key, c, methods, minBox)
					name := fmt.Sprintf("%v box %d %s copy %d: %v", methods, minBox, key, c, p)
					if len(p.Steps) == 0 {
						if c <= len(group)-1 || photoOK {
							t.Fatalf("%s: empty", name)
						}
						continue
					}
					if c > len(group)-1 && !photoOK {
						t.Fatalf("%s: want empty", name)
					}
					checkCanonical(t, name, p, methods)
					o := orientOfSteps(p.Steps)
					photo := photometricSteps(p)
					if o == identity && len(photo) == 0 {
						t.Fatalf("%s: equals the original", name)
					}
					if c > len(group)-1 && len(photo) == 0 {
						t.Fatalf("%s: repeats an orientation without a photometric step", name)
					}
					if c <= len(group)-1 {
						seen[o]++
					}
					for _, s := range photo {
						if s.Method == Blur && !blurOK {
							t.Fatalf("%s: blur with boxes of %d px", name, minBox)
						}
					}
					for _, q := range plans {
						if reflect.DeepEqual(p, q) {
							t.Fatalf("%s: repeats %v", name, q)
						}
					}
					plans = append(plans, p)
				}
				// The first copies take distinct non-identity orientations
				// (all of them when there are enough copies).
				if len(seen) != min(len(group)-1, MaxCopies(methods)) {
					t.Fatalf("%v %s: orientations %v, group %v", methods, key, seen, group)
				}
				for o, n := range seen {
					if o == identity || n != 1 {
						t.Fatalf("%v %s: orientations %v", methods, key, seen)
					}
				}
			}
		}
	}
}

// checkCanonical checks the order of the steps (rotate90, hflip, vflip,
// then photometric in Methods order, each at most once), that they are
// of methods, that the geometric ones are the representation of their
// element, and the parameters.
func checkCanonical(t *testing.T, name string, p Plan, methods []Method) {
	t.Helper()
	order := []Method{Rotate90, HFlip, VFlip, BrightnessContrast, Blur, Noise}
	last := -1
	var geo []Step
	for _, s := range p.Steps {
		i := slices.Index(order, s.Method)
		if i <= last || !slices.Contains(methods, s.Method) {
			t.Fatalf("%s: steps not canonical", name)
		}
		last = i
		if Geometric(s.Method) {
			geo = append(geo, s)
		}
		checkParams(t, name, s)
	}
	if want := represent(methods)[orientOfSteps(geo)].steps(); !reflect.DeepEqual(geo, want) {
		t.Fatalf("%s: geometric steps %v, canonical %v", name, geo, want)
	}
}

func checkParams(t *testing.T, name string, s Step) {
	t.Helper()
	ps := s.Params
	in := func(v, lo, hi float64) bool { return v >= lo && v <= hi }
	ok := true
	switch s.Method {
	case Rotate90:
		ok = len(ps) == 1 && (ps[0] == 1 || ps[0] == 2 || ps[0] == 3)
	case HFlip, VFlip:
		ok = len(ps) == 0
	case BrightnessContrast:
		ok = len(ps) == 2 && in(ps[0], -0.2, 0.2) && in(ps[1], 0.8, 1.2) &&
			(math.Abs(ps[0]) >= 0.05 || math.Abs(ps[1]-1) >= 0.05)
	case Blur:
		ok = len(ps) == 1 && in(ps[0], 0.3, 1.0)
	case Noise:
		ok = len(ps) == 2 && in(ps[0], 2, 6) && in(ps[1], 0, math.MaxUint32) && ps[1] == math.Trunc(ps[1])
	}
	if !ok {
		t.Fatalf("%s: params of %s: %v", name, s.Method, ps)
	}
}

func TestNewPlanEmpty(t *testing.T) {
	tests := []struct {
		methods []Method
		minBox  int
		copy    int
		empty   bool
	}{
		{[]Method{Blur}, 3, 1, true},
		{[]Method{Blur}, 3, 5, true},
		{[]Method{Blur}, 0, 1, false},
		{[]Method{Blur}, 8, 1, false},
		{[]Method{HFlip, Blur}, 3, 1, false},
		{[]Method{HFlip, Blur}, 3, 2, true},
		{[]Method{HFlip, Blur}, 0, 2, false},
		{[]Method{HFlip}, 0, 2, true}, // beyond MaxCopies
		{[]Method{Noise}, 0, 0, true},
		{[]Method{HFlip}, 0, -1, true},
		{nil, 0, 1, true},
	}
	for _, tt := range tests {
		p := NewPlan(1, "k", tt.copy, tt.methods, tt.minBox)
		if (len(p.Steps) == 0) != tt.empty {
			t.Errorf("NewPlan(%v, copy %d, box %d) = %v, want empty %v", tt.methods, tt.copy, tt.minBox, p, tt.empty)
		}
	}
	if got := NewPlan(1, "k", 1, []Method{HFlip, Blur}, 3); !reflect.DeepEqual(got, Plan{Steps: []Step{{Method: HFlip}}}) {
		t.Errorf("hflip with small boxes: %v", got)
	}
}

// TestNewPlanProbability: each photometric method is applied to about
// half of the copies that need none.
func TestNewPlanProbability(t *testing.T) {
	const n = 4000
	counts := map[Method]int{}
	for i := range n {
		p := NewPlan(3, fmt.Sprint(i), 1, []Method{HFlip, BrightnessContrast, Blur, Noise}, 0)
		for _, s := range photometricSteps(p) {
			counts[s.Method]++
		}
	}
	for _, m := range []Method{BrightnessContrast, Blur, Noise} {
		if f := float64(counts[m]) / n; f < 0.45 || f > 0.55 {
			t.Errorf("%s applied to %.3f of the copies", m, f)
		}
	}
}

// TestNewPlanStable pins one plan, so that a change of the derivation
// (which would change rebuilt versions) is noticed.
func TestNewPlanStable(t *testing.T) {
	got := fmt.Sprint(NewPlan(42, "images/line_a/NG/0001.jpg", 2, Methods, 0))
	const want = "{[{vflip []} {blur [0.5050148404763692]}]}"
	if got != want {
		t.Errorf("plan = %s, want %s", got, want)
	}
}
