// Package augment generates augmented copies of training images for data
// versions (数据版本): flips and right-angle rotations (one element of the
// dihedral group D4 per copy, so box coordinates stay exact) and mild
// photometric changes (brightness / contrast, blur, noise), with the
// bounding boxes transformed to match. It is pure (no database, no
// files): the caller decodes the image, applies a Plan and encodes the
// result. It also finds and resets the EXIF / TIFF orientation tag of
// image files (orientation.go).
//
// Plans are deterministic: the same seed, key and copy index always give
// the same steps and parameters, so a data version built again from the
// same images gives the same augmented images. The copies of one image
// never repeat each other and never equal the original.
package augment

import (
	"errors"
	"image"
	"io"
)

// Method is an augmentation method; the values are the API values.
type Method string

// The methods, in display order.
const (
	HFlip              Method = "hflip"               // 水平翻转
	VFlip              Method = "vflip"               // 垂直翻转
	Rotate90           Method = "rotate90"            // 旋转 90°（90° / 180° / 270°）
	BrightnessContrast Method = "brightness_contrast" // 亮度 / 对比度（±20 %）
	Blur               Method = "blur"                // 轻微模糊（σ 0.3–1.0）
	Noise              Method = "noise"               // 高斯噪声（σ 2–6）
)

// Methods lists every method in display order.
var Methods = []Method{HFlip, VFlip, Rotate90, BrightnessContrast, Blur, Noise}

// Geometric reports whether m changes the geometry (the boxes move).
func Geometric(m Method) bool { return m == HFlip || m == VFlip || m == Rotate90 }

// CopiesLimit bounds the augmented copies of one image whatever the
// methods (see MaxCopies).
const CopiesLimit = 5

// MinBlurBoxSide: blur is not applied to an image whose smallest box has a
// side shorter than this many pixels (it would erase small defects while
// their box stays).
const MinBlurBoxSide = 8

// ErrInvalidMethods is the kind of the errors of NormalizeMethods; the
// error message is Chinese and shown to users.
var ErrInvalidMethods = errors.New("invalid augmentation methods")

// NormalizeMethods validates the methods of a configuration: at least one,
// each known; the result is de-duplicated and in Methods order. Errors
// wrap ErrInvalidMethods and have a Chinese message.
func NormalizeMethods(raw []string) ([]Method, error) { return normalizeMethods(raw) }

// MaxCopies is how many distinct copies of one image methods can make:
// with geometric methods only, the number of non-identity orientations
// they generate (hflip or vflip: 1; both: 3; rotate90: 3; rotate90 with a
// flip: 7); with any photometric method, CopiesLimit. methods must be
// normalized.
func MaxCopies(methods []Method) int { return maxCopies(methods) }

// Box is a bounding box in whole pixels of the image it belongs to
// (0 <= Xmin < Xmax <= width, same for y). The coordinates are pixel
// edges, as in the platform's VOC files and YOLO labels: the box covers
// the pixels Xmin..Xmax-1 and its width is Xmax-Xmin.
type Box struct {
	Label                  string
	Xmin, Ymin, Xmax, Ymax int
	Difficult, Truncated   bool
}

// Step is one transformation of a Plan with its random parameters.
type Step struct {
	Method Method
	// Params are drawn when the plan is made; their meaning depends on the
	// method (rotate90: quarter turns 1..3; brightness_contrast: the two
	// factors; blur: sigma; noise: sigma and a noise seed). Flips have
	// none.
	Params []float64
}

// Plan is the transformations of one augmented copy, in the order they
// are applied: the geometric steps (at most one rotate90, then hflip,
// then vflip — together one D4 element), then the photometric ones.
type Plan struct {
	Steps []Step
}

// Methods returns the method ids the plan applies, in order (for
// data_version_images.augment).
func (p Plan) Methods() []string {
	out := make([]string, len(p.Steps))
	for i, s := range p.Steps {
		out[i] = string(s.Method)
	}
	return out
}

// NewPlan draws the plan of copy number copy (1-based, at most
// MaxCopies(methods)) of the image named key (its path in the data
// version). The copies 1..n of one key use distinct non-identity
// orientations of the subgroup the geometric methods generate (in an
// order shuffled by seed and key) while there are any; each photometric
// method is applied with probability 1/2, and one is forced when the copy
// would otherwise equal the original. minBoxSide is the smallest side of
// the image's boxes (0 when it has none): blur is left out below
// MinBlurBoxSide. methods must be normalized.
//
// The plan has no steps when the copy cannot be made different from the
// original and the other copies: copy is beyond the distinct orientations
// and no photometric method is allowed (methods {blur} with boxes under
// MinBlurBoxSide: every copy; {hflip, blur} with such boxes: copies 2 and
// up), or copy < 1. The caller does not write such a copy.
func NewPlan(seed int64, key string, copy int, methods []Method, minBoxSide int) Plan {
	return newPlan(seed, key, copy, methods, minBoxSide)
}

// Apply transforms src and its boxes. The result is an 8-bit *image.Gray
// for grayscale sources and an 8-bit opaque *image.NRGBA otherwise (alpha
// dropped by copying the color values, never premultiplied). src is not
// modified. The geometric steps are applied first (they move pixels and
// boxes exactly; 90° and 270° swap width and height), then the
// photometric ones in plan order (the boxes stay). Boxes are clipped to
// src, and dropped when nothing is left of them.
func Apply(src image.Image, boxes []Box, p Plan) (image.Image, []Box) { return apply(src, boxes, p) }

// OutputFormat is the format and file extension of the augmented copies of
// an image decoded as srcFormat (as image.DecodeConfig names it): JPEG for
// JPEG sources and lossy WebP, PNG otherwise (lossless WebP too).
func OutputFormat(srcFormat string, losslessWebP bool) (format, ext string) {
	switch {
	case srcFormat == "jpeg", srcFormat == "webp" && !losslessWebP:
		return "jpeg", ".jpg"
	default:
		return "png", ".png"
	}
}

// IsLosslessWebP reports whether r holds a lossless (VP8L) WebP image. It
// reads the RIFF header only (for an extended file, VP8X, the chunk
// headers within the first MiB).
func IsLosslessWebP(r io.ReaderAt) bool { return isLosslessWebP(r) }

// Encode writes img in format ("jpeg": quality 95; "png": best speed).
func Encode(w io.Writer, img image.Image, format string) error { return encode(w, img, format) }
