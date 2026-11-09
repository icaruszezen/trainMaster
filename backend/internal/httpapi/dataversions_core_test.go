package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// The flows of create.go and build.go (preview, create, a real build,
// retry, PATCH, DELETE, DELETE /projects?delete_data_versions=true),
// through the API with started services.

// startedDataVersionEnv is newDataVersionEnvWith(t, true): the services
// are started (the builder runs).
func startedDataVersionEnv(t *testing.T) *dataVersionEnv {
	t.Helper()
	return newDataVersionEnvWith(t, true)
}

// buildProject creates a project with the classes scratch and dent and a
// scanned dataset of four annotated images (one with no objects).
func (e *dataVersionEnv) buildProject(t *testing.T, name string) (project.Project, dataset.Dataset, string) {
	t.Helper()
	jpg := testJPEG(t, 64, 48)
	ds := e.scannedDataset(t, name+"-line", map[string][]byte{
		"a.jpg": jpg, "a.xml": []byte(testVOC("a.jpg", "scratch")),
		"b.jpg": jpg, "b.xml": []byte(testVOC("b.jpg", "dent")),
		"c.jpg": jpg, "c.xml": []byte(testVOC("c.jpg", "scratch", "dent")),
		"d.jpg": jpg, "d.xml": []byte(testVOC("d.jpg")),
	})
	p := e.classProject(t, name, "scratch", "dent")
	want[project.Project](t, e.do("POST", fmt.Sprintf("/api/v1/projects/%d/datasets", p.ID),
		map[string]any{"dataset_ids": []int64{ds.ID}}), http.StatusOK)
	classes := want[classListResponse](t, e.do("GET", fmt.Sprintf("/api/v1/projects/%d/classes", p.ID), nil), http.StatusOK)
	return p, ds, classes.Version
}

// createInput is the CreateInput the UI sends by default, split 50/50.
func createInput(projectID, datasetID int64, classVersion string) map[string]any {
	return map[string]any{"project_id": projectID, "note": "第一版", "class_version": classVersion,
		"sources": []map[string]any{{"dataset_id": datasetID, "include_base": true, "version_ids": []int64{}}},
		"split":   map[string]any{"train": 50, "val": 50, "test": 0, "seed": 42, "use_folders": true, "group_by": "stem"},
		"scope":   "annotated", "include_empty": true, "ignore_labels": []string{}, "augmentation": nil}
}

