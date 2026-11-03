package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

var (
	uploadKeys = []string{"id", "project_id", "yolo_version", "description", "file_name", "file_size", "received_bytes",
		"status", "detected_yolo_version", "error", "model_id", "created_at", "updated_at"}
	modelKeys = []string{"id", "project", "version", "description", "source", "training_job_id", "yolo_version", "task",
		"file_name", "size_bytes", "sha256", "info", "file_missing", "export_count", "download_url", "created_at", "updated_at"}
	modelInfoKeys = []string{"task", "model_class", "head", "yaml_file", "scale", "detected_yolo_version", "version_source",
		"class_names", "image_size", "channels", "parameters", "end2end", "ultralytics_version", "trained_at", "epochs",
		"epoch", "best_fitness", "metrics", "train_data", "base_model", "training_checkpoint"}
	exportKeys = []string{"id", "model_id", "format", "options", "status", "stage", "attempts", "node", "queue_position",
		"workers_online", "file_name", "size_bytes", "sha256", "result", "error", "error_detail", "download_url",
		"created_at", "updated_at", "started_at", "finished_at"}
	exportOptionKeys = []string{"imgsz", "dynamic", "postprocess", "opset"}
	exportResultKeys = []string{"ultralytics_version", "torch_version", "onnx_version", "onnxruntime_version", "opset",
		"end2end", "nms", "dynamic", "inputs", "outputs", "verified", "duration_ms", "node_name"}
	tensorKeys    = []string{"name", "shape", "type"}
	taskKeys      = []string{"kind", "id", "lease_seconds", "model", "options", "input_url", "progress_url", "output_url", "complete_url", "fail_url"}
	taskModelKeys = []string{"id", "project_id", "version", "yolo_version", "task", "file_name", "size_bytes", "sha256", "info"}
	latestKeys    = []string{"id", "version", "yolo_version", "created_at"}
)

// modelEnv serves the project, compute and model APIs on a private
// database. Its model service reads fake weights files (fakeWeights).
type modelEnv struct {
	*apiEnv
	models *model.Service
}

const fakeWeightsMagic = "fake-pt"

// fakeWeights is a weights file fakeInspect understands: the magic line,
// the checkpoint.Info as JSON and padding.
func fakeWeights(t *testing.T, yolo, task, padding string) []byte {
	t.Helper()
	info := checkpoint.Info{ModelClass: "DetectionModel", Task: task, YOLOVersion: yolo, VersionSource: "yaml_file",
		YAMLFile: yolo + "n.yaml", Scale: "n", ClassNames: []string{"scratch", "dent"}, ImageSize: 640, Channels: 3,
		Head: "Detect", End2End: yolo == "yolo26"}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(fakeWeightsMagic + "\n" + string(data) + "\n" + padding)
}

func fakeInspect(r io.ReaderAt, size int64) (*checkpoint.Info, error) {
	data, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	head, rest, _ := strings.Cut(string(data), "\n")
	if head != fakeWeightsMagic {
		return nil, &checkpoint.Error{Reason: checkpoint.ReasonNotTorch, Message: "文件不是 PyTorch 模型文件（.pt）"}
	}
	line, _, _ := strings.Cut(rest, "\n")
	var info checkpoint.Info
	if err := json.Unmarshal([]byte(line), &info); err != nil {
		return nil, &checkpoint.Error{Reason: checkpoint.ReasonCorrupt, Message: "模型文件已损坏"}
	}
	return &info, nil
}

func newModelEnv(t *testing.T, token string) *modelEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log, Inspect: fakeInspect})
	h := NewHandler(Options{Logger: log, DB: fakeDB{}, Storage: st, StartedAt: time.Now(),
		Datasets: dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log}),
		Projects: project.NewService(project.Options{DB: db.Pool, Logger: log}),
		Compute:  compute.NewService(compute.Options{DB: db.Pool, Logger: log, HeartbeatInterval: 10 * time.Second}),
		Models:   models, NodeToken: token})
	return &modelEnv{apiEnv: &apiEnv{h: h, st: st}, models: models}
}

func (e *modelEnv) project(t *testing.T, name string) project.Project {
	t.Helper()
	return want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]string{"name": name}), http.StatusCreated)
}

// uploadModel uploads and commits weights in one chunk.
func (e *modelEnv) uploadModel(t *testing.T, projectID int64, yolo string, data []byte) model.Model {
	t.Helper()
	up := want[model.Upload](t, e.do("POST", "/api/v1/models/uploads", map[string]any{"project_id": projectID,
		"yolo_version": yolo, "file_name": "best.pt", "file_size": len(data)}), http.StatusCreated)
	want[model.Upload](t, e.do("PUT", fmt.Sprintf("/api/v1/models/uploads/%d?offset=0", up.ID), data), http.StatusOK)
	return want[model.Model](t, e.do("POST", fmt.Sprintf("/api/v1/models/uploads/%d/commit", up.ID), nil), http.StatusCreated)
}

func (e *modelEnv) register(t *testing.T, key string, header ...string) int64 {
	t.Helper()
	res := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody(key, key, "null", "null"), header...), http.StatusOK)
	return res.NodeID
}

