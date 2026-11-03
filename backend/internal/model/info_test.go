package model

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
)

func TestNewInfo(t *testing.T) {
	epoch, fitness := -1, 0.61
	info := newInfo(&checkpoint.Info{
		ModelClass: "DetectionModel", Task: "detect", YOLOVersion: "yolo11", VersionSource: "yaml_file",
		YAMLFile: "yolo11n.yaml", Scale: "n", ClassNames: []string{"scratch", "划痕-凹坑"}, ImageSize: 96,
		Parameters: 2590230, Head: "Detect", UltralyticsVersion: "8.4.198", Date: "2026-10-30T08:00:00+08:00",
		Epochs: 100, Epoch: &epoch, BestFitness: &fitness,
		Metrics:   map[string]float64{"metrics/mAP50(B)": 0.91, "val/box_loss": math.NaN(), "x": math.Inf(1)},
		TrainData: "/data/exports/x/data.yaml", BaseModel: "yolo11n.pt",
	})
	if info.Task != "detect" || info.ModelClass != "DetectionModel" || info.DetectedYOLOVersion == nil ||
		*info.DetectedYOLOVersion != "yolo11" || info.VersionSource != "yaml_file" || info.Channels != 3 ||
		info.ImageSize == nil || *info.ImageSize != 96 || info.Parameters == nil || *info.Parameters != 2590230 ||
		info.Epochs == nil || *info.Epochs != 100 || info.Epoch == nil || *info.Epoch != -1 ||
		info.BestFitness == nil || *info.BestFitness != 0.61 || info.TrainedAt != "2026-10-30T08:00:00+08:00" ||
		!slices.Equal(info.ClassNames, []string{"scratch", "划痕-凹坑"}) {
		t.Errorf("info = %+v", info)
	}
	// Non-finite metrics are dropped: JSON cannot carry them.
	if len(info.Metrics) != 1 || info.Metrics["metrics/mAP50(B)"] != 0.91 {
		t.Errorf("metrics = %v", info.Metrics)
	}
	if _, err := json.Marshal(info); err != nil {
		t.Errorf("marshal: %v", err)
	}

	// Unknown values are null, collections empty.
	nan := math.NaN()
	empty := newInfo(&checkpoint.Info{BestFitness: &nan})
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"task", "model_class", "head", "yaml_file", "scale", "detected_yolo_version", "version_source",
		"class_names", "image_size", "channels", "parameters", "end2end", "ultralytics_version", "trained_at", "epochs",
		"epoch", "best_fitness", "metrics", "train_data", "base_model", "training_checkpoint"}
	if len(raw) != len(wantKeys) {
		t.Errorf("info keys = %v", raw)
	}
	for _, k := range []string{"detected_yolo_version", "image_size", "parameters", "epochs", "epoch", "best_fitness"} {
		if v, ok := raw[k]; !ok || v != nil {
			t.Errorf("%s = %v, want null", k, v)
		}
	}
	if names, ok := raw["class_names"].([]any); !ok || len(names) != 0 {
		t.Errorf("class_names = %#v", raw["class_names"])
	}
	if m, ok := raw["metrics"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("metrics = %#v", raw["metrics"])
	}
	if raw["channels"] != 3.0 {
		t.Errorf("channels = %v", raw["channels"])
	}
}

func TestNewInfoCleansStrings(t *testing.T) {
	// The pickle is untrusted: NUL (which jsonb cannot hold), invalid
	// UTF-8, control characters and huge values must not reach the
	// database.
	names := make([]string, maxClassNames+5)
	for i := range names {
		names[i] = "c"
	}
	names[0] = "a\x00b\nc\xff"
	names[1] = strings.Repeat("长", maxClassNameRunes+10)
	info := newInfo(&checkpoint.Info{Task: "detect\x00", ModelClass: "Det\tModel", ClassNames: names,
		TrainData: strings.Repeat("x", 5000), Metrics: map[string]float64{"\x00": 1, "ok\x01": 2}})
	if info.Task != "detect" || info.ModelClass != "Det Model" {
		t.Errorf("task = %q, class = %q", info.Task, info.ModelClass)
	}
	if len(info.ClassNames) != maxClassNames || info.ClassNames[0] != "ab c�" ||
		len([]rune(info.ClassNames[1])) != maxClassNameRunes {
		t.Errorf("class names: %d, %q", len(info.ClassNames), info.ClassNames[0])
	}
	if len([]rune(info.TrainData)) != maxPathRunes {
		t.Errorf("train_data has %d runes", len([]rune(info.TrainData)))
	}
	if len(info.Metrics) != 1 || info.Metrics["ok"] != 2 {
		t.Errorf("metrics = %v", info.Metrics)
	}
}

func TestDecodeInfo(t *testing.T) {
	info, err := decodeInfo([]byte(`{}`))
	if err != nil || info.ClassNames == nil || info.Metrics == nil {
		t.Errorf("decodeInfo({}) = %+v, %v", info, err)
	}
	if _, err := decodeInfo([]byte(`[`)); err == nil {
		t.Error("decodeInfo of broken JSON should fail")
	}
}

