package review

import (
	"encoding/json"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// The JSON field names of the types in this file are the API contract with
// the frontend (frontend/src/features/reviews/api.ts) and, for Task and
// the agent types, with the training node
// (trainer/trainmaster_trainer/review_inference.py); do not rename them.

// TaskKind is the kind of the inference tasks nodes claim.
const TaskKind = "review_inference"

// Review states. queued → running → ready; running → queued when the node
// gives up with a retryable error or stops reporting (at most maxAttempts
// claims), else failed; a retry queues a failed review again, which then
// continues with the images not inferred yet.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Stages of a running review, as reported by the node.
const (
	StageDownloading = "downloading"
	StageInferring   = "inferring"
)

// ModelRef is the snapshot of the model taken when the review was created.
type ModelRef = reviewfile.ModelRef

// InferenceOptions are the inference options of a review.
type InferenceOptions = reviewfile.Options

// Prediction is a box the model predicted.
type Prediction = reviewfile.Prediction

// Review is a model review (审图任务).
type Review struct {
	ID            int64            `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Model         ModelRef         `json:"model"`
	ModelExists   bool             `json:"model_exists"` // false after the model was deleted
	Project       *ProjectRef      `json:"project"`      // the model's project; nil once it is deleted
	Options       InferenceOptions `json:"options"`
	Status        string           `json:"status"`
	Stage         string           `json:"stage"` // running: downloading or inferring
	Attempts      int              `json:"attempts"`
	Node          *NodeRef         `json:"node"` // the node that claimed (or finished) it
	Error         string           `json:"error"`
	ErrorDetail   string           `json:"error_detail"`
	Result        json.RawMessage  `json:"result"`         // ready: InferenceResult, else null
	QueuePosition *int             `json:"queue_position"` // queued: position among the queued reviews, from 1
	WorkersOnline int              `json:"workers_online"` // nodes that recently claimed review inference
	Counts        Counts           `json:"counts"`
	Datasets      []Dataset        `json:"datasets"` // in selection order
	LastUpload    *Upload          `json:"last_upload"`
	ActiveMerge   *Merge           `json:"active_merge"`
	LastMerge     *Merge           `json:"last_merge"` // the newest finished merge
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
}

// ProjectRef names a project.
type ProjectRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// NodeRef names a training node.
type NodeRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Counts are the numbers of images of a review (or of one of its
// datasets), computed when read.
type Counts struct {
	Total           int `json:"total"`
	Inferred        int `json:"inferred"`
	InferenceFailed int `json:"inference_failed"`
	Pending         int `json:"pending"`
	Approved        int `json:"approved"`
	NeedsSample     int `json:"needs_sample"`
	Edited          int `json:"edited"`
	Merged          int `json:"merged"`
}

// Dataset is a dataset of a review with its folder in the review and the
// counts of its images.
type Dataset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"` // the current name (the name at creation once deleted)
	Folder string `json:"folder"`
	Counts Counts `json:"counts"`
	// NextVersion is the name the next review version of the dataset gets
	// if no other merge completes first, e.g. 审图1.3 ("" once the dataset
	// is deleted).
	NextVersion string `json:"next_version"`
}

// Image is an image of a review.
type Image struct {
	ID                int64                `json:"id"`
	ReviewID          int64                `json:"review_id"`
	DatasetID         int64                `json:"dataset_id"`
	ImageID           int64                `json:"image_id"` // the dataset image
	Position          int                  `json:"position"`
	Path              string               `json:"path"` // in the dataset, at creation
	FileName          string               `json:"file_name"`
	PackagePath       string               `json:"package_path"`
	AnnotationPath    string               `json:"annotation_path"`
	Width             *int                 `json:"width"`
	Height            *int                 `json:"height"`
	ImageVersion      string               `json:"image_version"`
	Inferred          bool                 `json:"inferred"`
	InferenceError    string               `json:"inference_error"`
	ObjectCount       int                  `json:"object_count"`
	Labels            []dataset.LabelCount `json:"labels"` // by count desc, then name
	PredictionCount   int                  `json:"prediction_count"`
	Edited            bool                 `json:"edited"`
	Status            string               `json:"status"`
	Revision          int                  `json:"revision"`
	ReviewedAt        *time.Time           `json:"reviewed_at"`
	MergedAt          *time.Time           `json:"merged_at"`
	AnnotationVersion string               `json:"annotation_version"`
}

// ImageDetail adds the boxes: the review annotation as it is in the VOC
// file now and the model's predictions.
type ImageDetail struct {
	Image
	Objects     []dataset.AnnotationObject `json:"objects"` // [] without a file
	Predictions []Prediction               `json:"predictions"`
	DatasetName string                     `json:"dataset_name"`
	// AnnotationError says why the VOC file cannot be used ("" if fine);
	// the editor is then read-only until a forced save replaces it.
	AnnotationError string `json:"annotation_error"`
}

// Neighbors locates an image among the images matching a filter, in
// review order. The image itself need not match.
type Neighbors struct {
	Prev     *Image `json:"prev"`
	Next     *Image `json:"next"`
	Position int    `json:"position"` // matching images before it
	Matches  bool   `json:"matches"`
	Total    int    `json:"total"`
}

// Label is a label of the review annotations with its totals.
type Label struct {
	Name        string `json:"name"`
	ObjectCount int    `json:"object_count"`
	ImageCount  int    `json:"image_count"`
}

// Class is a candidate class when editing boxes.
type Class = reviewfile.Class

// ClassList is the class list offered when editing the boxes of a review.
type ClassList struct {
	Items   []Class `json:"items"`
	Total   int     `json:"total"`
	Version string  `json:"version"` // of the project's class list, "" for the model's class names
}

// Problem is an image an upload or a merge could not handle.
type Problem struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // uploads: conflict, invalid, unknown; merges: skipped, failed (conflict before versions)
	Message string `json:"message"`
}

