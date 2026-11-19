package evaluation

import (
	"encoding/json"
	"io"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

// TaskKind is the compute-node claim kind for model evaluation tasks.
const TaskKind = "model_evaluation"

// Evaluation statuses.
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
	StageInferring   = "inferring"
	StageComputing   = "computing"
	StageUploading   = "uploading"
)

// Source types.
const (
	SourceDataVersion = "data_version"
	SourceDatasets    = "datasets"
)

// Dataset image inclusion modes (matching dataversion conventions).
const (
	ImageModeAnnotated  = "annotated"
	ImageModeBackground = "background"
	ImageModeAll        = "all"
)

// Image-level OK/NG outcomes.
const (
	OutcomeOKOK     = "ok_ok"
	OutcomeOverkill = "overkill" // OK image falsely predicted as NG
	OutcomeEscape   = "escape"   // NG image missed as OK
	OutcomeNGNG     = "ng_ng"
	OutcomeFailed   = "failed"
)

// Box match categories for sample inspection.
const (
	MatchTP            = "tp"
	MatchFP            = "fp"
	MatchMisclassified = "misclassified"
	MatchFN            = "fn"
)

// Limits and defaults.
const (
	DefaultConf         = 0.25
	DefaultIoU          = 0.50
	DefaultNMSIoU       = 0.70
	DefaultBatch        = 16
	DefaultMaxDet       = 300
	DefaultStoreMinConf = 0.05

	MaxModelsPerBatch   = 12
	MaxDatasetsPerEval  = 50
	MaxSamplesPerEval   = 200000
	MaxCompareItems     = 6
	DefaultSampleLimit  = 48
	MaxSampleLimit      = 200
	DefaultTaskImgLimit = 200
	MaxTaskImgLimit     = 500
)

// ModelRef snapshots a model from the model repository at creation time.
type ModelRef struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	ProjectName string   `json:"project_name"`
	Version     int      `json:"version"`
	YOLOVersion string   `json:"yolo_version"`
	Task        string   `json:"task"`
	Scale       string   `json:"scale"`
	FileName    string   `json:"file_name"`
	SizeBytes   int64    `json:"size_bytes"`
	SHA256      string   `json:"sha256"`
	ImageSize   *int     `json:"image_size,omitempty"`
	ClassNames  []string `json:"class_names"`
}

// VersionRef snapshots a selected dataset review version.
type VersionRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DatasetSourceInput selects one dataset and its annotation layering.
type DatasetSourceInput struct {
	DatasetID    int64   `json:"dataset_id"`
	IncludeBase  *bool   `json:"include_base,omitempty"`
	VersionIDs   []int64 `json:"version_ids,omitempty"`
	ImageMode    string  `json:"image_mode,omitempty"`    // annotated (default), background, all
	IncludeEmpty *bool   `json:"include_empty,omitempty"` // for annotated mode: include empty VOC negative samples (default true)
}

// SourceInput selects the data source for evaluation preview or creation.
type SourceInput struct {
	Type          string               `json:"type"` // data_version | datasets
	DataVersionID int64                `json:"data_version_id,omitempty"`
	Splits        []string             `json:"splits,omitempty"` // val, test, train
	Datasets      []DatasetSourceInput `json:"datasets,omitempty"`
}

// DataVersionSourceRef snapshots a data version source.
type DataVersionSourceRef struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	ProjectName string   `json:"project_name"`
	Version     int      `json:"version"`
	DirName     string   `json:"dir_name"`
	SharedLocal *bool    `json:"shared_local,omitempty"`
	Splits      []string `json:"splits"`
	ImageCount  int      `json:"image_count"`
	OKCount     int      `json:"ok_count"`
	NGCount     int      `json:"ng_count"`
	ObjectCount int      `json:"object_count"`
}

// DatasetSourceRef snapshots one dataset source.
type DatasetSourceRef struct {
	DatasetID    int64        `json:"dataset_id"`
	DatasetName  string       `json:"dataset_name"`
	DirName      string       `json:"dir_name"`
	IncludeBase  bool         `json:"include_base"`
	Versions     []VersionRef `json:"versions"`
	ImageMode    string       `json:"image_mode"`
	IncludeEmpty bool         `json:"include_empty"`
	ImageCount   int          `json:"image_count"`
	OKCount      int          `json:"ok_count"`
	NGCount      int          `json:"ng_count"`
	ObjectCount  int          `json:"object_count"`
}