// raw sends a request with full control over the body and headers.
func (e *modelEnv) raw(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestModelAPIUpload(t *testing.T) {
	e := newModelEnv(t, "")
	p := e.project(t, "A 线外观检测")
	data := fakeWeights(t, "yolo11", "detect", strings.Repeat("w", 100))

	// Create.
	rec := e.do("POST", "/api/v1/models/uploads", map[string]any{"project_id": p.ID, "yolo_version": "yolov8",
		"file_name": "best.pt", "file_size": len(data), "description": " 夜班补充数据 "})
	raw := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "upload", raw, uploadKeys...)
	if raw["status"] != "uploading" || raw["received_bytes"] != 0.0 || raw["detected_yolo_version"] != nil ||
		raw["model_id"] != nil || raw["description"] != "夜班补充数据" {
		t.Errorf("upload = %v", raw)
	}
	id := int64(raw["id"].(float64))
	base := fmt.Sprintf("/api/v1/models/uploads/%d", id)

	for _, tt := range []struct {
		body   any
		status int
		code   string
		msg    string
	}{
		{map[string]any{"project_id": p.ID, "file_name": "best.pt", "file_size": 10}, 422, codeValidationFailed, "请选择 YOLO 版本"},
		{map[string]any{"project_id": p.ID, "yolo_version": "yolov5", "file_name": "best.pt", "file_size": 10}, 422, codeValidationFailed, "不支持的 YOLO 版本"},
		{map[string]any{"project_id": p.ID, "yolo_version": "yolo11", "file_name": "best.onnx", "file_size": 10}, 422, codeValidationFailed, ".pt"},
		{map[string]any{"project_id": p.ID, "yolo_version": "yolo11", "file_name": "best.pt", "file_size": 0}, 422, codeValidationFailed, "模型文件大小无效"},
		{map[string]any{"project_id": p.ID, "yolo_version": "yolo11", "file_name": "best.pt", "file_size": 8<<30 + 1}, 422, codeValidationFailed, "8 GiB"},
		{map[string]any{"project_id": 99999, "yolo_version": "yolo11", "file_name": "best.pt", "file_size": 10}, 422, codeValidationFailed, "所选项目不存在"},
		{map[string]any{"project_id": "x"}, 400, codeBadRequest, "请求格式不正确"},
		{"{", 400, codeBadRequest, "请求格式不正确"},
	} {
		wantError(t, e.do("POST", "/api/v1/models/uploads", tt.body), tt.status, tt.code, tt.msg)
	}

	// Chunks.
	half := len(data) / 2
	wantError(t, e.do("PUT", base, data[:half]), 400, codeBadRequest, "参数 offset 无效")
	wantError(t, e.do("PUT", base+"?offset=-1", data[:half]), 400, codeBadRequest, "参数 offset 无效")
	wantError(t, e.do("PUT", base+"?offset=5", data[:half]), 409, codeUploadOffsetMismatch, "服务器已接收 0 字节")
	up := want[model.Upload](t, e.do("PUT", base+"?offset=0", data[:half]), 200)
	if up.ReceivedBytes != int64(half) {
		t.Errorf("received = %d", up.ReceivedBytes)
	}
	big := httptest.NewRequest("PUT", base+fmt.Sprintf("?offset=%d", half), bytes.NewReader(data[half:]))
	big.ContentLength = model.MaxChunkBytes + 1
	wantError(t, e.raw(big), 413, codePayloadTooLarge, "64 MiB")
	wantError(t, e.do("POST", base+"/commit", nil), 409, codeConflict, "尚未上传完成")
	want[model.Upload](t, e.do("PUT", base+fmt.Sprintf("?offset=%d", half), data[half:]), 200)
	if up := want[model.Upload](t, e.do("GET", base, nil), 200); up.ReceivedBytes != int64(len(data)) {
		t.Errorf("upload = %+v", up)
	}

	// Commit: the chosen version must match the file.
	wantError(t, e.do("POST", base+"/commit", "{"), 400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("POST", base+"/commit", nil), 422, codeYOLOVersionMismatch,
		"所选的版本是 YOLOv8，但文件中的模型是 YOLO11（yolo11n.yaml）。请改选 YOLO11 后再保存")
	up = want[model.Upload](t, e.do("GET", base, nil), 200)
	if up.Status != model.UploadUploading || up.DetectedYOLOVersion == nil || *up.DetectedYOLOVersion != "yolo11" {
		t.Errorf("after mismatch = %+v", up)
	}
	rec = e.do("POST", base+"/commit", map[string]any{"yolo_version": "yolo11", "description": "改过的说明"})
	m := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "model", m, modelKeys...)
	wantKeys(t, "model project", object(t, m["project"], "project"), "id", "name")
	wantKeys(t, "model info", object(t, m["info"], "info"), modelInfoKeys...)
	modelID := int64(m["id"].(float64))
	if m["version"] != 1.0 || m["yolo_version"] != "yolo11" || m["task"] != "detect" || m["source"] != "upload" ||
		m["training_job_id"] != nil || m["description"] != "改过的说明" || m["file_missing"] != false ||
		m["sha256"] != hexSHA256(data) || m["size_bytes"] != float64(len(data)) || m["export_count"] != 0.0 ||
		m["download_url"] != fmt.Sprintf("/api/v1/models/%d/file", modelID) {
		t.Errorf("model = %v", m)
	}
	// Committing again returns the same model.
	if again := want[model.Model](t, e.do("POST", base+"/commit", nil), 200); again.ID != modelID {
		t.Errorf("second commit = %+v", again)
	}
	if up := want[model.Upload](t, e.do("GET", base, nil), 200); up.Status != model.UploadCompleted ||
		up.ModelID == nil || *up.ModelID != modelID {
		t.Errorf("completed upload = %+v", up)
	}
	wantError(t, e.do("PUT", base+"?offset=0", data), 409, codeConflict, "上传已完成")

	// A file that is not a model fails its upload.
	junk := []byte("PK\x03\x04 not a model")
	bad := want[model.Upload](t, e.do("POST", "/api/v1/models/uploads", map[string]any{"project_id": p.ID,
		"yolo_version": "yolo11", "file_name": "BEST.PT", "file_size": len(junk)}), 201)
	badURL := fmt.Sprintf("/api/v1/models/uploads/%d", bad.ID)
	want[model.Upload](t, e.do("PUT", badURL+"?offset=0", junk), 200)
	wantError(t, e.do("POST", badURL+"/commit", nil), 422, codeInvalidModel, "文件不是 PyTorch 模型文件")
	if up := want[model.Upload](t, e.do("GET", badURL, nil), 200); up.Status != model.UploadFailed || up.Error == "" {
		t.Errorf("failed upload = %+v", up)
	}

	// Cancel.
	c := want[model.Upload](t, e.do("POST", "/api/v1/models/uploads", map[string]any{"project_id": p.ID,
		"yolo_version": "yolo11", "file_name": "best.pt", "file_size": 10}), 201)
	cancelURL := fmt.Sprintf("/api/v1/models/uploads/%d/cancel", c.ID)
	if up := want[model.Upload](t, e.do("POST", cancelURL, nil), 200); up.Status != model.UploadCanceled {
		t.Errorf("canceled = %+v", up)
	}
	if up := want[model.Upload](t, e.do("POST", cancelURL, nil), 200); up.Status != model.UploadCanceled {
		t.Errorf("canceled again = %+v", up)
	}
	wantError(t, e.do("PUT", fmt.Sprintf("/api/v1/models/uploads/%d?offset=0", c.ID), []byte("x")), 409, codeConflict, "上传已取消")
	wantError(t, e.do("GET", "/api/v1/models/uploads/99999", nil), 404, codeNotFound, "上传任务不存在")
	wantError(t, e.do("POST", "/api/v1/models/uploads/99999/commit", nil), 404, codeNotFound, "上传任务不存在")
	wantError(t, e.do("POST", "/api/v1/models/uploads/99999/cancel", nil), 404, codeNotFound, "上传任务不存在")
}