// MaxProblems bounds the problems recorded per upload or merge.
const MaxProblems = 100

// UploadSummary counts what applying uploaded results did.
type UploadSummary struct {
	Images            int `json:"images"`
	Applied           int `json:"applied"`
	StatusChanges     int `json:"status_changes"`
	AnnotationChanges int `json:"annotation_changes"`
	Unchanged         int `json:"unchanged"`
	Conflicts         int `json:"conflicts"`
	Invalid           int `json:"invalid"`
	Unknown           int `json:"unknown"`
}

// Upload is results uploaded from the local reviewer (review_uploads).
type Upload struct {
	ID        int64         `json:"id"`
	ReviewID  int64         `json:"review_id"`
	FileName  string        `json:"file_name"`
	Overwrite bool          `json:"overwrite"`
	Summary   UploadSummary `json:"summary"`
	Problems  []Problem     `json:"problems"`
	CreatedAt time.Time     `json:"created_at"`
}

// Merge states.
const (
	MergeRunning   = "running"
	MergeSucceeded = "succeeded"
	MergeFailed    = "failed"
)

// MergeSummary counts what a merge did. SkippedExisting and Conflicts
// only occur in merges made before versions existed, which wrote into the
// datasets' files.
type MergeSummary struct {
	Merged          int `json:"merged"`
	SkippedExisting int `json:"skipped_existing"`
	SkippedEmpty    int `json:"skipped_empty"`
	SkippedChanged  int `json:"skipped_changed"` // the review status changed to one not merged meanwhile
	Conflicts       int `json:"conflicts"`
	Failed          int `json:"failed"`
}

