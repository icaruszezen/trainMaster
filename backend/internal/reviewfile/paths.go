// Package reviewfile holds what the platform (internal/review) and the
// local reviewer (internal/reviewer) share about the files of a model
// review: where every image and VOC file goes inside a review folder and a
// review package (paths that are valid and unique on Windows), the VOC
// files of review annotations, and review.json, the manifest of a package
// and of the results exported from it. It is pure Go without database or
// HTTP, so it builds for Windows as well.
package reviewfile

import (
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Files at the top of a review folder, package or results folder, which no
// dataset folder may take.
const (
	ManifestFile = "review.json"          // the manifest (review folder: the task description)
	ProgressFile = "review-progress.json" // the local reviewer's progress in an opened package
	ReadmeFile   = "README.txt"           // explains a downloaded package
)

// reservedTop are the names of ManifestFile, ProgressFile and ReadmeFile
// as FoldKey gives them.
var reservedTop = map[string]bool{
	FoldKey(ManifestFile): true,
	FoldKey(ProgressFile): true,
	FoldKey(ReadmeFile):   true,
}

// maxSegmentUnits is the longest file name NTFS allows, in UTF-16 code
// units.
const maxSegmentUnits = 255

// maxRelPathBytes bounds the paths ValidRelPath accepts.
const maxRelPathBytes = 4096

// windowsInvalid are the characters a Windows file name cannot contain
// (besides control characters); "/" separates segments anyway.
const windowsInvalid = `<>:"|?*\/`

// SafeSegment turns s into one path segment that is a valid file or
// folder name on Windows (and anywhere else): the characters Windows
// forbids (< > : " | ? * \ /, control characters, invalid UTF-8) become
// "_", so do trailing dots and spaces (Windows drops them); reserved device
// names (CON, PRN, AUX, NUL, COM0–COM9, LPT0–LPT9, also COM¹ ... and with
// an extension such as con.txt, in any case) get a "_" prefix; a name
// longer than 255 UTF-16 units is shortened before its extension; ""
// becomes "_". It never returns "." or "..", and SafeSegment of its result
// is the result itself.
func SafeSegment(s string) string {
	s = strings.ToValidUTF8(s, "_")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(windowsInvalid, r) {
			return '_'
		}
		return r
	}, s)
	s = fitUnits(s)
	// Trailing dots and spaces: Windows strips them, so "a." and "a" would
	// be the same file.
	trimmed := strings.TrimRight(s, ". ")
	s = trimmed + strings.Repeat("_", len(s)-len(trimmed))
	if s == "" {
		return "_"
	}
	if isDeviceName(s) {
		s = fitUnits("_" + s)
	}
	return s
}

// fitUnits shortens a name to maxSegmentUnits UTF-16 units, cutting the
// stem and keeping a short extension (the image or .xml type).
func fitUnits(s string) string {
	if utf16Len(s) <= maxSegmentUnits {
		return s
	}
	stem, ext := s, path.Ext(s)
	if utf16Len(ext) > 32 { // not a real extension
		ext = ""
	}
	stem = s[:len(s)-len(ext)]
	runes := []rune(stem)
	for len(runes) > 0 && utf16Len(string(runes))+utf16Len(ext) > maxSegmentUnits {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ext
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// isDeviceName reports whether Windows treats the file name as a device:
// the part before the first dot (without trailing spaces) is CON, PRN, AUX,
// NUL, CONIN$, CONOUT$, COM or LPT followed by a digit or superscript digit,
// ignoring case.
func isDeviceName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) < 4 || (base[:3] != "COM" && base[:3] != "LPT") {
		return false
	}
	switch rest := base[3:]; rest {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

// FoldKey is the key under which two names are the same file on a
// case-insensitive file system (Windows, macOS): every character is
// replaced by the smallest character of its Unicode case-folding orbit
// (so "A" and "a", or "K" and the Kelvin sign, share a key). It folds more
// than NTFS does, never less, so names with different keys never clash.
func FoldKey(s string) string {
	return strings.Map(func(r rune) rune {
		lowest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			lowest = min(lowest, f)
		}
		return lowest
	}, s)
}

// ValidRelPath reports whether p is a path that review folders, packages
// and results may contain: relative and slash-separated, of segments that
// SafeSegment leaves as they are (so no "", "." or "..", no backslashes,
// drive letters or names Windows refuses), and at most 4096 bytes.
func ValidRelPath(p string) bool {
	if p == "" || len(p) > maxRelPathBytes || !utf8.ValidString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || SafeSegment(seg) != seg {
			return false
		}
	}
	return true
}