func TestModelAPIModels(t *testing.T) {
	e := newModelEnv(t, "")
	p := e.project(t, "A 线外观检测")
	other := e.project(t, "Other: 线/B")
	v1 := e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "1"))
	v2 := e.uploadModel(t, p.ID, "yolo26", fakeWeights(t, "yolo26", "detect", "2"))
	o1 := e.uploadModel(t, other.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "3"))
	if v1.Version != 1 || v2.Version != 2 || o1.Version != 1 {
		t.Fatalf("versions = %d %d %d", v1.Version, v2.Version, o1.Version)
	}

	// List, newest first, with filters.
	list := want[map[string]any](t, e.do("GET", "/api/v1/models", nil), 200)
	if list["total"] != 3.0 {
		t.Fatalf("list = %v", list)
	}
	wantKeys(t, "listed model", firstItem(t, list["items"], "items"), modelKeys...)
	ids := func(target string) []int64 {
		t.Helper()
		l := want[List[model.Model]](t, e.do("GET", target, nil), 200)
		out := []int64{}
		for _, m := range l.Items {
			out = append(out, m.ID)
		}
		return out
	}
	for target, wantIDs := range map[string][]int64{
		"/api/v1/models": {o1.ID, v2.ID, v1.ID},
		fmt.Sprintf("/api/v1/models?project_id=%d", p.ID):   {v2.ID, v1.ID},
		"/api/v1/models?yolo_version=yolo26":                {v2.ID},
		"/api/v1/models?source=training":                    {},
		"/api/v1/models?source=upload&q=other":              {o1.ID},
		"/api/v1/models?q=" + "%E7%BA%BF":                   {o1.ID, v2.ID, v1.ID}, // 线, in both project names
		"/api/v1/models?project_id=99999":                   {},
		fmt.Sprintf("/api/v1/projects/%d/models", p.ID):     {v2.ID, v1.ID},
		fmt.Sprintf("/api/v1/projects/%d/models", other.ID): {o1.ID},
	} {
		if got := ids(target); fmt.Sprint(got) != fmt.Sprint(wantIDs) {
			t.Errorf("GET %s = %v, want %v", target, got, wantIDs)
		}
	}
	for _, q := range []string{"project_id=0", "project_id=x", "yolo_version=yolov5", "source=bogus"} {
		rec := e.do("GET", "/api/v1/models?"+q, nil)
		if rec.Code != 400 || decode[errorBody](t, rec).Error.Code != codeBadRequest {
			t.Errorf("GET /models?%s = %d %s", q, rec.Code, rec.Body)
		}
	}
	wantError(t, e.do("GET", "/api/v1/projects/99999/models", nil), 404, codeNotFound, "项目不存在")

	// The project shows its models.
	rawProject := want[map[string]any](t, e.do("GET", fmt.Sprintf("/api/v1/projects/%d", p.ID), nil), 200)
	wantKeys(t, "project", rawProject, projectKeys...)
	if rawProject["model_count"] != 2.0 {
		t.Errorf("model_count = %v", rawProject["model_count"])
	}
	latest := object(t, rawProject["latest_model"], "latest_model")
	wantKeys(t, "latest model", latest, latestKeys...)
	if latest["id"] != float64(v2.ID) || latest["version"] != 2.0 || latest["yolo_version"] != "yolo26" {
		t.Errorf("latest_model = %v", latest)
	}

	// Detail and update.
	base := fmt.Sprintf("/api/v1/models/%d", v1.ID)
	if m := want[model.Model](t, e.do("GET", base, nil), 200); m.ID != v1.ID || m.Project.Name != "A 线外观检测" {
		t.Errorf("model = %+v", m)
	}
	if m := want[model.Model](t, e.do("PATCH", base, map[string]any{"description": " 新说明 "}), 200); m.Description != "新说明" {
		t.Errorf("updated = %+v", m)
	}
	wantError(t, e.do("PATCH", base, map[string]any{"description": strings.Repeat("字", 501)}), 422, codeValidationFailed, "500")
	wantError(t, e.do("PATCH", "/api/v1/models/99999", map[string]any{"description": "x"}), 404, codeNotFound, "模型不存在")

	// Download, with ranges.
	weights := fakeWeights(t, "yolo11", "detect", "1")
	rec := e.do("GET", base+"/file", nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), weights) {
		t.Fatalf("download = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if h := rec.Header(); h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Accept-Ranges") != "bytes" || h.Get("Content-Disposition") != attachmentDisposition("A 线外观检测_v1.pt") {
		t.Errorf("download headers = %v", h)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	rec = e.do("GET", base+"/file", nil, "Range", "bytes=0-6")
	if rec.Code != 206 || rec.Body.String() != "fake-pt" {
		t.Errorf("range = %d %q", rec.Code, rec.Body)
	}
	rec = e.do("GET", fmt.Sprintf("/api/v1/models/%d/file", o1.ID), nil)
	if got := rec.Header().Get("Content-Disposition"); got != attachmentDisposition("Other_ 线_B_v1.pt") {
		t.Errorf("Content-Disposition = %q", got)
	}

	// A file removed on the server.
	if err := os.Remove(filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", p.ID), "v1", "model.pt")); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", base+"/file", nil), 404, codeNotFound, "模型文件不存在")
	if m := want[model.Model](t, e.do("GET", base, nil), 200); !m.FileMissing {
		t.Error("file_missing should be true")
	}
	wantError(t, e.do("POST", base+"/exports", map[string]any{"format": "onnx"}), 409, codeConflict, "模型文件不存在")

	// Delete.
	if rec := e.do("DELETE", base, nil); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "模型不存在")
	wantError(t, e.do("DELETE", base, nil), 404, codeNotFound, "模型不存在")
	if exists := dirExists(filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", p.ID), "v1")); exists {
		t.Error("the model folder should be removed")
	}
	// Versions are not reused.
	if v3 := e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "4")); v3.Version != 3 {
		t.Errorf("next version = %d", v3.Version)
	}
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestModelAPIRouting(t *testing.T) {
	e := newModelEnv(t, "")
	for _, target := range []string{"/api/v1/models/0", "/api/v1/models/abc", "/api/v1/models/-1", "/api/v1/models/42",
		"/api/v1/models/42/file", "/api/v1/models/42/exports", "/api/v1/models/99999999999999999999"} {
		wantError(t, e.do("GET", target, nil), 404, codeNotFound, "模型不存在")
	}
	for _, target := range []string{"/api/v1/models/uploads/0", "/api/v1/models/uploads/x", "/api/v1/models/uploads/42"} {
		wantError(t, e.do("GET", target, nil), 404, codeNotFound, "上传任务不存在")
	}
	p := e.project(t, "p")
	m := e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", ""))
	for _, suffix := range []string{"/exports/0", "/exports/x", "/exports/42", "/exports/42/file"} {
		wantError(t, e.do("GET", fmt.Sprintf("/api/v1/models/%d%s", m.ID, suffix), nil), 404, codeNotFound, "转换任务不存在")
	}
	wantError(t, e.do("PATCH", "/api/v1/models", nil), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("POST", fmt.Sprintf("/api/v1/models/%d", m.ID), nil), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("GET", "/api/v1/models/1/bogus", nil), 404, codeNotFound, "")
	if l := want[List[model.Model]](t, e.do("GET", "/api/v1/models?project_id=99999", nil), 200); l.Total != 0 || l.Items == nil {
		t.Errorf("empty list = %+v", l)
	}
	// The agent routes need the X-Node-Key header.
	wantError(t, e.do("POST", "/api/v1/compute/agent/model-exports/1/progress", `{"stage": "converting"}`), 400, codeBadRequest, "X-Node-Key")
	wantError(t, e.do("POST", "/api/v1/compute/agent/model-exports/x/progress", `{}`), 404, codeNotFound, "转换任务不存在")
}

