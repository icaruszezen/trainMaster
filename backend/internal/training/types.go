package training

import (
	"encoding/json"
	"io"
	"time"
)

// TaskKind is the compute node claim kind of training jobs.
const TaskKind = "training_job"

// Job statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Running stages reported by the compute node.
const (
	StagePreparing   = "preparing"
	StageDownloading = "downloading"
	StageTraining    = "training"
	StageUploading   = "uploading"
)

// Weight variants produced by a training run.
const (
	VariantBest = "best"
	VariantLast = "last"
)

// DataVersionRef snapshots the data version a training job was created with.
type DataVersionRef struct {
	ID             int64    `json:"id"`
	Version        int      `json:"version"`
	DirName        string   `json:"dir_name"`
	ClassNames     []string `json:"class_names"`
	ImageCount     int      `json:"image_count"`
	TrainCount     int      `json:"train_count"`
	ValCount       int      `json:"val_count"`
	TestCount      int      `json:"test_count"`
	AugmentedCount int      `json:"augmented_count"`
}

// BaseModelRef snapshots the optional base model used for finetune or resume.
type BaseModelRef struct {
	ID                 int64  `json:"id"`
	ProjectID          int64  `json:"project_id"`
	ProjectName        string `json:"project_name"`
	Version            int    `json:"version"`
	YOLOVersion        string `json:"yolo_version"`
	Task               string `json:"task"`
	Scale              string `json:"scale"`
	FileName           string `json:"file_name"`
	SizeBytes          int64  `json:"size_bytes"`
	SHA256             string `json:"sha256"`
	TrainingCheckpoint bool   `json:"training_checkpoint"`
}

// RegisteredModelRef summarizes a model registered into the model repository
// from best.pt or last.pt.
type RegisteredModelRef struct {
	ID          int64  `json:"id"`
	Version     int    `json:"version"`
	YOLOVersion string `json:"yolo_version"`
}

// NodeRef identifies the compute node running or having finished a job.
type NodeRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

// SummaryMetrics holds the latest and best training/validation metrics of a job.
type SummaryMetrics struct {
	TrainBoxLoss *float64 `json:"train_box_loss,omitempty"`
	TrainClsLoss *float64 `json:"train_cls_loss,omitempty"`
	TrainDFLLoss *float64 `json:"train_dfl_loss,omitempty"`
	ValBoxLoss   *float64 `json:"val_box_loss,omitempty"`
	ValClsLoss   *float64 `json:"val_cls_loss,omitempty"`
	ValDFLLoss   *float64 `json:"val_dfl_loss,omitempty"`
	Precision    *float64 `json:"precision,omitempty"`
	Recall       *float64 `json:"recall,omitempty"`
	MAP50        *float64 `json:"map50,omitempty"`
	MAP50_95     *float64 `json:"map50_95,omitempty"`
	Fitness      *float64 `json:"fitness,omitempty"`
	BestFitness  *float64 `json:"best_fitness,omitempty"`
	BestEpoch    *int     `json:"best_epoch,omitempty"`
	BestMAP50    *float64 `json:"best_map50,omitempty"`
	BestMAP50_95 *float64 `json:"best_map50_95,omitempty"`
	LR           *float64 `json:"lr,omitempty"`
	GPUMemGB     *float64 `json:"gpu_mem_gb,omitempty"`
}

// EpochMetrics records one completed epoch of a training job.
type EpochMetrics struct {
	Epoch        int      `json:"epoch"`
	TotalEpochs  int      `json:"total_epochs"`
	TrainBoxLoss *float64 `json:"train_box_loss,omitempty"`
	TrainClsLoss *float64 `json:"train_cls_loss,omitempty"`
	TrainDFLLoss *float64 `json:"train_dfl_loss,omitempty"`
	ValBoxLoss   *float64 `json:"val_box_loss,omitempty"`
	ValClsLoss   *float64 `json:"val_cls_loss,omitempty"`
	ValDFLLoss   *float64 `json:"val_dfl_loss,omitempty"`
	Precision    *float64 `json:"precision,omitempty"`
	Recall       *float64 `json:"recall,omitempty"`
	MAP50        *float64 `json:"map50,omitempty"`
	MAP50_95     *float64 `json:"map50_95,omitempty"`
	Fitness      *float64 `json:"fitness,omitempty"`
	LR           *float64 `json:"lr,omitempty"`
	GPUMemGB     *float64 `json:"gpu_mem_gb,omitempty"`
	DurationSec  *float64 `json:"duration_sec,omitempty"`
}

// Artifact describes a file saved under runs/job-<id>/.
type Artifact struct {
	Name      string `json:"name"`
	RelPath   string `json:"rel_path"`
	Kind      string `json:"kind"` // weight, plot, csv, yaml, log
	SizeBytes int64  `json:"size_bytes"`
}

