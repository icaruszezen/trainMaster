package model

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// fixture reads a weights file from the checkpoint package's test data,
// skipping the test when it is not there.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("checkpoint", "testdata", name))
	if os.IsNotExist(err) {
		t.Skipf("fixture checkpoint/testdata/%s is missing", name)
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// newRealEnv is newTestEnv with the real parser (checkpoint.Inspect).
func newRealEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	e.svc = NewService(Options{DB: e.db.Pool, Storage: e.st, Logger: slog.New(slog.DiscardHandler), Now: e.clock.Now})
	return e
}

// TestIntegrationRealCheckpoints runs uploads of real Ultralytics files
// through the real parser, from upload to conversion request.
func TestIntegrationRealCheckpoints(t *testing.T) {
	ctx := context.Background()

	t.Run("trained detection model", func(t *testing.T) {
		data := fixture(t, "yolo11n-best.pt")
		e := newRealEnv(t)
		projectID := e.project(t, "A 线外观检测")
		up := e.upload(t, projectID, "yolo11", data)
		m, created, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		if err != nil || !created {
			t.Fatalf("CommitUpload: %v", err)
		}
		if m.Task != "detect" || m.YOLOVersion != "yolo11" || m.Version != 1 || m.SizeBytes != int64(len(data)) ||
			m.Info.DetectedYOLOVersion == nil || *m.Info.DetectedYOLOVersion != "yolo11" || m.Info.VersionSource != "yaml_file" ||
			!slices.Equal(m.Info.ClassNames, []string{"scratch", "划痕-凹坑"}) || m.Info.ImageSize == nil || *m.Info.ImageSize != 96 ||
			m.Info.End2End || m.Info.Head != "Detect" || m.Info.UltralyticsVersion == "" {
			t.Errorf("model = %+v", m)
		}
		stored, err := os.ReadFile(e.modelPath(m, modelFile))
		if err != nil || !bytes.Equal(stored, data) {
			t.Errorf("model.pt: %v", err)
		}
		var side map[string]any
		raw, err := os.ReadFile(e.modelPath(m, sidecarFile))
		if err != nil || json.Unmarshal(raw, &side) != nil || side["sha256"] != m.SHA256 || side["yolo_version"] != "yolo11" {
			t.Errorf("model.json = %s (%v)", raw, err)
		}
		if got := AllowedPostprocess(m); !slices.Equal(got, []string{PostprocessRaw, PostprocessNMS}) {
			t.Errorf("allowed postprocess = %v", got)
		}
		exp, ready, err := e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: FormatONNX})
		if err != nil || ready || exp.Options.ImgSize != [2]int{96, 96} || exp.Options.Postprocess != PostprocessRaw ||
			exp.FileName != "A 线外观检测_v1_96.onnx" {
			t.Errorf("export = %+v, %v", exp, err)
		}
	})

	t.Run("end-to-end model and the version check", func(t *testing.T) {
		data := fixture(t, "yolo26n-best.pt")
		e := newRealEnv(t)
		projectID := e.project(t, "p")
		up := e.upload(t, projectID, "yolo11", data)
		_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		wantKind(t, err, ErrVersionMismatch, "所选的版本是 YOLO11，但文件中的模型是 YOLO26（yolo26n.yaml）。请改选 YOLO26 后再保存")
		got, _ := e.svc.GetUpload(ctx, up.ID)
		if got.Status != UploadUploading || got.DetectedYOLOVersion == nil || *got.DetectedYOLOVersion != "yolo26" {
			t.Errorf("upload = %+v", got)
		}
		yolo26 := "yolo26"
		m, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{YOLOVersion: &yolo26})
		if err != nil {
			t.Fatal(err)
		}
		if !m.Info.End2End || m.YOLOVersion != "yolo26" {
			t.Errorf("model = %+v", m)
		}
		if got := AllowedPostprocess(m); !slices.Equal(got, []string{PostprocessEnd2End, PostprocessRaw, PostprocessNMS}) {
			t.Errorf("allowed postprocess = %v", got)
		}
		exp, _, err := e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: FormatONNX})
		if err != nil || exp.Options.Postprocess != PostprocessEnd2End {
			t.Errorf("export = %+v, %v", exp, err)
		}
	})

	t.Run("version recognized by architecture", func(t *testing.T) {
		data := fixture(t, "line-a-detector.pt")
		e := newRealEnv(t)
		up := e.upload(t, e.project(t, "p"), "yolov8", data)
		_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		wantKind(t, err, ErrVersionMismatch, "但文件中的模型是 YOLO26（按网络结构识别")
	})

	t.Run("classification model", func(t *testing.T) {
		data := fixture(t, "yolo11n-cls.pt")
		e := newRealEnv(t)
		up := e.upload(t, e.project(t, "p"), "yolo11", data)
		m, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		if err != nil {
			t.Fatal(err)
		}
		if m.Task != "classify" || len(m.Info.ClassNames) != 1000 {
			t.Errorf("model: task %q, %d classes", m.Task, len(m.Info.ClassNames))
		}
		_, _, err = e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: FormatONNX, Postprocess: PostprocessNMS})
		wantKind(t, err, ErrInvalid, "分类模型只支持原始输出")
	})

	t.Run("files that are not usable models", func(t *testing.T) {
		for name, msg := range map[string]string{
			"yolov5-repo.pt":   "YOLOv5",
			"state-dict.pt":    "state_dict",
			"torchscript.pt":   "TorchScript",
			"rtdetr-l.pt":      "RT-DETR",
			"legacy-format.pt": "旧格式",
		} {
			t.Run(name, func(t *testing.T) {
				data := fixture(t, name)
				e := newRealEnv(t)
				up := e.upload(t, e.project(t, "p"), "yolo11", data)
				_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
				wantKind(t, err, ErrInvalidModel, msg)
				got, _ := e.svc.GetUpload(ctx, up.ID)
				if got.Status != UploadFailed || !strings.Contains(got.Error, msg) || exists(e.svc.uploadDir(up.ID)) {
					t.Errorf("upload = %+v", got)
				}
			})
		}
	})

	t.Run("registered training result", func(t *testing.T) {
		data := fixture(t, "yolo11n-last.pt")
		e := newRealEnv(t)
		projectID := e.project(t, "p")
		weights := filepath.Join(e.st.Dir(storage.Runs), "job-3", "weights", "last.pt")
		if err := os.MkdirAll(filepath.Dir(weights), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(weights, data, 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := e.svc.RegisterTrainedModel(ctx, TrainedModelInput{ProjectID: projectID, TrainingJobID: 3,
			YOLOVersion: "yolo11", WeightsPath: "job-3/weights/last.pt"})
		if err != nil {
			t.Fatal(err)
		}
		if m.Source != SourceTraining || m.FileName != "last.pt" || m.Task != "detect" || m.Info.Epoch == nil {
			t.Errorf("model = %+v", m)
		}
	})
}