// waitBuilt polls a version until its build ends.
func (e *dataVersionEnv) waitBuilt(t *testing.T, id int64) dataversion.DataVersion {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		v := want[dataversion.DataVersion](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d", id), nil), http.StatusOK)
		if v.Status == dataversion.StatusReady || v.Status == dataversion.StatusFailed {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("version %d still %s", id, v.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDataVersionAPIBuild(t *testing.T) {
	e := startedDataVersionEnv(t)
	p, ds, classVersion := e.buildProject(t, "外观检测")
	in := createInput(p.ID, ds.ID, classVersion)

	// Preview: the index counts, and the validation of create.
	raw := want[map[string]any](t, e.do("POST", "/api/v1/data-versions/preview", in), http.StatusOK)
	wantKeys(t, "preview", raw, "classes", "class_version", "image_count", "object_count", "augmented_count", "sources",
		"skipped", "dropped_objects", "degenerate_objects", "splits", "labels", "unlisted_labels", "compare")
	pv := want[dataversion.Preview](t, e.do("POST", "/api/v1/data-versions/preview", in), http.StatusOK)
	if pv.ImageCount != 4 || pv.ObjectCount != 4 || pv.ClassVersion != classVersion || len(pv.Labels) != 2 ||
		pv.Compare != nil || len(pv.Sources) != 1 || pv.Sources[0].DatasetName != ds.Name {
		t.Errorf("preview = %+v", pv)
	}
	wantError(t, e.do("POST", "/api/v1/data-versions/preview", "{"), 400, codeBadRequest, "")
	missing := createInput(424242, ds.ID, classVersion)
	wantError(t, e.do("POST", "/api/v1/data-versions/preview", missing), 422, codeValidationFailed, "项目不存在")

	// Create: refused with a stale class list, then queued and built.
	stale := createInput(p.ID, ds.ID, "stale")
	wantError(t, e.do("POST", "/api/v1/data-versions", stale), 409, codeConflict, "项目的类别列表已经修改")
	rec := e.do("POST", "/api/v1/data-versions", in)
	created := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "data version", created, dataVersionKeys...)
	id := int64(created["id"].(float64))
	if created["version"] != 1.0 || created["note"] != "第一版" || created["project_name"] != "外观检测" {
		t.Errorf("created = %v", created)
	}
	v := e.waitBuilt(t, id)
	if v.Status != dataversion.StatusReady || v.ImageCount != 4 || v.TrainCount+v.ValCount != 4 || v.TrainCount == 0 ||
		v.ValCount == 0 || v.ObjectCount != 4 || v.ClassVersion != classVersion {
		t.Fatalf("built = %+v", v)
	}
	l := want[List[dataversion.DataVersion]](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions?project_id=%d", p.ID), nil), 200)
	if l.Total != 1 || l.Items[0].ID != id {
		t.Errorf("list = %+v", l)
	}
	got := want[project.Project](t, e.do("GET", fmt.Sprintf("/api/v1/projects/%d", p.ID), nil), 200)
	if got.DataVersionCount != 1 || got.LatestDataVersion == nil || got.LatestDataVersion.ID != id ||
		got.LatestDataVersion.Status != dataversion.StatusReady {
		t.Errorf("project = %+v", got)
	}
	images := want[List[dataversion.Image]](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d/images", id), nil), 200)
	if images.Total != 4 || len(images.Items) != 4 {
		t.Fatalf("images = %+v", images)
	}
	if rec := e.do("GET", images.Items[0].FileURL, nil); rec.Code != 200 || rec.Body.Len() == 0 {
		t.Errorf("image file = %d", rec.Code)
	}
	ticket := want[exportResponse](t, e.do("POST", fmt.Sprintf("/api/v1/data-versions/%d/exports", id),
		map[string]any{"format": "yolo"}), http.StatusCreated)
	if rec := e.do("GET", ticket.URL, nil); rec.Code != 200 || len(readZip(t, rec.Body.Bytes())) != 2+4+4 {
		t.Errorf("download = %d", rec.Code)
	}
	if pv := want[dataversion.Preview](t, e.do("POST", "/api/v1/data-versions/preview", in), 200); pv.Compare == nil ||
		pv.Compare.ID != id || pv.Compare.Added != 0 || pv.Compare.Removed != 0 {
		t.Errorf("compare = %+v", pv.Compare)
	}

	// Retry is for failed versions only; the note can change in any state.
	wantError(t, e.do("POST", fmt.Sprintf("/api/v1/data-versions/%d/retry", id), nil), 409, codeConflict,
		"只有生成失败的数据版本可以重新生成")
	base := fmt.Sprintf("/api/v1/data-versions/%d", id)
	wantError(t, e.do("PATCH", base, "{"), 400, codeBadRequest, "")
	if v := want[dataversion.DataVersion](t, e.do("PATCH", base, map[string]any{"note": "改过的备注"}), 200); v.Note != "改过的备注" {
		t.Errorf("patched = %+v", v)
	}

	// DELETE moves a ready version to the recycle bin; restore brings it back.
	if rec := e.do("DELETE", base, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "数据版本不存在")
	wantError(t, e.do("DELETE", base, nil), 404, codeNotFound, "数据版本不存在")
	bin := want[recycleBin](t, e.do("GET", "/api/v1/recycle-bin", nil), 200)
	if len(bin.DataVersions) != 1 || bin.DataVersions[0].ID != id || bin.DataVersions[0].ProjectDeleted {
		t.Fatalf("recycle bin = %+v", bin.DataVersions)
	}
	want[dataversion.DataVersion](t, e.do("POST", fmt.Sprintf("/api/v1/recycle-bin/data-versions/%d/restore", id), nil), 200)
	want[dataversion.DataVersion](t, e.do("GET", base, nil), 200)
}

func TestDataVersionAPIProjectDeleteVersions(t *testing.T) {
	e := startedDataVersionEnv(t)
	p, ds, classVersion := e.buildProject(t, "p")
	created := want[dataversion.DataVersion](t, e.do("POST", "/api/v1/data-versions",
		createInput(p.ID, ds.ID, classVersion)), http.StatusCreated)
	ready := e.waitBuilt(t, created.ID)
	if ready.Status != dataversion.StatusReady {
		t.Fatalf("built = %+v", ready)
	}
	// A failed version (the next number: the project's sequence is at 1).
	failed := e.insertDataVersion(t, p.ID, 2, dataversion.StatusFailed)
	readyDir := filepath.Join(e.st.Dir(storage.Exports), "data-versions", fmt.Sprintf("project-%d", p.ID), "v1")
	if !dirExists(readyDir) {
		t.Fatalf("no version folder %s", readyDir)
	}

	projectURL := fmt.Sprintf("/api/v1/projects/%d", p.ID)
	wantError(t, e.do("DELETE", projectURL, nil), 409, codeConflict, "项目中有 2 个数据版本")
	if rec := e.do("DELETE", projectURL+"?delete_data_versions=true", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", projectURL, nil), 404, codeNotFound, "项目不存在")
	// The ready version went to the bin with its project, the failed one
	// is gone with its folder.
	bin := want[recycleBin](t, e.do("GET", "/api/v1/recycle-bin", nil), 200)
	if len(bin.DataVersions) != 1 || bin.DataVersions[0].ID != ready.ID || !bin.DataVersions[0].ProjectDeleted {
		t.Fatalf("recycle bin = %+v", bin.DataVersions)
	}
	e.versions.WaitBackground()
	if !dirExists(readyDir) {
		t.Error("the folder of a version in the bin must stay")
	}
	if _, err := os.Stat(failed.dir); !os.IsNotExist(err) {
		t.Errorf("the folder of the failed version: %v", err)
	}
	got := want[dataversion.DataVersion](t, e.do("POST", fmt.Sprintf("/api/v1/recycle-bin/data-versions/%d/restore",
		ready.ID), nil), 200)
	if got.ProjectID != p.ID {
		t.Errorf("restored = %+v", got)
	}
	want[project.Project](t, e.do("GET", projectURL, nil), 200)
}

func TestDataVersionAPIScope(t *testing.T) {
	e := startedDataVersionEnv(t)
	p, ds, classVersion := e.buildProject(t, "背景图")

	// The default scope is required, a source's own is optional.
	in := createInput(p.ID, ds.ID, classVersion)
	delete(in, "scope")
	wantError(t, e.do("POST", "/api/v1/data-versions/preview", in), 422, codeValidationFailed, "参与训练的图片无效")
	in["scope"] = "bogus"
	wantError(t, e.do("POST", "/api/v1/data-versions", in), 422, codeValidationFailed, "参与训练的图片无效")
	in = createInput(p.ID, ds.ID, classVersion)
	in["sources"] = []map[string]any{{"dataset_id": ds.ID, "include_base": true, "version_ids": []int64{}, "scope": "x"}}
	wantError(t, e.do("POST", "/api/v1/data-versions/preview", in), 422, codeValidationFailed,
		fmt.Sprintf("数据集 ID %d 参与训练的图片无效", ds.ID))

	// The JSON contract of the new fields.
	in["sources"] = []map[string]any{{"dataset_id": ds.ID, "include_base": true, "version_ids": []int64{}, "scope": "all"}}
	raw := want[map[string]any](t, e.do("POST", "/api/v1/data-versions/preview", in), http.StatusOK)
	src := raw["sources"].([]any)[0].(map[string]any)
	wantKeys(t, "preview source", src, "dataset_id", "dataset_name", "scope", "selected_count", "from_versions",
		"image_count", "negatives", "by_folder")
	if src["scope"] != "all" {
		t.Errorf("preview source = %v", src)
	}
	if _, ok := raw["skipped"].(map[string]any)["with_objects"]; !ok {
		t.Errorf("skipped = %v", raw["skipped"])
	}
	created := want[map[string]any](t, e.do("POST", "/api/v1/data-versions", in), http.StatusCreated)
	config := created["config"].(map[string]any)
	wantKeys(t, "config", config, "split", "scope", "include_empty", "ignore_labels", "augmentation")
	sources := created["sources"].([]any)
	wantKeys(t, "source", sources[0].(map[string]any), "dataset_id", "dataset_name", "dir_name", "include_base",
		"versions", "scope", "image_count", "dataset_deleted")
	if config["scope"] != "annotated" || sources[0].(map[string]any)["scope"] != "all" {
		t.Errorf("created = %v", created)
	}
	v := e.waitBuilt(t, int64(created["id"].(float64)))
	if v.Status != dataversion.StatusReady || v.Sources[0].Scope != dataversion.ScopeAll {
		t.Errorf("built = %+v", v)
	}
}
