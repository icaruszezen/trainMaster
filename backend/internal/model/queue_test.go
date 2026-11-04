package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// requestExport asks for a conversion and fails the test on an error.
func (e *testEnv) requestExport(t *testing.T, modelID int64, req ExportRequest) (Export, bool) {
	t.Helper()
	if req.Format == "" {
		req.Format = FormatONNX
	}
	exp, ready, err := e.svc.RequestExport(context.Background(), modelID, req)
	if err != nil {
		t.Fatalf("RequestExport: %v", err)
	}
	return exp, ready
}

// claim claims a conversion for the node without waiting.
func (e *testEnv) claim(t *testing.T, nodeID int64) *Task {
	t.Helper()
	task, err := e.claimTask(context.Background(), nodeID, []string{TaskModelExport}, 0)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	return task
}

// claimTask is ClaimTask for tests that expect conversions (or nothing).
func (e *testEnv) claimTask(ctx context.Context, nodeID int64, kinds []string, wait time.Duration) (*Task, error) {
	task, err := e.svc.ClaimTask(ctx, nodeID, kinds, wait)
	if task == nil {
		return nil, err
	}
	conv, ok := task.(*Task)
	if !ok {
		return nil, fmt.Errorf("claimed a %T, want *Task", task)
	}
	return conv, err
}

func (e *testEnv) export(t *testing.T, modelID, exportID int64) Export {
	t.Helper()
	exp, err := e.svc.GetExport(context.Background(), modelID, exportID)
	if err != nil {
		t.Fatalf("GetExport: %v", err)
	}
	return exp
}

func (e *testEnv) putOutput(t *testing.T, exportID, nodeID int64, data []byte) {
	t.Helper()
	n, err := e.svc.PutTaskOutput(context.Background(), exportID, nodeID, int64(len(data)), sha256Hex(data), bytes.NewReader(data))
	if err != nil || n != int64(len(data)) {
		t.Fatalf("PutTaskOutput = %d, %v", n, err)
	}
}

// expireLease moves the lease of a conversion into the past.
func (e *testEnv) expireLease(t *testing.T, exportID int64) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(),
		"UPDATE model_exports SET lease_expires_at = now() - interval '1 second' WHERE id = $1", exportID); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRequestExport(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")

	exp, ready := e.requestExport(t, m.ID, ExportRequest{})
	if ready || exp.Status != ExportQueued || exp.ModelID != m.ID || exp.Format != FormatONNX || exp.Attempts != 0 ||
		exp.Node != nil || exp.QueuePosition == nil || *exp.QueuePosition != 1 || exp.DownloadURL != nil ||
		exp.SizeBytes != nil || exp.StartedAt != nil || string(exp.Result) != "null" {
		t.Errorf("export = %+v", exp)
	}
	if exp.Options != (ExportOptions{ImgSize: [2]int{640, 640}, Postprocess: PostprocessRaw}) {
		t.Errorf("options = %+v", exp.Options)
	}
	if exp.FileName != "p_v1_640.onnx" {
		t.Errorf("file_name = %q", exp.FileName)
	}

	// The same options (spelled differently) give the same conversion.
	same, ready := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{640}, Dynamic: boolPtr(false), Postprocess: "raw"})
	if ready || same.ID != exp.ID || same.Status != ExportQueued {
		t.Errorf("same options = %+v, ready %v", same, ready)
	}
	// New options give a new one, behind it in the queue.
	other, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{480, 640}, Postprocess: "nms", Opset: intPtr(17)})
	if other.ID == exp.ID || other.QueuePosition == nil || *other.QueuePosition != 2 ||
		other.FileName != "p_v1_480x640_nms_opset17.onnx" {
		t.Errorf("other = %+v", other)
	}
	list, err := e.svc.ListExports(ctx, m.ID)
	if err != nil || len(list) != 2 || list[0].ID != other.ID || list[1].ID != exp.ID {
		t.Fatalf("ListExports = %+v, %v", list, err)
	}
	if got, _ := e.svc.GetModel(ctx, m.ID); got.ExportCount != 0 {
		t.Errorf("export_count counts succeeded conversions only, got %d", got.ExportCount)
	}

	// Errors.
	_, _, err = e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: "tensorrt"})
	wantKind(t, err, ErrInvalid, "onnx")
	_, _, err = e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: FormatONNX, Postprocess: "end2end"})
	wantKind(t, err, ErrInvalid, "end2end")
	_, _, err = e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: FormatONNX, ImgSize: []int{20}})
	wantKind(t, err, ErrInvalid, "")
	_, _, err = e.svc.RequestExport(ctx, 99999, ExportRequest{Format: FormatONNX})
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, err = e.svc.GetExport(ctx, m.ID, 99999)
	wantKind(t, err, ErrNotFound, "转换任务不存在")
	_, err = e.svc.GetExport(ctx, 99999, exp.ID)
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, err = e.svc.ListExports(ctx, 99999)
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, err = e.svc.OpenExportFile(ctx, m.ID, exp.ID)
	wantKind(t, err, ErrConflict, "转换尚未完成")

	// Conversions of another model are not reachable through this one.
	m2 := e.addModel(t, m.Project.ID, "yolo11")
	_, err = e.svc.GetExport(ctx, m2.ID, exp.ID)
	wantKind(t, err, ErrNotFound, "转换任务不存在")
	wantKind(t, e.svc.DeleteExport(ctx, m2.ID, exp.ID), ErrNotFound, "转换任务不存在")

	// Without the weights nothing can be converted.
	if err := os.Remove(e.modelPath(m2, modelFile)); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.RequestExport(ctx, m2.ID, ExportRequest{Format: FormatONNX})
	wantKind(t, err, ErrConflict, "模型文件不存在")

	// Deleting queued conversions.
	if err := e.svc.DeleteExport(ctx, m.ID, exp.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.export(t, m.ID, other.ID); got.QueuePosition == nil || *got.QueuePosition != 1 {
		t.Errorf("queue position after delete = %v", got.QueuePosition)
	}
	wantKind(t, e.svc.DeleteExport(ctx, m.ID, exp.ID), ErrNotFound, "转换任务不存在")
	wantKind(t, e.svc.DeleteExport(ctx, 99999, exp.ID), ErrNotFound, "模型不存在")
}

func TestIntegrationExportLifecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo26")
	weights, err := os.ReadFile(e.modelPath(m, modelFile))
	if err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "gpu-server-01")
	other := e.node(t, "gpu-server-02")

	exp, _ := e.requestExport(t, m.ID, ExportRequest{Dynamic: boolPtr(true)})
	if exp.Options.Postprocess != PostprocessEnd2End || !exp.Options.Dynamic || exp.WorkersOnline != 0 {
		t.Errorf("export = %+v", exp)
	}

	task := e.claim(t, node)
	if task == nil {
		t.Fatal("no task")
	}
	base := fmt.Sprintf("/api/v1/compute/agent/model-exports/%d/", exp.ID)
	if task.Kind != TaskModelExport || task.ID != exp.ID || task.LeaseSeconds != 60 || task.Options != exp.Options ||
		task.InputURL != base+"input" || task.ProgressURL != base+"progress" || task.OutputURL != base+"output" ||
		task.CompleteURL != base+"complete" || task.FailURL != base+"fail" {
		t.Errorf("task = %+v", task)
	}
	if task.Model.ID != m.ID || task.Model.ProjectID != m.Project.ID || task.Model.Version != 1 ||
		task.Model.YOLOVersion != "yolo26" || task.Model.Task != "detect" || task.Model.FileName != "best.pt" ||
		task.Model.SizeBytes != int64(len(weights)) || task.Model.SHA256 != sha256Hex(weights) ||
		!task.Model.Info.End2End || !slices.Equal(task.Model.Info.ClassNames, []string{"scratch", "dent"}) {
		t.Errorf("task model = %+v", task.Model)
	}
	if again := e.claim(t, other); again != nil {
		t.Errorf("a claimed conversion was handed out again: %+v", again)
	}

	running := e.export(t, m.ID, exp.ID)
	if running.Status != ExportRunning || running.Stage != StageDownloading || running.Attempts != 1 ||
		running.Node == nil || running.Node.ID != node || running.Node.Name != "gpu-server-01" ||
		running.QueuePosition != nil || running.StartedAt == nil || running.WorkersOnline != 2 {
		t.Errorf("running = %+v", running)
	}

	// Input.
	f, err := e.svc.OpenTaskInput(ctx, exp.ID, node)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f.Content)
	f.Close()
	if err != nil || !bytes.Equal(got, weights) || f.Name != "model.pt" || f.Size != int64(len(weights)) {
		t.Errorf("input: %d bytes, name %q, size %d, %v", len(got), f.Name, f.Size, err)
	}

	// Progress.
	if lease, err := e.svc.ReportProgress(ctx, exp.ID, node, ProgressInput{Stage: StageConverting, Message: "exporting\n"}); err != nil || lease != 60 {
		t.Errorf("ReportProgress = %d, %v", lease, err)
	}
	if got := e.export(t, m.ID, exp.ID); got.Stage != StageConverting {
		t.Errorf("stage = %q", got.Stage)
	}
	_, err = e.svc.ReportProgress(ctx, exp.ID, node, ProgressInput{Stage: "done"})
	wantKind(t, err, ErrInvalid, "stage 必须是")

	// Completing needs the result first.
	_, err = e.svc.CompleteTask(ctx, exp.ID, node, nil)
	wantKind(t, err, ErrConflict, "尚未上传转换结果")

	// Output: every check, and a bad upload leaves nothing behind.
	onnx := []byte(strings.Repeat("onnx-bytes ", 500))
	sum := sha256Hex(onnx)
	part := e.svc.outputPath(exp.ID)
	for _, tt := range []struct {
		size int64
		sum  string
		body []byte
		kind error
		msg  string
	}{
		{0, sum, nil, ErrInvalid, "转换结果是空文件"},
		{MaxOutputBytes + 1, sum, onnx, ErrTooLarge, "不能超过 4 GiB"},
		{int64(len(onnx)), "xyz", onnx, ErrBadRequest, "X-Content-Sha256"},
		{int64(len(onnx)), sum, onnx[:100], ErrBadRequest, "与 Content-Length 不一致"},
		{int64(len(onnx)) - 1, sum, onnx, ErrBadRequest, "与 Content-Length 不一致"},
		{int64(len(onnx)), sha256Hex([]byte("other")), onnx, ErrInvalid, "SHA-256 校验失败"},
	} {
		_, err := e.svc.PutTaskOutput(ctx, exp.ID, node, tt.size, tt.sum, bytes.NewReader(tt.body))
		wantKind(t, err, tt.kind, tt.msg)
		if exists(part) {
			t.Errorf("%s: the rejected result should be removed", tt.msg)
		}
	}
	_, err = e.svc.PutTaskOutput(ctx, exp.ID, node, int64(len(onnx)), sum, &failingReader{data: onnx[:10]})
	if err == nil || exists(part) {
		t.Errorf("cut off upload: %v, part kept %v", err, exists(part))
	}
	// Uploading again replaces the result; the sum may be upper case.
	e.putOutput(t, exp.ID, node, []byte("first try"))
	n, err := e.svc.PutTaskOutput(ctx, exp.ID, node, int64(len(onnx)), strings.ToUpper(sum), bytes.NewReader(onnx))
	if err != nil || n != int64(len(onnx)) {
		t.Fatalf("PutTaskOutput = %d, %v", n, err)
	}
	if got := e.export(t, m.ID, exp.ID); got.Stage != StageUploading || got.SizeBytes != nil || got.SHA256 != "" {
		t.Errorf("after upload = %+v (size and sum show once succeeded)", got)
	}

	// Complete.
	result := json.RawMessage(`{"ultralytics_version": "8.4.198", "opset": 19, "end2end": true, "nms": false,
		"dynamic": true, "inputs": [{"name": "images", "shape": ["batch", 3, "height", "width"], "type": "float32"}],
		"outputs": [{"name": "output0", "shape": ["batch", 300, 6], "type": "float32"}], "verified": true,
		"duration_ms": 8123, "evil": "x"}`)
	done, err := e.svc.CompleteTask(ctx, exp.ID, node, result)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := fmt.Sprintf("/api/v1/models/%d/exports/%d/file", m.ID, exp.ID)
	if done.Status != ExportSucceeded || done.Stage != "" || done.SizeBytes == nil || *done.SizeBytes != int64(len(onnx)) ||
		done.SHA256 != sum || done.DownloadURL == nil || *done.DownloadURL != wantURL || done.FinishedAt == nil ||
		done.Node == nil || done.Node.ID != node || done.FileName != "p_v1_640_dynamic_end2end.onnx" || done.Error != "" {
		t.Errorf("done = %+v", done)
	}
	var res ExportResult
	if err := json.Unmarshal(done.Result, &res); err != nil || res.Opset == nil || *res.Opset != 19 ||
		len(res.Inputs) != 1 || res.Inputs[0].Shape[0] != "batch" || strings.Contains(string(done.Result), "evil") {
		t.Errorf("result = %s (%v)", done.Result, err)
	}
	if exists(part) || exists(e.svc.exportTempDir(exp.ID)) {
		t.Error("the temporary folder should be gone")
	}
	stored, err := os.ReadFile(e.modelPath(m, filepath.Join("onnx", fmt.Sprintf("%d.onnx", exp.ID))))
	if err != nil || !bytes.Equal(stored, onnx) {
		t.Errorf("stored result: %v", err)
	}
	if got, _ := e.svc.GetModel(ctx, m.ID); got.ExportCount != 1 {
		t.Errorf("export_count = %d", got.ExportCount)
	}

	// The node does not hold it any more.
	_, err = e.svc.ReportProgress(ctx, exp.ID, node, ProgressInput{Stage: StageUploading})
	wantKind(t, err, ErrConflict, "已不属于该节点或已结束")
	_, err = e.svc.CompleteTask(ctx, exp.ID, node, nil)
	wantKind(t, err, ErrConflict, "已不属于该节点或已结束")
	_, err = e.svc.FailTask(ctx, exp.ID, node, FailInput{Error: "x"})
	wantKind(t, err, ErrConflict, "已不属于该节点或已结束")
	_, err = e.svc.OpenTaskInput(ctx, exp.ID, node)
	wantKind(t, err, ErrConflict, "已不属于该节点或已结束")

	// Download.
	df, err := e.svc.OpenExportFile(ctx, m.ID, exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(df.Content)
	df.Close()
	if !bytes.Equal(got, onnx) || df.Name != "p_v1_640_dynamic_end2end.onnx" || df.Size != int64(len(onnx)) {
		t.Errorf("download: %d bytes, %q", len(got), df.Name)
	}

	// The cache: the same options are ready at once.
	cached, ready := e.requestExport(t, m.ID, ExportRequest{Dynamic: boolPtr(true), Postprocess: "end2end"})
	if !ready || cached.ID != exp.ID || cached.Status != ExportSucceeded {
		t.Errorf("cached = %+v, ready %v", cached, ready)
	}
	// A result file that is gone (or changed) is converted again.
	if err := os.WriteFile(e.modelPath(m, filepath.Join("onnx", fmt.Sprintf("%d.onnx", exp.ID))), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, ready := e.requestExport(t, m.ID, ExportRequest{Dynamic: boolPtr(true)})
	if ready || again.ID != exp.ID || again.Status != ExportQueued || again.Attempts != 0 || again.Node != nil ||
		again.SizeBytes != nil || again.FinishedAt != nil || string(again.Result) != "null" {
		t.Errorf("requeued = %+v, ready %v", again, ready)
	}
	_, err = e.svc.OpenExportFile(ctx, m.ID, exp.ID)
	wantKind(t, err, ErrConflict, "转换尚未完成")

	// Convert it again, then lose the file: the download says so.
	if task := e.claim(t, other); task == nil || task.ID != exp.ID {
		t.Fatalf("claim = %+v", task)
	}
	e.putOutput(t, exp.ID, other, onnx)
	if _, err := e.svc.CompleteTask(ctx, exp.ID, other, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.modelPath(m, filepath.Join("onnx", fmt.Sprintf("%d.onnx", exp.ID)))); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenExportFile(ctx, m.ID, exp.ID)
	wantKind(t, err, ErrNotFound, "转换结果文件不存在")

	// Deleting a succeeded conversion removes its file.
	exp2, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{320}})
	e.claim(t, node)
	e.putOutput(t, exp2.ID, node, onnx)
	if _, err := e.svc.CompleteTask(ctx, exp2.ID, node, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	file2 := e.modelPath(m, filepath.Join("onnx", fmt.Sprintf("%d.onnx", exp2.ID)))
	if !exists(file2) {
		t.Fatal("result missing")
	}
	if err := e.svc.DeleteExport(ctx, m.ID, exp2.ID); err != nil {
		t.Fatal(err)
	}
	if exists(file2) {
		t.Error("the result file should be removed with its conversion")
	}
}