// SourceRef snapshots the complete evaluation data source.
type SourceRef struct {
	Type         string                `json:"type"`
	DataVersion  *DataVersionSourceRef `json:"data_version,omitempty"`
	Datasets     []DatasetSourceRef    `json:"datasets,omitempty"`
	TotalImages  int                   `json:"total_images"`
	OKImages     int                   `json:"ok_images"`
	NGImages     int                   `json:"ng_images"`
	TotalObjects int                   `json:"total_objects"`
}

// Options holds normalized evaluation execution and default display parameters.
type Options struct {
	Conf         float64 `json:"conf"`
	IoU          float64 `json:"iou"`
	NMSIoU       float64 `json:"nms_iou"`
	ImgSize      *[2]int `json:"imgsz,omitempty"`
	Batch        int     `json:"batch"`
	MaxDet       int     `json:"max_det"`
	Half         bool    `json:"half"`
	Device       string  `json:"device"`
	StoreMinConf float64 `json:"store_min_conf"`
}

// OptionsInput is the optional user-provided evaluation parameters.
type OptionsInput struct {
	Conf    *float64 `json:"conf,omitempty"`
	IoU     *float64 `json:"iou,omitempty"`
	NMSIoU  *float64 `json:"nms_iou,omitempty"`
	ImgSize []int    `json:"imgsz,omitempty"` // [] or [s] or [h, w]
	Batch   *int     `json:"batch,omitempty"`
	MaxDet  *int     `json:"max_det,omitempty"`
	Half    *bool    `json:"half,omitempty"`
	Device  *string  `json:"device,omitempty"`
}

// ClassInfo describes one class in the aligned union class list.
type ClassInfo struct {
	Name     string `json:"name"`
	Color    string `json:"color"`
	InModel  bool   `json:"in_model"`
	InGT     bool   `json:"in_gt"`
	ModelIdx *int   `json:"model_idx,omitempty"`
	GTCount  int    `json:"gt_count"`
	GTImages int    `json:"gt_images"`
}

// NodeRef identifies the compute node executing or having finished an evaluation.
type NodeRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

// SpeedMetrics holds inference latency and throughput statistics.
type SpeedMetrics struct {
	PreprocessMs  float64 `json:"preprocess_ms"`
	InferenceMs   float64 `json:"inference_ms"`
	PostprocessMs float64 `json:"postprocess_ms"`
	TotalMs       float64 `json:"total_ms"`
	FPS           float64 `json:"fps"`
	Device        string  `json:"device"`
	BatchSize     int     `json:"batch_size"`
	ImgSize       [2]int  `json:"imgsz"`
}

// OKNGMetrics summarizes image-level industrial inspection outcomes.
type OKNGMetrics struct {
	TotalImages  int     `json:"total_images"`
	OKImages     int     `json:"ok_images"`
	NGImages     int     `json:"ng_images"`
	FailedImages int     `json:"failed_images"`
	TN           int     `json:"tn"`            // OK predicted OK
	Overkill     int     `json:"overkill"`      // OK predicted NG (过杀)
	Escape       int     `json:"escape"`        // NG predicted OK (漏杀)
	TP           int     `json:"tp"`            // NG predicted NG
	OverkillRate float64 `json:"overkill_rate"` // Overkill / OKImages
	EscapeRate   float64 `json:"escape_rate"`   // Escape / NGImages
	Accuracy     float64 `json:"accuracy"`
	Precision    float64 `json:"precision"`
	Recall       float64 `json:"recall"`
	F1           float64 `json:"f1"`
}

