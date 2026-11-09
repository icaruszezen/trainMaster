package dataversion

import (
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// The JSON field names of the types in this file are the API contract with
// the frontend (frontend/src/features/data-versions/api.ts); do not rename
// them.

// Version states. queued → building → ready | failed; a failed version can
// be queued again (Retry). Only ready versions go to the recycle bin.
const (
	StatusQueued   = "queued"
	StatusBuilding = "building"
	StatusReady    = "ready"
	StatusFailed   = "failed"
)

// Stages of a build.
const (
	StagePreparing  = "preparing"  // reading the datasets' images and annotations
	StageLinking    = "linking"    // linking or copying the images into the version folder
	StageAugmenting = "augmenting" // generating augmented copies
	StageFinishing  = "finishing"  // writing the lists and the manifest
)

// Splits.
const (
	SplitTrain = "train"
	SplitVal   = "val"
	SplitTest  = "test"
)

// Splits lists the splits in display order.
var Splits = []string{SplitTrain, SplitVal, SplitTest}

// How an image got its split.
const (
	SplitByRatio  = "ratio"  // hashed into a split by the configured ratios
	SplitByFolder = "folder" // its path is in a train / val / test folder (use_folders)
)

// How images are grouped so that related images share a split.
const (
	GroupByStem   = "stem"   // the same normalized file name in the same folder (and duplicates)
	GroupByFolder = "folder" // every image of a leaf folder (one part or batch)
)

// How an image file is stored in the version folder.
const (
	StoredLink      = "link"      // hard link to the dataset file
	StoredCopy      = "copy"      // copy of the dataset file
	StoredGenerated = "generated" // augmented copy
)

// Scopes (参与训练的图片): which images of a dataset a version takes. A
// version has a default scope (Config.Scope); each source can choose
// another one.
const (
	// ScopeAnnotated takes the images that have an annotation: those with
	// objects and, with Config.IncludeEmpty, those whose annotation has
	// none (仅有标注的图片). Images without an annotation are skipped.
	ScopeAnnotated = "annotated"
	// ScopeBackground takes only the images without objects, as negative
	// samples: those without an annotation and those whose annotation has
	// no objects (仅无标注的背景图). Images with objects are skipped.
	ScopeBackground = "background"
	// ScopeAll takes both: every image, those without objects (with or
	// without an annotation) as negative samples (整个数据集).
	ScopeAll = "all"
)

// Scopes lists the scopes in display order.
var Scopes = []string{ScopeAnnotated, ScopeBackground, ScopeAll}

// Download formats.
const (
	FormatYOLO = "yolo"
	FormatVOC  = "voc"
	FormatCOCO = "coco"
)

// DataVersion is a frozen training set of a project.
type DataVersion struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name"` // the project's current name
	Version     int    `json:"version"`      // within the project: 1, 2, ...; never given again
	Note        string `json:"note"`
	Status      string `json:"status"`
	Error       string `json:"error"` // failed: why, shown to users
	// Progress is set while building.
	Progress *Progress `json:"progress"`
	// QueuePosition is set while queued: 1 is built next.
	QueuePosition *int `json:"queue_position"`

	Classes      []Class  `json:"classes"`       // the project's class list when it was created; order = YOLO class id
	ClassVersion string   `json:"class_version"` // hash of Classes, like project.ClassList.Version
	Config       Config   `json:"config"`
	Sources      []Source `json:"sources"`

	// Counts, 0 until ready. ImageCount, ObjectCount and TrainCount
	// include the augmented copies.
	ImageCount     int   `json:"image_count"`
	ObjectCount    int   `json:"object_count"`
	TrainCount     int   `json:"train_count"`
	ValCount       int   `json:"val_count"`
	TestCount      int   `json:"test_count"`
	AugmentedCount int   `json:"augmented_count"`
	TotalBytes     int64 `json:"total_bytes"`
	Stats          Stats `json:"stats"` // always complete; zeros until ready

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Class is an entry of the frozen class list (project.Class).
type Class struct {
	Name  string `json:"name"`
	Color string `json:"color"` // "#rrggbb", or "" for the color derived from the name
}

// Progress of a build.
type Progress struct {
	Stage string `json:"stage"`
	Done  int    `json:"done"`
	Total int    `json:"total"` // 0 while unknown
}

// Config is the normalized configuration of a version.
type Config struct {
	Split SplitConfig `json:"split"`
	// Scope is the default scope of the sources (Source.Scope is the one a
	// source uses). Versions created before scopes have ScopeAnnotated.
	Scope string `json:"scope"`
	// IncludeEmpty takes the images of ScopeAnnotated sources whose
	// annotation has no objects (negative samples); the other scopes take
	// the images without objects anyway.
	IncludeEmpty bool          `json:"include_empty"`
	IgnoreLabels []string      `json:"ignore_labels"` // labels not in the class list whose objects are dropped; sorted
	Augmentation *Augmentation `json:"augmentation"`  // nil: none
}

// SplitConfig is the split of the images, in percent.
type SplitConfig struct {
	Train      int    `json:"train"` // >= 1
	Val        int    `json:"val"`   // >= 1
	Test       int    `json:"test"`  // >= 0; train + val + test = 100
	Seed       int64  `json:"seed"`  // 0..2147483647
	UseFolders bool   `json:"use_folders"`
	GroupBy    string `json:"group_by"` // GroupByStem or GroupByFolder
}

// Augmentation generates augmented copies of the train images.
type Augmentation struct {
	Methods         []string `json:"methods"`           // augment.Method values, in augment.Methods order
	Copies          int      `json:"copies"`            // per train image, 1..augment.MaxCopies(methods)
	OnlyWithObjects bool     `json:"only_with_objects"` // negatives get no copies
}

// Source is a dataset a version takes images from, as it was when the
// version was created.
type Source struct {
	DatasetID   int64           `json:"dataset_id"`
	DatasetName string          `json:"dataset_name"`
	DirName     string          `json:"dir_name"` // its folder: the images are under images/<dir_name>/
	IncludeBase bool            `json:"include_base"`
	Versions    []SourceVersion `json:"versions"` // review versions, newest first
	Scope       string          `json:"scope"`    // the scope it uses: its own or the version's default
	// ImageCount is the originals taken from it (0 until ready).
	ImageCount int `json:"image_count"`
	// DatasetDeleted: the dataset is no longer in use (in the recycle bin
	// or purged). Computed when read, not stored.
	DatasetDeleted bool `json:"dataset_deleted"`
}

// SourceVersion is a review version of a source.
type SourceVersion struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // 审图1.2, as when the version was created
}

