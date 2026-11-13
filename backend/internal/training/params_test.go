package training

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParamsSchemaAndOfficialDefaults(t *testing.T) {
	schema := ParamsSchema()
	if len(schema.Groups) != 4 {
		t.Fatalf("len(Groups) = %d, want 4", len(schema.Groups))
	}
	if len(schema.Specs) != 86 {
		t.Fatalf("len(Specs) = %d, want 86", len(schema.Specs))
	}
	if len(schema.Defaults) != 86 {
		t.Fatalf("len(Defaults) = %d, want 86", len(schema.Defaults))
	}

	// Spot-check Ultralytics 8.4.198 cfg/default.yaml official defaults.
	wantDefaults := map[string]any{
		"epochs":          100,
		"time":            nil,
		"patience":        100,
		"batch":           16,
		"imgsz":           640,
		"save":            true,
		"save_period":     -1,
		"cache":           false,
		"device":          nil,
		"workers":         8,
		"pretrained":      true,
		"optimizer":       "auto",
		"verbose":         true,
		"seed":            0,
		"deterministic":   true,
		"single_cls":      false,
		"rect":            false,
		"cos_lr":          false,
		"close_mosaic":    10,
		"resume":          false,
		"amp":             true,
		"fraction":        1.0,
		"profile":         false,
		"freeze":          nil,
		"multi_scale":     0.0,
		"compile":         false,
		"quantize":        nil,
		"cls_remap":       true,
		"channels_last":   nil,
		"lr0":             0.01,
		"lrf":             0.01,
		"momentum":        0.937,
		"weight_decay":    0.0005,
		"warmup_epochs":   3.0,
		"warmup_momentum": 0.8,
		"warmup_bias_lr":  0.1,
		"box":             7.5,
		"cls":             0.5,
		"dfl":             1.5,
		"pose":            12.0,
		"kobj":            1.0,
		"rle":             1.0,
		"angle":           1.0,
		"dlog":            1.0,
		"dgrad":           0.5,
		"dlam":            1.0,
		"dis":             6.0,
		"nbs":             64,
		"hsv_h":           0.015,
		"hsv_s":           0.7,
		"hsv_v":           0.4,
		"degrees":         0.0,
		"translate":       0.1,
		"scale":           0.5,
		"shear":           0.0,
		"perspective":     0.0,
		"flipud":          0.0,
		"fliplr":          0.5,
		"bgr":             0.0,
		"mosaic":          1.0,
		"mixup":           0.0,
		"cutmix":          0.0,
		"copy_paste":      0.0,
		"copy_paste_mode": "flip",
		"auto_augment":    "randaugment",
		"erasing":         0.4,
		"val":             true,
		"split":           "val",
		"iou":             0.7,
		"max_det":         300,
		"plots":           true,
	}
	for k, want := range wantDefaults {
		got, ok := schema.Defaults[k]
		if !ok {
			t.Errorf("missing default key %q", k)
			continue
		}
		if got != want {
			t.Errorf("default[%q] = %#v (%T), want %#v (%T)", k, got, got, want, want)
		}
	}

	// Ensure scales cover standard and yolov9 variants.
	if yamlName, err := ArchYAML("yolo11", "n"); err != nil || yamlName != "yolo11n.yaml" {
		t.Errorf("ArchYAML(yolo11, n) = (%q, %v)", yamlName, err)
	}
	if yamlName, err := ArchYAML("yolov9", "c"); err != nil || yamlName != "yolov9c.yaml" {
		t.Errorf("ArchYAML(yolov9, c) = (%q, %v)", yamlName, err)
	}
	if _, err := ArchYAML("yolov9", "n"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ArchYAML(yolov9, n) err = %v, want ErrInvalid", err)
	}
	if _, err := ArchYAML("yolov5", "n"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ArchYAML(yolov5, n) err = %v, want ErrInvalid", err)
	}
}

