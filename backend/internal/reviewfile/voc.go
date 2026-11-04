package reviewfile

import (
	"cmp"
	"path"
	"slices"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Review annotations are VOC files the platform owns: every save
// regenerates the whole file with the writer dataset imports use (no
// patching), so a review file always looks the same for the same boxes and
// its version (AnnotationVersion) changes exactly when its content does.

// Image statuses of a review.
const (
	StatusPending     = "pending"      // 待审核: not reviewed yet
	StatusApproved    = "approved"     // 已审核: the model's result is right; no need to learn the image
	StatusNeedsSample = "needs_sample" // 需加样: the image should join later training
)

// ValidStatus reports whether s is an image status.
func ValidStatus(s string) bool {
	return s == StatusPending || s == StatusApproved || s == StatusNeedsSample
}

// Prediction is a box the model predicted, in whole pixels of the image as
// stored (clamped to it), with its confidence (0–1, 4 decimals).
type Prediction struct {
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Xmin       int     `json:"xmin"`
	Ymin       int     `json:"ymin"`
	Xmax       int     `json:"xmax"`
	Ymax       int     `json:"ymax"`
}

// EncodeAnnotation writes the review VOC file of the image at sourcePath
// (its path in the dataset): <folder> is the name of the image's folder in
// the dataset (none at the top level), <filename> its file name, <size>
// the image size when known, then the objects.
func EncodeAnnotation(sourcePath string, width, height *int, objects []dataset.AnnotationObject) []byte {
	w, h := 0, 0
	if width != nil && height != nil {
		w, h = *width, *height
	}
	data, _ := dataset.EncodeVOC(path.Base(path.Dir(sourcePath)), path.Base(sourcePath), w, h, objects)
	return data
}

// DecodeAnnotation parses a review VOC file (or any VOC file, as dataset
// scans do).
func DecodeAnnotation(data []byte) (dataset.VOCFile, error) { return dataset.DecodeVOC(data) }

// AnnotationVersion identifies the content of a VOC file ("" stands for no
// file), as for dataset annotations.
func AnnotationVersion(data []byte) string { return dataset.AnnotationVersion(data) }

// PredictionObjects returns the predictions as annotation objects (the
// annotation a review starts with).
func PredictionObjects(predictions []Prediction) []dataset.AnnotationObject {
	out := make([]dataset.AnnotationObject, len(predictions))
	for i, p := range predictions {
		out[i] = dataset.AnnotationObject{Name: p.Name, Xmin: p.Xmin, Ymin: p.Ymin, Xmax: p.Xmax, Ymax: p.Ymax}
	}
	return out
}

// boxKey is what SameBoxes compares.
type boxKey struct {
	name                   string
	xmin, ymin, xmax, ymax int
}

func compareBoxes(a, b boxKey) int {
	return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.xmin, b.xmin), cmp.Compare(a.ymin, b.ymin),
		cmp.Compare(a.xmax, b.xmax), cmp.Compare(a.ymax, b.ymax))
}

// SameBoxes reports whether objects are the predictions: the same boxes
// (name and coordinates) as multisets, in any order. The difficult and
// truncated flags and the confidences do not count. A review image is
// "edited" when its annotation is not the same boxes as its predictions.
func SameBoxes(objects []dataset.AnnotationObject, predictions []Prediction) bool {
	if len(objects) != len(predictions) {
		return false
	}
	a := make([]boxKey, len(objects))
	for i, o := range objects {
		a[i] = boxKey{o.Name, o.Xmin, o.Ymin, o.Xmax, o.Ymax}
	}
	b := make([]boxKey, len(predictions))
	for i, p := range predictions {
		b[i] = boxKey{p.Name, p.Xmin, p.Ymin, p.Xmax, p.Ymax}
	}
	slices.SortFunc(a, compareBoxes)
	slices.SortFunc(b, compareBoxes)
	return slices.Equal(a, b)
}

// LabelCounts returns the number of objects per label.
func LabelCounts(objects []dataset.AnnotationObject) map[string]int {
	counts := make(map[string]int)
	for _, o := range objects {
		counts[o.Name]++
	}
	return counts
}

// SameObjects reports whether a and b are the same annotation: the same
// objects (name, coordinates and the difficult and truncated flags) as
// multisets, in any order. Uploaded results whose boxes are what the
// platform has already are no change.
func SameObjects(a, b []dataset.AnnotationObject) bool {
	if len(a) != len(b) {
		return false
	}
	compare := func(x, y dataset.AnnotationObject) int {
		return cmp.Or(compareBoxes(boxKey{x.Name, x.Xmin, x.Ymin, x.Xmax, x.Ymax}, boxKey{y.Name, y.Xmin, y.Ymin, y.Xmax, y.Ymax}),
			compareFlag(x.Difficult, y.Difficult), compareFlag(x.Truncated, y.Truncated))
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, compare)
	slices.SortFunc(b, compare)
	return slices.Equal(a, b)
}

func compareFlag(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}
