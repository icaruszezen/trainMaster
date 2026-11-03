package model

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
)

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }

// testModel is a model of the given version and task with a training size.
func testModel(yolo, task string, imgsz int, end2end bool) Model {
	m := Model{YOLOVersion: yolo, Task: task, Info: Info{Task: task, End2End: end2end}}
	if imgsz > 0 {
		m.Info.ImageSize = intPtr(imgsz)
	}
	return m
}

func TestAllowedPostprocess(t *testing.T) {
	for _, tt := range []struct {
		yolo, task string
		end2end    bool
		want       []string
	}{
		{"yolov8", "detect", false, []string{"raw", "nms"}},
		{"yolov9", "segment", false, []string{"raw", "nms"}},
		{"yolo11", "pose", false, []string{"raw", "nms"}},
		{"yolo12", "obb", false, []string{"raw", "nms"}},
		{"yolov10", "detect", false, []string{"end2end", "raw", "nms"}},
		{"yolo26", "detect", false, []string{"end2end", "raw", "nms"}},
		{"yolo26", "segment", true, []string{"end2end", "raw", "nms"}},
		// A file with an end-to-end head of a version without one by default.
		{"yolo11", "detect", true, []string{"end2end", "raw", "nms"}},
		// Classification: raw only, whatever the version.
		{"yolo11", "classify", false, []string{"raw"}},
		{"yolo26", "classify", false, []string{"raw"}},
		{"yolov10", "classify", true, []string{"raw"}},
		// Tasks without boxes have neither an end-to-end head nor NMS, even
		// in YOLO26.
		{"yolo11", "", false, []string{"raw"}},
		{"yolo26", "", false, []string{"raw"}},
		{"yolo26", "semantic", false, []string{"raw"}},
		{"yolo26", "depth", false, []string{"raw"}},
		{"yolo26", "semantic", true, []string{"raw"}},
	} {
		got := AllowedPostprocess(testModel(tt.yolo, tt.task, 0, tt.end2end))
		if !slices.Equal(got, tt.want) {
			t.Errorf("AllowedPostprocess(%s %s end2end=%v) = %v, want %v", tt.yolo, tt.task, tt.end2end, got, tt.want)
		}
	}
}