func TestModelAPIProjectDelete(t *testing.T) {
	e := newModelEnv(t, "")
	p := e.project(t, "p")
	e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "1"))
	e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "2"))
	base := fmt.Sprintf("/api/v1/projects/%d", p.ID)
	projectDir := filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", p.ID))

	wantError(t, e.do("DELETE", base, nil), 409, codeConflict, "项目中有 2 个模型，请先删除模型，或选择同时删除模型")
	wantError(t, e.do("DELETE", base+"?delete_models=false", nil), 409, codeConflict, "项目中有 2 个模型")
	wantError(t, e.do("DELETE", base+"?delete_models=maybe", nil), 400, codeBadRequest, "参数 delete_models 无效")
	if !dirExists(projectDir) {
		t.Fatal("the models must be kept")
	}
	if rec := e.do("DELETE", base+"?delete_models=true", nil); rec.Code != 204 {
		t.Fatalf("delete with models = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "项目不存在")
	if dirExists(projectDir) {
		t.Error("the project's model folder should be removed")
	}
	if l := want[List[model.Model]](t, e.do("GET", "/api/v1/models", nil), 200); l.Total != 0 {
		t.Errorf("models left = %+v", l)
	}
	wantError(t, e.do("DELETE", base+"?delete_models=true", nil), 404, codeNotFound, "项目不存在")
	// A project without models needs no flag.
	empty := e.project(t, "empty")
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=1", empty.ID), nil); rec.Code != 204 {
		t.Errorf("delete empty project = %d %s", rec.Code, rec.Body)
	}
}

