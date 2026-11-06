package model

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// expireModels makes the models of a project in the recycle bin look as if
// they had been there for longer than recycle.Retention.
func (e *testEnv) expireModels(t *testing.T, projectID int64) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(), `UPDATE models SET deleted_at = now() - interval '8 days'
		WHERE project_id = $1 AND deleted_at IS NOT NULL`, projectID); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) trashed(t *testing.T) []TrashedModel {
	t.Helper()
	items, err := e.svc.ListTrashedModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestIntegrationRecycleModel(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	m := e.addModel(t, projectID, "yolo11")
	if _, _, err := e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: "onnx"}); err != nil {
		t.Fatal(err)
	}
	// A finished conversion, cached in the model folder.
	if err := os.MkdirAll(e.modelPath(m, exportsSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.modelPath(m, "onnx/9.onnx"), []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO model_exports (model_id, format, options, options_key, status, file_name, size_bytes)
		VALUES ($1, 'onnx', '{}', 'done', 'succeeded', 'onnx/9.onnx', 4)`, m.ID); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	_, err := e.svc.GetModel(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型不存在")
	wantKind(t, e.svc.DeleteModel(ctx, m.ID), ErrNotFound, "模型不存在")
	_, err = e.svc.OpenModelFile(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, _, err = e.svc.RequestExport(ctx, m.ID, ExportRequest{Format: "onnx"})
	wantKind(t, err, ErrNotFound, "模型不存在")
	_, err = e.svc.ListExports(ctx, m.ID)
	wantKind(t, err, ErrNotFound, "模型不存在")
	if ms, err := e.svc.ListModels(ctx, ModelQuery{}); err != nil || len(ms) != 0 {
		t.Errorf("ListModels = %+v, %v", ms, err)
	}
	if ms, err := e.svc.ListProjectModels(ctx, projectID); err != nil || len(ms) != 0 {
		t.Errorf("ListProjectModels = %+v, %v", ms, err)
	}
	// The conversion in progress is canceled; the folder and the finished
	// conversion stay for a restore.
	var statuses []string
	if err := e.db.QueryRow(ctx, "SELECT array_agg(status ORDER BY id) FROM model_exports").Scan(&statuses); err != nil ||
		len(statuses) != 1 || statuses[0] != ExportSucceeded {
		t.Errorf("exports left = %v, %v", statuses, err)
	}
	if !exists(e.modelPath(m, modelFile)) || !exists(e.modelPath(m, "onnx/9.onnx")) {
		t.Error("the model folder must stay while the model is in the bin")
	}

	items := e.trashed(t)
	if len(items) != 1 {
		t.Fatalf("recycle bin = %+v", items)
	}
	if it := items[0]; it.ID != m.ID || it.Version != m.Version || it.Project.Name != "p" || it.ProjectDeleted ||
		it.DownloadURL != "" || it.FileMissing || it.PurgeAt.Sub(it.DeletedAt) != recycle.Retention {
		t.Errorf("trashed model = %+v", it)
	}

	got, err := e.svc.RestoreModel(ctx, m.ID, RestoreInput{})
	if err != nil || got.ID != m.ID || got.ExportCount != 1 || got.FileMissing {
		t.Fatalf("RestoreModel = %+v, %v", got, err)
	}
	if exps, err := e.svc.ListExports(ctx, m.ID); err != nil || len(exps) != 1 {
		t.Errorf("exports after the restore = %+v, %v", exps, err)
	}
	_, err = e.svc.RestoreModel(ctx, m.ID, RestoreInput{})
	wantKind(t, err, ErrNotFound, "回收站中没有这个模型")

	// Purged with its folder once the retention is over, not before; its
	// version is not given again.
	if err := e.svc.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.purgeExpired(ctx); err != nil || len(e.trashed(t)) != 1 {
		t.Fatalf("purged before the retention ended: %v", err)
	}
	e.expireModels(t, projectID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.trashed(t)) != 0 || exists(e.modelPath(m, "")) {
		t.Error("the model and its folder should be purged")
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM models) + (SELECT count(*) FROM model_exports)").
		Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left: %d, %v", n, err)
	}
	if m2 := e.addModel(t, projectID, "yolo11"); m2.Version != m.Version+1 {
		t.Errorf("version after the purge = %d", m2.Version)
	}
}

// A purge that cannot remove the folder completely leaves the rest to the
// hourly cleanup.
func TestIntegrationPurgeModelLeavesUndeletableFiles(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projectID := e.project(t, "p")
	m := e.addModel(t, projectID, "yolo11")
	if err := e.svc.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	e.expireModels(t, projectID)
	e.svc.removeAll = func(string) error { return &fs.PathError{Op: "unlinkat", Path: "x", Err: fs.ErrPermission} }
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.removeAll = os.RemoveAll
	if _, err := e.svc.RestoreModel(ctx, m.ID, RestoreInput{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("the model should be purged: %v", err)
	}
	trash := filepath.Join(e.st.Dir(storage.Models), deletingPrefix+strconv.FormatInt(m.ID, 10))
	if !exists(filepath.Join(trash, modelFile)) {
		t.Fatalf("the rest should wait in %s", trash)
	}
	e.svc.cleanup(ctx)
	if exists(trash) {
		t.Error("the cleanup did not remove the rest")
	}
}

// Deleting a project with its models moves them to the bin; the project
// waits there with them and comes back with the first one restored.
func TestIntegrationRecycleProjectModels(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projects := project.NewService(project.Options{DB: e.db.Pool, Logger: slog.New(slog.DiscardHandler)})
	projectID, other := e.project(t, "p"), e.project(t, "q")
	m1 := e.addModel(t, projectID, "yolo11")
	m2 := e.addModel(t, projectID, "yolo26")
	kept := e.addModel(t, other, "yolo11")
	up := e.upload(t, projectID, "yolo11", fakePT(t, detectInfo("yolo11"), ""))

	if err := e.svc.DeleteProjectModels(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := e.svc.ListModels(ctx, ModelQuery{}); len(ms) != 1 || ms[0].ID != kept.ID {
		t.Errorf("models left = %+v", ms)
	}
	if err := projects.DeleteProject(ctx, projectID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	e.svc.RemoveProjectFolder(ctx, projectID)
	if !exists(e.modelPath(m1, modelFile)) || !exists(e.modelPath(m2, modelFile)) {
		t.Error("the folders of models in the bin must stay")
	}
	// Its upload in progress went with it.
	_, _, err := e.svc.CommitUpload(ctx, up.ID, CommitInput{})
	wantKind(t, err, ErrNotFound, "上传任务不存在")
	if err := e.svc.cleanUploadTemp(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(e.svc.uploadDir(up.ID)) {
		t.Error("staged file of the deleted project's upload should be removed")
	}
	_, err = e.svc.CreateUpload(ctx, CreateUploadInput{ProjectID: projectID, YOLOVersion: "yolo11", FileName: "best.pt",
		FileSize: 10})
	wantKind(t, err, ErrInvalid, "所选项目不存在或已被删除")
	_, err = e.svc.ListProjectModels(ctx, projectID)
	wantKind(t, err, ErrNotFound, "项目不存在")
	items := e.trashed(t)
	if len(items) != 2 || !items[0].ProjectDeleted || !items[1].ProjectDeleted || items[0].Project.Name != "p" {
		t.Fatalf("recycle bin = %+v", items)
	}

	// Another project took the name meanwhile.
	if _, err := projects.CreateProject(ctx, project.CreateProjectInput{Name: "P"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RestoreModel(ctx, m1.ID, RestoreInput{})
	var pe *project.Error
	if !errors.As(err, &pe) || !errors.Is(err, project.ErrNameTaken) {
		t.Fatalf("RestoreModel with the project name taken = %v", err)
	}
	if len(e.trashed(t)) != 2 {
		t.Error("a refused restore must change nothing")
	}
	name := "p（恢复）"
	got, err := e.svc.RestoreModel(ctx, m1.ID, RestoreInput{ProjectName: &name})
	if err != nil || got.Project.ID != projectID || got.Project.Name != name {
		t.Fatalf("RestoreModel = %+v, %v", got, err)
	}
	if p, err := projects.GetProject(ctx, projectID); err != nil || p.Name != name || p.ModelCount != 1 {
		t.Errorf("restored project = %+v, %v", p, err)
	}
	if items := e.trashed(t); len(items) != 1 || items[0].ID != m2.ID || items[0].ProjectDeleted {
		t.Errorf("recycle bin = %+v", items)
	}
	if m3 := e.addModel(t, projectID, "yolo11"); m3.Version != 3 {
		t.Errorf("version in the restored project = %d", m3.Version)
	}

	// Purging the last model of a project in the bin deletes the project
	// and its folder too.
	if err := e.svc.DeleteProjectModels(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if err := projects.DeleteProject(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	e.expireModels(t, projectID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM projects WHERE id = $1", projectID).Scan(&n); err != nil || n != 0 {
		t.Errorf("project rows left: %d, %v", n, err)
	}
	if dir := filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", projectID)); exists(dir) {
		entries, _ := os.ReadDir(dir)
		t.Errorf("project folder kept: %v", entries)
	}
	if len(e.trashed(t)) != 0 {
		t.Error("models left in the bin")
	}
	if _, err := e.svc.GetModel(ctx, kept.ID); err != nil {
		t.Errorf("the other project's model: %v", err)
	}
}
