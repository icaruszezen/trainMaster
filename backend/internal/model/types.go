package model

import (
	"encoding/json"
	"time"
)

// The JSON field names of the types in this file are the API contract with
// the frontend (frontend/src/features/models/api.ts) and, for Task and
// ExportResult, with the training node (trainer/trainmaster_trainer); do
// not rename them.

// Model sources.
const (
	SourceUpload   = "upload"   // uploaded by a user
	SourceTraining = "training" // produced by a training job on the platform (RegisterTrainedModel)
)

// Upload states. uploading → completed | failed | canceled. A commit whose
// file holds another YOLO version than the chosen one leaves the upload
// uploading (with detected_yolo_version), so it can be committed again.
const (
	UploadUploading = "uploading"
	UploadCompleted = "completed"
	UploadFailed    = "failed"
	UploadCanceled  = "canceled"
)

// Export states. queued → running → succeeded | failed; a running export
// goes back to queued when its node gives up with a retryable error or
// stops reporting (lease expiry), at most maxAttempts times. A failed
// export, or a succeeded one whose file is gone, is queued again when
// someone requests it again.
const (
	ExportQueued    = "queued"
	ExportRunning   = "running"
	ExportSucceeded = "succeeded"
	ExportFailed    = "failed"
)

// Stages of a running export, as reported by the node.
const (
	StageDownloading = "downloading"
	StageConverting  = "converting"
	StageUploading   = "uploading"
)

// FormatONNX is the only conversion format so far.
const FormatONNX = "onnx"

// Postprocessing modes of an ONNX export.
const (
	PostprocessRaw     = "raw"     // raw head outputs; the user runs NMS (ultralytics nms=None)
	PostprocessNMS     = "nms"     // NMS embedded in the graph (nms=True)
	PostprocessEnd2End = "end2end" // the end-to-end (NMS-free) head (nms=False); YOLOv10, YOLO26
)

// Task kinds a training node can claim (POST /api/v1/compute/agent/tasks/claim).
// Training jobs will add theirs.
const TaskModelExport = "model_export"