func TestNormalizeExportDefaults(t *testing.T) {
	for _, tt := range []struct {
		name string
		m    Model
		want ExportOptions
	}{
		{"v8 without a training size", testModel("yolov8", "detect", 0, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "raw"}},
		{"training size", testModel("yolo11", "detect", 96, false),
			ExportOptions{ImgSize: [2]int{96, 96}, Postprocess: "raw"}},
		{"training size rounded up to the stride", testModel("yolo11", "detect", 600, false),
			ExportOptions{ImgSize: [2]int{608, 608}, Postprocess: "raw"}},
		{"tiny training size", testModel("yolo11", "detect", 8, false),
			ExportOptions{ImgSize: [2]int{32, 32}, Postprocess: "raw"}},
		{"huge training size", testModel("yolo11", "detect", 10000, false),
			ExportOptions{ImgSize: [2]int{4096, 4096}, Postprocess: "raw"}},
		{"yolo26 end2end", testModel("yolo26", "detect", 640, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "end2end"}},
		{"yolov10 end2end", testModel("yolov10", "detect", 0, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "end2end"}},
		{"yolo26 classify raw", testModel("yolo26", "classify", 224, false),
			ExportOptions{ImgSize: [2]int{224, 224}, Postprocess: "raw"}},
		{"yolo26 semantic raw", testModel("yolo26", "semantic", 320, false),
			ExportOptions{ImgSize: [2]int{320, 320}, Postprocess: "raw"}},
		{"yolo26 depth raw", testModel("yolo26", "depth", 0, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "raw"}},
		{"dynamic keeps the default", testModel("yolo11", "detect", 640, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Dynamic: true, Postprocess: "raw"}},
		{"dynamic yolo26 end2end", testModel("yolo26", "detect", 640, false),
			ExportOptions{ImgSize: [2]int{640, 640}, Dynamic: true, Postprocess: "end2end"}},
	} {
		req := ExportRequest{Format: "onnx"}
		if tt.want.Dynamic {
			req.Dynamic = boolPtr(true)
		}
		got, err := NormalizeExport(tt.m, req)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got.ImgSize != tt.want.ImgSize || got.Dynamic != tt.want.Dynamic || got.Postprocess != tt.want.Postprocess ||
			got.Opset != nil {
			t.Errorf("%s: options = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestNormalizeExportExplicit(t *testing.T) {
	m := testModel("yolo26", "detect", 640, false)
	got, err := NormalizeExport(m, ExportRequest{Format: "onnx", ImgSize: []int{480, 640}, Dynamic: boolPtr(true),
		Postprocess: "end2end", Opset: intPtr(17)})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImgSize != [2]int{480, 640} || !got.Dynamic || got.Postprocess != "end2end" || got.Opset == nil || *got.Opset != 17 {
		t.Errorf("options = %+v", got)
	}
	if key := OptionsKey(got); key != "imgsz=480x640;dynamic=1;postprocess=end2end;opset=17" {
		t.Errorf("key = %s", key)
	}
	got, err = NormalizeExport(m, ExportRequest{Format: "onnx", ImgSize: []int{480, 640}, Dynamic: boolPtr(false),
		Postprocess: "nms", Opset: intPtr(17)})
	if err != nil {
		t.Fatal(err)
	}
	if key := OptionsKey(got); key != "imgsz=480x640;dynamic=0;postprocess=nms;opset=17" {
		t.Errorf("key = %s", key)
	}
	// Dynamic with raw outputs is fine.
	if got, err = NormalizeExport(testModel("yolo11", "pose", 640, false), ExportRequest{Format: "onnx",
		Dynamic: boolPtr(true), Postprocess: "raw"}); err != nil || !got.Dynamic || got.Postprocess != "raw" {
		t.Errorf("dynamic raw = %+v, %v", got, err)
	}
	got, err = NormalizeExport(m, ExportRequest{Format: "onnx", ImgSize: []int{320}, Dynamic: boolPtr(false), Postprocess: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImgSize != [2]int{320, 320} || got.Dynamic || got.Postprocess != "raw" {
		t.Errorf("options = %+v", got)
	}
	if key := OptionsKey(got); key != "imgsz=320x320;dynamic=0;postprocess=raw;opset=auto" {
		t.Errorf("key = %s", key)
	}
	// The bounds are valid.
	for _, opset := range []int{11, 21} {
		if _, err := NormalizeExport(m, ExportRequest{Format: "onnx", ImgSize: []int{32, 4096}, Opset: intPtr(opset)}); err != nil {
			t.Errorf("opset %d: %v", opset, err)
		}
	}
}

func TestNormalizeExportInvalid(t *testing.T) {
	v11 := testModel("yolo11", "detect", 640, false)
	for _, tt := range []struct {
		name string
		m    Model
		req  ExportRequest
		msg  string
	}{
		{"no format", v11, ExportRequest{}, "不支持的转换格式"},
		{"other format", v11, ExportRequest{Format: "engine"}, "目前只支持 onnx"},
		{"zero size", v11, ExportRequest{Format: "onnx", ImgSize: []int{0}}, "输入尺寸必须在 32–4096 之间"},
		{"not a stride multiple", v11, ExportRequest{Format: "onnx", ImgSize: []int{640, 500}}, "（收到 500）"},
		{"too large", v11, ExportRequest{Format: "onnx", ImgSize: []int{4128}}, "输入尺寸"},
		{"negative", v11, ExportRequest{Format: "onnx", ImgSize: []int{-32}}, "输入尺寸"},
		{"three values", v11, ExportRequest{Format: "onnx", ImgSize: []int{640, 640, 3}}, "一个整数或 [高, 宽]"},
		{"unknown postprocess", v11, ExportRequest{Format: "onnx", Postprocess: "NMS"}, "raw、nms 或 end2end"},
		// Ultralytics silently ignores nms=False without an end-to-end head.
		{"end2end without the head", v11, ExportRequest{Format: "onnx", Postprocess: "end2end"}, "YOLO11 模型没有端到端"},
		{"end2end for yolov8", testModel("yolov8", "segment", 0, false), ExportRequest{Format: "onnx", Postprocess: "end2end"}, "YOLOv8 模型"},
		{"classify nms", testModel("yolo11", "classify", 224, false), ExportRequest{Format: "onnx", Postprocess: "nms"}, "分类模型只支持原始输出"},
		{"classify end2end", testModel("yolo26", "classify", 224, false), ExportRequest{Format: "onnx", Postprocess: "end2end"}, "分类模型只支持原始输出"},
		{"nms without boxes", testModel("yolo11", "", 0, false), ExportRequest{Format: "onnx", Postprocess: "nms"}, "不能嵌入 NMS"},
		// YOLO26 semantic segmentation and depth models have no end-to-end
		// head; nms is not suggested for them.
		{"semantic end2end", testModel("yolo26", "semantic", 640, false), ExportRequest{Format: "onnx", Postprocess: "end2end"},
			"语义分割模型没有检测框，不能导出为 end2end，请选择 raw"},
		{"depth end2end", testModel("yolo26", "depth", 640, false), ExportRequest{Format: "onnx", Postprocess: "end2end"},
			"深度估计模型没有检测框，不能导出为 end2end，请选择 raw"},
		{"depth nms", testModel("yolo26", "depth", 640, false), ExportRequest{Format: "onnx", Postprocess: "nms"},
			"深度估计模型没有检测框，不能嵌入 NMS，请选择 raw"},
		// Embedded NMS is traced for batch 1: no dynamic batch.
		{"dynamic nms", v11, ExportRequest{Format: "onnx", Dynamic: boolPtr(true), Postprocess: "nms"},
			"嵌入 NMS 的 ONNX 只能固定 batch 为 1，不能与动态尺寸同时使用；请关闭动态尺寸，或选择原始输出（raw）"},
		{"dynamic nms with an end-to-end head", testModel("yolo26", "segment", 640, false),
			ExportRequest{Format: "onnx", Dynamic: boolPtr(true), Postprocess: "nms", ImgSize: []int{320}},
			"请关闭动态尺寸，或选择原始输出（raw）/ 端到端（end2end）"},
		{"opset low", v11, ExportRequest{Format: "onnx", Opset: intPtr(10)}, "opset 必须在 11–21 之间"},
		{"opset high", v11, ExportRequest{Format: "onnx", Opset: intPtr(22)}, "opset"},
	} {
		_, err := NormalizeExport(tt.m, tt.req)
		wantKind(t, err, ErrInvalid, tt.msg)
		if t.Failed() {
			t.Logf("case %s", tt.name)
		}
	}
	// Only models that have it are pointed to end2end.
	_, err := NormalizeExport(v11, ExportRequest{Format: "onnx", Dynamic: boolPtr(true), Postprocess: "nms"})
	if err == nil || strings.Contains(err.Error(), "end2end") || strings.Contains(err.Error(), "端到端") {
		t.Errorf("dynamic nms of YOLO11: %v", err)
	}
}

func TestExportFileNames(t *testing.T) {
	if got := ModelFileName("A 线外观检测", 3); got != "A 线外观检测_v3.pt" {
		t.Errorf("ModelFileName = %s", got)
	}
	if got := ModelFileName(`a\b/c:d*e?f"g<h>i|j`+"\tk\x7f", 12); got != "a_b_c_d_e_f_g_h_i_j_k__v12.pt" {
		t.Errorf("ModelFileName with unsafe characters = %s", got)
	}
	if got := ModelFileName("", 1); got != "model_v1.pt" {
		t.Errorf("ModelFileName of an empty name = %s", got)
	}
	for _, tt := range []struct {
		o    ExportOptions
		want string
	}{
		{ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "raw"}, "线体_v3_640.onnx"},
		{ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: "end2end"}, "线体_v3_640_end2end.onnx"},
		{ExportOptions{ImgSize: [2]int{480, 640}, Dynamic: true, Postprocess: "end2end", Opset: intPtr(17)},
			"线体_v3_480x640_dynamic_end2end_opset17.onnx"},
		{ExportOptions{ImgSize: [2]int{480, 640}, Postprocess: "nms"}, "线体_v3_480x640_nms.onnx"},
		{ExportOptions{ImgSize: [2]int{320, 320}, Dynamic: true, Postprocess: "raw", Opset: intPtr(11)},
			"线体_v3_320_dynamic_opset11.onnx"},
	} {
		if got := ExportFileName("线体", 3, tt.o); got != tt.want {
			t.Errorf("ExportFileName(%+v) = %s, want %s", tt.o, got, tt.want)
		}
	}
}

func TestCheckYOLOVersion(t *testing.T) {
	info := func(version, source, yaml string) *checkpoint.Info {
		return &checkpoint.Info{YOLOVersion: version, VersionSource: source, YAMLFile: yaml}
	}
	for _, tt := range []struct {
		chosen string
		info   *checkpoint.Info
		msg    string // "" accepted
	}{
		{"yolo11", info("yolo11", "yaml_file", "yolo11n.yaml"), ""},
		{"yolo11", info("", "", ""), ""},                  // unrecognized: the choice stands
		{"yolo11", info("yolov8", "", "custom.yaml"), ""}, // no reliable source
		{"yolo11", info("yolov8", "yaml_file", "yolov8n.yaml"),
			"所选的版本是 YOLO11，但文件中的模型是 YOLOv8（yolov8n.yaml）。请改选 YOLOv8 后再保存"},
		{"yolov8", info("yolo26", "architecture", "my-net.yaml"),
			"所选的版本是 YOLOv8，但文件中的模型是 YOLO26（按网络结构识别，my-net.yaml）。请改选 YOLO26 后再保存"},
		{"yolov8", info("yolov10", "architecture", ""), "文件中的模型是 YOLOv10（按网络结构识别）。"},
	} {
		err := checkYOLOVersion(tt.chosen, tt.info)
		if tt.msg == "" {
			if err != nil {
				t.Errorf("checkYOLOVersion(%s, %+v) = %v", tt.chosen, *tt.info, err)
			}
			continue
		}
		wantKind(t, err, ErrVersionMismatch, tt.msg)
		var me *Error
		if errors.As(err, &me) && me.detected != tt.info.YOLOVersion {
			t.Errorf("detected = %q", me.detected)
		}
	}
}

func TestNormalizeYOLOVersion(t *testing.T) {
	for _, v := range []string{"yolov8", "yolov9", "yolov10", "yolo11", "yolo12", " yolo26 "} {
		if got, err := normalizeYOLOVersion(v); err != nil || got != strings.TrimSpace(v) {
			t.Errorf("normalizeYOLOVersion(%q) = %q, %v", v, got, err)
		}
	}
	_, err := normalizeYOLOVersion("")
	wantKind(t, err, ErrInvalid, "请选择 YOLO 版本")
	for _, v := range []string{"yolov5", "YOLO11", "11", "yolo11n"} {
		_, err := normalizeYOLOVersion(v)
		wantKind(t, err, ErrInvalid, "不支持的 YOLO 版本："+v)
	}
}

func wantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	var me *Error
	if !errors.As(err, &me) || !errors.Is(err, kind) || !strings.Contains(me.Message, msgPart) {
		t.Errorf("err = %v, want kind %v containing %q", err, kind, msgPart)
	}
}
