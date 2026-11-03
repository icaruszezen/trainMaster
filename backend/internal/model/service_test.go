package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testEnv is a Service on a private database and data directory. Its
// Inspect reads fake weights files (fakePT) instead of real ones.
type testEnv struct {
	svc   *Service
	db    *database.DB
	st    *storage.Storage
	clock *testClock
	// inspectHook, when set, runs inside Inspect (tests block commits).
	inspectHook func()
}

const fakePTMagic = "fake-pt\n"

// fakePT is a weights file the test Inspect understands: the magic, the
// Info as JSON and padding (to vary the content and size).
func fakePT(t testing.TB, info checkpoint.Info, padding string) []byte {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(fakePTMagic + string(data) + "\n" + padding)
}

// detectInfo is what Inspect reports for a detection model of a version.
func detectInfo(yolo string) checkpoint.Info {
	return checkpoint.Info{ModelClass: "DetectionModel", Task: "detect", YOLOVersion: yolo, VersionSource: "yaml_file",
		YAMLFile: yolo + "n.yaml", Scale: "n", ClassNames: []string{"scratch", "dent"}, ImageSize: 640, Channels: 3,
		Parameters: 2590230, Head: "Detect", End2End: yolo == "yolo26" || yolo == "yolov10"}
}

func (e *testEnv) inspect(r io.ReaderAt, size int64) (*checkpoint.Info, error) {
	if e.inspectHook != nil {
		e.inspectHook()
	}
	data, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	head, rest, ok := strings.Cut(string(data), "\n")
	if !ok || head+"\n" != fakePTMagic {
		return nil, &checkpoint.Error{Reason: checkpoint.ReasonNotTorch, Message: "文件不是 PyTorch 模型文件（.pt）"}
	}
	line, _, _ := strings.Cut(rest, "\n")
	var info checkpoint.Info
	if err := json.Unmarshal([]byte(line), &info); err != nil {
		return nil, &checkpoint.Error{Reason: checkpoint.ReasonCorrupt, Message: "模型文件已损坏"}
	}
	return &info, nil
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{db: db, st: st, clock: &testClock{t: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)}}
	e.svc = NewService(Options{DB: db.Pool, Storage: st, Logger: slog.New(slog.DiscardHandler), Now: e.clock.Now,
		Inspect: e.inspect})
	return e
}

// project inserts a project and returns its ID.
func (e *testEnv) project(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), "INSERT INTO projects (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// node inserts a registered training node and returns its ID.
func (e *testEnv) node(t *testing.T, key string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), `INSERT INTO compute_nodes (node_key, name, hardware)
		VALUES ($1, $1, '{}') RETURNING id`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// upload creates an upload of data and sends it in two chunks.
func (e *testEnv) upload(t *testing.T, projectID int64, yolo string, data []byte) Upload {
	t.Helper()
	ctx := context.Background()
	up, err := e.svc.CreateUpload(ctx, CreateUploadInput{ProjectID: projectID, YOLOVersion: yolo, FileName: "best.pt",
		FileSize: int64(len(data))})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	half := len(data) / 2
	if _, err := e.svc.WriteChunk(ctx, up.ID, 0, bytes.NewReader(data[:half])); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if up, err = e.svc.WriteChunk(ctx, up.ID, int64(half), bytes.NewReader(data[half:])); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	return up
}

// addModel uploads and commits a fake detection model of a version.
func (e *testEnv) addModel(t *testing.T, projectID int64, yolo string) Model {
	t.Helper()
	up := e.upload(t, projectID, yolo, fakePT(t, detectInfo(yolo), fmt.Sprint(time.Now().UnixNano())))
	m, created, err := e.svc.CommitUpload(context.Background(), up.ID, CommitInput{})
	if err != nil || !created {
		t.Fatalf("CommitUpload: %v (created %v)", err, created)
	}
	return m
}

