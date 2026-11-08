package dataset

import (
	"image"
	"os"
	"path"
	"strings"
)

// Helpers exported for package dataversion (data versions build training
// sets from datasets and write the same file formats).

// UltralyticsLabelPath is where Ultralytics looks for the labels of the
// image at the slash-separated path rel: the last "images" folder becomes
// "labels" and the extension ".txt".
func UltralyticsLabelPath(rel string) string { return ultralyticsLabelPath(rel) }

// SidecarPath is <stem><ext> next to the image at the slash-separated
// path rel.
func SidecarPath(rel, ext string) string { return sidecarPath(rel, ext) }

// DecodeImageConfig reads the dimensions and format (as image.DecodeConfig
// names it, "tiff" for TIFF) of the image in f. Decoder panics are errors.
func DecodeImageConfig(f *os.File) (image.Config, string, error) { return decodeImageConfig(f) }

// DecodeImage decodes the whole image in f (TIFF included). Decoder panics
// are errors.
func DecodeImage(f *os.File) (image.Image, error) { return decodeImage(f) }

// IsTrainableImage reports whether training tools (Ultralytics, OpenCV)
// read images with the extension of rel: GIF and AVIF are not read.
func IsTrainableImage(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".gif", ".avif":
		return false
	}
	return imageContentTypes[strings.ToLower(path.Ext(rel))] != ""
}
