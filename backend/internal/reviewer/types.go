// Package reviewer is the local reviewer (本地审图程序): a small web server
// on 127.0.0.1 that opens a review package downloaded from the platform
// (a zip, extracted next to it, or a folder) and serves the same review
// API as the platform (below /api/review), so that the platform's review
// editor works on it unchanged; plus a few local endpoints to browse for
// packages, open and close them and export the results to upload back.
//
// Everything a user does is kept in the package folder: the VOC files are
// rewritten atomically on every save, and the review statuses go to
// review-progress.json, so closing the program (or a crash) loses
// nothing and opening the package again continues where it stopped. The
// program cmd/trainmaster-reviewer puts the server in a window.
package reviewer

import (
	"errors"
	"fmt"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// The JSON field names of the review types in this file are the API
// contract with the platform's review editor
// (frontend/src/features/reviews/api.ts): they are those of package review
// (internal/review/types.go), which this package does not import (it
// would bring the database into the program). A test compares them. The
// local types (Session, ...) are the contract with frontend/src/reviewer.

// Counts are the numbers of images of the package (or of one of its
// datasets). Every image of a package was inferred.
type Counts struct {
	Total           int `json:"total"`
	Inferred        int `json:"inferred"`
	InferenceFailed int `json:"inference_failed"`
	Pending         int `json:"pending"`
	Approved        int `json:"approved"`
	NeedsSample     int `json:"needs_sample"`
	Edited          int `json:"edited"`
	Merged          int `json:"merged"` // always 0: merging happens on the platform
}

// Dataset is a dataset of the package with the counts of its images.
type Dataset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Counts Counts `json:"counts"`
	// NextVersion is "" here: the editor does not merge into datasets.
	NextVersion string `json:"next_version"`
}

// Summary is what the review editor needs to know about the review
// (GET /api/review): the fields of the platform's Review it reads.
type Summary struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Model       reviewfile.ModelRef `json:"model"`
	Project     *struct{}           `json:"project"` // always null: the platform's links are not offered
	Options     reviewfile.Options  `json:"options"`
	Status      string              `json:"status"` // always "ready"
	Stage       string              `json:"stage"`  // always ""
	Counts      Counts              `json:"counts"`
	Datasets    []Dataset           `json:"datasets"`
	CreatedAt   time.Time           `json:"created_at"`
}

// Image is an image of the package, as the platform lists review images.
type Image struct {
	ID                int64                `json:"id"`
	ReviewID          int64                `json:"review_id"`
	DatasetID         int64                `json:"dataset_id"`
	ImageID           int64                `json:"image_id"`
	Position          int                  `json:"position"`
	Path              string               `json:"path"` // in the dataset
	FileName          string               `json:"file_name"`
	PackagePath       string               `json:"package_path"`
	AnnotationPath    string               `json:"annotation_path"`
	Width             *int                 `json:"width"`
	Height            *int                 `json:"height"`
	ImageVersion      string               `json:"image_version"` // of the file in the folder ("" missing)
	Inferred          bool                 `json:"inferred"`      // always true
	InferenceError    string               `json:"inference_error"`
	ObjectCount       int                  `json:"object_count"`
	Labels            []dataset.LabelCount `json:"labels"` // by count desc, then name
	PredictionCount   int                  `json:"prediction_count"`
	Edited            bool                 `json:"edited"`
	Status            string               `json:"status"`
	Revision          int                  `json:"revision"`
	ReviewedAt        *time.Time           `json:"reviewed_at"`
	MergedAt          *time.Time           `json:"merged_at"` // always null
	AnnotationVersion string               `json:"annotation_version"`
}

// ImageDetail adds the boxes: the annotation as the VOC file has it now and
// the model's predictions.
type ImageDetail struct {
	Image
	Objects         []dataset.AnnotationObject `json:"objects"`
	Predictions     []reviewfile.Prediction    `json:"predictions"`
	DatasetName     string                     `json:"dataset_name"`
	AnnotationError string                     `json:"annotation_error"`
}

// Neighbors locates an image among the images matching a filter, in
// review order. The image itself need not match.
type Neighbors struct {
	Prev     *Image `json:"prev"`
	Next     *Image `json:"next"`
	Position int    `json:"position"`
	Matches  bool   `json:"matches"`
	Total    int    `json:"total"`
}

// Label is a label of the annotations with its totals.
type Label struct {
	Name        string `json:"name"`
	ObjectCount int    `json:"object_count"`
	ImageCount  int    `json:"image_count"`
}

// ClassList is the class list offered when editing boxes: the candidate
// classes of the package.
type ClassList struct {
	Items   []reviewfile.Class `json:"items"`
	Total   int                `json:"total"`
	Version string             `json:"version"` // always ""
}

// ---- errors ----

// Error kinds. Every error for a problem the user can act on is an *Error
// wrapping one of these; its message is in Chinese and shown as is.
var (
	ErrNotFound          = errors.New("not found")
	ErrInvalid           = errors.New("invalid input")
	ErrBadRequest        = errors.New("malformed request")
	ErrConflict          = errors.New("conflict")
	ErrAnnotationChanged = errors.New("annotation changed")
	ErrForbidden         = errors.New("forbidden")
	ErrNoSpace           = errors.New("insufficient storage")
	ErrUnsupported       = errors.New("not supported")
	ErrIO                = errors.New("file error") // reading or writing a file of the user failed; the message says which and why
)

// Error is a user-facing error.
type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Kind }

func newError(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

var (
	errNoPackage = &Error{Kind: ErrNotFound, Message: "没有打开审图包，请先在开始页打开一个审图包"}
	// errPackageSwitched: a write for a package that is no longer open.
	errPackageSwitched = &Error{Kind: ErrConflict, Message: "程序中已经打开了另一个审图包，这次修改没有保存；请回到开始页重新打开要审的审图包"}
	errImageMissing    = &Error{Kind: ErrNotFound, Message: "图片不存在"}
	errFileMissing     = &Error{Kind: ErrNotFound, Message: "图片文件不存在（审图包中没有它，或者已被移动或删除）"}
	// errAnnotationChanged: the VOC file changed since the version an edit
	// started from (another window, or another program such as LabelImg).
	errAnnotationChanged = &Error{Kind: ErrAnnotationChanged,
		Message: "这张图片的标注文件已被修改（可能在其他窗口或程序中修改过），请重新加载后再编辑，或选择覆盖"}
)