// SplitCounts is a count per split.
type SplitCounts struct {
	Train int `json:"train"`
	Val   int `json:"val"`
	Test  int `json:"test"`
}

// ObjectImages counts the objects of a class in one split and the images
// that have them.
type ObjectImages struct {
	Objects int `json:"objects"`
	Images  int `json:"images"`
}

// LabelStats is a class of the class list with its objects per split.
type LabelStats struct {
	Name  string       `json:"name"`
	Color string       `json:"color"`
	Train ObjectImages `json:"train"`
	Val   ObjectImages `json:"val"`
	Test  ObjectImages `json:"test"`
}

// Skipped counts the images of the sources a version did not take, by
// reason.
type Skipped struct {
	Unannotated       int `json:"unannotated"`        // no annotation
	Invalid           int `json:"invalid"`            // annotation file that cannot be read
	Unlisted          int `json:"unlisted"`           // labels neither in the class list nor ignored
	Empty             int `json:"empty"`              // negative samples left out (IncludeEmpty false)
	WithObjects       int `json:"with_objects"`       // images with objects of a ScopeBackground source
	Missing           int `json:"missing"`            // file gone since the last scan (build only)
	Changed           int `json:"changed"`            // file changed since the last scan (build only)
	UnknownSize       int `json:"unknown_size"`       // image size unknown (cannot be decoded)
	UnsupportedFormat int `json:"unsupported_format"` // a format training tools do not read (GIF, AVIF)
	AugmentSkipped    int `json:"augment_skipped"`    // train images with fewer copies than asked, or none (build only; counted once per image)
}