// SummaryMetrics holds the key evaluation KPIs at a specific (conf, iou).
type SummaryMetrics struct {
	Conf                  float64       `json:"conf"`
	IoU                   float64       `json:"iou"`
	MAP50                 float64       `json:"map50"`
	MAP75                 float64       `json:"map75"`
	MAP50_95              float64       `json:"map50_95"`
	Precision             float64       `json:"precision"`
	Recall                float64       `json:"recall"`
	F1                    float64       `json:"f1"`
	BestF1                float64       `json:"best_f1"`
	BestF1Conf            float64       `json:"best_f1_conf"`
	TotalImages           int           `json:"total_images"`
	EvaluatedImages       int           `json:"evaluated_images"`
	FailedImages          int           `json:"failed_images"`
	TotalGT               int           `json:"total_gt"`
	TotalPred             int           `json:"total_pred"`
	TP                    int           `json:"tp"`
	FP                    int           `json:"fp"`
	FN                    int           `json:"fn"`
	Misclassified         int           `json:"misclassified"`
	OKNG                  OKNGMetrics   `json:"ok_ng"`
	Speed                 *SpeedMetrics `json:"speed,omitempty"`
	EvaluatedClassCount   int           `json:"evaluated_class_count"`
	UnmatchedModelClasses int           `json:"unmatched_model_classes"`
	UnmatchedGTClasses    int           `json:"unmatched_gt_classes"`
}

// ClassMetrics holds per-class detection metrics at the active (conf, iou).
type ClassMetrics struct {
	Name      string   `json:"name"`
	Color     string   `json:"color"`
	InModel   bool     `json:"in_model"`
	InGT      bool     `json:"in_gt"`
	GTCount   int      `json:"gt_count"`
	PredCount int      `json:"pred_count"`
	TP        int      `json:"tp"`
	FP        int      `json:"fp"`
	FN        int      `json:"fn"`
	Precision float64  `json:"precision"`
	Recall    float64  `json:"recall"`
	F1        float64  `json:"f1"`
	AP50      *float64 `json:"ap50,omitempty"`
	AP75      *float64 `json:"ap75,omitempty"`
	AP50_95   *float64 `json:"ap50_95,omitempty"`
}

// CurveSeries is one polyline in an evaluation curve chart.
type CurveSeries struct {
	Name  string    `json:"name"`
	Color string    `json:"color,omitempty"`
	X     []float64 `json:"x"`
	Y     []float64 `json:"y"`
}

// CurvesData holds threshold-independent mAP scores and 4 curves computed at conf=0.001.
type CurvesData struct {
	PR           []CurveSeries      `json:"pr"`
	F1           []CurveSeries      `json:"f1"`
	Precision    []CurveSeries      `json:"precision"`
	Recall       []CurveSeries      `json:"recall"`
	ClassAP50    map[string]float64 `json:"class_ap50,omitempty"`
	ClassAP75    map[string]float64 `json:"class_ap75,omitempty"`
	ClassAP50_95 map[string]float64 `json:"class_ap50_95,omitempty"`
	MAP50        float64            `json:"map50"`
	MAP75        float64            `json:"map75"`
	MAP50_95     float64            `json:"map50_95"`
	BestF1       float64            `json:"best_f1"`
	BestF1Conf   float64            `json:"best_f1_conf"`
	Speed        *SpeedMetrics      `json:"speed,omitempty"`
}

// ConfusionMatrix is the (N+1)x(N+1) detection confusion matrix where the last
// index represents background (GT=background -> FP, Pred=background -> FN).
type ConfusionMatrix struct {
	Labels []string `json:"labels"` // N class names + "background"
	Matrix [][]int  `json:"matrix"` // Matrix[gtIdx][predIdx]
}