// Merge is a merge of review annotations into review versions of the
// datasets (review_merges).
type Merge struct {
	ID         int64    `json:"id"`
	ReviewID   int64    `json:"review_id"`
	DatasetIDs []int64  `json:"dataset_ids"`
	Statuses   []string `json:"statuses"`
	Note       string   `json:"note"` // of the versions made ("" before versions existed)
	// OnExisting is set (skip or overwrite) on merges made before versions
	// existed, which wrote into the datasets' VOC files; nil otherwise.
	OnExisting *string      `json:"on_existing"`
	Status     string       `json:"status"`
	Total      int          `json:"total"`
	Done       int          `json:"done"`
	Summary    MergeSummary `json:"summary"`
	Problems   []Problem    `json:"problems"`
	Error      string       `json:"error"`
	// Versions are the review versions the merge made (ready) or is making
	// (building), by dataset ID; versions deleted since are not listed.
	Versions   []dataset.VersionRef `json:"versions"`
	CreatedAt  time.Time            `json:"created_at"`
	FinishedAt *time.Time           `json:"finished_at"`
}

// DeleteResult describes a delete that removed the review but could not
// remove all of its files (the same shape as dataset.DeleteResult).
type DeleteResult struct {
	FilesRemoved bool   `json:"files_removed"` // always false
	Message      string `json:"message"`       // what is left and where, in Chinese
}

// ---- inference tasks (agent API) ----

// Task is a review inference handed to a training node by a claim.
type Task struct {
	Kind         string           `json:"kind"` // TaskKind
	ID           int64            `json:"id"`   // the review ID
	LeaseSeconds int              `json:"lease_seconds"`
	Model        TaskModel        `json:"model"`
	Options      InferenceOptions `json:"options"`
	ImageCount   int              `json:"image_count"`
	Remaining    int              `json:"remaining"` // images not inferred yet
	ModelURL     string           `json:"model_url"`
	ImagesURL    string           `json:"images_url"`
	ProgressURL  string           `json:"progress_url"`
	ResultsURL   string           `json:"results_url"`
	CompleteURL  string           `json:"complete_url"`
	FailURL      string           `json:"fail_url"`
}

// TaskModel describes the model of a Task; size_bytes and sha256 let the
// node verify the downloaded file.
type TaskModel struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	Version     int      `json:"version"`
	YOLOVersion string   `json:"yolo_version"`
	Task        string   `json:"task"`
	FileName    string   `json:"file_name"`
	SizeBytes   int64    `json:"size_bytes"`
	SHA256      string   `json:"sha256"`
	ClassNames  []string `json:"class_names"`
}

// TaskImage is an image a node still has to infer.
type TaskImage struct {
	ID        int64  `json:"id"` // the review image
	FileName  string `json:"file_name"`
	Width     *int   `json:"width"`
	Height    *int   `json:"height"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url"`
}

// TaskImages is a page of the images a node still has to infer.
type TaskImages struct {
	Items     []TaskImage `json:"items"`
	Remaining int         `json:"remaining"` // all images not inferred yet, this page included
}

// ImageResult is a node's result for one image: the boxes (OK) or why the
// image could not be inferred.
type ImageResult struct {
	ID     int64       `json:"id"`
	OK     bool        `json:"ok"`
	Width  int         `json:"width"`  // as decoded by the node (0: not given)
	Height int         `json:"height"` //
	Boxes  []ResultBox `json:"boxes"`
	Error  string      `json:"error"` // not OK: in Chinese, shown to users
}

// ResultBox is a box as the node reports it, in pixels of the image as
// stored.
type ResultBox struct {
	Name       string  `json:"name"`
	ClassID    *int    `json:"class_id"`
	Confidence float64 `json:"confidence"`
	Xmin       float64 `json:"xmin"`
	Ymin       float64 `json:"ymin"`
	Xmax       float64 `json:"xmax"`
	Ymax       float64 `json:"ymax"`
}

// InferenceResult is what a node reports about a finished inference,
// after cleaning. Unknown values are null.
type InferenceResult struct {
	Device             string   `json:"device"`
	TorchVersion       string   `json:"torch_version"`
	UltralyticsVersion string   `json:"ultralytics_version"`
	NodeName           string   `json:"node_name"`
	ElapsedSeconds     *float64 `json:"elapsed_seconds"`
	ImagesPerSecond    *float64 `json:"images_per_second"`
}
