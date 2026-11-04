package dataset

import (
	"image/jpeg"
	"io"
	"os"
)

// Exported image helpers for other programs that show image files the way
// datasets do: the local reviewer (internal/reviewer) serves the images of
// an opened review package with the same media types and makes their
// thumbnails with the same decoders, limits and scaling. They are pure
// functions: no database, no cache.

// ImageContentType returns the media type of an image file by its
// extension (application/octet-stream for one that is not recognized).
func ImageContentType(name string) string { return imageContentType(name) }

// ImageDimensions returns the dimensions of the image in f, or nil when
// they cannot be determined (AVIF, a damaged file).
func ImageDimensions(f *os.File) (width, height *int) { return imageDimensions(f) }

// ThumbnailSide returns the longest side of the thumbnails of a size
// (ThumbSmall or ThumbLarge).
func ThumbnailSide(size string) (int, bool) {
	side, ok := thumbSides[size]
	return side, ok
}

// WriteThumbnail renders the image in f as a JPEG thumbnail whose sides
// fit in side pixels (never scaled up, transparency on white, EXIF
// orientation ignored) and writes it to w, exactly as dataset thumbnails
// are made. Images whose decoding and scaling would need too much memory
// are refused. reserve, if not nil, is asked for the estimated memory in
// bytes before decoding (to bound concurrent generations) and its release
// is called once the image is scaled. A file that cannot be decoded, or is
// too large, is an *Error of kind ErrUnsupportedImage.
func WriteThumbnail(w io.Writer, f *os.File, side int, reserve func(cost int64) (release func(), err error)) error {
	cfg, format, err := decodeImageConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return errUnsupportedImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxThumbnailCost {
		return errImageTooLarge
	}
	tw, th := fitSize(cfg.Width, cfg.Height, side)
	cost := thumbnailCost(cfg.Width, cfg.Height, tw, th, decodeCost(f, format, cfg))
	if cost > maxThumbnailCost {
		return errImageTooLarge
	}
	release := func() {}
	if reserve != nil {
		if release, err = reserve(cost); err != nil {
			return err
		}
	}
	src, err := decodeImage(f)
	if err != nil {
		release()
		return errUnsupportedImage
	}
	thumb := fitWithin(src, side)
	release()
	return jpeg.Encode(w, thumb, &jpeg.Options{Quality: thumbQuality})
}