// Evaluation is the main evaluation resource returned by the API.
type Evaluation struct {
	ID                int64           `json:"id"`
	ProjectID         int64           `json:"project_id"`
	ProjectName       string          `json:"project_name"`
	ModelID           *int64          `json:"model_id"`
	ModelExists       bool            `json:"model_exists"`
	Model             ModelRef        `json:"model"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	SourceType        string          `json:"source_type"`
	SourceFingerprint string          `json:"source_fingerprint"`
	DataVersionID     *int64          `json:"data_version_id"`
	DataVersionLive   bool            `json:"data_version_live"`
	Source            SourceRef       `json:"source"`
	Options           Options         `json:"options"`
	Classes           []ClassInfo     `json:"classes"`
	Status            string          `json:"status"`
	Stage             string          `json:"stage"`
	QueuePosition     *int            `json:"queue_position"`
	WorkersOnline     int             `json:"workers_online"`
	Node              *NodeRef        `json:"node"`
	Attempts          int             `json:"attempts"`
	ProgressDone      int             `json:"progress_done"`
	ProgressTotal     int             `json:"progress_total"`
	ETASeconds        *int            `json:"eta_seconds"`
	Summary           SummaryMetrics  `json:"summary"`
	Error             string          `json:"error"`
	ErrorDetail       string          `json:"error_detail"`
	Result            json.RawMessage `json:"result"`
	QueuedAt          time.Time       `json:"queued_at"`
	StartedAt         *time.Time      `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// AnalysisResult is returned by GET /api/v1/evaluations/{id}/analysis?conf=&iou=.
type AnalysisResult struct {
	EvaluationID int64           `json:"evaluation_id"`
	Conf         float64         `json:"conf"`
	IoU          float64         `json:"iou"`
	StoreMinConf float64         `json:"store_min_conf"`
	Summary      SummaryMetrics  `json:"summary"`
	Classes      []ClassMetrics  `json:"classes"`
	Confusion    ConfusionMatrix `json:"confusion"`
	OKNG         OKNGMetrics     `json:"ok_ng"`
	Curves       CurvesData      `json:"curves"`
}

// GTBox is a frozen ground-truth bounding box (matching dataset.AnnotationObject).
type GTBox = dataset.AnnotationObject

// PredBox is one raw model prediction retained for interactive thresholding.
type PredBox struct {
	Name     string  `json:"name"`
	ClassIdx int     `json:"class_idx,omitempty"`
	Xmin     float64 `json:"xmin"`
	Ymin     float64 `json:"ymin"`
	Xmax     float64 `json:"xmax"`
	Ymax     float64 `json:"ymax"`
	Conf     float64 `json:"conf"`
}

// MatchedPredBox annotates a prediction box with its matching result at (conf, iou).
type MatchedPredBox struct {
	PredBox
	Match         string  `json:"match"` // tp, fp, misclassified
	MatchedGTIdx  *int    `json:"matched_gt_idx,omitempty"`
	MatchedGTName string  `json:"matched_gt_name,omitempty"`
	IoU           float64 `json:"iou,omitempty"`
}

// MatchedGTBox annotates a GT box with its matching result at (conf, iou).
type MatchedGTBox struct {
	GTBox
	Index           int     `json:"index"`
	Matched         bool    `json:"matched"`
	Match           string  `json:"match"` // tp, misclassified, fn
	MatchedPredIdx  *int    `json:"matched_pred_idx,omitempty"`
	MatchedPredName string  `json:"matched_pred_name,omitempty"`
	MatchedPredConf float64 `json:"matched_pred_conf,omitempty"`
	IoU             float64 `json:"iou,omitempty"`
}

// SampleItem is one row in the error sample browser.
type SampleItem struct {
	ID                 int64   `json:"id"`
	Position           int     `json:"position"`
	SampleKey          string  `json:"sample_key"`
	Split              string  `json:"split"`
	DatasetID          *int64  `json:"dataset_id,omitempty"`
	DatasetName        string  `json:"dataset_name"`
	DatasetImageID     *int64  `json:"dataset_image_id,omitempty"`
	DataVersionImageID *int64  `json:"data_version_image_id,omitempty"`
	Path               string  `json:"path"`
	FileName           string  `json:"file_name"`
	Width              int     `json:"width"`
	Height             int     `json:"height"`
	GTCount            int     `json:"gt_count"`
	PredCount          int     `json:"pred_count"`
	TPCount            int     `json:"tp_count"`
	FPCount            int     `json:"fp_count"`
	FNCount            int     `json:"fn_count"`
	MisclassifiedCount int     `json:"misclassified_count"`
	ImageOutcome       string  `json:"image_outcome"`
	MaxFPConf          float64 `json:"max_fp_conf"`
	InferenceError     string  `json:"inference_error"`
	ThumbnailURL       string  `json:"thumbnail_url"`
	FileURL            string  `json:"file_url"`
	AnnotateURL        string  `json:"annotate_url,omitempty"`
}

// SampleDetail includes matched GT & prediction boxes for overlay inspection.
type SampleDetail struct {
	SampleItem
	GTObjects     []MatchedGTBox   `json:"gt_objects"`
	Predictions   []MatchedPredBox `json:"predictions"`
	SourceMissing bool             `json:"source_missing"`
}

// SampleFilter filters GET /api/v1/evaluations/{id}/images.
type SampleFilter struct {
	Conf      *float64
	IoU       *float64
	ErrorType string // all, fn, fp, misclassified, overkill, escape, perfect, failed, any_error
	Class     string // either gt or pred matches this class
	GTClass   string // confusion matrix drilldown: class name or "background"
	PredClass string // confusion matrix drilldown: class name or "background"
	Search    string // filename/path contains
	Sort      string // position, fn_desc, fp_desc, max_fp_conf_desc, errors_desc
	Offset    int
	Limit     int
}

// PreviewInput is POST /api/v1/evaluations/preview.
type PreviewInput struct {
	ModelIDs []int64     `json:"model_ids"`
	Source   SourceInput `json:"source"`
}

// ModelAlignmentPreview shows how one selected model aligns with the data source classes.
type ModelAlignmentPreview struct {
	Model          ModelRef    `json:"model"`
	Classes        []ClassInfo `json:"classes"`
	MatchedCount   int         `json:"matched_count"`
	ModelOnlyCount int         `json:"model_only_count"`
	GTOnlyCount    int         `json:"gt_only_count"`
}

// PreviewLabelStat describes one GT label's object and image counts in a preview.
type PreviewLabelStat struct {
	Name        string `json:"name"`
	ObjectCount int    `json:"object_count"`
	ImageCount  int    `json:"image_count"`
}

// PreviewResult is returned by POST /api/v1/evaluations/preview.
type PreviewResult struct {
	Source   SourceRef               `json:"source"`
	GTLabels []PreviewLabelStat      `json:"gt_labels"`
	Models   []ModelAlignmentPreview `json:"models"`
	Warnings []string                `json:"warnings"`
}

// CreateInput is POST /api/v1/evaluations.
type CreateInput struct {
	ModelIDs    []int64      `json:"model_ids"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Source      SourceInput  `json:"source"`
	Options     OptionsInput `json:"options"`
}

// UpdateInput is PATCH /api/v1/evaluations/{id}.
type UpdateInput struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	DefaultConf *float64 `json:"default_conf,omitempty"`
	DefaultIoU  *float64 `json:"default_iou,omitempty"`
}

// ListFilter filters GET /api/v1/evaluations.
type ListFilter struct {
	ProjectID     int64
	ModelID       int64
	DataVersionID int64
	SourceType    string
	Status        string
	Search        string
}

// CompareItem is one evaluation column in multi-model comparison.
type CompareItem struct {
	Evaluation Evaluation      `json:"evaluation"`
	Conf       float64         `json:"conf"`
	IoU        float64         `json:"iou"`
	Summary    SummaryMetrics  `json:"summary"`
	Classes    []ClassMetrics  `json:"classes"`
	OKNG       OKNGMetrics     `json:"ok_ng"`
	PRAll      *CurveSeries    `json:"pr_all,omitempty"`
	F1All      *CurveSeries    `json:"f1_all,omitempty"`
	Confusion  ConfusionMatrix `json:"confusion"`
}

// CompareResult is returned by GET /api/v1/evaluations/compare.
type CompareResult struct {
	Mode              string        `json:"mode"` // default | unified
	Conf              *float64      `json:"conf,omitempty"`
	IoU               *float64      `json:"iou,omitempty"`
	SameSource        bool          `json:"same_source"`
	SourceFingerprint string        `json:"source_fingerprint,omitempty"`
	ClassNames        []string      `json:"class_names"`
	Items             []CompareItem `json:"items"`
}

// CompareDiffItem compares two same-source evaluations on one image.
type CompareDiffItem struct {
	SampleKey    string       `json:"sample_key"`
	Path         string       `json:"path"`
	FileName     string       `json:"file_name"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	GTCount      int          `json:"gt_count"`
	DiffCategory string       `json:"diff_category"` // improved, regressed, fp_reduced, fp_added, outcome_changed, same
	Base         SampleDetail `json:"base"`
	Target       SampleDetail `json:"target"`
	DeltaFN      int          `json:"delta_fn"` // target.FN - base.FN (<0 means improved)
	DeltaFP      int          `json:"delta_fp"` // target.FP - base.FP (<0 means improved)
	DeltaTP      int          `json:"delta_tp"` // target.TP - base.TP (>0 means improved)
}

// CompareDiffResult is returned by GET /api/v1/evaluations/compare/diff.
type CompareDiffResult struct {
	BaseID   int64             `json:"base_id"`
	TargetID int64             `json:"target_id"`
	Counts   map[string]int    `json:"counts"`
	Items    []CompareDiffItem `json:"items"`
	Total    int               `json:"total"`
}

// ImageFile is an open image stream for HTTP serving.
type ImageFile struct {
	File        io.ReadSeekCloser
	ContentType string
	Name        string
	ModTime     time.Time
}

// Close closes the underlying image stream.
func (f *ImageFile) Close() error {
	if f == nil || f.File == nil {
		return nil
	}
	return f.File.Close()
}

// ---- Agent task protocol types ----

// DatasetArchiveRef advertises a .tar.zst data-version bulk archive to the trainer.
type DatasetArchiveRef struct {
	URL    string `json:"url"`
	Format string `json:"format"`
}

// Task is returned to a compute node when it claims a model_evaluation task.
type Task struct {
	Kind           string                               `json:"kind"`
	ID             int64                                `json:"id"`
	LeaseSeconds   int                                  `json:"lease_seconds"`
	ProjectID      int64                                `json:"project_id"`
	Model          ModelRef                             `json:"model"`
	SourceType     string                               `json:"source_type"`
	DataVersion    *DataVersionSourceRef                `json:"data_version,omitempty"`
	DVTotalBytes   int64                                `json:"dv_total_bytes,omitempty"`
	DVTransferMode string                               `json:"dv_transfer_mode,omitempty"`
	DatasetCache   *storagenode.TrainerCacheTaskPayload `json:"dataset_cache,omitempty"`
	DatasetArchive *DatasetArchiveRef                   `json:"dataset_archive,omitempty"`
	Options        Options                              `json:"options"`
	Classes        []ClassInfo                          `json:"classes"`
	TotalImages    int                                  `json:"total_images"`
	ModelURL       string                               `json:"model_url"`
	DatasetURL     string                               `json:"dataset_url,omitempty"`
	ImagesURL      string                               `json:"images_url"`
	ProgressURL    string                               `json:"progress_url"`
	ResultsURL     string                               `json:"results_url"`
	CompleteURL    string                               `json:"complete_url"`
	FailURL        string                               `json:"fail_url"`
}

// TaskImage describes one sample returned to the compute node via GET .../images.
type TaskImage struct {
	ID        int64   `json:"id"`
	Position  int     `json:"position"`
	Path      string  `json:"path"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	GTObjects []GTBox `json:"gt_objects"`
	FileURL   string  `json:"file_url"`
}

// TaskImagePage is a cursor-paginated slice of samples for the compute node.
type TaskImagePage struct {
	Items   []TaskImage `json:"items"`
	HasMore bool        `json:"has_more"`
	NextID  int64       `json:"next_id"`
}

// ProgressInput is posted periodically by the compute node during evaluation.
type ProgressInput struct {
	Stage         string `json:"stage"`
	ProgressDone  *int   `json:"progress_done,omitempty"`
	ProgressTotal *int   `json:"progress_total,omitempty"`
	ETASeconds    *int   `json:"eta_seconds,omitempty"`
}

// FailInput is posted when a compute node cannot finish an evaluation.
type FailInput struct {
	Error     string `json:"error"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}

// StoredImagePrediction is the compact on-disk representation per evaluation_image.
type StoredImagePrediction struct {
	SampleID       int64     `json:"sample_id"`
	Width          int       `json:"width,omitempty"`
	Height         int       `json:"height,omitempty"`
	InferenceError string    `json:"inference_error,omitempty"`
	Predictions    []PredBox `json:"predictions"`
}

// StoredPredictionsFile is persisted as runs/evaluations/eval-<id>/predictions.json.gz.
type StoredPredictionsFile struct {
	EvaluationID int64                   `json:"evaluation_id"`
	StoreMinConf float64                 `json:"store_min_conf"`
	Items        []StoredImagePrediction `json:"items"`
}

// UploadResultsPayload is uploaded by the compute node (optionally gzip-encoded)
// to POST /api/v1/compute/agent/evaluations/{id}/results.
type UploadResultsPayload struct {
	Curves      CurvesData              `json:"curves"`
	Predictions []StoredImagePrediction `json:"predictions"`
}