// Model is a weights file in the repository, bound to one project.
type Model struct {
	ID            int64      `json:"id"`
	Project       ProjectRef `json:"project"`
	Version       int        `json:"version"` // within the project: v1, v2, ...; never reused
	Description   string     `json:"description"`
	Source        string     `json:"source"`          // SourceUpload or SourceTraining
	TrainingJobID *int64     `json:"training_job_id"` // SourceTraining: the job that produced it
	YOLOVersion   string     `json:"yolo_version"`
	Task          string     `json:"task"`      // detect, segment, classify, pose, obb, ... ("" unknown)
	FileName      string     `json:"file_name"` // original file name, e.g. best.pt
	SizeBytes     int64      `json:"size_bytes"`
	SHA256        string     `json:"sha256"`
	Info          Info       `json:"info"`
	FileMissing   bool       `json:"file_missing"` // model.pt is not on disk (any more)
	ExportCount   int        `json:"export_count"` // succeeded conversions
	DownloadURL   string     `json:"download_url"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ProjectRef names the project of a model.
type ProjectRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Info is the metadata read from the weights file when the model was
// added (models.info). Unknown numbers are null.
type Info struct {
	Task                string             `json:"task"`
	ModelClass          string             `json:"model_class"` // e.g. DetectionModel
	Head                string             `json:"head"`        // class of the last layer: Detect, v10Detect, Segment, ...
	YAMLFile            string             `json:"yaml_file"`   // e.g. yolo11n.yaml
	Scale               string             `json:"scale"`       // n, s, m, l, x, ...
	DetectedYOLOVersion *string            `json:"detected_yolo_version"`
	VersionSource       string             `json:"version_source"` // "yaml_file", "architecture" or ""
	ClassNames          []string           `json:"class_names"`    // by class id
	ImageSize           *int               `json:"image_size"`     // training image size
	Channels            int                `json:"channels"`
	Parameters          *int64             `json:"parameters"`
	End2End             bool               `json:"end2end"` // has an end-to-end (NMS-free) head
	UltralyticsVersion  string             `json:"ultralytics_version"`
	TrainedAt           string             `json:"trained_at"` // as saved in the file
	Epochs              *int               `json:"epochs"`
	Epoch               *int               `json:"epoch"` // -1: final weights
	BestFitness         *float64           `json:"best_fitness"`
	Metrics             map[string]float64 `json:"metrics"` // e.g. "metrics/mAP50(B)"
	TrainData           string             `json:"train_data"`
	BaseModel           string             `json:"base_model"`          // e.g. yolo11n.pt
	TrainingCheckpoint  bool               `json:"training_checkpoint"` // resumable (has optimizer state)
}

// Upload is a chunked upload of a weights file that becomes a model when
// it is committed.
type Upload struct {
	ID                  int64     `json:"id"`
	ProjectID           int64     `json:"project_id"`
	YOLOVersion         string    `json:"yolo_version"`
	Description         string    `json:"description"`
	FileName            string    `json:"file_name"`
	FileSize            int64     `json:"file_size"`
	ReceivedBytes       int64     `json:"received_bytes"`
	Status              string    `json:"status"`
	DetectedYOLOVersion *string   `json:"detected_yolo_version"` // found by a commit that was refused for it
	Error               string    `json:"error"`
	ModelID             *int64    `json:"model_id"` // completed: the model (null once that is deleted)
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// ExportOptions are the normalized options of an ONNX conversion.
type ExportOptions struct {
	ImgSize     [2]int `json:"imgsz"` // [height, width]
	Dynamic     bool   `json:"dynamic"`
	Postprocess string `json:"postprocess"`
	Opset       *int   `json:"opset"` // nil: chosen by Ultralytics
}

// Export is a conversion of a model (and the cache entry of its result).
type Export struct {
	ID            int64           `json:"id"`
	ModelID       int64           `json:"model_id"`
	Format        string          `json:"format"`
	Options       ExportOptions   `json:"options"`
	Status        string          `json:"status"`
	Stage         string          `json:"stage"` // running: downloading, converting or uploading
	Attempts      int             `json:"attempts"`
	Node          *NodeRef        `json:"node"`           // the node that claimed (or finished) it
	QueuePosition *int            `json:"queue_position"` // queued: position in the queue, from 1
	WorkersOnline int             `json:"workers_online"` // nodes that recently claimed conversions
	FileName      string          `json:"file_name"`      // download file name
	SizeBytes     *int64          `json:"size_bytes"`     // succeeded
	SHA256        string          `json:"sha256"`         // succeeded
	Result        json.RawMessage `json:"result"`         // succeeded: ExportResult
	Error         string          `json:"error"`
	ErrorDetail   string          `json:"error_detail"`
	DownloadURL   *string         `json:"download_url"` // succeeded
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	StartedAt     *time.Time      `json:"started_at"`
	FinishedAt    *time.Time      `json:"finished_at"`
}

// NodeRef names a training node.
type NodeRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Task is a conversion handed to a training node by a claim.
type Task struct {
	Kind         string        `json:"kind"` // TaskModelExport
	ID           int64         `json:"id"`   // the export ID
	LeaseSeconds int           `json:"lease_seconds"`
	Model        TaskModel     `json:"model"`
	Options      ExportOptions `json:"options"`
	InputURL     string        `json:"input_url"`
	ProgressURL  string        `json:"progress_url"`
	OutputURL    string        `json:"output_url"`
	CompleteURL  string        `json:"complete_url"`
	FailURL      string        `json:"fail_url"`
}

// TaskModel describes the model of a Task; size_bytes and sha256 let the
// node verify the downloaded file.
type TaskModel struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Version     int    `json:"version"`
	YOLOVersion string `json:"yolo_version"`
	Task        string `json:"task"`
	FileName    string `json:"file_name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	Info        Info   `json:"info"`
}

// ExportResult is what a node reports about a finished conversion, after
// cleaning (sanitizeResult). Unknown values are null.
type ExportResult struct {
	UltralyticsVersion string       `json:"ultralytics_version"`
	TorchVersion       string       `json:"torch_version"`
	ONNXVersion        string       `json:"onnx_version"`
	ONNXRuntimeVersion string       `json:"onnxruntime_version"`
	Opset              *int64       `json:"opset"`
	End2End            *bool        `json:"end2end"`
	NMS                *bool        `json:"nms"`
	Dynamic            *bool        `json:"dynamic"`
	Inputs             []TensorInfo `json:"inputs"`
	Outputs            []TensorInfo `json:"outputs"`
	Verified           *bool        `json:"verified"` // a test inference with onnxruntime passed
	DurationMS         *int64       `json:"duration_ms"`
	NodeName           string       `json:"node_name"`
}

// TensorInfo is an input or output of the ONNX graph. Shape elements are
// numbers, or strings for dynamic dimensions (e.g. "batch").
type TensorInfo struct {
	Name  string `json:"name"`
	Shape []any  `json:"shape"`
	Type  string `json:"type"`
}

// DeleteResult describes a delete that removed the model (or models) but
// could not remove all of the files. The JSON field names are part of the
// API (the same shape as dataset.DeleteResult).
type DeleteResult struct {
	FilesRemoved bool   `json:"files_removed"` // always false
	Message      string `json:"message"`       // what is left and where, in Chinese
}