// Job is a YOLO training job returned by the API. Epochs is populated in Get
// (and omitted or empty in List).
type Job struct {
	ID              int64               `json:"id"`
	ProjectID       int64               `json:"project_id"`
	ProjectName     string              `json:"project_name"`
	DataVersionID   int64               `json:"data_version_id"`
	DataVersionLive bool                `json:"data_version_live"`
	DataVersion     DataVersionRef      `json:"data_version"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	InitMode        string              `json:"init_mode"`
	YOLOVersion     string              `json:"yolo_version"`
	ModelScale      string              `json:"model_scale"`
	Task            string              `json:"task"`
	BaseModelID     *int64              `json:"base_model_id"`
	BaseModel       *BaseModelRef       `json:"base_model"`
	Params          map[string]any      `json:"params"`
	AutoRegister    bool                `json:"auto_register"`
	Status          string              `json:"status"`
	Stage           string              `json:"stage"`
	QueuePosition   *int                `json:"queue_position"`
	WorkersOnline   int                 `json:"workers_online"`
	Node            *NodeRef            `json:"node"`
	Attempts        int                 `json:"attempts"`
	CurrentEpoch    int                 `json:"current_epoch"`
	TotalEpochs     int                 `json:"total_epochs"`
	BatchDone       int                 `json:"batch_done"`
	BatchTotal      int                 `json:"batch_total"`
	ETASeconds      *int                `json:"eta_seconds"`
	Metrics         SummaryMetrics      `json:"metrics"`
	Epochs          []EpochMetrics      `json:"epochs"`
	Artifacts       []Artifact          `json:"artifacts"`
	BestModel       *RegisteredModelRef `json:"best_model"`
	LastModel       *RegisteredModelRef `json:"last_model"`
	Error           string              `json:"error"`
	ErrorDetail     string              `json:"error_detail"`
	Result          json.RawMessage     `json:"result"`
	QueuedAt        time.Time           `json:"queued_at"`
	StartedAt       *time.Time          `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

// CreateInput holds the fields for creating a new training job.
type CreateInput struct {
	ProjectID     int64          `json:"project_id"`
	DataVersionID int64          `json:"data_version_id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	InitMode      string         `json:"init_mode"` // scratch, finetune, resume
	YOLOVersion   string         `json:"yolo_version"`
	ModelScale    string         `json:"model_scale"`
	BaseModelID   *int64         `json:"base_model_id"`
	Params        map[string]any `json:"params"`
	AutoRegister  *bool          `json:"auto_register"`
}

// UpdateInput holds editable metadata of a training job.
type UpdateInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// ListFilter filters List queries.
type ListFilter struct {
	ProjectID     int64
	DataVersionID int64
	Status        string
	YOLOVersion   string
	Search        string
}

// RegisterModelInput selects which weight file of a job to add to the model
// repository.
type RegisterModelInput struct {
	Variant     string `json:"variant"` // "best" (default) or "last"
	Description string `json:"description"`
}

// LogSlice is a byte-offset slice of train.log for incremental tailing.
type LogSlice struct {
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	TotalBytes int64  `json:"total_bytes"`
	Truncated  bool   `json:"truncated"`
}

// ArtifactFile is an open artifact file to stream over HTTP.
type ArtifactFile struct {
	Content     io.ReadSeeker
	Name        string
	ContentType string
	Size        int64
	ModTime     time.Time
	CloseFunc   func() error
}

func (f *ArtifactFile) Close() error {
	if f == nil || f.CloseFunc == nil {
		return nil
	}
	return f.CloseFunc()
}

// ---- Agent task protocol types ----

// Task is returned to a compute node when it claims a training_job task.
type Task struct {
	Kind         string         `json:"kind"`
	ID           int64          `json:"id"`
	LeaseSeconds int            `json:"lease_seconds"`
	ProjectID    int64          `json:"project_id"`
	InitMode     string         `json:"init_mode"`
	YOLOVersion  string         `json:"yolo_version"`
	ModelScale   string         `json:"model_scale"`
	ArchYAML     string         `json:"arch_yaml"` // e.g. "yolo11n.yaml" when scratch
	Task         string         `json:"task"`
	DataVersion  DataVersionRef `json:"data_version"`
	BaseModel    *BaseModelRef  `json:"base_model,omitempty"`
	Params       map[string]any `json:"params"`
	DatasetURL   string         `json:"dataset_url"`
	BaseModelURL string         `json:"base_model_url,omitempty"`
	ProgressURL  string         `json:"progress_url"`
	ArtifactsURL string         `json:"artifacts_url"`
	CompleteURL  string         `json:"complete_url"`
	FailURL      string         `json:"fail_url"`
}

// ProgressInput is posted periodically by the compute node during training.
type ProgressInput struct {
	Stage        string         `json:"stage"`
	CurrentEpoch *int           `json:"current_epoch,omitempty"`
	TotalEpochs  *int           `json:"total_epochs,omitempty"`
	BatchDone    *int           `json:"batch_done,omitempty"`
	BatchTotal   *int           `json:"batch_total,omitempty"`
	ETASeconds   *int           `json:"eta_seconds,omitempty"`
	Epochs       []EpochMetrics `json:"epochs,omitempty"`
	LogChunk     string         `json:"log_chunk,omitempty"`
}

// FailInput is posted when a compute node cannot finish a training job.
type FailInput struct {
	Error     string `json:"error"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}
