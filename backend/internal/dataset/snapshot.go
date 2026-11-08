package dataset

import (
	"context"
	"os"
	"time"
)

// OWNER: backend-core agent (data versions). A snapshot is the composed
// image list of a VersionSelection (what a download of it would take, with
// the same layering rules as layerSQL / composedSQL), read consistently for
// package dataversion, with the dataset folder opened to read the files.

// SnapshotOptions tune OpenSnapshot.
type SnapshotOptions struct {
	// IndexOnly: the folder is not opened (Root is nil) and the review
	// version objects are not loaded (previews count from the index).
	IndexOnly bool
	// AllowTrashedVersions accepts selected review versions that are in
	// the recycle bin (a data version build: they were in use when the
	// data version was created and keep their rows until purged).
	AllowTrashedVersions bool
}

// Snapshot is the composed image list of a selection of a dataset.
type Snapshot struct {
	Dataset  Dataset
	Versions []Version       // the selected review versions, newest first
	Root     *os.Root        // the dataset folder; nil with IndexOnly
	Images   []SnapshotImage // in ImageOrder
}

// Close closes the dataset folder.
func (s *Snapshot) Close() error {
	if s == nil || s.Root == nil {
		return nil
	}
	return s.Root.Close()
}

// SnapshotImage is an image of a snapshot with the annotation that applies
// to it.
type SnapshotImage struct {
	ID        int64
	RelPath   string // slash-separated, in the dataset folder
	SizeBytes int64
	ModTime   time.Time // as indexed (microseconds)
	Width     *int      // from the index; nil when unknown
	Height    *int

	// The base annotation, from the index.
	AnnotationPath  *string        // the VOC file; nil: none
	AnnotationState string         // none, annotated, empty or invalid
	BaseLabels      map[string]int // label counts

	// The review version image whose annotation applies (VersionID nil:
	// the base applies).
	VersionID      *int64
	ReviewStatus   string             // of that version image
	VersionLabels  map[string]int     // its label counts
	VersionObjects []AnnotationObject // its objects; nil with IndexOnly
	VersionWidth   *int               // the image size when it was merged
	VersionHeight  *int
}

// OpenSnapshot reads the composed images of sel for the dataset in one
// REPEATABLE READ, READ ONLY transaction, then (unless IndexOnly) loads the
// review version objects in chunks without the recycle bin filter and
// opens the dataset folder. Errors are *Error: the dataset is missing or in
// the recycle bin (ErrNotFound); a selected review version is missing, of
// another dataset, or in the bin without AllowTrashedVersions
// (ErrNotFound, "所选审图版本不存在（可能已被删除）"); not ready
// (errVersionBuilding); purged while its objects were loaded (ErrNotFound,
// "所选审图版本“<name>”已被永久删除"); the folder is missing (ErrConflict,
// "数据集“<name>”的文件夹不存在", without the server path). Close the
// snapshot when done.
func (s *Service) OpenSnapshot(ctx context.Context, datasetID int64, sel VersionSelection, opts SnapshotOptions) (*Snapshot, error) {
	panic("dataset: OpenSnapshot not implemented")
}