// StorageStats describes how the image files are stored.
type StorageStats struct {
	Linked           int   `json:"linked"`
	Copied           int   `json:"copied"`
	Generated        int   `json:"generated"`
	OrientationFixed int   `json:"orientation_fixed"` // copies whose EXIF / TIFF orientation tag was reset to 1
	CopiedBytes      int64 `json:"copied_bytes"`
	GeneratedBytes   int64 `json:"generated_bytes"`
}

// Compare compares the originals of a version with those of an earlier
// version of the project, matched by (dataset, path in the dataset).
type Compare struct {
	ID                int64 `json:"id"`
	Version           int   `json:"version"`
	Added             int   `json:"added"`              // here, not there
	Removed           int   `json:"removed"`            // there, not here
	SplitChanged      int   `json:"split_changed"`      // in both, another split
	AnnotationChanged int   `json:"annotation_changed"` // in both, other objects (0 in a preview)
}

// Stats describes a ready version.
type Stats struct {
	Labels            []LabelStats `json:"labels"`    // the class list, in order; originals only
	Negatives         SplitCounts  `json:"negatives"` // originals without objects
	ByFolder          int          `json:"by_folder"` // originals whose split came from their folder
	Skipped           Skipped      `json:"skipped"`
	DroppedObjects    int          `json:"dropped_objects"`    // objects of ignored labels removed
	DegenerateObjects int          `json:"degenerate_objects"` // boxes with no area after clipping, removed
	Storage           StorageStats `json:"storage"`
	Compare           *Compare     `json:"compare"` // nil: no earlier ready version
}

// ---- creating ----

// CreateInput is a new version. The server requires every field (the
// defaults are the UI's).
type CreateInput struct {
	ProjectID    int64         `json:"project_id"`
	Note         string        `json:"note"`
	ClassVersion string        `json:"class_version"` // the class list version the client showed
	Sources      []SourceInput `json:"sources"`
	Split        SplitConfig   `json:"split"`
	Scope        string        `json:"scope"` // the default scope of the sources
	IncludeEmpty bool          `json:"include_empty"`
	IgnoreLabels []string      `json:"ignore_labels"`
	Augmentation *Augmentation `json:"augmentation"`
}

// SourceInput selects a dataset of the project and the versions of its
// annotations (see dataset.VersionSelection).
type SourceInput struct {
	DatasetID   int64   `json:"dataset_id"`
	IncludeBase bool    `json:"include_base"`
	VersionIDs  []int64 `json:"version_ids"`
	Scope       string  `json:"scope"` // a scope of its own; "": CreateInput.Scope
}

// Preview is what a version created with an input would contain, counted
// from the datasets' index.
type Preview struct {
	Classes           []Class         `json:"classes"`       // the project's current class list
	ClassVersion      string          `json:"class_version"` // its version (send it with Create)
	ImageCount        int             `json:"image_count"`   // originals
	ObjectCount       int             `json:"object_count"`
	AugmentedCount    int             `json:"augmented_count"` // estimate
	Sources           []PreviewSource `json:"sources"`
	Skipped           Skipped         `json:"skipped"` // Missing, Changed and AugmentSkipped are known only when building
	DroppedObjects    int             `json:"dropped_objects"`
	DegenerateObjects int             `json:"degenerate_objects"`
	Splits            PreviewSplits   `json:"splits"`
	Labels            []LabelStats    `json:"labels"`          // the class list, in order
	UnlistedLabels    []UnlistedLabel `json:"unlisted_labels"` // labels of the selection not in the class list, by objects desc, then name
	Compare           *Compare        `json:"compare"`         // vs the newest ready version of the project
}

// PreviewSource is a source with its counts.
type PreviewSource struct {
	DatasetID     int64  `json:"dataset_id"`
	DatasetName   string `json:"dataset_name"`
	Scope         string `json:"scope"`          // the scope it uses
	SelectedCount int    `json:"selected_count"` // images of its selection
	FromVersions  int    `json:"from_versions"`  // of them, with the annotation of a review version
	ImageCount    int    `json:"image_count"`    // of them, those taken
	Negatives     int    `json:"negatives"`      // of those, without objects
	ByFolder      int    `json:"by_folder"`      // of those, split by their folder
}