// Folder returns the folder of a dataset inside a review: SafeSegment of
// the dataset's folder name, or, when taken reports that another dataset
// of the review uses that name already (taken is asked with the FoldKey of
// a candidate), the name with "~<datasetID>" appended (then "~<id>~2", ...).
// The names of the files at the top of a review folder (review.json, ...)
// are never returned.
func Folder(dirName string, datasetID int64, taken func(key string) bool) string {
	base := SafeSegment(dirName)
	used := func(name string) bool { k := FoldKey(name); return reservedTop[k] || taken(k) }
	if !used(base) {
		return base
	}
	suffix := "~" + strconv.FormatInt(datasetID, 10)
	for n := 1; ; n++ {
		candidate := nameWith(base, suffix, "")
		if n > 1 {
			candidate = nameWith(base, suffix+"~"+strconv.Itoa(n), "")
		}
		if !used(candidate) {
			return candidate
		}
	}
}

// nameWith joins stem, suffix and ext into one segment (see SafeSegment),
// shortening the stem so that the suffix and the extension are kept.
func nameWith(stem, suffix, ext string) string {
	runes := []rune(stem)
	for len(runes) > 0 && utf16Len(string(runes))+utf16Len(suffix)+utf16Len(ext) > maxSegmentUnits {
		runes = runes[:len(runes)-1]
	}
	return SafeSegment(string(runes) + suffix + ext)
}

// PackageFolder is the folder a review package is in, and the name of its
// zip file without ".zip": "审图-<review ID>-<name>", the name made a valid
// file name (SafeSegment).
func PackageFolder(reviewID int64, name string) string {
	return SafeSegment("审图-" + strconv.FormatInt(reviewID, 10) + "-" + name)
}

// LayoutImage is an image to place in a review: the folder of its dataset
// (from Folder) and its path in the dataset folder.
type LayoutImage struct {
	Folder  string
	RelPath string // slash-separated, relative to the dataset folder
}

// Placement is where Layout puts an image: the image file and its VOC
// file, both relative to the review folder (or package folder).
type Placement struct {
	Image      string
	Annotation string
}

// Layout places the images of a review, in the given order (which makes it
// deterministic): an image goes to <folder>/<SafeSegment of each segment of
// its path>, its VOC file next to it as <stem>.xml. Every file path is
// unique ignoring case and never the path of a folder that holds images:
// a clashing image name gets "~2", "~3", ... before its extension; a
// clashing VOC file is named <image file name>.xml (a.png.xml for a.png
// next to a.jpg) and otherwise <stem>~2.xml, .... Folders that differ only
// in case are the same folder.
func Layout(images []LayoutImage) []Placement {
	// Every folder that will hold an image, so that no file takes its name.
	dirs := map[string]bool{}
	imageDirs := make([]string, len(images))
	for i, img := range images {
		segs := strings.Split(img.RelPath, "/")
		dir := img.Folder
		dirs[FoldKey(dir)] = true
		for _, seg := range segs[:len(segs)-1] {
			dir += "/" + SafeSegment(seg)
			dirs[FoldKey(dir)] = true
		}
		imageDirs[i] = dir
	}
	files := map[string]bool{}
	free := func(p string) bool { k := FoldKey(p); return !files[k] && !dirs[k] }
	take := func(p string) string { files[FoldKey(p)] = true; return p }

	out := make([]Placement, len(images))
	for i, img := range images {
		dir := imageDirs[i] + "/"
		name := SafeSegment(path.Base(img.RelPath))
		ext := path.Ext(name)
		stem := name[:len(name)-len(ext)]
		candidate := dir + name
		for n := 2; !free(candidate); n++ {
			candidate = dir + nameWith(stem, "~"+strconv.Itoa(n), ext)
		}
		imagePath := take(candidate)
		name = path.Base(imagePath)
		stem = name[:len(name)-len(path.Ext(name))]

		annotation := dir + nameWith(stem, "", ".xml")
		if !free(annotation) {
			annotation = dir + nameWith(name, "", ".xml")
		}
		for n := 2; !free(annotation); n++ {
			annotation = dir + nameWith(stem, "~"+strconv.Itoa(n), ".xml")
		}
		out[i] = Placement{Image: imagePath, Annotation: take(annotation)}
	}
	return out
}
