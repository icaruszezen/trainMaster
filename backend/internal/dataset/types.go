package dataset

import (
	"path"
	"strconv"
	"time"
)

// The JSON field names of the types in this file are the API contract with
// the frontend (frontend/src/features/datasets/api.ts); do not rename them.

// Dataset origins.
const (
	OriginUpload    = "upload"    // created empty, filled by uploads
	OriginDirectory = "directory" // an existing folder registered in place
)

// Import job sources.
const (
	SourceArchive = "archive"
	SourceFolder  = "folder"
	SourceScan    = "scan"
)

// Import job states. uploading → queued → running → succeeded | failed |
// canceled; uploading and queued jobs can also be canceled directly.
const (
	StatusUploading = "uploading"
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// What an import does with an image whose path already exists.
const (
	ConflictSkip      = "skip"      // keep the existing image and its annotation
	ConflictOverwrite = "overwrite" // replace the image (and its annotation if the import has one)
)

// Processing stages and progress units of a running job.
const (
	stageExtracting = "extracting"
	stageImporting  = "importing"
	stageScanning   = "scanning"

	unitBytes   = "bytes"
	unitFiles   = "files"
	unitEntries = "entries"
)

// Dataset is a folder of images under the storage directory datasets/.
type Dataset struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	DirName          string     `json:"dir_name"`
	Path             string     `json:"path"` // absolute folder path on the server
	Origin           string     `json:"origin"`
	DirectoryMissing bool       `json:"directory_missing"`
	ImageCount       int        `json:"image_count"`
	AnnotatedCount   int        `json:"annotated_count"` // VOC file with >= 1 object
	EmptyCount       int        `json:"empty_count"`     // VOC file with 0 objects
	InvalidCount     int        `json:"invalid_count"`   // VOC file that cannot be parsed
	ObjectCount      int        `json:"object_count"`
	LabelCount       int        `json:"label_count"`
	TotalBytes       int64      `json:"total_bytes"`
	Covers           []ImageRef `json:"covers"`        // up to 4 images for the card mosaic
	ActiveImport     *Import    `json:"active_import"` // latest uploading/queued/running job
	ScannedAt        *time.Time `json:"scanned_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ImageRef identifies an image and the version of its file, for
// cache-busting thumbnail URLs.
type ImageRef struct {
	ID      int64  `json:"id"`
	Version string `json:"version"`
}

// Image is an indexed image of a dataset.
type Image struct {
	ID        int64     `json:"id"`
	DatasetID int64     `json:"dataset_id"`
	Path      string    `json:"path"` // relative to the dataset folder, slash separated
	FileName  string    `json:"file_name"`
	SizeBytes int64     `json:"size_bytes"`
	Width     *int      `json:"width"`
	Height    *int      `json:"height"`
	ModTime   time.Time `json:"mod_time"`
	Version   string    `json:"version"`
	// AnnotationState is "none" (no VOC file), "invalid" (the VOC file
	// cannot be parsed), "empty" (no objects) or "annotated".
	AnnotationState string `json:"annotation_state"`
	// AnnotationPath is the VOC file the last scan paired with the image,
	// nil if none. An ImageDetail keeps it when that file has disappeared
	// since: the state is then "none" with an AnnotationError.
	AnnotationPath *string          `json:"annotation_path"`
	Annotation     *ImageAnnotation `json:"annotation"` // nil without a valid VOC file
}

// ImageAnnotation summarizes the VOC file paired with an image.
type ImageAnnotation struct {
	Path        string       `json:"path"`
	ObjectCount int          `json:"object_count"`
	Labels      []LabelCount `json:"labels"` // by count desc, then name
}

// LabelCount is the number of objects of one label in an image.
type LabelCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ImageDetail adds the objects parsed from the VOC file on disk. Its
// annotation fields describe the file as it is now, which may differ from
// the index until the next scan.
type ImageDetail struct {
	Image
	Objects         []AnnotationObject `json:"objects"`
	AnnotationError string             `json:"annotation_error"` // why the VOC file cannot be used, or ""
}

// Label is one label of a dataset with its totals.
type Label struct {
	Name        string `json:"name"`
	ObjectCount int    `json:"object_count"`
	ImageCount  int    `json:"image_count"`
}

// Import is an import or scan job.
type Import struct {
	ID            int64            `json:"id"`
	DatasetID     int64            `json:"dataset_id"`
	Source        string           `json:"source"`
	Status        string           `json:"status"`
	OnConflict    string           `json:"on_conflict"`
	FileName      string           `json:"file_name"`
	FileSize      int64            `json:"file_size"`
	FileCount     int              `json:"file_count"`
	ReceivedBytes int64            `json:"received_bytes"`
	ReceivedFiles int              `json:"received_files"`
	Stage         string           `json:"stage"`
	Progress      Progress         `json:"progress"`
	Summary       map[string]int64 `json:"summary"`
	Error         string           `json:"error"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
}

// Progress of the current stage of a running job.
type Progress struct {
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Unit  string `json:"unit"`
}

// Directory is a direct child folder of the storage directory datasets/.
type Directory struct {
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	Registered      bool      `json:"registered"`
	DatasetID       *int64    `json:"dataset_id"`
	ImageCount      int       `json:"image_count"`
	AnnotationCount int       `json:"annotation_count"` // .xml files
	TotalBytes      int64     `json:"total_bytes"`      // bytes of the images counted
	Approximate     bool      `json:"approximate"`      // the count stopped early
	ModifiedAt      time.Time `json:"modified_at"`
}

// Summary keys of import and scan jobs (spec §5.5).
const (
	sumImagesAdded        = "images_added"
	sumImagesReplaced     = "images_replaced"
	sumImagesSkipped      = "images_skipped"
	sumImagesUpdated      = "images_updated"
	sumImagesRemoved      = "images_removed"
	sumImagesUnchanged    = "images_unchanged"
	sumImagesTotal        = "images_total"
	sumAnnotationsMatched = "annotations_matched"
	sumAnnotationsInvalid = "annotations_invalid"
	sumAnnotationsOrphan  = "annotations_orphaned"
	sumFilesIgnored       = "files_ignored"
	sumEntriesUnsafe      = "entries_unsafe"
	sumNamesInvalid       = "names_invalid" // files and folders skipped: name not valid UTF-8
)

// fileVersion identifies the content of a file by its modification time
// (microseconds, the precision PostgreSQL stores) and size.
func fileVersion(size int64, modTime time.Time) string {
	return strconv.FormatInt(modTime.UnixMicro(), 36) + "-" + strconv.FormatInt(size, 36)
}

// dbTime truncates a file time to what a timestamptz column keeps.
func dbTime(t time.Time) time.Time { return time.UnixMicro(t.UnixMicro()).UTC() }

func fileName(rel string) string { return path.Base(rel) }