func TestIntegrationCompleteChecksFile(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "n")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	e.claim(t, node)
	e.putOutput(t, exp.ID, node, []byte("onnx"))

	// The verified file was removed or changed on the server meanwhile.
	if err := os.WriteFile(e.svc.outputPath(exp.ID), []byte("onnx!"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CompleteTask(ctx, exp.ID, node, nil)
	wantKind(t, err, ErrConflict, "服务器上的转换结果不完整")
	if err := os.Remove(e.svc.outputPath(exp.ID)); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CompleteTask(ctx, exp.ID, node, nil)
	wantKind(t, err, ErrConflict, "服务器上的转换结果不完整")
	if got := e.export(t, m.ID, exp.ID); got.Status != ExportRunning {
		t.Errorf("status = %q", got.Status)
	}
	// Upload again and complete.
	e.putOutput(t, exp.ID, node, []byte("onnx"))
	if done, err := e.svc.CompleteTask(ctx, exp.ID, node, nil); err != nil || done.Status != ExportSucceeded {
		t.Errorf("CompleteTask = %+v, %v", done, err)
	}
}

func TestIntegrationTaskOwnership(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	intruder := e.node(t, "b")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})

	// Nobody holds a queued conversion.
	_, err := e.svc.ReportProgress(ctx, exp.ID, node, ProgressInput{Stage: StageConverting})
	wantKind(t, err, ErrConflict, "已不属于该节点")

	e.claim(t, node)
	data := []byte("onnx")
	calls := map[string]func(nodeID int64) error{
		"progress": func(n int64) error {
			_, err := e.svc.ReportProgress(ctx, exp.ID, n, ProgressInput{Stage: StageConverting})
			return err
		},
		"input": func(n int64) error {
			f, err := e.svc.OpenTaskInput(ctx, exp.ID, n)
			if err == nil {
				f.Close()
			}
			return err
		},
		"output": func(n int64) error {
			_, err := e.svc.PutTaskOutput(ctx, exp.ID, n, int64(len(data)), sha256Hex(data), bytes.NewReader(data))
			return err
		},
		"complete": func(n int64) error { _, err := e.svc.CompleteTask(ctx, exp.ID, n, nil); return err },
		"fail":     func(n int64) error { _, err := e.svc.FailTask(ctx, exp.ID, n, FailInput{Error: "x"}); return err },
	}
	for name, call := range calls {
		if err := call(intruder); !errors.Is(err, ErrConflict) {
			t.Errorf("%s by another node: %v, want a conflict", name, err)
		}
	}
	if got := e.export(t, m.ID, exp.ID); got.Status != ExportRunning || got.Node.ID != node {
		t.Fatalf("the intruder changed the conversion: %+v", got)
	}

	// Deleting the conversion while it runs: its node gets "not found"
	// from then on, and the partial result is removed.
	e.putOutput(t, exp.ID, node, data)
	if err := e.svc.DeleteExport(ctx, m.ID, exp.ID); err != nil {
		t.Fatal(err)
	}
	if exists(e.svc.exportTempDir(exp.ID)) {
		t.Error("the partial result should be removed")
	}
	for name, call := range calls {
		err := call(node)
		wantKind(t, err, ErrNotFound, "转换任务不存在")
		if name == "output" && exists(e.svc.exportTempDir(exp.ID)) {
			t.Error("an output for a deleted conversion must not be kept")
		}
	}

	// Deleting the model while a conversion runs.
	exp2, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{320}})
	if task := e.claim(t, node); task == nil || task.ID != exp2.ID {
		t.Fatalf("claim = %+v", task)
	}
	e.putOutput(t, exp2.ID, node, data)
	if _, err := e.svc.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if exists(e.svc.exportTempDir(exp2.ID)) {
		t.Error("the partial result should be removed with the model")
	}
	_, err = e.svc.ReportProgress(ctx, exp2.ID, node, ProgressInput{Stage: StageConverting})
	wantKind(t, err, ErrNotFound, "转换任务不存在")
	_, err = e.svc.CompleteTask(ctx, exp2.ID, node, nil)
	wantKind(t, err, ErrNotFound, "转换任务不存在")
	_, err = e.svc.OpenTaskInput(ctx, exp2.ID, node)
	wantKind(t, err, ErrNotFound, "转换任务不存在")
}