func (e *testEnv) modelPath(m Model, rel string) string {
	return filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", m.Project.ID), fmt.Sprintf("v%d", m.Version), rel)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// failingReader yields data and then fails, like a request body cut off.
type failingReader struct{ data []byte }

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, errors.New("connection reset")
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestIntegrationUploadAndCommit(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "A 线外观检测")
	data := fakePT(t, detectInfo("yolo11"), strings.Repeat("w", 1000))

	// Validation.
	for _, tt := range []struct {
		in  CreateUploadInput
		msg string
	}{
		{CreateUploadInput{ProjectID: projectID, FileName: "best.pt", FileSize: 10}, "请选择 YOLO 版本"},
		{CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolov5", FileName: "best.pt", FileSize: 10}, "不支持的 YOLO 版本"},
		{CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "best.onnx", FileSize: 10}, ".pt"},
		{CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "best.pt"}, "模型文件大小无效"},
		{CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "best.pt", FileSize: 8<<30 + 1}, "不能超过 8 GiB"},
		{CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "best.pt", FileSize: 10,
			Description: "a\x00"}, "说明包含无效字符"},
		{CreateUploadInput{ProjectID: 99999, YOLOVersion: "yolo11", FileName: "best.pt", FileSize: 10}, "所选项目不存在"},
		{CreateUploadInput{YOLOVersion: "yolo11", FileName: "best.pt", FileSize: 10}, "请选择项目"},
	} {
		_, err := e.svc.CreateUpload(ctx, tt.in)
		wantKind(t, err, ErrInvalid, tt.msg)
	}

	up, err := e.svc.CreateUpload(ctx, CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: " best.pt ",
		FileSize: int64(len(data)), Description: " 夜班补充数据 "})
	if err != nil {
		t.Fatal(err)
	}
	if up.Status != UploadUploading || up.FileName != "best.pt" || up.Description != "夜班补充数据" || up.ReceivedBytes != 0 ||
		up.DetectedYOLOVersion != nil || up.ModelID != nil || up.ProjectID != projectID {
		t.Errorf("upload = %+v", up)
	}
	if !exists(e.svc.uploadDir(up.ID)) {
		t.Error("upload folder not created")
	}

	// A chunk cut off midway leaves nothing behind; the offset must match.
	_, err = e.svc.WriteChunk(ctx, up.ID, 0, &failingReader{data: data[:100]})
	wantKind(t, err, ErrBadRequest, "上传数据接收不完整")
	if got, _ := e.svc.GetUpload(ctx, up.ID); got.ReceivedBytes != 0 {
		t.Errorf("received after a failed chunk = %d", got.ReceivedBytes)
	}
	if fi, err := os.Stat(e.svc.uploadPath(up.ID)); err != nil || fi.Size() != 0 {
		t.Errorf("staged file after a failed chunk: %v %v", fi, err)
	}
	if up, err = e.svc.WriteChunk(ctx, up.ID, 0, bytes.NewReader(data[:300])); err != nil || up.ReceivedBytes != 300 {
		t.Fatalf("WriteChunk = %+v, %v", up, err)
	}
	_, err = e.svc.WriteChunk(ctx, up.ID, 0, bytes.NewReader(data[:300]))
	wantKind(t, err, ErrOffsetMismatch, "服务器已接收 300 字节")
	_, err = e.svc.WriteChunk(ctx, up.ID, 300, bytes.NewReader(append(slices.Clone(data[300:]), 'x')))
	wantKind(t, err, ErrBadRequest, "超过了声明的文件大小")
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrConflict, "尚未上传完成")
	if up, err = e.svc.WriteChunk(ctx, up.ID, 300, bytes.NewReader(data[300:])); err != nil || up.ReceivedBytes != int64(len(data)) {
		t.Fatalf("WriteChunk = %+v, %v", up, err)
	}

	// Commit.
	m, created, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	if err != nil || !created {
		t.Fatalf("CommitUpload = %v, %v", created, err)
	}
	if m.Version != 1 || m.Project != (ProjectRef{ID: projectID, Name: "A 线外观检测"}) || m.Source != SourceUpload ||
		m.TrainingJobID != nil || m.YOLOVersion != "yolo11" || m.Task != "detect" || m.FileName != "best.pt" ||
		m.SizeBytes != int64(len(data)) || m.Description != "夜班补充数据" || m.FileMissing || m.ExportCount != 0 ||
		m.DownloadURL != fmt.Sprintf("/api/v1/models/%d/file", m.ID) || len(m.SHA256) != 64 {
		t.Errorf("model = %+v", m)
	}
	if m.Info.DetectedYOLOVersion == nil || *m.Info.DetectedYOLOVersion != "yolo11" || m.Info.ImageSize == nil ||
		*m.Info.ImageSize != 640 || !slices.Equal(m.Info.ClassNames, []string{"scratch", "dent"}) {
		t.Errorf("info = %+v", m.Info)
	}
	if stored, err := os.ReadFile(e.modelPath(m, "model.pt")); err != nil || !bytes.Equal(stored, data) {
		t.Errorf("model.pt: %v", err)
	}
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("upload folder should be removed")
	}
	var sc map[string]any
	if raw, err := os.ReadFile(e.modelPath(m, "model.json")); err != nil || json.Unmarshal(raw, &sc) != nil {
		t.Fatalf("model.json: %v", err)
	}
	if sc["model_id"] != float64(m.ID) || sc["version"] != 1.0 || sc["yolo_version"] != "yolo11" || sc["sha256"] != m.SHA256 ||
		sc["file_name"] != "best.pt" || sc["source"] != "upload" || sc["project"].(map[string]any)["name"] != "A 线外观检测" ||
		sc["info"].(map[string]any)["task"] != "detect" || sc["created_at"] == nil {
		t.Errorf("model.json = %v", sc)
	}
	if up, _ = e.svc.GetUpload(ctx, up.ID); up.Status != UploadCompleted || up.ModelID == nil || *up.ModelID != m.ID {
		t.Errorf("upload after commit = %+v", up)
	}

	// Committing again returns the same model; the upload accepts nothing more.
	again, created, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	if err != nil || created || again.ID != m.ID {
		t.Errorf("second commit = %+v, %v, %v", again, created, err)
	}
	_, err = e.svc.WriteChunk(ctx, up.ID, up.ReceivedBytes, bytes.NewReader([]byte("x")))
	wantKind(t, err, ErrConflict, "上传已完成")
	if c, err := e.svc.CancelUpload(ctx, up.ID); err != nil || c.Status != UploadCompleted {
		t.Errorf("cancel of a completed upload = %+v, %v", c, err)
	}

	// Download.
	f, err := e.svc.OpenModelFile(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f.Content)
	f.Close()
	if !bytes.Equal(got, data) || f.Name != "A 线外观检测_v1.pt" || f.Size != int64(len(data)) {
		t.Errorf("download %s (%d bytes)", f.Name, len(got))
	}

	// Reading and changing.
	m2 := e.addModel(t, projectID, "yolo11")
	other := e.project(t, "其它项目")
	m3 := e.addModel(t, other, "yolov8")
	if m2.Version != 2 || m3.Version != 1 {
		t.Errorf("versions = %d, %d", m2.Version, m3.Version)
	}
	ids := func(ms []Model) []int64 {
		out := []int64{}
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	for _, tt := range []struct {
		q    ModelQuery
		want []int64
	}{
		{ModelQuery{}, []int64{m3.ID, m2.ID, m.ID}},
		{ModelQuery{ProjectID: projectID}, []int64{m2.ID, m.ID}},
		{ModelQuery{YOLOVersion: "yolov8"}, []int64{m3.ID}},
		{ModelQuery{Source: SourceTraining}, []int64{}},
		{ModelQuery{Search: "夜班"}, []int64{m.ID}},
		{ModelQuery{Search: "BEST.PT"}, []int64{m3.ID, m2.ID, m.ID}},
		{ModelQuery{Search: "其它"}, []int64{m3.ID}},
		{ModelQuery{Search: "nothing"}, []int64{}},
	} {
		ms, err := e.svc.ListModels(ctx, tt.q)
		if err != nil || !slices.Equal(ids(ms), tt.want) {
			t.Errorf("ListModels(%+v) = %v, %v; want %v", tt.q, ids(ms), err, tt.want)
		}
	}
	if ms, err := e.svc.ListProjectModels(ctx, projectID); err != nil || !slices.Equal(ids(ms), []int64{m2.ID, m.ID}) {
		t.Errorf("ListProjectModels = %v, %v", ids(ms), err)
	}
	_, err = e.svc.ListProjectModels(ctx, 99999)
	wantKind(t, err, ErrNotFound, "项目不存在")

	desc := "  新说明\n第二行 "
	if u, err := e.svc.UpdateModel(ctx, m.ID, UpdateModelInput{Description: &desc}); err != nil || u.Description != "新说明\n第二行" {
		t.Errorf("UpdateModel = %+v, %v", u, err)
	}
	if u, err := e.svc.UpdateModel(ctx, m.ID, UpdateModelInput{}); err != nil || u.Description != "新说明\n第二行" {
		t.Errorf("UpdateModel without changes = %+v, %v", u, err)
	}
	bad := strings.Repeat("长", 501)
	_, err = e.svc.UpdateModel(ctx, m.ID, UpdateModelInput{Description: &bad})
	wantKind(t, err, ErrInvalid, "不能超过 500 个字符")
	_, err = e.svc.UpdateModel(ctx, 99999, UpdateModelInput{Description: &desc})
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, err = e.svc.GetModel(ctx, 99999)
	wantKind(t, err, ErrNotFound, "模型不存在")

	// A file removed on the server shows as missing.
	if err := os.Remove(e.modelPath(m, "model.pt")); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetModel(ctx, m.ID); !got.FileMissing {
		t.Error("file_missing should be true")
	}
	_, err = e.svc.OpenModelFile(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型文件不存在")
	// A symbolic link out of models/ is not followed.
	outside := filepath.Join(t.TempDir(), "secret.pt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.modelPath(m, "model.pt")); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenModelFile(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型文件不存在")
	if got, _ := e.svc.GetModel(ctx, m.ID); !got.FileMissing {
		t.Error("a symbolic link should count as missing")
	}
}

func TestIntegrationCommitOverridesAndMismatch(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")

	// The file is a YOLOv8 model; the user chose YOLO11.
	up := e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolov8"), "x"))
	_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrVersionMismatch, "所选的版本是 YOLO11，但文件中的模型是 YOLOv8（yolov8n.yaml）。请改选 YOLOv8 后再保存")
	up, _ = e.svc.GetUpload(ctx, up.ID)
	if up.Status != UploadUploading || up.DetectedYOLOVersion == nil || *up.DetectedYOLOVersion != "yolov8" ||
		up.YOLOVersion != "yolo11" {
		t.Errorf("upload after mismatch = %+v", up)
	}
	if !exists(e.svc.uploadPath(up.ID)) {
		t.Error("the staged file must stay for the next commit")
	}
	// Nothing was allocated.
	var seq int
	if err := e.db.QueryRow(ctx, "SELECT model_version_seq FROM projects WHERE id = $1", projectID).Scan(&seq); err != nil || seq != 0 {
		t.Errorf("model_version_seq = %d, %v", seq, err)
	}
	// Invalid overrides are refused before anything happens.
	bad := "yolov5"
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{YOLOVersion: &bad})
	wantKind(t, err, ErrInvalid, "不支持的 YOLO 版本")

	// Again with the detected version and a new description.
	v8, desc := "yolov8", "改为 v8"
	m, created, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{YOLOVersion: &v8, Description: &desc})
	if err != nil || !created || m.YOLOVersion != "yolov8" || m.Description != "改为 v8" || m.Version != 1 {
		t.Fatalf("commit with the right version = %+v, %v", m, err)
	}
	up, _ = e.svc.GetUpload(ctx, up.ID)
	if up.Status != UploadCompleted || up.YOLOVersion != "yolov8" || up.Description != "改为 v8" || up.DetectedYOLOVersion != nil {
		t.Errorf("upload = %+v", up)
	}

	// A version found from the architecture also counts; an unrecognized
	// one does not.
	arch := detectInfo("yolo26")
	arch.VersionSource, arch.YAMLFile = "architecture", "my-net.yaml"
	up = e.upload(t, projectID, "yolo11", fakePT(t, arch, "y"))
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrVersionMismatch, "（按网络结构识别，my-net.yaml）")
	unknown := detectInfo("")
	unknown.VersionSource = ""
	up = e.upload(t, projectID, "yolo12", fakePT(t, unknown, "z"))
	if m, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{}); err != nil || m.YOLOVersion != "yolo12" ||
		m.Info.DetectedYOLOVersion != nil || m.Version != 2 {
		t.Errorf("unrecognized version = %+v, %v", m, err)
	}
}