func TestModelAPIExportFlow(t *testing.T) {
	const token = "s3cr3t-node-token-0123"
	auth := []string{"Authorization", "Bearer " + token}
	e := newModelEnv(t, token)
	p := e.project(t, "A 线外观检测")
	m := e.uploadModel(t, p.ID, "yolo26", fakeWeights(t, "yolo26", "detect", "x"))
	weights := fakeWeights(t, "yolo26", "detect", "x")
	nodeID := e.register(t, "gpu-server-01", auth...)
	e.register(t, "gpu-server-02", auth...)
	exportsURL := fmt.Sprintf("/api/v1/models/%d/exports", m.ID)

	// Request.
	raw := want[map[string]any](t, e.do("POST", exportsURL, map[string]any{"format": "onnx"}), http.StatusAccepted)
	wantKeys(t, "export", raw, exportKeys...)
	opts := object(t, raw["options"], "options")
	wantKeys(t, "export options", opts, exportOptionKeys...)
	if fmt.Sprint(opts["imgsz"]) != "[640 640]" || opts["dynamic"] != false || opts["postprocess"] != "end2end" ||
		opts["opset"] != nil {
		t.Errorf("options = %v", opts)
	}
	if raw["status"] != "queued" || raw["queue_position"] != 1.0 || raw["workers_online"] != 0.0 || raw["node"] != nil ||
		raw["result"] != nil || raw["download_url"] != nil || raw["size_bytes"] != nil ||
		raw["file_name"] != "A 线外观检测_v1_640_end2end.onnx" {
		t.Errorf("export = %v", raw)
	}
	exportID := int64(raw["id"].(float64))
	if again := want[model.Export](t, e.do("POST", exportsURL, map[string]any{"format": "onnx", "imgsz": 640}), 202); again.ID != exportID {
		t.Errorf("same options gave export %d, want %d", again.ID, exportID)
	}
	other := want[model.Export](t, e.do("POST", exportsURL, map[string]any{"format": "onnx", "imgsz": []int{480, 640},
		"postprocess": "nms", "dynamic": true, "opset": 17}), 202)
	if other.Options.ImgSize != [2]int{480, 640} || other.FileName != "A 线外观检测_v1_480x640_dynamic_nms_opset17.onnx" {
		t.Errorf("other = %+v", other)
	}
	for _, body := range []any{
		map[string]any{"format": "onnx", "imgsz": "640"},
		map[string]any{"format": "onnx", "imgsz": 640.5},
		map[string]any{"format": "onnx", "imgsz": []any{}},
		map[string]any{"format": "onnx", "imgsz": []int{1, 2, 3}},
		map[string]any{"format": "onnx", "imgsz": 16},
		map[string]any{"format": "pdf"},
		map[string]any{},
		map[string]any{"format": "onnx", "postprocess": "bogus"},
		map[string]any{"format": "onnx", "opset": 9},
	} {
		rec := e.do("POST", exportsURL, body)
		if rec.Code != 422 || decode[errorBody](t, rec).Error.Code != codeValidationFailed {
			t.Errorf("POST exports %v = %d %s", body, rec.Code, rec.Body)
		}
	}
	wantError(t, e.do("POST", "/api/v1/models/99999/exports", map[string]any{"format": "onnx"}), 404, codeNotFound, "模型不存在")
	if l := want[List[model.Export]](t, e.do("GET", exportsURL, nil), 200); l.Total != 2 || l.Items[0].ID != other.ID {
		t.Errorf("exports = %+v", l)
	}

	// Claim: token, node and parameters are checked.
	claim := func(body string, header ...string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/compute/agent/tasks/claim", body, header...)
	}
	wantError(t, claim(`{"node_key": "gpu-server-01", "kinds": ["model_export"]}`), 401, codeUnauthorized, "")
	wantError(t, claim(`{"node_key": "nobody", "kinds": ["model_export"]}`, auth...), 404, codeUnknownNode, "请重新注册")
	wantError(t, claim(`{"kinds": ["model_export"]}`, auth...), 422, codeValidationFailed, "node_key 不能为空")
	wantError(t, claim(`{"node_key": "gpu-server-01", "kinds": ["model_export"], "wait_seconds": 31}`, auth...), 422, codeValidationFailed, "wait_seconds")
	wantError(t, claim(`{"node_key": "gpu-server-01", "kinds": ["model_export"], "wait_seconds": -1}`, auth...), 422, codeValidationFailed, "wait_seconds")
	wantError(t, claim(`{"node_key": "gpu-server-01", "kinds": []}`, auth...), 422, codeValidationFailed, "kinds 不能为空")
	wantError(t, claim(`{"node_key": "gpu-server-01", "kinds": "model_export"}`, auth...), 400, codeBadRequest, "")
	if body := want[map[string]any](t, claim(`{"node_key": "gpu-server-01", "kinds": ["training_job"]}`, auth...), 200); body["task"] != nil {
		t.Errorf("claim of an unknown kind = %v", body)
	}

	body := want[map[string]any](t, claim(`{"node_key": "gpu-server-01", "kinds": ["model_export"], "wait_seconds": 0}`, auth...), 200)
	wantKeys(t, "claim", body, "task")
	task := object(t, body["task"], "task")
	wantKeys(t, "task", task, taskKeys...)
	taskModel := object(t, task["model"], "task model")
	wantKeys(t, "task model", taskModel, taskModelKeys...)
	wantKeys(t, "task model info", object(t, taskModel["info"], "info"), modelInfoKeys...)
	wantKeys(t, "task options", object(t, task["options"], "options"), exportOptionKeys...)
	taskBase := fmt.Sprintf("/api/v1/compute/agent/model-exports/%d/", exportID)
	if task["kind"] != "model_export" || task["id"] != float64(exportID) || task["lease_seconds"] != 60.0 ||
		task["input_url"] != taskBase+"input" || task["progress_url"] != taskBase+"progress" ||
		task["output_url"] != taskBase+"output" || task["complete_url"] != taskBase+"complete" ||
		task["fail_url"] != taskBase+"fail" || taskModel["sha256"] != hexSHA256(weights) || taskModel["version"] != 1.0 {
		t.Errorf("task = %v", task)
	}
	// The node key may come in the header instead.
	if body := want[map[string]any](t, claim(`{"kinds": ["model_export"]}`, append([]string{"X-Node-Key", "gpu-server-02"}, auth...)...), 200); body["task"] == nil {
		t.Errorf("the second node should get the other conversion: %v", body)
	} else if id := object(t, body["task"], "task")["id"]; id != float64(other.ID) {
		t.Errorf("second claim got %v", id)
	}
	running := want[model.Export](t, e.do("GET", fmt.Sprintf("%s/%d", exportsURL, exportID), nil), 200)
	if running.Status != model.ExportRunning || running.Node == nil || running.Node.ID != nodeID ||
		running.Node.Name != "gpu-server-01" || running.Attempts != 1 || running.WorkersOnline != 2 || running.QueuePosition != nil {
		t.Errorf("running = %+v", running)
	}

	// The task calls: token and X-Node-Key.
	node := append([]string{"X-Node-Key", "gpu-server-01"}, auth...)
	intruder := append([]string{"X-Node-Key", "gpu-server-02"}, auth...)
	unknown := append([]string{"X-Node-Key", "nobody"}, auth...)
	wantError(t, e.do("GET", taskBase+"input", nil, "X-Node-Key", "gpu-server-01"), 401, codeUnauthorized, "")
	wantError(t, e.do("GET", taskBase+"input", nil, auth...), 400, codeBadRequest, "缺少请求头 X-Node-Key")
	wantError(t, e.do("GET", taskBase+"input", nil, unknown...), 404, codeUnknownNode, "")
	wantError(t, e.do("GET", taskBase+"input", nil, intruder...), 409, codeConflict, "已不属于该节点")
	wantError(t, e.do("POST", taskBase+"progress", `{"stage": "converting"}`, intruder...), 409, codeConflict, "")
	wantError(t, e.do("POST", "/api/v1/compute/agent/model-exports/99999/progress", `{"stage": "converting"}`, node...), 404, codeNotFound, "转换任务不存在")

	// Input, with ranges.
	rec := e.do("GET", taskBase+"input", nil, node...)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), weights) || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("input = %d, %d bytes, %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	if rec := e.do("GET", taskBase+"input", nil, append([]string{"Range", "bytes=8-"}, node...)...); rec.Code != 206 ||
		!bytes.Equal(rec.Body.Bytes(), weights[8:]) {
		t.Errorf("input range = %d", rec.Code)
	}

	// Progress.
	if body := want[map[string]any](t, e.do("POST", taskBase+"progress", `{"stage": "converting", "message": "exporting"}`, node...), 200); body["lease_seconds"] != 60.0 || len(body) != 1 {
		t.Errorf("progress = %v", body)
	}
	wantError(t, e.do("POST", taskBase+"progress", `{"stage": "done"}`, node...), 422, codeValidationFailed, "stage")

	// Output.
	onnx := []byte(strings.Repeat("onnx ", 1000))
	putTo := func(base string, body io.Reader, size int64, sum string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", base+"output", body)
		req.ContentLength = size
		if sum != "" {
			req.Header.Set("X-Content-Sha256", sum)
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		return e.raw(req)
	}
	put := func(body io.Reader, size int64, sum string, header ...string) *httptest.ResponseRecorder {
		return putTo(taskBase, body, size, sum, header...)
	}
	wantError(t, put(bytes.NewReader(onnx), int64(len(onnx)), hexSHA256(onnx), "X-Node-Key", "gpu-server-01"), 401, codeUnauthorized, "")
	wantError(t, put(bytes.NewReader(onnx), -1, hexSHA256(onnx), node...), 400, codeBadRequest, "Content-Length")
	wantError(t, put(bytes.NewReader(onnx), model.MaxOutputBytes+1, hexSHA256(onnx), node...), 413, codePayloadTooLarge, "4 GiB")
	wantError(t, put(bytes.NewReader(onnx), int64(len(onnx)), "", node...), 400, codeBadRequest, "X-Content-Sha256")
	wantError(t, put(bytes.NewReader(onnx), int64(len(onnx)), hexSHA256([]byte("x")), node...), 422, codeValidationFailed, "SHA-256 校验失败")
	wantError(t, put(bytes.NewReader(onnx), int64(len(onnx)), hexSHA256(onnx), intruder...), 409, codeConflict, "")
	wantError(t, e.do("POST", taskBase+"complete", `{"result": {}}`, node...), 409, codeConflict, "尚未上传转换结果")
	if body := want[map[string]any](t, put(bytes.NewReader(onnx), int64(len(onnx)), hexSHA256(onnx), node...), 200); body["received_bytes"] != float64(len(onnx)) || len(body) != 1 {
		t.Errorf("output = %v", body)
	}

	// Complete.
	result := `{"result": {"ultralytics_version": "8.4.198", "torch_version": "2.14.1+cpu", "onnx_version": "1.23.2",
		"onnxruntime_version": "1.23.2", "opset": 19, "end2end": true, "nms": false, "dynamic": false,
		"inputs": [{"name": "images", "shape": [1, 3, 640, 640], "type": "float32"}],
		"outputs": [{"name": "output0", "shape": [1, 300, 6], "type": "float32"}],
		"verified": true, "duration_ms": 8123, "node_name": "gpu-server-01"}}`
	wantError(t, e.do("POST", taskBase+"complete", result, intruder...), 409, codeConflict, "")
	done := want[map[string]any](t, e.do("POST", taskBase+"complete", result, node...), 200)
	wantKeys(t, "succeeded export", done, exportKeys...)
	res := object(t, done["result"], "result")
	wantKeys(t, "export result", res, exportResultKeys...)
	wantKeys(t, "result input", firstItem(t, res["inputs"], "inputs"), tensorKeys...)
	if done["status"] != "succeeded" || done["sha256"] != hexSHA256(onnx) || done["size_bytes"] != float64(len(onnx)) ||
		done["download_url"] != fmt.Sprintf("%s/%d/file", exportsURL, exportID) || done["finished_at"] == nil {
		t.Errorf("done = %v", done)
	}
	wantError(t, e.do("POST", taskBase+"progress", `{"stage": "uploading"}`, node...), 409, codeConflict, "")

	// The cache, the download.
	if cached := want[model.Export](t, e.do("POST", exportsURL, map[string]any{"format": "onnx", "postprocess": "end2end"}), 200); cached.ID != exportID {
		t.Errorf("cached = %+v", cached)
	}
	if m := want[model.Model](t, e.do("GET", fmt.Sprintf("/api/v1/models/%d", m.ID), nil), 200); m.ExportCount != 1 {
		t.Errorf("export_count = %d", m.ExportCount)
	}
	rec = e.do("GET", fmt.Sprintf("%s/%d/file", exportsURL, exportID), nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), onnx) ||
		rec.Header().Get("Content-Disposition") != attachmentDisposition("A 线外观检测_v1_640_end2end.onnx") {
		t.Errorf("export download = %d %v", rec.Code, rec.Header())
	}
	if rec := e.do("GET", fmt.Sprintf("%s/%d/file", exportsURL, exportID), nil, "Range", "bytes=0-3"); rec.Code != 206 || rec.Body.String() != "onnx" {
		t.Errorf("export range = %d %q", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/%d/file", exportsURL, other.ID), nil), 409, codeConflict, "转换尚未完成")

	// Fail: retryable first, then for good.
	otherBase := fmt.Sprintf("/api/v1/compute/agent/model-exports/%d/", other.ID)
	node2 := intruder
	failed := want[model.Export](t, e.do("POST", otherBase+"fail", `{"error": "上传结果失败：连接被重置", "retryable": true}`, node2...), 200)
	if failed.Status != model.ExportQueued || failed.Attempts != 1 || failed.Node != nil {
		t.Errorf("retryable failure = %+v", failed)
	}
	want[map[string]any](t, claim(`{"node_key": "gpu-server-02", "kinds": ["model_export"]}`, auth...), 200)
	failed = want[model.Export](t, e.do("POST", otherBase+"fail", `{"error": "导出失败：不支持的算子", "detail": "Traceback ..."}`, node2...), 200)
	if failed.Status != model.ExportFailed || failed.Error != "导出失败：不支持的算子" || failed.ErrorDetail != "Traceback ..." {
		t.Errorf("failure = %+v", failed)
	}
	wantError(t, e.do("POST", otherBase+"fail", `{"error": "x"}`, node2...), 409, codeConflict, "")
	// Requesting it again queues it again.
	if again := want[model.Export](t, e.do("POST", exportsURL, map[string]any{"format": "onnx", "imgsz": []int{480, 640},
		"postprocess": "nms", "dynamic": true, "opset": 17}), 202); again.ID != other.ID || again.Status != model.ExportQueued ||
		again.Attempts != 0 || again.Error != "" {
		t.Errorf("requeued = %+v", again)
	}

	// Deleting a running conversion: its node gets 404 from then on.
	want[map[string]any](t, claim(`{"node_key": "gpu-server-02", "kinds": ["model_export"]}`, auth...), 200)
	if rec := e.do("DELETE", fmt.Sprintf("%s/%d", exportsURL, other.ID), nil); rec.Code != 204 {
		t.Fatalf("delete export = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/%d", exportsURL, other.ID), nil), 404, codeNotFound, "转换任务不存在")
	wantError(t, e.do("POST", otherBase+"progress", `{"stage": "converting"}`, node2...), 404, codeNotFound, "转换任务不存在")
	wantError(t, putTo(otherBase, bytes.NewReader(onnx), int64(len(onnx)), hexSHA256(onnx), node2...), 404, codeNotFound, "转换任务不存在")

	// Deleting the model while a conversion runs.
	third := want[model.Export](t, e.do("POST", exportsURL, map[string]any{"format": "onnx", "imgsz": 320}), 202)
	want[map[string]any](t, claim(`{"node_key": "gpu-server-01", "kinds": ["model_export"]}`, auth...), 200)
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/models/%d", m.ID), nil); rec.Code != 204 {
		t.Fatalf("delete model = %d %s", rec.Code, rec.Body)
	}
	thirdBase := fmt.Sprintf("/api/v1/compute/agent/model-exports/%d/", third.ID)
	wantError(t, e.do("POST", thirdBase+"progress", `{"stage": "converting"}`, node...), 404, codeNotFound, "转换任务不存在")
	wantError(t, e.do("GET", thirdBase+"input", nil, node...), 404, codeNotFound, "转换任务不存在")
	wantError(t, e.do("POST", thirdBase+"complete", `{"result": {}}`, node...), 404, codeNotFound, "转换任务不存在")
	wantError(t, e.do("POST", thirdBase+"fail", `{"error": "x"}`, node...), 404, codeNotFound, "转换任务不存在")
}

// TestModelAPIClaimShutdown checks that a waiting claim ends at once when
// the server shuts down, as main.go wires it.
func TestModelAPIClaimShutdown(t *testing.T) {
	e := newModelEnv(t, "")
	e.register(t, "node-a")
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.RegisterOnShutdown(e.models.StopLongPolls)
	srv.Start()
	defer srv.Close()

	type reply struct {
		status int
		body   string
		err    error
	}
	done := make(chan reply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/v1/compute/agent/tasks/claim", "application/json",
			strings.NewReader(`{"node_key": "node-a", "kinds": ["model_export"], "wait_seconds": 30}`))
		if err != nil {
			done <- reply{err: err}
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		done <- reply{resp.StatusCode, string(data), err}
	}()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("shutdown took %v", d)
	}
	select {
	case r := <-done:
		if r.err != nil || r.status != 200 || strings.TrimSpace(r.body) != `{"task":null}` {
			t.Errorf("claim = %d %q, %v", r.status, r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the claim did not return")
	}
}