func TestIntegrationTaskInputMissing(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	e.claim(t, node)
	if err := os.Remove(e.modelPath(m, modelFile)); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.OpenTaskInput(ctx, exp.ID, node)
	wantKind(t, err, ErrNotFound, "模型文件不存在")
	got := e.export(t, m.ID, exp.ID)
	if got.Status != ExportFailed || got.Error != "模型文件不存在，无法转换。请重新上传该模型" || got.FinishedAt == nil {
		t.Errorf("export = %+v", got)
	}
}

func TestIntegrationFailTask(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})

	// Retryable failures queue it again, up to three claims.
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if task := e.claim(t, node); task == nil || task.ID != exp.ID {
			t.Fatalf("attempt %d: claim = %+v", attempt, task)
		}
		e.putOutput(t, exp.ID, node, []byte("partial"))
		got, err := e.svc.FailTask(ctx, exp.ID, node, FailInput{Error: "下载模型文件失败：连接被重置", Retryable: true})
		if err != nil {
			t.Fatal(err)
		}
		if exists(e.svc.exportTempDir(exp.ID)) {
			t.Errorf("attempt %d: the partial result should be removed", attempt)
		}
		if attempt < maxAttempts {
			if got.Status != ExportQueued || got.Attempts != attempt || got.Node != nil || got.StartedAt != nil ||
				got.QueuePosition == nil || got.Error != "" {
				t.Errorf("attempt %d: %+v", attempt, got)
			}
			continue
		}
		if got.Status != ExportFailed || got.Attempts != maxAttempts || got.Error != "下载模型文件失败：连接被重置" ||
			got.FinishedAt == nil || got.Node == nil || got.Node.ID != node {
			t.Errorf("last attempt: %+v", got)
		}
	}
	if task := e.claim(t, node); task != nil {
		t.Errorf("a failed conversion was handed out: %+v", task)
	}

	// Requesting it again starts over.
	again, _ := e.requestExport(t, m.ID, ExportRequest{})
	if again.ID != exp.ID || again.Status != ExportQueued || again.Attempts != 0 || again.Error != "" {
		t.Errorf("requeued = %+v", again)
	}

	// A failure that is not retryable ends it at once; the texts are
	// cleaned and bounded, an empty one gets a default.
	e.claim(t, node)
	got, err := e.svc.FailTask(ctx, exp.ID, node, FailInput{Error: "  " + strings.Repeat("错", 600) + "\x00",
		Detail: strings.Repeat("Traceback\x00 line\n", 2000)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ExportFailed || got.Attempts != 1 || len([]rune(got.Error)) != maxErrorRunes ||
		len(got.ErrorDetail) > maxDetailBytes || strings.Contains(got.ErrorDetail, "\x00") ||
		!strings.HasPrefix(got.ErrorDetail, "Traceback line\n") {
		t.Errorf("failed = status %q, attempts %d, error %d runes, detail %d bytes", got.Status, got.Attempts,
			len([]rune(got.Error)), len(got.ErrorDetail))
	}
	e.requestExport(t, m.ID, ExportRequest{})
	e.claim(t, node)
	if got, _ := e.svc.FailTask(ctx, exp.ID, node, FailInput{Error: " \n "}); got.Error != "转换失败（训练节点没有给出原因）" {
		t.Errorf("default error = %q", got.Error)
	}
}

// TestIntegrationUploadPreflight checks the report a node sends before it
// uploads its result: stage uploading with the size of the file.
func TestIntegrationUploadPreflight(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	intruder := e.node(t, "b")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	e.claim(t, node)
	var mu sync.Mutex
	free := uint64(10 << 30)
	e.svc.usage = func() (storage.Usage, error) {
		mu.Lock()
		defer mu.Unlock()
		return storage.Usage{TotalBytes: 20 << 30, FreeBytes: free}, nil
	}
	setFree := func(n uint64) {
		mu.Lock()
		defer mu.Unlock()
		free = n
	}
	report := func(nodeID int64, stage string, size int64) error {
		_, err := e.svc.ReportProgress(ctx, exp.ID, nodeID, ProgressInput{Stage: stage, SizeBytes: size})
		return err
	}

	if err := report(node, StageUploading, 100<<20); err != nil {
		t.Fatalf("preflight with room: %v", err)
	}
	if got := e.export(t, m.ID, exp.ID); got.Stage != StageUploading {
		t.Errorf("stage = %q", got.Stage)
	}
	wantKind(t, report(node, StageUploading, -1), ErrInvalid, "size_bytes")
	wantKind(t, report(node, StageUploading, MaxOutputBytes+1), ErrTooLarge, "不能超过 4 GiB")

	// Not enough room for the file plus the reserve: refused like the
	// upload itself would be.
	setFree(600 << 20)
	wantKind(t, report(node, StageUploading, 100<<20), ErrInsufficientStorage, "磁盘剩余空间不足")
	data := []byte("onnx")
	_, err := e.svc.PutTaskOutput(ctx, exp.ID, node, 100<<20, sha256Hex(data), bytes.NewReader(data))
	wantKind(t, err, ErrInsufficientStorage, "磁盘剩余空间不足")
	// The check needs the stage and the size; older nodes send no size.
	if err := report(node, StageUploading, 0); err != nil {
		t.Errorf("uploading without a size: %v", err)
	}
	if err := report(node, StageConverting, 100<<20); err != nil {
		t.Errorf("converting with a size: %v", err)
	}
	if err := report(node, StageUploading, 1<<20); err != nil {
		t.Errorf("a file that fits: %v", err)
	}
	// Whether the node holds the conversion comes first: it stops then.
	wantKind(t, report(intruder, StageUploading, 100<<20), ErrConflict, "已不属于该节点")
	if got := e.export(t, m.ID, exp.ID); got.Status != ExportRunning || got.Node == nil || got.Node.ID != node {
		t.Errorf("after the preflights: %+v", got)
	}
	if err := e.svc.DeleteExport(ctx, m.ID, exp.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, report(node, StageUploading, 100<<20), ErrNotFound, "转换任务不存在")
}

func TestIntegrationLeaseExpiry(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	fresh, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{320}})

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if task := e.claim(t, node); task == nil || task.ID != exp.ID {
			t.Fatalf("attempt %d: claim = %+v", attempt, task)
		}
		e.expireLease(t, exp.ID)
		if err := e.svc.expireLeases(ctx); err != nil {
			t.Fatal(err)
		}
		got := e.export(t, m.ID, exp.ID)
		if attempt < maxAttempts {
			if got.Status != ExportQueued || got.Attempts != attempt || got.Node != nil || got.Stage != "" {
				t.Errorf("attempt %d: %+v", attempt, got)
			}
			// The node that lost it cannot report on it any more.
			_, err := e.svc.ReportProgress(ctx, exp.ID, node, ProgressInput{Stage: StageConverting})
			wantKind(t, err, ErrConflict, "已不属于该节点")
			continue
		}
		if got.Status != ExportFailed || got.Error != msgLeaseLost || got.FinishedAt == nil {
			t.Errorf("last attempt: %+v", got)
		}
	}
	if !strings.Contains(msgLeaseLost, "已尝试 3 次") {
		t.Errorf("msgLeaseLost = %q", msgLeaseLost)
	}

	// A lease that is renewed does not expire.
	if task := e.claim(t, node); task == nil || task.ID != fresh.ID {
		t.Fatalf("claim = %+v", task)
	}
	e.expireLease(t, fresh.ID)
	if _, err := e.svc.ReportProgress(ctx, fresh.ID, node, ProgressInput{Stage: StageConverting}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.expireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.export(t, m.ID, fresh.ID); got.Status != ExportRunning {
		t.Errorf("renewed conversion = %+v", got)
	}
	// Uploading the result renews it too.
	e.expireLease(t, fresh.ID)
	e.putOutput(t, fresh.ID, node, []byte("onnx"))
	if err := e.svc.expireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.export(t, m.ID, fresh.ID); got.Status != ExportRunning {
		t.Errorf("conversion after upload = %+v", got)
	}
}