func TestIntegrationCommitInvalidModel(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	up := e.upload(t, projectID, "yolo11", []byte("PK\x03\x04 not a model"))
	_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrInvalidModel, "文件不是 PyTorch 模型文件")
	up, _ = e.svc.GetUpload(ctx, up.ID)
	if up.Status != UploadFailed || up.Error != "文件不是 PyTorch 模型文件（.pt）" {
		t.Errorf("upload = %+v", up)
	}
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("the staged file should be removed")
	}
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrConflict, "上传已失败")
	_, err = e.svc.WriteChunk(ctx, up.ID, 0, strings.NewReader("x"))
	wantKind(t, err, ErrConflict, "上传已失败")

	// Inspect failing for another reason (I/O) is not the file's fault.
	up = e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolo11"), ""))
	e.inspectHook = func() { panic("unreachable") }
	e.svc.inspect = func(io.ReaderAt, int64) (*checkpoint.Info, error) { return nil, errors.New("disk on fire") }
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	var me *Error
	if err == nil || errors.As(err, &me) || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("err = %v", err)
	}
	if up, _ = e.svc.GetUpload(ctx, up.ID); up.Status != UploadUploading || !exists(e.svc.uploadPath(up.ID)) {
		t.Errorf("upload after an internal error = %+v", up)
	}
}

