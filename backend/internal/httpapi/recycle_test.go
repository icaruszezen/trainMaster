package httpapi

import (
	"fmt"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

func TestRecycleBinAPI(t *testing.T) {
	e := newModelEnv(t, "")
	const bin = "/api/v1/recycle-bin"
	empty := want[map[string]any](t, e.do("GET", bin, nil), 200)
	wantKeys(t, "recycle bin", empty, "retention_days", "datasets", "versions", "models", "data_versions")
	if empty["retention_days"] != float64(7) || len(empty["datasets"].([]any)) != 0 || len(empty["models"].([]any)) != 0 {
		t.Errorf("empty recycle bin = %v", empty)
	}

	// A dataset, restored under another name when its own is taken.
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "ds"}), 201)
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/datasets/%d?delete_files=true", ds.ID), nil); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	raw := want[map[string]any](t, e.do("GET", bin, nil), 200)
	items := raw["datasets"].([]any)
	if len(items) != 1 {
		t.Fatalf("recycle bin = %v", raw)
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"id", "name", "deleted_at", "purge_at", "delete_files", "restore_projects", "review_count",
		"image_count", "directory_missing"} {
		if _, ok := item[key]; !ok {
			t.Errorf("trashed dataset has no %q: %v", key, item)
		}
	}
	want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "DS"}), 201)
	restore := fmt.Sprintf("%s/datasets/%d/restore", bin, ds.ID)
	wantError(t, e.do("POST", restore, nil), 409, codeNameTaken, "已有名为“ds”的数据集")
	wantError(t, e.do("POST", restore, "{"), 400, codeBadRequest, "")
	wantError(t, e.do("POST", restore, map[string]any{"name": " "}), 422, codeValidationFailed, "")
	if got := want[dataset.Dataset](t, e.do("POST", restore, map[string]any{"name": "ds-2"}), 200); got.ID != ds.ID ||
		got.Name != "ds-2" || got.DirName != "ds-2" {
		t.Errorf("restored = %+v", got)
	}
	wantError(t, e.do("POST", restore, nil), 404, codeNotFound, "回收站中没有这个数据集")
	wantError(t, e.do("POST", bin+"/datasets/x/restore", nil), 404, codeNotFound, "回收站中没有这个数据集")
	wantError(t, e.do("POST", bin+"/versions/424242/restore", nil), 404, codeNotFound, "回收站中没有这个版本")
	wantError(t, e.do("GET", bin+"/datasets", nil), 404, codeNotFound, "")

	// Models deleted with their project; the project comes back with them.
	p := e.project(t, "p")
	m := e.uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "1"))
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=true", p.ID), nil); rec.Code != 204 {
		t.Fatalf("delete project = %d %s", rec.Code, rec.Body)
	}
	raw = want[map[string]any](t, e.do("GET", bin, nil), 200)
	if items := raw["models"].([]any); len(items) != 1 || items[0].(map[string]any)["project_deleted"] != true ||
		items[0].(map[string]any)["download_url"] != "" {
		t.Fatalf("models in the bin = %v", raw["models"])
	}
	e.project(t, "p")
	restoreModel := fmt.Sprintf("%s/models/%d/restore", bin, m.ID)
	wantError(t, e.do("POST", restoreModel, nil), 409, codeNameTaken, "已存在同名项目“p”")
	got := want[model.Model](t, e.do("POST", restoreModel, map[string]any{"project_name": "p-old"}), 200)
	if got.ID != m.ID || got.Project.ID != p.ID || got.Project.Name != "p-old" {
		t.Errorf("restored model = %+v", got)
	}
	want[model.Model](t, e.do("GET", fmt.Sprintf("/api/v1/models/%d", m.ID), nil), 200)
	wantError(t, e.do("POST", restoreModel, nil), 404, codeNotFound, "回收站中没有这个模型")
	if l := want[recycleBin](t, e.do("GET", bin, nil), 200); len(l.Datasets) != 0 || len(l.Models) != 0 {
		t.Errorf("recycle bin after the restores = %+v", l)
	}
}