// PreviewSplit counts the originals of one split.
type PreviewSplit struct {
	Images    int `json:"images"`
	Objects   int `json:"objects"`
	Negatives int `json:"negatives"`
	ByFolder  int `json:"by_folder"`
}

// PreviewSplits is a PreviewSplit per split.
type PreviewSplits struct {
	Train PreviewSplit `json:"train"`
	Val   PreviewSplit `json:"val"`
	Test  PreviewSplit `json:"test"`
}

// UnlistedLabel is a label of the selected images that is not in the
// class list.
type UnlistedLabel struct {
	Name    string `json:"name"`
	Objects int    `json:"objects"`
	Images  int    `json:"images"`
	// OtherProject: the label is in the class list of another project in
	// use that shares one of the selected datasets (ignoring it is
	// usually right).
	OtherProject bool `json:"other_project"`
}

// ---- listing ----

// ListFilter selects versions; zero values select all.
type ListFilter struct {
	ProjectID int64
}

// UpdateInput holds the fields to change; nil leaves a field alone.
type UpdateInput struct {
	Note *string
}

// ---- images ----

// Image is an image of a ready version.
type Image struct {
	ID               int64                `json:"id"`
	Position         int                  `json:"position"` // ascending, not dense
	Split            string               `json:"split"`
	SplitBy          string               `json:"split_by"`
	Path             string               `json:"path"`      // in the version folder
	FileName         string               `json:"file_name"` // base name of Path
	Width            int                  `json:"width"`
	Height           int                  `json:"height"`
	SizeBytes        int64                `json:"size_bytes"`
	ObjectCount      int                  `json:"object_count"`
	Labels           []dataset.LabelCount `json:"labels"` // by count desc, then name
	DatasetID        int64                `json:"dataset_id"`
	DatasetName      string               `json:"dataset_name"` // from the version's sources
	SourcePath       string               `json:"source_path"`  // in the dataset folder
	AnnotationSource *SourceVersion       `json:"annotation_source"`
	AugmentedFrom    *int64               `json:"augmented_from"`
	Augment          []string             `json:"augment"` // methods applied, [] for originals
	Stored           string               `json:"stored"`
	ThumbnailURL     string               `json:"thumbnail_url"`
	FileURL          string               `json:"file_url"`
}

// ImageDetail is an image with its objects.
type ImageDetail struct {
	Image
	Objects []dataset.AnnotationObject `json:"objects"` // [] for a negative sample
}

// ImageFilter selects the images of a version; zero values select all.
type ImageFilter struct {
	Split     string // "" or a split
	Label     string // images with at least one object of this label
	DatasetID int64
	Augmented *bool  // true: augmented copies only; false: originals only
	Search    string // substring of the path or the source path (case-insensitive)
	Offset    int
	Limit     int // 1..200, default 60
}

// ---- recycle bin ----

// TrashedDataVersion is a version in the recycle bin.
type TrashedDataVersion struct {
	DataVersion
	DeletedAt time.Time `json:"deleted_at"`
	PurgeAt   time.Time `json:"purge_at"`
	// ProjectDeleted: the project went to the bin with it (deleted while it
	// had data versions or models in the bin); restoring the version
	// restores the project.
	ProjectDeleted bool `json:"project_deleted"`
}

// RestoreInput holds the optional new name of the version's project, when
// the project is in the recycle bin and its name is taken meanwhile.
type RestoreInput struct {
	ProjectName *string
}

// ---- downloads ----

// ExportInput is a download to prepare.
type ExportInput struct {
	Format           string `json:"format"`            // FormatYOLO, FormatVOC or FormatCOCO
	IncludeAugmented bool   `json:"include_augmented"` // the augmented copies too
}

// ExportTicket is a prepared download.
type ExportTicket struct {
	Token     string
	FileName  string
	ExpiresAt time.Time
}