func TestIntegrationCancelUpload(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	data := fakePT(t, detectInfo("yolo11"), "")
	up, err := e.svc.CreateUpload(ctx, CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "a.pt",
		FileSize: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	if up, err = e.svc.CancelUpload(ctx, up.ID); err != nil || up.Status != UploadCanceled {
		t.Fatalf("CancelUpload = %+v, %v", up, err)
	}
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("upload folder should be removed")
	}
	_, err = e.svc.WriteChunk(ctx, up.ID, 0, bytes.NewReader(data))
	wantKind(t, err, ErrConflict, "上传已取消")
	_, _, err = e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrConflict, "上传已取消")
	if again, err := e.svc.CancelUpload(ctx, up.ID); err != nil || again.Status != UploadCanceled {
		t.Errorf("cancel again = %+v, %v", again, err)
	}
	_, err = e.svc.CancelUpload(ctx, 99999)
	wantKind(t, err, ErrNotFound, "上传任务不存在")
	_, err = e.svc.WriteChunk(ctx, 99999, 0, bytes.NewReader(data))
	wantKind(t, err, ErrNotFound, "上传任务不存在")

	// Canceled while the commit inspects the file: the commit fails and
	// allocates nothing.
	up = e.upload(t, projectID, "yolo11", data)
	inspecting, release := make(chan struct{}), make(chan struct{})
	e.inspectHook = func() { close(inspecting); <-release }
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		done <- err
	}()
	<-inspecting
	if c, err := e.svc.CancelUpload(ctx, up.ID); err != nil || c.Status != UploadCanceled {
		t.Errorf("cancel during commit = %+v, %v", c, err)
	}
	close(release)
	wantKind(t, <-done, ErrConflict, "上传已取消")
	e.inspectHook = nil
	if ms, _ := e.svc.ListModels(ctx, ModelQuery{}); len(ms) != 0 {
		t.Errorf("models = %+v", ms)
	}
	if m := e.addModel(t, projectID, "yolo11"); m.Version != 1 {
		t.Errorf("next version = %d, want 1 (the canceled commit rolled back)", m.Version)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", projectID))); len(entries) != 1 {
		t.Errorf("model folders = %v", entries)
	}
}

