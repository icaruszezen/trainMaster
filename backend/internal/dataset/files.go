package dataset

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// File recognition rules (spec §4). The frontend mirrors them in
// frontend/src/features/datasets/files.ts for the pre-upload summary.

type fileKind int

const (
	kindIgnored    fileKind = iota // junk or neither image nor annotation
	kindImage                      // a supported image extension
	kindAnnotation                 // a VOC XML candidate
)

// imageContentTypes maps the recognized image extensions (lower case) to
// their media types.
var imageContentTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".bmp":  "image/bmp",
	".gif":  "image/gif",
	".webp": "image/webp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".avif": "image/avif",
}

// classify returns the kind of the file at the slash-separated relative
// path rel.
func classify(rel string) fileKind {
	if isJunkPath(rel) {
		return kindIgnored
	}
	ext := strings.ToLower(path.Ext(rel))
	switch {
	case imageContentTypes[ext] != "":
		return kindImage
	case ext == ".xml":
		return kindAnnotation
	}
	return kindIgnored
}

// isJunkSegment reports whether a path segment hides everything below it:
// hidden names (".git", "._a.jpg" AppleDouble files) and macOS resource
// fork folders.
func isJunkSegment(seg string) bool {
	return strings.HasPrefix(seg, ".") || seg == "__MACOSX"
}

// isJunkFile reports whether a file name is operating-system clutter.
func isJunkFile(base string) bool {
	return strings.EqualFold(base, "Thumbs.db") || strings.EqualFold(base, "desktop.ini")
}

// isJunkPath applies isJunkSegment to every segment and isJunkFile to the
// last one.
func isJunkPath(rel string) bool {
	for seg := range strings.SplitSeq(rel, "/") {
		if isJunkSegment(seg) {
			return true
		}
	}
	return isJunkFile(path.Base(rel))
}

// cleanEntryPath turns an archive entry name or an uploaded relative path
// into a clean slash-separated path inside the target folder. ok is false
// for names that are not valid UTF-8, are absolute, carry a drive letter,
// contain NUL or escape the folder with "..".
func cleanEntryPath(name string) (rel string, ok bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || hasDriveLetter(name) {
		return "", false
	}
	rel = path.Clean(name)
	if rel == "." || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", false
	}
	return rel, true
}

func hasDriveLetter(name string) bool {
	return len(name) >= 2 && name[1] == ':' && ('a' <= name[0]|0x20 && name[0]|0x20 <= 'z')
}

// pairAnnotations matches images with VOC annotation files (spec §4.4).
// Both lists hold slash-separated relative paths. For each image the first
// existing candidate wins:
//
//  1. same directory and stem with ".xml", exact match first, then ignoring
//     case of the file name;
//  2. Pascal VOC layout: an image in "JPEGImages" (or "images") takes
//     "<stem>.xml" from the sibling directory "Annotations" (any case).
//
// When several images claim one file (a.jpg and a.png both want a.xml),
// the <filename> recorded in it decides (vocFilename returns it, "" when
// unknown); otherwise the first image in sorted order gets it and the
// others stay unannotated. The result maps image path to annotation path;
// each annotation file is used at most once.
func pairAnnotations(images, xmls []string, vocFilename func(xml string) string) map[string]string {
	pairs := make(map[string]string)
	if len(images) == 0 || len(xmls) == 0 {
		return pairs
	}
	images = slices.Sorted(slices.Values(images))
	xmls = slices.Sorted(slices.Values(xmls))

	exact := make(map[string]bool, len(xmls))
	byDir := make(map[string]string, len(xmls)) // dir + "\x00" + lower(base) -> first xml
	byVOC := make(map[string]string)            // parent + "\x00" + lower(dir name) + "\x00" + lower(base)
	for _, x := range xmls {
		exact[x] = true
		dir, base := path.Split(x)
		key := dir + "\x00" + strings.ToLower(base)
		if _, dup := byDir[key]; !dup {
			byDir[key] = x
		}
		if dir != "" {
			parent, dirName := path.Split(strings.TrimSuffix(dir, "/"))
			key := parent + "\x00" + strings.ToLower(dirName) + "\x00" + strings.ToLower(base)
			if _, dup := byVOC[key]; !dup {
				byVOC[key] = x
			}
		}
	}

	claims := make(map[string][]string)
	var claimed []string // annotation files in first-claim order, for a deterministic result
	for _, img := range images {
		dir, base := path.Split(img)
		stem := strings.TrimSuffix(base, path.Ext(base))
		x := dir + stem + ".xml"
		if !exact[x] {
			x = byDir[dir+"\x00"+strings.ToLower(stem+".xml")]
		}
		if x == "" && dir != "" {
			parent, dirName := path.Split(strings.TrimSuffix(dir, "/"))
			if d := strings.ToLower(dirName); d == "jpegimages" || d == "images" {
				x = byVOC[parent+"\x00annotations\x00"+strings.ToLower(stem+".xml")]
			}
		}
		if x == "" {
			continue
		}
		if len(claims[x]) == 0 {
			claimed = append(claimed, x)
		}
		claims[x] = append(claims[x], img)
	}

	for _, x := range claimed {
		imgs := claims[x]
		winner := imgs[0]
		if len(imgs) > 1 && vocFilename != nil {
			winner = pickByFilename(imgs, vocFilename(x))
		}
		pairs[winner] = x
	}
	return pairs
}

// pickByFilename returns the image whose file name equals want (exactly,
// then ignoring case), or the first image.
func pickByFilename(imgs []string, want string) string {
	if want != "" {
		for _, img := range imgs {
			if path.Base(img) == want {
				return img
			}
		}
		for _, img := range imgs {
			if strings.EqualFold(path.Base(img), want) {
				return img
			}
		}
	}
	return imgs[0]
}
