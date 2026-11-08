package dataversion

import (
	"context"
	"os"
)

// OWNER: backend-integration agent. The images of a ready version: list,
// detail, files and thumbnails.

// thumbnails generates and caches the thumbnails of version images.
type thumbnails struct{}

// ListImages returns a page of the images of a ready version and their
// total.
func (s *Service) ListImages(ctx context.Context, id int64, f ImageFilter) ([]Image, int, error) {
	panic("dataversion: ListImages not implemented")
}

// GetImage returns an image of a ready version with its objects.
func (s *Service) GetImage(ctx context.Context, id, imageID int64) (ImageDetail, error) {
	panic("dataversion: GetImage not implemented")
}

// ImageFile is an opened image or thumbnail file.
type ImageFile struct {
	File        *os.File
	ContentType string
	Name        string
}

// OpenImage opens the file of an image of a ready version.
func (s *Service) OpenImage(ctx context.Context, id, imageID int64) (*ImageFile, error) {
	panic("dataversion: OpenImage not implemented")
}

// OpenThumbnail opens the thumbnail of an image ("sm" or "lg"), generating
// it when it is not cached yet.
func (s *Service) OpenThumbnail(ctx context.Context, id, imageID int64, size string) (*ImageFile, error) {
	panic("dataversion: OpenThumbnail not implemented")
}
