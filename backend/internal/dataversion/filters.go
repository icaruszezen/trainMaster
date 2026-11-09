package dataversion

import (
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Filters (过滤): which images a version takes and which of their objects.
// An image with an annotation file that cannot be read is never taken. The
// scope of its source decides about the rest (see ScopeAnnotated): an
// image without an annotation is taken, as a negative sample, only by the
// background and all scopes; an image with objects is skipped by the
// background scope. Objects of the ignored labels are dropped; any other
// label not in the class list makes the image skipped (unlisted: it may
// show a real defect that would otherwise train as background). An image
// left without objects is a negative sample: the annotated scope takes it
// only with include_empty, the others always. Boxes are clipped to the
// image; a box without area after clipping is dropped (degenerate). The
// preview applies the same rules to the label counts of the index.

// labelFilter holds the class list and the ignored labels of a version.
type labelFilter struct {
	classes      map[string]int // name -> YOLO class id
	ignore       map[string]bool
	includeEmpty bool
}

func newLabelFilter(classes []Class, cfg Config) labelFilter {
	f := labelFilter{classes: map[string]int{}, ignore: map[string]bool{}, includeEmpty: cfg.IncludeEmpty}
	for i, c := range classes {
		f.classes[c.Name] = i
	}
	for _, l := range cfg.IgnoreLabels {
		f.ignore[l] = true
	}
	return f
}

// countResult is what the filter makes of the label counts of an image.
type countResult struct {
	unlisted bool           // a label neither in the class list nor ignored
	kept     map[string]int // objects per class
	objects  int            // kept objects
	dropped  int            // objects of ignored labels
}

// counts filters the label counts of an image (the index).
func (f labelFilter) counts(labels map[string]int) countResult {
	r := countResult{kept: map[string]int{}}
	for name, n := range labels {
		switch {
		case f.ignore[name]:
			r.dropped += n
		case f.has(name):
			r.kept[name] += n
			r.objects += n
		case n > 0:
			r.unlisted = true
		}
	}
	return r
}

// takesNegatives reports whether a source with the scope takes the images
// left without objects.
func (f labelFilter) takesNegatives(scope string) bool {
	return scope != ScopeAnnotated || f.includeEmpty
}

// hasObjects reports whether label counts have an object.
func hasObjects(labels map[string]int) bool {
	for _, n := range labels {
		if n > 0 {
			return true
		}
	}
	return false
}

func (f labelFilter) has(name string) bool {
	_, ok := f.classes[name]
	return ok
}

// objectResult is what the filter makes of the objects of an image.
type objectResult struct {
	unlisted   bool
	kept       []dataset.AnnotationObject // clipped to the image, never nil
	dropped    int
	degenerate int
}

// objects filters the objects of an image of width x height pixels and
// clips their boxes.
func (f labelFilter) objects(objs []dataset.AnnotationObject, width, height int) objectResult {
	r := objectResult{kept: []dataset.AnnotationObject{}}
	for _, o := range objs {
		if !f.ignore[o.Name] && !f.has(o.Name) {
			r.unlisted = true
			return r
		}
	}
	for _, o := range objs {
		if f.ignore[o.Name] {
			r.dropped++
			continue
		}
		c, ok := clipObject(o, width, height)
		if !ok {
			r.degenerate++
			continue
		}
		r.kept = append(r.kept, c)
	}
	return r
}

// clipObject clips a box to the image: 0 <= xmin < xmax <= width, the same
// for y. ok is false when nothing is left.
func clipObject(o dataset.AnnotationObject, width, height int) (dataset.AnnotationObject, bool) {
	x1, x2 := min(o.Xmin, o.Xmax), max(o.Xmin, o.Xmax)
	y1, y2 := min(o.Ymin, o.Ymax), max(o.Ymin, o.Ymax)
	o.Xmin, o.Xmax = min(max(x1, 0), width), min(max(x2, 0), width)
	o.Ymin, o.Ymax = min(max(y1, 0), height), min(max(y2, 0), height)
	return o, o.Xmax > o.Xmin && o.Ymax > o.Ymin
}

// objectLabels counts the objects per label.
func objectLabels(objs []dataset.AnnotationObject) map[string]int {
	out := map[string]int{}
	for _, o := range objs {
		out[o.Name]++
	}
	return out
}
