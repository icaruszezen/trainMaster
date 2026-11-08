package dataversion

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// pathNamer gives the images of a version unique paths. Two images must
// differ in lower(<folder>/<stem>) (one annotation, label or JSON entry
// per image in every download format, whatever the extensions) and in
// lower(UltralyticsLabelPath(path)) (one YOLO label file per image). A
// later image whose name is taken gets <stem>~2 (then ~3, ...). Names are
// assigned in position order, so they are deterministic.
type pathNamer struct {
	stems  map[string]bool
	labels map[string]bool
}

func newPathNamer() *pathNamer {
	return &pathNamer{stems: map[string]bool{}, labels: map[string]bool{}}
}

// assign reserves a unique "<folder>/<stem>" (slash-separated, without
// extension) for want and returns it; the caller appends the extension.
func (n *pathNamer) assign(want string) string {
	for k := 1; ; k++ {
		stem := want
		if k > 1 {
			stem = want + "~" + strconv.Itoa(k)
		}
		sk := strings.ToLower(stem)
		// The label path depends on the stem only.
		lk := strings.ToLower(dataset.UltralyticsLabelPath(stem + ".x"))
		if !n.stems[sk] && !n.labels[lk] {
			n.stems[sk], n.labels[lk] = true, true
			return stem
		}
	}
}

// originalPath assigns the version path of an original of the dataset
// folder dirName at rel: images/<dirName>/<rel> (listSafe), renamed when
// taken.
func (n *pathNamer) originalPath(dirName, rel string) string {
	p := listSafe(dirName + "/" + rel)
	ext := path.Ext(p)
	return n.assign("images/"+strings.TrimSuffix(p, ext)) + ext
}

// listSafe makes a slash-separated path usable as a line of an Ultralytics
// image list: Ultralytics splits the list at every line break (control
// characters, U+2028, U+2029) and replaces every "./" in a line with the
// list's folder, so these characters become "_", and so do the dots that
// end a folder name.
func listSafe(rel string) string {
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		seg = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
				return '_'
			}
			return r
		}, seg)
		if i < len(segs)-1 {
			trimmed := strings.TrimRight(seg, ".")
			seg = trimmed + strings.Repeat("_", len(seg)-len(trimmed))
		}
		segs[i] = seg
	}
	return strings.Join(segs, "/")
}

// copyStem assigns the path without extension of augmented copy k of the
// original stored at orig: next to it, <stem>-aug<k>.
func (n *pathNamer) copyStem(orig string, k int) string {
	return n.assign(strings.TrimSuffix(orig, path.Ext(orig)) + "-aug" + strconv.Itoa(k))
}

// labelFileLines renders the objects of an image of width x height pixels (in
// the class list, clipped) as Ultralytics label lines "class cx cy w h",
// normalized, 6 decimals. A negative sample has an empty file.
func labelFileLines(objects []dataset.AnnotationObject, width, height int, ids map[string]int) []byte {
	var b bytes.Buffer
	W, H := float64(width), float64(height)
	for _, o := range objects {
		id, ok := ids[o.Name]
		if !ok {
			continue
		}
		x1, x2, y1, y2 := float64(o.Xmin), float64(o.Xmax), float64(o.Ymin), float64(o.Ymax)
		fmt.Fprintf(&b, "%d %.6f %.6f %.6f %.6f\n", id, (x1+x2)/2/W, (y1+y2)/2/H, (x2-x1)/W, (y2-y1)/H)
	}
	return b.Bytes()
}