func TestIntegrationBackgroundLoop(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	other := e.node(t, "b")
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	e.claim(t, node)
	e.expireLease(t, exp.ID)

	// The loop queues the expired conversion again, and a claim gets it.
	e.svc.Start(ctx)
	claimed := make(chan *Task, 1)
	go func() {
		task, _ := e.claimTask(ctx, other, []string{TaskModelExport}, 20*time.Second)
		claimed <- task
	}()
	select {
	case task := <-claimed:
		if task == nil || task.ID != exp.ID {
			t.Errorf("claimed %+v", task)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the background loop did not queue the expired conversion again")
	}

	// Stop ends waiting claims.
	waiting := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := e.claimTask(ctx, node, []string{TaskModelExport}, 20*time.Second)
		waiting <- err
	}()
	time.Sleep(100 * time.Millisecond)
	e.svc.Stop()
	select {
	case err := <-waiting:
		if err != nil || time.Since(start) > 5*time.Second {
			t.Errorf("claim after Stop: %v after %v", err, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not end the waiting claim")
	}
}

func TestIntegrationClaimWaits(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")

	for _, tt := range []struct {
		kinds []string
		wait  time.Duration
		msg   string
	}{
		{nil, 0, "kinds 不能为空"},
		{[]string{TaskModelExport}, -time.Second, "wait_seconds 必须在 0–30 之间"},
		{[]string{TaskModelExport}, 31 * time.Second, "wait_seconds 必须在 0–30 之间"},
		{[]string{""}, 0, "无效的任务类型"},
		{[]string{strings.Repeat("k", 65)}, 0, "无效的任务类型"},
		{slices.Repeat([]string{"k"}, 17), 0, "kinds 最多 16 项"},
	} {
		_, err := e.claimTask(ctx, node, tt.kinds, tt.wait)
		wantKind(t, err, ErrInvalid, tt.msg)
	}

	// Nothing queued: wait 0 returns at once, a wait times out.
	if task := e.claim(t, node); task != nil {
		t.Fatalf("task = %+v", task)
	}
	start := time.Now()
	task, err := e.claimTask(ctx, node, []string{TaskModelExport}, 300*time.Millisecond)
	if task != nil || err != nil || time.Since(start) < 250*time.Millisecond {
		t.Errorf("timed out claim = %+v, %v after %v", task, err, time.Since(start))
	}

	// Kinds the server does not know are ignored; a node offering only
	// those gets nothing, and does not count as a conversion worker.
	e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{320}})
	other := e.node(t, "trainer-only")
	if task, err := e.claimTask(ctx, other, []string{"training_job"}, 0); task != nil || err != nil {
		t.Errorf("unknown kind claim = %+v, %v", task, err)
	}
	if n := e.svc.WorkersOnline(TaskModelExport); n != 1 {
		t.Errorf("workers = %d, want 1", n)
	}
	if task, err := e.claimTask(ctx, node, []string{"training_job", TaskModelExport}, 0); task == nil || err != nil {
		t.Errorf("mixed kinds claim = %+v, %v", task, err)
	}

	// A waiting claim is woken by a new conversion.
	claimed := make(chan *Task, 1)
	go func() {
		task, err := e.claimTask(ctx, node, []string{TaskModelExport}, 20*time.Second)
		if err != nil {
			t.Error(err)
		}
		claimed <- task
	}()
	time.Sleep(100 * time.Millisecond)
	start = time.Now()
	exp, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{480}})
	select {
	case task := <-claimed:
		if task == nil || task.ID != exp.ID || time.Since(start) > 5*time.Second {
			t.Errorf("woken claim = %+v after %v", task, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting claim was not woken")
	}

	// ... and by a conversion queued again after a retryable failure.
	third := e.node(t, "c")
	go func() {
		task, _ := e.claimTask(ctx, third, []string{TaskModelExport}, 20*time.Second)
		claimed <- task
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := e.svc.FailTask(ctx, exp.ID, node, FailInput{Error: "网络错误", Retryable: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case task := <-claimed:
		if task == nil || task.ID != exp.ID {
			t.Errorf("woken claim = %+v", task)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting claim was not woken by the requeue")
	}

	// The request ending ends the wait.
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := e.claimTask(cctx, node, []string{TaskModelExport}, 20*time.Second)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled claim: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceling did not end the claim")
	}

	// StopLongPolls ends waiting claims, and later ones do not wait.
	go func() {
		_, err := e.claimTask(ctx, node, []string{TaskModelExport}, 20*time.Second)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	start = time.Now()
	e.svc.StopLongPolls()
	e.svc.StopLongPolls() // twice is fine
	select {
	case err := <-errc:
		if err != nil || time.Since(start) > 5*time.Second {
			t.Errorf("claim after StopLongPolls: %v after %v", err, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StopLongPolls did not end the claim")
	}
	start = time.Now()
	if task, err := e.claimTask(ctx, node, []string{TaskModelExport}, 20*time.Second); task != nil || err != nil ||
		time.Since(start) > 5*time.Second {
		t.Errorf("claim after shutdown = %+v, %v after %v", task, err, time.Since(start))
	}
}

func TestIntegrationConcurrentClaims(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	const exports = 12
	want := map[int64]bool{}
	for range exports / 3 {
		m := e.addModel(t, projectID, "yolo11")
		for _, size := range []int{320, 480, 640} {
			exp, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{size}})
			want[exp.ID] = true
		}
	}
	var nodes []int64
	for i := range 6 {
		nodes = append(nodes, e.node(t, fmt.Sprintf("node-%d", i)))
	}

	var mu sync.Mutex
	got := map[int64]int64{} // export -> node
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task, err := e.claimTask(ctx, node, []string{TaskModelExport}, 0)
				if err != nil {
					t.Error(err)
					return
				}
				if task == nil {
					return
				}
				mu.Lock()
				if prev, ok := got[task.ID]; ok {
					t.Errorf("export %d claimed by nodes %d and %d", task.ID, prev, node)
				}
				got[task.ID] = node
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(got) != exports {
		t.Errorf("claimed %d of %d conversions", len(got), exports)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("export %d was not claimed", id)
		}
	}
}

func TestIntegrationQueueOrder(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	var ids []int64
	for _, size := range []int{320, 480, 640} {
		exp, _ := e.requestExport(t, m.ID, ExportRequest{ImgSize: []int{size}})
		ids = append(ids, exp.ID)
	}
	if task := e.claim(t, node); task == nil || task.ID != ids[0] {
		t.Fatalf("the oldest conversion goes first, got %+v", task)
	}
	list, err := e.svc.ListExports(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[int64]*int{}
	for _, x := range list {
		pos[x.ID] = x.QueuePosition
		if x.WorkersOnline != 1 {
			t.Errorf("workers_online = %d", x.WorkersOnline)
		}
	}
	if pos[ids[0]] != nil || pos[ids[1]] == nil || *pos[ids[1]] != 1 || pos[ids[2]] == nil || *pos[ids[2]] != 2 {
		t.Errorf("queue positions = %v %v %v", pos[ids[0]], pos[ids[1]], pos[ids[2]])
	}

	// workers_online forgets nodes that stopped claiming.
	e.clock.Advance(59 * time.Second)
	if got := e.export(t, m.ID, ids[1]); got.WorkersOnline != 1 {
		t.Errorf("workers_online after 59s = %d", got.WorkersOnline)
	}
	e.clock.Advance(2 * time.Second)
	if got := e.export(t, m.ID, ids[1]); got.WorkersOnline != 0 {
		t.Errorf("workers_online after 61s = %d", got.WorkersOnline)
	}
	// Working on a conversion counts as being online.
	if _, err := e.svc.ReportProgress(ctx, ids[0], node, ProgressInput{Stage: StageConverting}); err != nil {
		t.Fatal(err)
	}
	if got := e.export(t, m.ID, ids[1]); got.WorkersOnline != 1 {
		t.Errorf("workers_online after progress = %d", got.WorkersOnline)
	}
}

func TestLeaseReaderWriter(t *testing.T) {
	var renewals int
	var fail error
	renew := func() error { renewals++; return fail }

	r := &leaseReader{ReadSeeker: strings.NewReader("weights"), renew: renew, last: time.Now()}
	buf := make([]byte, 3)
	if _, err := r.Read(buf); err != nil || renewals != 0 {
		t.Errorf("fresh lease: %v, %d renewals", err, renewals)
	}
	r.last = time.Now().Add(-leaseRenewEvery)
	if _, err := r.Read(buf); err != nil || renewals != 1 || time.Since(r.last) > time.Second {
		t.Errorf("due lease: %v, %d renewals", err, renewals)
	}
	fail = errTaskGone
	r.last = time.Now().Add(-leaseRenewEvery)
	if _, err := r.Read(buf); !errors.Is(err, errTaskGone) {
		t.Errorf("lost lease: %v", err)
	}

	var out bytes.Buffer
	w := &leaseWriter{w: &out, renew: renew, last: time.Now().Add(-leaseRenewEvery)}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errTaskGone) || out.Len() != 0 {
		t.Errorf("lost lease write: %v, %d bytes", err, out.Len())
	}
	fail = nil
	if n, err := w.Write([]byte("onnx")); err != nil || n != 4 || out.String() != "onnx" {
		t.Errorf("write = %d, %v", n, err)
	}
}

// fakeSource is a TaskSource with an in-memory queue.
type fakeSource struct {
	kind string
	mu   sync.Mutex
	next []string
	err  error
	nil_ bool // return a typed nil
}

type fakeTask struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Node int64  `json:"node"`
}

func (f *fakeSource) Kind() string { return f.kind }

func (f *fakeSource) Claim(_ context.Context, nodeID int64) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.err != nil:
		return nil, f.err
	case f.nil_:
		return (*fakeTask)(nil), nil
	case len(f.next) == 0:
		return nil, nil
	}
	name := f.next[0]
	f.next = f.next[1:]
	return &fakeTask{Kind: f.kind, Name: name, Node: nodeID}, nil
}

