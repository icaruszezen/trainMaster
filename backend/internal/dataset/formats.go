package dataset

import (
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Annotation formats besides VOC (docs/architecture.md, “标注格式”). The
// frontend mirrors the file rules in frontend/src/features/datasets/files.ts
// and the class list rules in frontend/src/features/datasets/class-list.ts.

// validFormat reports whether f is one of the Format* constants.
func validFormat(f string) bool {
	return f == FormatVOC || f == FormatXAnyLabeling || f == FormatYOLO
}

// annotationExt is the extension of a format's annotation files.
func annotationExt(format string) string {
	switch format {
	case FormatXAnyLabeling:
		return ".json"
	case FormatYOLO:
		return ".txt"
	}
	return ".xml"
}

// yoloClassesFile is the class list YOLO tools keep next to the labels. It
// is never a label file.
const yoloClassesFile = "classes.txt"

// classifyFor is classify for an upload whose annotations are in format:
// the annotation candidates are that format's files, and annotation files
// of other formats are ignored like any unrecognized file.
func classifyFor(rel, format string) fileKind {
	if format == FormatVOC || format == "" {
		return classify(rel)
	}
	if isJunkPath(rel) {
		return kindIgnored
	}
	ext := strings.ToLower(path.Ext(rel))
	switch {
	case imageContentTypes[ext] != "":
		return kindImage
	case ext != annotationExt(format):
		return kindIgnored
	case format == FormatYOLO && strings.EqualFold(path.Base(rel), yoloClassesFile):
		return kindIgnored
	}
	return kindAnnotation
}

// Limits of a YOLO class list.
const (
	maxClasses        = 1000
	maxClassNameRunes = 255
)

// NormalizeClasses trims the class names of a YOLO import or export and
// validates them: 1 to maxClasses names (the index is the class id), each
// non-empty, without control characters (C0 and C1, like the frontend) and
// unique (case-sensitive, as YOLO tools compare them).
func NormalizeClasses(raw []string) ([]string, error) {
	invalid := func(format string, args ...any) ([]string, error) {
		return nil, newError(ErrInvalid, format, args...)
	}
	switch {
	case len(raw) == 0:
		return invalid("YOLO 格式需要提供类别顺序（每行一个类别名称）")
	case len(raw) > maxClasses:
		return invalid("类别数量不能超过 %d 个", maxClasses)
	}
	out := make([]string, len(raw))
	seen := make(map[string]int, len(raw))
	for i, name := range raw {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			return invalid("第 %d 个类别名称为空", i+1)
		case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl):
			return invalid("第 %d 个类别名称包含换行或控制字符", i+1)
		case utf8.RuneCountInString(name) > maxClassNameRunes:
			return invalid("第 %d 个类别名称不能超过 %d 个字符", i+1, maxClassNameRunes)
		}
		if j, dup := seen[name]; dup {
			return invalid("类别“%s”重复（第 %d 个和第 %d 个）", name, j+1, i+1)
		}
		seen[name] = i
		out[i] = name
	}
	return out, nil
}

// normalizeFormat validates the annotation format of an import or export
// ("" means VOC) and its class list, which only YOLO uses.
func normalizeFormat(format string, classes []string, what string) (string, []string, error) {
	if format == "" {
		format = FormatVOC
	}
	if !validFormat(format) {
		return "", nil, newError(ErrInvalid, "%s格式必须是 voc（Pascal VOC）、xanylabeling（X-AnyLabeling）或 yolo（YOLO）", what)
	}
	if format != FormatYOLO {
		return format, nil, nil
	}
	classes, err := NormalizeClasses(classes)
	return format, classes, err
}

// yoloLabelDir maps an image folder to the folder Ultralytics reads its
// labels from: the last path segment named "images" becomes "labels"
// ("ds/images/train" → "ds/labels/train"). ok is false without such a
// segment. Upload pairing compares the result ignoring case.
func yoloLabelDir(dir string) (labels string, ok bool) {
	segs := strings.Split(dir, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.EqualFold(segs[i], "images") {
			segs[i] = "labels"
			return strings.Join(segs, "/"), true
		}
	}
	return "", false
}

// pairSidecars matches images with the annotation files (candidates) of a
// format that names them after the image: <stem><ext>. Both lists hold
// slash-separated relative paths. For each image the first existing
// candidate wins:
//
//  1. same directory and stem, exact match first, then ignoring case of
//     the file name;
//  2. if alt maps the image's directory ("" for the top level) to another
//     one, the stem there, ignoring case of the whole path.
//
// When several images claim one file (a.jpg and a.png both want a.json),
// filename decides by the image file name recorded in it ("" when unknown;
// nil for formats that record none); otherwise the first image in sorted
// order gets it. The result maps image path to annotation path; each file
// is used at most once.
func pairSidecars(images, candidates []string, ext string, alt func(dir string) (string, bool),
	filename func(ann string) string) map[string]string {
	pairs := make(map[string]string)
	if len(images) == 0 || len(candidates) == 0 {
		return pairs
	}
	images = slices.Sorted(slices.Values(images))
	candidates = slices.Sorted(slices.Values(candidates))

	exact := make(map[string]bool, len(candidates))
	byDir := make(map[string]string, len(candidates)) // dir + "\x00" + lower(base) -> first candidate
	folded := make(map[string]string, len(candidates))
	for _, c := range candidates {
		exact[c] = true
		dir, base := path.Split(c)
		if key := dir + "\x00" + strings.ToLower(base); byDir[key] == "" {
			byDir[key] = c
		}
		if key := strings.ToLower(c); folded[key] == "" {
			folded[key] = c
		}
	}

	claims := make(map[string][]string)
	var claimed []string // candidates in first-claim order, for a deterministic result
	for _, img := range images {
		dir, base := path.Split(img)
		name := strings.TrimSuffix(base, path.Ext(base)) + ext
		c := dir + name
		if !exact[c] {
			c = byDir[dir+"\x00"+strings.ToLower(name)]
		}
		if c == "" && alt != nil {
			if altDir, ok := alt(strings.TrimSuffix(dir, "/")); ok {
				c = folded[strings.ToLower(path.Join(altDir, name))]
			}
		}
		if c == "" {
			continue
		}
		if len(claims[c]) == 0 {
			claimed = append(claimed, c)
		}
		claims[c] = append(claims[c], img)
	}

	for _, c := range claimed {
		imgs := claims[c]
		winner := imgs[0]
		if len(imgs) > 1 && filename != nil {
			winner = pickByFilename(imgs, filename(c))
		}
		pairs[winner] = c
	}
	return pairs
}
