package dataversion

import (
	"context"
	"io"
)

// OWNER: backend-integration agent. Downloads: YOLO, Pascal VOC and COCO
// zip archives streamed from the version folder.

// exportTickets holds the prepared downloads in memory.
type exportTickets struct{}

// CreateExport prepares the download of a ready version.
func (s *Service) CreateExport(ctx context.Context, id int64, in ExportInput) (ExportTicket, error) {
	panic("dataversion: CreateExport not implemented")
}

// Export is an opened download; Write it, then Close it.
type Export struct {
	FileName string
}

// OpenExport opens a download prepared by CreateExport.
func (s *Service) OpenExport(ctx context.Context, id int64, token string) (*Export, error) {
	panic("dataversion: OpenExport not implemented")
}

// Write sends the zip archive to w.
func (e *Export) Write(ctx context.Context, w io.Writer) error {
	panic("dataversion: Export.Write not implemented")
}

// Close releases the version folder.
func (e *Export) Close() error { return nil }
