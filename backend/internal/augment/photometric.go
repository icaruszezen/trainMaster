package augment

import (
	"math"
	"math/rand/v2"
	"sync"
)

// The photometric steps work in place on the 8-bit pixels of the result
// (channels 1: gray; 4: R, G, B and an alpha of 255 they leave alone).

// brightnessContrast maps each color value v to
// (v-128)·factor + 128 + offset·255, rounded and clamped.
func brightnessContrast(pix []uint8, channels int, offset, factor float64) {
	var lut [256]uint8
	for v := range lut {
		lut[v] = clamp8(float64(v-128)*factor + 128 + offset*255)
	}
	if channels == 1 {
		for i, v := range pix {
			pix[i] = lut[v]
		}
		return
	}
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2] = lut[pix[i]], lut[pix[i+1]], lut[pix[i+2]]
	}
}

// gaussianBlur blurs the w×h image in pix with a gaussian kernel of sigma
// (radius ceil(3σ), edges clamped), separably and in place: a ring of the
// last 2r+1 source rows is kept, so the scratch memory is a few rows.
// Each result row is the vertical pass over the ring (all bytes of a
// pixel, in float32), then the horizontal pass over that.
func gaussianBlur(pix []uint8, w, h, channels int, sigma float64) {
	r := int(math.Ceil(3 * sigma))
	if r < 1 || w == 0 || h == 0 {
		return
	}
	kernel := make([]float32, r+1) // kernel[|k|], k = -r..r
	sum := 0.0
	for k := range kernel {
		v := math.Exp(-float64(k*k) / (2 * sigma * sigma))
		kernel[k] = float32(v)
		sum += v
		if k > 0 {
			sum += v
		}
	}
	for k := range kernel {
		kernel[k] = float32(float64(kernel[k]) / sum)
	}

	colors := min(channels, 3)
	rowLen := w * channels
	n := 2*r + 1
	ring := make([]uint8, n*rowLen)
	slot := func(j int) []uint8 {
		s := (j % n) * rowLen
		return ring[s : s+rowLen]
	}
	tmp := make([]float32, rowLen)
	loaded := -1
	for y := range h {
		for loaded < min(y+r, h-1) { // rows up to y+r, before row y is written
			loaded++
			copy(slot(loaded), pix[loaded*rowLen:(loaded+1)*rowLen])
		}

		c := kernel[0]
		for i, v := range slot(y) {
			tmp[i] = c * float32(v)
		}
		for k := 1; k <= r; k++ {
			c, up, down := kernel[k], slot(max(y-k, 0)), slot(min(y+k, h-1))
			for i := range tmp {
				tmp[i] += c * (float32(up[i]) + float32(down[i]))
			}
		}

		dst := pix[y*rowLen : (y+1)*rowLen]
		for x := range w {
			interior := x >= r && x+r < w
			for ch := range colors {
				i := x*channels + ch
				s := kernel[0] * tmp[i]
				if interior {
					for k := 1; k <= r; k++ {
						d := k * channels
						s += kernel[k] * (tmp[i-d] + tmp[i+d])
					}
				} else {
					for k := 1; k <= r; k++ {
						left, right := max(x-k, 0), min(x+k, w-1)
						s += kernel[k] * (tmp[left*channels+ch] + tmp[right*channels+ch])
					}
				}
				dst[i] = clamp8f(s)
			}
		}
	}
}

// addNoise adds gaussian noise of sigma (in 0..255 units) to each color
// value, independently, from a generator seeded by seed.
func addNoise(pix []uint8, channels int, sigma float64, seed uint32) {
	table := gaussianTable()
	src := rand.NewPCG(uint64(seed), noiseStream)
	colors := min(channels, 3)
	sg := float32(sigma)
	var bits uint64
	left := 0
	for i := 0; i+channels <= len(pix); i += channels {
		for ch := range colors {
			if left == 0 {
				bits, left = src.Uint64(), 4
			}
			z := table[uint16(bits)]
			bits >>= 16
			left--
			pix[i+ch] = clamp8f(float32(pix[i+ch]) + sg*z)
		}
	}
}

// noiseStream is the second seed word of the noise generator.
const noiseStream = 0x6e6f697365 // "noise"

// gaussianTable holds the standard normal quantiles of (i+0.5)/65536:
// indexing it with 16 uniform bits gives a normal sample (exact to the
// table's resolution, tails cut at about ±4.3σ), four from each output of
// the generator.
var gaussianTable = sync.OnceValue(func() *[1 << 16]float32 {
	var t [1 << 16]float32
	for i := range t {
		p := (float64(i) + 0.5) / (1 << 16)
		t[i] = float32(math.Sqrt2 * math.Erfinv(2*p-1))
	}
	return &t
})

func clamp8(f float64) uint8 {
	switch {
	case !(f > 0): // NaN too
		return 0
	case f >= 254.5:
		return 255
	}
	return uint8(f + 0.5)
}

func clamp8f(f float32) uint8 {
	switch {
	case !(f > 0):
		return 0
	case f >= 254.5:
		return 255
	}
	return uint8(f + 0.5)
}