func TestIntegrationVersionsNotReused(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	m1 := e.addModel(t, projectID, "yolo11")
	m2 := e.addModel(t, projectID, "yolo11")
	if res, err := e.svc.DeleteModel(ctx, m2.ID); err != nil || res != nil {
		t.Fatalf("DeleteModel = %+v, %v", res, err)
	}
	if m3 := e.addModel(t, projectID, "yolo11"); m3.Version != 3 {
		t.Errorf("version after deleting v2 = %d, want 3", m3.Version)
	}
	for _, id := range []int64{m1.ID} {
		if _, err := e.svc.DeleteModel(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if m4 := e.addModel(t, projectID, "yolo11"); m4.Version != 4 {
		t.Errorf("version = %d, want 4", m4.Version)
	}

	// A folder left behind by hand (or by a crash) is kept, and its version
	// skipped.
	stray := filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", projectID), "v5")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "model.pt"), []byte("restored by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := e.addModel(t, projectID, "yolo11"); m.Version != 6 {
		t.Errorf("version with a stray v5 folder = %d, want 6", m.Version)
	}
	if b, err := os.ReadFile(filepath.Join(stray, "model.pt")); err != nil || string(b) != "restored by hand" {
		t.Errorf("stray folder changed: %q, %v", b, err)
	}
}

func TestIntegrationConcurrentCommits(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	const n = 6
	uploads := make([]Upload, n)
	for i := range uploads {
		uploads[i] = e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolo11"), strings.Repeat("x", i)))
	}
	versions := make(chan int, n)
	var wg sync.WaitGroup
	for _, up := range uploads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
			if err != nil {
				t.Errorf("CommitUpload: %v", err)
				return
			}
			versions <- m.Version
		}()
	}
	wg.Wait()
	close(versions)
	var got []int
	for v := range versions {
		got = append(got, v)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{1, 2, 3, 4, 5, 6}) {
		t.Errorf("versions = %v", got)
	}
	ms, err := e.svc.ListProjectModels(ctx, projectID)
	if err != nil || len(ms) != n {
		t.Fatalf("ListProjectModels = %d, %v", len(ms), err)
	}
	for _, m := range ms {
		if m.FileMissing {
			t.Errorf("v%d has no file", m.Version)
		}
	}
}