func TestNormalizeParams(t *testing.T) {
	// Empty input yields all 86 defaults, with resume synchronized to initMode.
	norm, err := NormalizeParams(nil, InitScratch)
	if err != nil {
		t.Fatalf("NormalizeParams(nil, scratch) error: %v", err)
	}
	if len(norm) != 86 || norm["epochs"] != 100 || norm["resume"] != false {
		t.Fatalf("unexpected normalized defaults: epochs=%v resume=%v len=%d", norm["epochs"], norm["resume"], len(norm))
	}

	resumeNorm, err := NormalizeParams(nil, InitResume)
	if err != nil {
		t.Fatalf("NormalizeParams(nil, resume) error: %v", err)
	}
	if resumeNorm["resume"] != true {
		t.Errorf("resume mode should set resume=true, got %v", resumeNorm["resume"])
	}

	// Hybrid and JSON-decoded values.
	rawJSON := `{
		"epochs": 50,
		"batch": 0.75,
		"imgsz": [640, 480],
		"cache": "ram",
		"device": "0,1",
		"optimizer": "AdamW",
		"lr0": 0.001,
		"freeze": [0, 1, 2],
		"classes": [0, 3],
		"compile": "reduce-overhead",
		"multi_scale": 0.5,
		"cutmix": 0.2,
		"quantize": "fp16",
		"channels_last": false
	}`
	dec := json.NewDecoder(strings.NewReader(rawJSON))
	dec.UseNumber()
	var input map[string]any
	if err := dec.Decode(&input); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeParams(input, InitFinetune)
	if err != nil {
		t.Fatalf("NormalizeParams(hybrid) error: %v", err)
	}
	if got["epochs"] != 50 || got["batch"] != 0.75 || got["cache"] != "ram" || got["optimizer"] != "AdamW" ||
		got["compile"] != "reduce-overhead" || got["quantize"] != "fp16" || got["channels_last"] != false {
		t.Errorf("unexpected hybrid normalization: %#v", got)
	}
	if sz, ok := got["imgsz"].([]int); !ok || len(sz) != 2 || sz[0] != 640 || sz[1] != 480 {
		t.Errorf("imgsz = %#v, want [640 480]", got["imgsz"])
	}
	if fr, ok := got["freeze"].([]int); !ok || len(fr) != 3 || fr[2] != 2 {
		t.Errorf("freeze = %#v, want [0 1 2]", got["freeze"])
	}

	// Auto-batch (-1) and bool cache.
	gotAuto, err := NormalizeParams(map[string]any{"batch": -1, "cache": true, "device": 0, "freeze": 10}, InitScratch)
	if err != nil {
		t.Fatalf("NormalizeParams(auto-batch) error: %v", err)
	}
	if gotAuto["batch"] != -1 || gotAuto["cache"] != true || gotAuto["device"] != "0" || gotAuto["freeze"] != 10 {
		t.Errorf("gotAuto = %#v", gotAuto)
	}

	// Invalid & reserved cases.
	badCases := []struct {
		name string
		raw  map[string]any
		sub  string
	}{
		{"reserved data", map[string]any{"data": "/etc/passwd"}, "不可直接指定"},
		{"reserved project", map[string]any{"project": "/tmp"}, "不可直接指定"},
		{"reserved model", map[string]any{"model": "yolo11n.pt"}, "不可直接指定"},
		{"unknown key", map[string]any{"not_a_yolo_arg": 1}, "不支持的 YOLO 训练参数"},
		{"epochs zero", map[string]any{"epochs": 0}, "epochs"},
		{"batch zero", map[string]any{"batch": 0}, "batch"},
		{"batch bad float", map[string]any{"batch": 1.5}, "batch"},
		{"imgsz bad", map[string]any{"imgsz": []any{16}}, "imgsz"},
		{"optimizer bad", map[string]any{"optimizer": "RMSPropX"}, "optimizer"},
		{"cache bad", map[string]any{"cache": "ssd"}, "cache"},
		{"lr0 zero", map[string]any{"lr0": 0}, "lr0"},
		{"device shell chars", map[string]any{"device": "0;rm -rf /"}, "device"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeParams(tc.raw, InitScratch)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.sub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.sub)
			}
		})
	}
}