func (f *fakeSource) push(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = append(f.next, names...)
}

func TestAddTaskSourceRejects(t *testing.T) {
	s := NewService(Options{})
	s.AddTaskSource(&fakeSource{kind: "a"})
	for _, kind := range []string{"a", "", TaskModelExport, strings.Repeat("k", 65)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("AddTaskSource(%q) did not panic", kind)
				}
			}()
			s.AddTaskSource(&fakeSource{kind: kind})
		}()
	}
}

func TestIntegrationTaskSources(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), "yolo11")
	node := e.node(t, "a")
	other := e.node(t, "b")
	first := &fakeSource{kind: "first"}
	second := &fakeSource{kind: "second"}
	e.svc.AddTaskSource(first)
	e.svc.AddTaskSource(second)

	// Nothing queued anywhere.
	if task, err := e.svc.ClaimTask(ctx, node, []string{"first", "second", TaskModelExport}, 0); task != nil || err != nil {
		t.Fatalf("empty claim = %#v, %v", task, err)
	}
	// Conversions go first, then the sources in registration order, and
	// only the kinds the node offers.
	exp, _ := e.requestExport(t, m.ID, ExportRequest{})
	first.push("f1")
	second.push("s1", "s2")
	kinds := []string{"second", "first", TaskModelExport}
	var got []string
	for {
		task, err := e.svc.ClaimTask(ctx, node, kinds, 0)
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			break
		}
		switch task := task.(type) {
		case *Task:
			if task.ID != exp.ID {
				t.Errorf("claimed export %d", task.ID)
			}
			got = append(got, "export")
		case *fakeTask:
			if task.Node != node {
				t.Errorf("task for node %d", task.Node)
			}
			got = append(got, task.Name)
		default:
			t.Fatalf("claimed a %T", task)
		}
		if len(got) == 1 {
			kinds = kinds[:2] // the export is held; offer only the sources from now on
		}
	}
	if want := []string{"export", "f1", "s1", "s2"}; !slices.Equal(got, want) {
		t.Errorf("claimed %q, want %q", got, want)
	}
	second.push("s3")
	if task, err := e.svc.ClaimTask(ctx, node, []string{"first"}, 0); task != nil || err != nil {
		t.Errorf("claim of first only = %#v, %v", task, err)
	}

	// A typed nil is no task; an error ends the claim.
	first.nil_ = true
	if task, err := e.svc.ClaimTask(ctx, node, []string{"first"}, 0); task != nil || err != nil {
		t.Errorf("typed nil claim = %#v, %v", task, err)
	}
	first.nil_ = false
	first.err = errors.New("database down")
	if _, err := e.svc.ClaimTask(ctx, node, []string{"first", "second"}, 0); err == nil || !strings.Contains(err.Error(), "database down") {
		t.Errorf("failing source: %v", err)
	}
	first.err = nil
	if task, err := e.svc.ClaimTask(ctx, node, []string{TaskModelExport, "first", "second"}, 0); err != nil ||
		task.(*fakeTask).Name != "s3" {
		t.Errorf("claim after the failure = %#v, %v", task, err)
	}

	// NotifyTasks wakes a waiting claim.
	claimed := make(chan any, 1)
	go func() {
		task, err := e.svc.ClaimTask(ctx, other, []string{"first"}, 20*time.Second)
		if err != nil {
			t.Error(err)
		}
		claimed <- task
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	first.push("f2")
	e.svc.NotifyTasks()
	select {
	case task := <-claimed:
		if ft, ok := task.(*fakeTask); !ok || ft.Name != "f2" || time.Since(start) > 5*time.Second {
			t.Errorf("woken claim = %#v", task)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("NotifyTasks did not wake the claim")
	}

	// Workers by kind: node last offered all three kinds, other only
	// "first".
	for kind, want := range map[string]int{TaskModelExport: 1, "first": 2, "second": 1, "unknown": 0} {
		if n := e.svc.WorkersOnline(kind); n != want {
			t.Errorf("WorkersOnline(%s) = %d, want %d", kind, n, want)
		}
	}
	third := e.node(t, "c")
	e.svc.TouchWorker(third, "second")
	if n := e.svc.WorkersOnline("second"); n != 2 {
		t.Errorf("WorkersOnline(second) after TouchWorker = %d", n)
	}
	e.clock.Advance(61 * time.Second)
	if n := e.svc.WorkersOnline("first"); n != 0 {
		t.Errorf("WorkersOnline(first) a minute later = %d", n)
	}
}