func TestIntegrationDeleteModel(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	m := e.addModel(t, projectID, "yolo11")
	_, _, err := e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: "onnx"})
	if err != nil {
		t.Fatal(err)
	}
	dir := e.modelPath(m, "")
	if res, err := e.svc.DeleteModel(ctx, m.ID); err != nil || res != nil {
		t.Fatalf("DeleteModel = %+v, %v", res, err)
	}
	if exists(dir) {
		t.Error("model folder should be removed")
	}
	var exports int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM model_exports").Scan(&exports); err != nil || exports != 0 {
		t.Errorf("exports left: %d, %v", exports, err)
	}
	_, err = e.svc.DeleteModel(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型不存在")

	// A model whose folder is gone can still be deleted.
	m = e.addModel(t, projectID, "yolo11")
	if err := os.RemoveAll(e.modelPath(m, "")); err != nil {
		t.Fatal(err)
	}
	if res, err := e.svc.DeleteModel(ctx, m.ID); err != nil || res != nil {
		t.Errorf("delete without folder = %+v, %v", res, err)
	}

	// The folder cannot be removed completely: the record is gone, the rest
	// keeps its name.
	m = e.addModel(t, projectID, "yolo11")
	e.svc.removeAll = func(string) error { return &fs.PathError{Op: "unlinkat", Path: "x", Err: fs.ErrPermission} }
	res, err := e.svc.DeleteModel(ctx, m.ID)
	if err != nil || res == nil || res.FilesRemoved || !strings.Contains(res.Message, "权限不足") ||
		!strings.Contains(res.Message, e.modelPath(m, "")) {
		t.Fatalf("DeleteModel = %+v, %v", res, err)
	}
	if !exists(e.modelPath(m, "model.pt")) {
		t.Error("the leftover should be back under its name")
	}
	if _, err := e.svc.GetModel(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("model should be deleted: %v", err)
	}
	e.svc.removeAll = os.RemoveAll
}

