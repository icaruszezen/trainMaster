package augment

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// methodsError is an error of NormalizeMethods: a Chinese message of kind
// ErrInvalidMethods.
type methodsError struct{ msg string }

func (e *methodsError) Error() string { return e.msg }
func (e *methodsError) Unwrap() error { return ErrInvalidMethods }

func normalizeMethods(raw []string) ([]Method, error) {
	if len(raw) == 0 {
		return nil, &methodsError{"请至少选择一种增强方式"}
	}
	seen := make([]bool, len(Methods))
	for _, s := range raw {
		i := slices.Index(Methods, Method(s))
		if i < 0 {
			return nil, &methodsError{fmt.Sprintf("未知的增强方式“%s”", displayValue(s))}
		}
		seen[i] = true
	}
	var out []Method
	for i, m := range Methods {
		if seen[i] {
			out = append(out, m)
		}
	}
	return out, nil
}

// displayValue makes a value from a request safe to show in a message:
// valid UTF-8 without control characters, at most 40 characters.
func displayValue(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40]) + "…"
	}
	return s
}

func maxCopies(methods []Method) int {
	for _, m := range methods {
		if _, geometric := generator(m); !geometric && slices.Contains(Methods, m) {
			return CopiesLimit
		}
	}
	return len(subgroup(methods)) - 1
}

// newPlan implements NewPlan (n is the copy number).
//
// Randomness comes from two generators, both seeded from sha256 of the
// inputs: one from (seed, key) shuffles the orientations, so that all the
// copies of an image see the same order and take distinct elements; one
// from (seed, key, n) decides the photometric steps and draws every
// parameter. The values are taken from the PCG generator's raw output
// (not from rand.Rand helpers), so plans do not change with the Go
// version.
func newPlan(seed int64, key string, n int, methods []Method, minBoxSide int) Plan {
	if n < 1 {
		return Plan{}
	}
	var photo []Method // the photometric methods this image allows, in Methods order
	for _, m := range Methods {
		if Geometric(m) || !slices.Contains(methods, m) ||
			m == Blur && minBoxSide > 0 && minBoxSide < MinBlurBoxSide {
			continue
		}
		photo = append(photo, m)
	}

	group := subgroup(methods)
	shuffler := newPRNG("orientation", strconv.FormatInt(seed, 10), key)
	others := slices.Clone(group[1:]) // the non-identity elements
	shuffler.shuffle(others)
	var o orient
	force := false // a photometric step is needed to make the copy distinct
	if n <= len(others) {
		o = others[n-1]
	} else {
		if len(photo) == 0 {
			return Plan{}
		}
		// The orientations repeat from here on: cycle through the whole
		// group (identity included) in a second shuffled order and make
		// the copy distinct by its photometric steps.
		all := slices.Clone(group)
		shuffler.shuffle(all)
		o = all[(n-len(others)-1)%len(all)]
		force = true
	}

	rng := newPRNG("copy", strconv.FormatInt(seed, 10), key, strconv.Itoa(n))
	applied := make([]bool, len(photo))
	some := false
	for i := range photo {
		applied[i] = rng.coin()
		some = some || applied[i]
	}
	if force && !some {
		applied[rng.intn(len(photo))] = true
	}

	steps := represent(methods)[o].steps()
	for i, m := range photo {
		if applied[i] {
			steps = append(steps, Step{Method: m, Params: rng.params(m)})
		}
	}
	return Plan{Steps: steps}
}

// Parameter ranges of the photometric methods.
const (
	maxBrightness = 0.2  // offset, fraction of 255
	maxContrast   = 0.2  // factor 1 ± this, around 128
	minChange     = 0.05 // at least one of offset and factor-1 changes this much
	minBlurSigma  = 0.3
	maxBlurSigma  = 1.0
	minNoiseSigma = 2.0 // of 255
	maxNoiseSigma = 6.0
)

// params draws the parameters of a photometric step.
func (r *prng) params(m Method) []float64 {
	switch m {
	case BrightnessContrast:
		for range 1000 { // a draw is rejected with probability 1/16
			offset := r.uniform(-maxBrightness, maxBrightness)
			factor := r.uniform(1-maxContrast, 1+maxContrast)
			if math.Abs(offset) >= minChange || math.Abs(factor-1) >= minChange {
				return []float64{offset, factor}
			}
		}
		return []float64{2 * minChange, 1}
	case Blur:
		return []float64{r.uniform(minBlurSigma, maxBlurSigma)}
	case Noise:
		sigma := r.uniform(minNoiseSigma, maxNoiseSigma)
		return []float64{sigma, float64(uint32(r.src.Uint64() >> 32))}
	}
	return nil
}

// prng draws the random values of plans from a PCG generator.
type prng struct{ src *rand.PCG }

// newPRNG seeds a generator from sha256 of the parts (separated by NUL,
// which neither a number nor a path contains).
func newPRNG(parts ...string) *prng {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return &prng{rand.NewPCG(binary.BigEndian.Uint64(h[:8]), binary.BigEndian.Uint64(h[8:16]))}
}

// float64 is uniform in [0, 1).
func (r *prng) float64() float64 { return float64(r.src.Uint64()>>11) / (1 << 53) }

func (r *prng) uniform(lo, hi float64) float64 { return lo + (hi-lo)*r.float64() }

func (r *prng) coin() bool { return r.src.Uint64()>>63 == 1 }

// intn is uniform in [0, n) (the bias of at most n/2^64 is irrelevant
// here).
func (r *prng) intn(n int) int {
	hi, _ := bits.Mul64(r.src.Uint64(), uint64(n))
	return int(hi)
}

// shuffle permutes s (Fisher–Yates).
func (r *prng) shuffle(s []orient) {
	for i := len(s) - 1; i > 0; i-- {
		j := r.intn(i + 1)
		s[i], s[j] = s[j], s[i]
	}
}
