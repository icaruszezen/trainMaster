// Package checkpoint reads the metadata of PyTorch .pt files saved by
// Ultralytics (YOLOv8 and later): the task, the class names, the input
// size, the YOLO version and the training information. It parses the
// pickle in the torch zip archive with a small read-only interpreter that
// never executes anything, so the platform needs no Python or PyTorch.
package checkpoint

import (
	"errors"
	"fmt"
	"io"
)

// Info is what Inspect learns from an Ultralytics checkpoint.
type Info struct {
	ModelClass         string             // class name of the model object, e.g. DetectionModel
	Task               string             // detect, segment, classify, pose, obb, ... ("" if unknown)
	YOLOVersion        string             // detected YOLO version (yolov8 ... yolo26), "" if unknown
	VersionSource      string             // how YOLOVersion was found: "yaml_file", "architecture" or ""
	YAMLFile           string             // model yaml, e.g. yolo11n.yaml
	Scale              string             // n, s, m, l, x, ... ("" if unknown)
	ClassNames         []string           // by class id
	ImageSize          int                // training image size (train_args.imgsz), 0 if unknown
	Channels           int                // input channels, 3 by default
	Parameters         int64              // elements of the model's nn.Parameter tensors, 0 if unknown
	End2End            bool               // the detection head has an end-to-end (one-to-one, NMS-free) branch
	Head               string             // class name of the last layer: Detect, v10Detect, Segment, ...
	UltralyticsVersion string             // ckpt["version"]
	Date               string             // ckpt["date"] as saved
	Epochs             int                // train_args.epochs, 0 if unknown
	Epoch              *int               // ckpt["epoch"] (-1: final weights), nil if unknown
	BestFitness        *float64           // ckpt["best_fitness"]
	Metrics            map[string]float64 // numeric entries of ckpt["train_metrics"], keys as saved
	TrainData          string             // train_args.data
	BaseModel          string             // train_args.model, e.g. yolo11n.pt
	TrainingCheckpoint bool               // has optimizer state (a resumable checkpoint such as last.pt)
}

// Reasons of an *Error.
const (
	ReasonNotTorch         = "not_torch"         // not a PyTorch file (no zip, or no data.pkl in it)
	ReasonLegacyFormat     = "legacy_format"     // the pre-1.6 PyTorch format (not a zip)
	ReasonCorrupt          = "corrupt"           // damaged or truncated zip / pickle
	ReasonNotUltralytics   = "not_ultralytics"   // a PyTorch file, but not an Ultralytics checkpoint
	ReasonYOLOv5Repo       = "yolov5_repo"       // trained with the original YOLOv5 repository
	ReasonUnsupportedModel = "unsupported_model" // an Ultralytics model the platform does not support (RT-DETR, ...)
	ReasonTooLarge         = "too_large"         // the pickle exceeds the size or complexity limits
)

// Error explains why a file cannot be used. Message is written in Chinese
// and shown to users as is.
type Error struct {
	Reason  string
	Message string
}

func (e *Error) Error() string { return e.Message }

// How Info.YOLOVersion was found.
const (
	VersionSourceYAMLFile     = "yaml_file"    // from the model yaml file name (or the name of the model trained from)
	VersionSourceArchitecture = "architecture" // from the modules of the model yaml (custom yaml names)
)

func newError(reason, message string) *Error { return &Error{Reason: reason, Message: message} }

func corruptError(detail string) *Error {
	return newError(ReasonCorrupt, "模型文件已损坏或不完整（"+detail+"），请重新导出或重新上传")
}

func pickleTooLarge() *Error {
	return newError(ReasonTooLarge, "模型文件中的 data.pkl 超过 64 MiB，不像是 Ultralytics 保存的模型，平台拒绝解析")
}

// Inspect parses a PyTorch .pt file (torch zip format) produced by
// Ultralytics. Problems with the file are returned as *Error; other errors
// come from reading r. Inspect only reads data.pkl, at most 64 MiB, and
// is safe to call concurrently.
func Inspect(r io.ReaderAt, size int64) (info *Info, err error) {
	// The parser is written not to panic on any input (FuzzInspect); this
	// keeps an overlooked case from taking the server down.
	defer func() {
		if v := recover(); v != nil {
			info, err = nil, newError(ReasonCorrupt, fmt.Sprintf("模型文件解析失败（解析器内部错误：%v）", v))
		}
	}()
	return inspect(r, size)
}

func inspect(r io.ReaderAt, size int64) (*Info, error) {
	data, err := readPickle(r, size)
	if err != nil {
		return nil, err
	}
	root, err := unpickle(data, defaultLimits)
	if err != nil {
		var pe *pickleError
		if errors.As(err, &pe) && pe.tooLarge {
			return nil, newError(ReasonTooLarge, "模型文件的结构过于复杂（"+pe.Error()+"），不像是 Ultralytics 保存的模型，平台拒绝解析")
		}
		return nil, corruptError("pickle 解析失败，" + err.Error())
	}
	return extract(root)
}
