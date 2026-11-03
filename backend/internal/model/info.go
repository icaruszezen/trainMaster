package model

import (
	"encoding/json"
	"math"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
)

// Bounds of what is kept from a weights file: the pickle is untrusted
// input, and the info is stored and sent with every model.
const (
	maxClassNames     = 10_000
	maxClassNameRunes = 256
	maxMetrics        = 64
	maxShortRunes     = 128  // class names of the model and head, versions, ...
	maxPathRunes      = 1024 // train_data, base_model
)

// newInfo turns what Inspect found into the stored form: cleaned strings,
// finite numbers, null for unknown values.
func newInfo(ci *checkpoint.Info) Info {
	info := Info{
		Task:               cleanLabel(ci.Task, 32),
		ModelClass:         cleanLabel(ci.ModelClass, maxShortRunes),
		Head:               cleanLabel(ci.Head, maxShortRunes),
		YAMLFile:           cleanLabel(ci.YAMLFile, 256),
		Scale:              cleanLabel(ci.Scale, 16),
		VersionSource:      cleanLabel(ci.VersionSource, 32),
		ClassNames:         make([]string, 0, min(len(ci.ClassNames), maxClassNames)),
		Channels:           ci.Channels,
		End2End:            ci.End2End,
		UltralyticsVersion: cleanLabel(ci.UltralyticsVersion, 64),
		TrainedAt:          cleanLabel(ci.Date, 64),
		Epoch:              ci.Epoch,
		Metrics:            map[string]float64{},
		TrainData:          cleanLabel(ci.TrainData, maxPathRunes),
		BaseModel:          cleanLabel(ci.BaseModel, maxPathRunes),
		TrainingCheckpoint: ci.TrainingCheckpoint,
	}
	if v := cleanLabel(ci.YOLOVersion, 32); v != "" {
		info.DetectedYOLOVersion = &v
	}
	for _, name := range ci.ClassNames[:min(len(ci.ClassNames), maxClassNames)] {
		info.ClassNames = append(info.ClassNames, cleanLabel(name, maxClassNameRunes))
	}
	if ci.ImageSize > 0 {
		v := ci.ImageSize
		info.ImageSize = &v
	}
	if info.Channels <= 0 {
		info.Channels = 3
	}
	if ci.Parameters > 0 {
		v := ci.Parameters
		info.Parameters = &v
	}
	if ci.Epochs > 0 {
		v := ci.Epochs
		info.Epochs = &v
	}
	if ci.BestFitness != nil && isFinite(*ci.BestFitness) {
		v := *ci.BestFitness
		info.BestFitness = &v
	}
	for k, v := range ci.Metrics {
		if len(info.Metrics) == maxMetrics {
			break
		}
		if k := cleanLabel(k, maxShortRunes); k != "" && isFinite(v) {
			info.Metrics[k] = v
		}
	}
	return info
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// decodeInfo reads a stored info; slices and maps are never nil, so they
// encode as [] and {}.
func decodeInfo(data []byte) (Info, error) {
	var info Info
	if len(data) > 0 {
		if err := json.Unmarshal(data, &info); err != nil {
			return info, err
		}
	}
	if info.ClassNames == nil {
		info.ClassNames = []string{}
	}
	if info.Metrics == nil {
		info.Metrics = map[string]float64{}
	}
	return info, nil
}

// Bounds of a node's report about a conversion (ExportResult).
const (
	maxResultTensors = 16
	maxTensorDims    = 16
)

// sanitizeResult cleans what a node reported about a conversion: known
// fields of the expected types are kept (bounded), anything else is
// dropped, so a newer or broken node cannot store junk. A body that is
// not a JSON object yields an empty result.
func sanitizeResult(raw json.RawMessage) ExportResult {
	r := ExportResult{Inputs: []TensorInfo{}, Outputs: []TensorInfo{}}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return r
	}
	r.UltralyticsVersion = stringField(m, "ultralytics_version", 64)
	r.TorchVersion = stringField(m, "torch_version", 64)
	r.ONNXVersion = stringField(m, "onnx_version", 64)
	r.ONNXRuntimeVersion = stringField(m, "onnxruntime_version", 64)
	r.NodeName = stringField(m, "node_name", maxShortRunes)
	r.Opset = intField(m, "opset", 0, 1000)
	r.DurationMS = intField(m, "duration_ms", 0, 1<<40)
	r.End2End = boolField(m, "end2end")
	r.NMS = boolField(m, "nms")
	r.Dynamic = boolField(m, "dynamic")
	r.Verified = boolField(m, "verified")
	r.Inputs = tensorsField(m, "inputs")
	r.Outputs = tensorsField(m, "outputs")
	return r
}

func stringField(m map[string]any, key string, n int) string {
	s, _ := m[key].(string)
	return cleanLabel(s, n)
}

func boolField(m map[string]any, key string) *bool {
	if b, ok := m[key].(bool); ok {
		return &b
	}
	return nil
}

// intField returns an integral number within [lo, hi], or nil.
func intField(m map[string]any, key string, lo, hi int64) *int64 {
	f, ok := m[key].(float64)
	if !ok || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		return nil
	}
	v := int64(f)
	return &v
}

func tensorsField(m map[string]any, key string) []TensorInfo {
	out := []TensorInfo{}
	items, _ := m[key].([]any)
	for _, item := range items {
		if len(out) == maxResultTensors {
			break
		}
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ti := TensorInfo{Name: stringField(t, "name", maxShortRunes), Type: stringField(t, "type", 32), Shape: []any{}}
		dims, _ := t["shape"].([]any)
		for _, d := range dims[:min(len(dims), maxTensorDims)] {
			switch v := d.(type) {
			case float64:
				if v == math.Trunc(v) && v >= -1 && v <= math.MaxInt32 {
					ti.Shape = append(ti.Shape, int64(v))
				} else {
					ti.Shape = append(ti.Shape, nil)
				}
			case string:
				ti.Shape = append(ti.Shape, cleanLabel(v, 64))
			default:
				ti.Shape = append(ti.Shape, nil) // keeps the rank
			}
		}
		out = append(out, ti)
	}
	return out
}
