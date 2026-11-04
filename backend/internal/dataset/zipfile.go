package dataset

import (
	"archive/zip"
	"path"
	"strings"
)

// Exported zip helpers for other packages that stream archives of dataset
// images the way dataset downloads do: the model review's packages
// (internal/review) pack the images of a review with its VOC files.

// RegisterZipCompressor makes zw deflate with the pooled fast compressor
// of dataset downloads (a new flate writer per entry costs about 800 KB).
func RegisterZipCompressor(zw *zip.Writer) { zw.RegisterCompressor(zip.Deflate, newFlateWriter) }

// ZipMethod returns how a file is best stored in a zip archive by its
// name: images in formats that are compressed already (JPEG, PNG, GIF,
// WebP, AVIF) are stored as they are, anything else (BMP, TIFF, VOC files,
// ...) is deflated.
func ZipMethod(name string) uint16 {
	if storedExtensions[strings.ToLower(path.Ext(name))] {
		return zip.Store
	}
	return zip.Deflate
}