func TestSanitizeResult(t *testing.T) {
	r := sanitizeResult(json.RawMessage(`{
		"ultralytics_version": "8.4.198", "torch_version": "2.14.1+cpu", "onnx_version": "1.23.2",
		"onnxruntime_version": "1.23.2", "opset": 19, "end2end": true, "nms": false, "dynamic": "no",
		"inputs": [{"name": "images", "shape": ["batch", 3, 640.5, -1, {"x": 1}], "type": "float32"}, "junk"],
		"outputs": [{"name": "output0", "shape": [1, 300, 6], "type": "float32", "extra": 1}],
		"verified": true, "duration_ms": 8123.0, "node_name": "gpu-server-01\u0000", "unknown": [1, 2]}`))
	if r.UltralyticsVersion != "8.4.198" || r.TorchVersion != "2.14.1+cpu" || r.Opset == nil || *r.Opset != 19 ||
		r.End2End == nil || !*r.End2End || r.NMS == nil || *r.NMS || r.Dynamic != nil || r.Verified == nil ||
		r.DurationMS == nil || *r.DurationMS != 8123 || r.NodeName != "gpu-server-01" {
		t.Errorf("result = %+v", r)
	}
	if len(r.Inputs) != 1 || r.Inputs[0].Name != "images" || len(r.Inputs[0].Shape) != 5 ||
		r.Inputs[0].Shape[0] != "batch" || r.Inputs[0].Shape[1] != int64(3) || r.Inputs[0].Shape[2] != nil ||
		r.Inputs[0].Shape[3] != int64(-1) || r.Inputs[0].Shape[4] != nil {
		t.Errorf("inputs = %+v", r.Inputs)
	}
	if len(r.Outputs) != 1 || r.Outputs[0].Type != "float32" {
		t.Errorf("outputs = %+v", r.Outputs)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"shape":["batch",3,null,-1,null]`) {
		t.Errorf("encoded = %s", data)
	}

	// Anything else gives an empty result with [] tensors.
	for _, raw := range []string{``, `null`, `[]`, `"x"`, `{"inputs": 5, "opset": "18"}`} {
		r := sanitizeResult(json.RawMessage(raw))
		if r.Opset != nil || r.Inputs == nil || len(r.Inputs) != 0 || r.Outputs == nil {
			t.Errorf("sanitizeResult(%s) = %+v", raw, r)
		}
	}
	// Bounds.
	var many []string
	for range maxResultTensors + 3 {
		many = append(many, `{"name": "t", "shape": [`+strings.Repeat("1,", maxTensorDims+3)+`1]}`)
	}
	r = sanitizeResult(json.RawMessage(`{"inputs": [` + strings.Join(many, ",") + `], "opset": 1e30}`))
	if len(r.Inputs) != maxResultTensors || len(r.Inputs[0].Shape) != maxTensorDims || r.Opset != nil {
		t.Errorf("bounded result: %d tensors, %d dims, opset %v", len(r.Inputs), len(r.Inputs[0].Shape), r.Opset)
	}
}

func TestNormalizeDescription(t *testing.T) {
	if d, err := NormalizeDescription("  夜班\n补充数据\t "); err != nil || d != "夜班\n补充数据" {
		t.Errorf("NormalizeDescription = %q, %v", d, err)
	}
	if d, err := NormalizeDescription(strings.Repeat("字", 500)); err != nil || len([]rune(d)) != 500 {
		t.Errorf("500 runes: %v", err)
	}
	_, err := NormalizeDescription(strings.Repeat("字", 501))
	wantKind(t, err, ErrInvalid, "说明不能超过 500 个字符")
	for _, bad := range []string{"a\x00b", "a\x1bb", "\xff"} {
		_, err := NormalizeDescription(bad)
		wantKind(t, err, ErrInvalid, "说明包含无效字符")
	}
}

func TestNormalizeFileName(t *testing.T) {
	for in, want := range map[string]string{" best.pt ": "best.pt", "Best.PT": "Best.PT", "模型 v2.pt": "模型 v2.pt"} {
		if got, err := normalizeFileName(in); err != nil || got != want {
			t.Errorf("normalizeFileName(%q) = %q, %v", in, got, err)
		}
	}
	for in, msg := range map[string]string{
		"":                               "请提供模型文件名",
		"best.pth":                       "请选择 .pt 格式的模型文件",
		"best.onnx":                      ".pt",
		"a\nb.pt":                        "文件名包含无效字符",
		"\xff.pt":                        "文件名包含无效字符",
		strings.Repeat("a", 253) + ".pt": "文件名不能超过 255 个字符",
	} {
		_, err := normalizeFileName(in)
		wantKind(t, err, ErrInvalid, msg)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncateBytes("ab划痕", 4); got != "ab" {
		t.Errorf("truncateBytes = %q", got)
	}
	if got := truncateBytes("ab划痕", 5); got != "ab划" {
		t.Errorf("truncateBytes = %q", got)
	}
	if got := truncateRunes("划痕凹坑", 2); got != "划痕" {
		t.Errorf("truncateRunes = %q", got)
	}
}

func TestWorkerSet(t *testing.T) {
	var w workerSet
	now := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	if w.count(now) != 0 {
		t.Error("empty set should count 0")
	}
	w.touch(1, now)
	w.touch(2, now.Add(-50*time.Second))
	w.touch(3, now.Add(-61*time.Second))
	if n := w.count(now); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if _, ok := w.seen[3]; ok {
		t.Error("stale nodes should be forgotten")
	}
	w.touch(2, now) // seen again
	if n := w.count(now.Add(30 * time.Second)); n != 2 {
		t.Errorf("count later = %d, want 2", n)
	}
}

func TestNotifier(t *testing.T) {
	var n notifier
	n.notify() // nobody waits: no panic
	a, b := n.wait(), n.wait()
	select {
	case <-a:
		t.Fatal("woken before notify")
	default:
	}
	n.notify()
	<-a
	<-b
	c := n.wait()
	select {
	case <-c:
		t.Fatal("a new wait must not see the earlier notify")
	default:
	}
}