func TestIntegrationDeleteProjectModels(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID, other := e.project(t, "p"), e.project(t, "q")
	e.addModel(t, projectID, "yolo11")
	e.addModel(t, projectID, "yolo26")
	kept := e.addModel(t, other, "yolo11")
	if res, err := e.svc.DeleteProjectModels(ctx, projectID); err != nil || res != nil {
		t.Fatalf("DeleteProjectModels = %+v, %v", res, err)
	}
	if ms, _ := e.svc.ListModels(ctx, ModelQuery{}); len(ms) != 1 || ms[0].ID != kept.ID {
		t.Errorf("models left = %+v", ms)
	}
	if exists(filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", projectID))) {
		t.Error("the project's model folder should be removed")
	}
	// Now the project itself can go; an upload in progress goes with it and
	// its staged file is cleaned up.
	up := e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolo11"), ""))
	if _, err := e.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", projectID); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrNotFound, "上传任务不存在")
	if err := e.svc.cleanUploadTemp(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("staged file of the deleted project's upload should be removed")
	}
	if res, err := e.svc.DeleteProjectModels(ctx, 99999); err != nil || res != nil {
		t.Errorf("DeleteProjectModels of a missing project = %+v, %v", res, err)
	}

	// Leftovers are reported once for all models.
	e.addModel(t, other, "yolo11")
	e.svc.removeAll = func(string) error { return errors.New("busy") }
	res, err := e.svc.DeleteProjectModels(ctx, other)
	if err != nil || res == nil || !strings.Contains(res.Message, "部分文件无法删除") || !strings.Contains(res.Message, "另有 1 个模型") {
		t.Errorf("DeleteProjectModels with leftovers = %+v, %v", res, err)
	}
	e.svc.removeAll = os.RemoveAll
}

func TestIntegrationProjectDeletedDuringCommit(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	up := e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolo11"), ""))
	inspecting, release := make(chan struct{}), make(chan struct{})
	e.inspectHook = func() { close(inspecting); <-release }
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
		done <- err
	}()
	<-inspecting
	if _, err := e.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", projectID); err != nil {
		t.Fatal(err)
	}
	close(release)
	wantKind(t, <-done, ErrInvalid, "所选项目不存在或已被删除")
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("the staged file should be removed")
	}
	if entries, _ := os.ReadDir(e.st.Dir(storage.Models)); len(entries) != 0 {
		t.Errorf("models/ = %v", entries)
	}
}

func TestIntegrationRegisterTrainedModel(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	e.addModel(t, projectID, "yolo26")
	weights := filepath.Join(e.st.Dir(storage.Runs), "job-7", "weights", "best.pt")
	data := fakePT(t, detectInfo("yolo26"), "trained")
	if err := os.MkdirAll(filepath.Dir(weights), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(weights, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.RegisterTrainedModel(ctx, TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo26",
		Description: "训练任务 #7", WeightsPath: "job-7/weights/best.pt"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != SourceTraining || m.TrainingJobID == nil || *m.TrainingJobID != 7 || m.Version != 2 ||
		m.FileName != "best.pt" || m.Description != "训练任务 #7" || m.FileMissing {
		t.Errorf("model = %+v", m)
	}
	if b, err := os.ReadFile(weights); err != nil || !bytes.Equal(b, data) {
		t.Error("the run keeps its weights")
	}
	if b, err := os.ReadFile(e.modelPath(m, "model.pt")); err != nil || !bytes.Equal(b, data) {
		t.Error("model.pt is a copy of the weights")
	}
	if entries, _ := os.ReadDir(e.svc.registrationsRoot()); len(entries) != 0 {
		t.Errorf("registration staging left: %v", entries)
	}
	if ms, _ := e.svc.ListModels(ctx, ModelQuery{Source: SourceTraining}); len(ms) != 1 || ms[0].ID != m.ID {
		t.Errorf("training models = %+v", ms)
	}

	for _, tt := range []struct {
		in   TrainedModelInput
		kind error
		msg  string
	}{
		{TrainedModelInput{ProjectID: projectID, YOLOVersion: "yolo26", WeightsPath: "job-7/weights/best.pt"}, ErrInvalid, "缺少训练任务 ID"},
		{TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo26", WeightsPath: "../tmp/x.pt"}, ErrInvalid, "权重文件路径无效"},
		{TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo26", WeightsPath: "/etc/passwd"}, ErrInvalid, "权重文件路径无效"},
		{TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo26", WeightsPath: "job-8/best.pt"}, ErrNotFound, "权重文件不存在"},
		{TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo26", WeightsPath: "job-7"}, ErrInvalid, ".pt"},
		{TrainedModelInput{ProjectID: projectID, TrainingJobID: 7, YOLOVersion: "yolo11", WeightsPath: "job-7/weights/best.pt"}, ErrVersionMismatch, "YOLO26"},
		{TrainedModelInput{ProjectID: 99999, TrainingJobID: 7, YOLOVersion: "yolo26", WeightsPath: "job-7/weights/best.pt"}, ErrInvalid, "所选项目不存在"},
	} {
		_, err := e.svc.RegisterTrainedModel(ctx, tt.in)
		wantKind(t, err, tt.kind, tt.msg)
	}
	if entries, _ := os.ReadDir(e.svc.registrationsRoot()); len(entries) != 0 {
		t.Errorf("registration staging left after failures: %v", entries)
	}
	// The training_job_id rule is also a database constraint.
	if _, err := e.db.Exec(ctx, `INSERT INTO models (project_id, version, source, yolo_version, task, file_name, dir_name,
		size_bytes, sha256) VALUES ($1, 99, 'training', 'yolo11', 'detect', 'a.pt', 'x', 1, '')`, projectID); err == nil {
		t.Error("a training model without training_job_id should violate the check")
	}
}

func TestIntegrationCleanup(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	data := fakePT(t, detectInfo("yolo11"), "")
	stale := e.upload(t, projectID, "yolo11", data)
	fresh := e.upload(t, projectID, "yolo11", data)
	if _, err := e.db.Exec(ctx, "UPDATE model_uploads SET updated_at = now() - interval '25 hours' WHERE id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}
	// Leftovers of every kind.
	orphans := []string{
		filepath.Join(e.svc.uploadsRoot(), "99999", "upload.pt"),
		filepath.Join(e.svc.uploadsRoot(), "junk"),
		filepath.Join(e.svc.exportsRoot(), "424242", "output.onnx.part"),
		filepath.Join(e.svc.exportsRoot(), "junk"),
		filepath.Join(e.svc.registrationsRoot(), "abc", "model.pt"),
		filepath.Join(e.st.Dir(storage.Models), ".deleting-17", "model.pt"),
	}
	for _, p := range orphans {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// In use: a registration in progress and a running conversion's result.
	m := e.addModel(t, projectID, "yolo11")
	busy := filepath.Join(e.svc.registrationsRoot(), "busy")
	e.svc.busy.add(busy)
	if err := os.MkdirAll(busy, 0o755); err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "node-a")
	exp, _, err := e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: "onnx"})
	if err != nil {
		t.Fatal(err)
	}
	if task, err := e.svc.ClaimTask(ctx, node, []string{TaskModelExport}, 0); err != nil || task == nil {
		t.Fatalf("ClaimTask = %v, %v", task, err)
	}
	part := e.svc.outputPath(exp.ID)
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.svc.cleanup(ctx)
	if up, _ := e.svc.GetUpload(ctx, stale.ID); up.Status != UploadCanceled || up.Error != msgUploadExpired {
		t.Errorf("stale upload = %+v", up)
	}
	if exists(e.svc.uploadDir(stale.ID)) {
		t.Error("stale upload folder should be removed")
	}
	if up, _ := e.svc.GetUpload(ctx, fresh.ID); up.Status != UploadUploading || !exists(e.svc.uploadPath(fresh.ID)) {
		t.Errorf("fresh upload = %+v", up)
	}
	for _, p := range orphans {
		if exists(p) {
			t.Errorf("%s should be removed", p)
		}
	}
	if !exists(busy) || !exists(part) {
		t.Error("files in use must be kept")
	}
}
