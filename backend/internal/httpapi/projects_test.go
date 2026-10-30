package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

var (
	projectKeys = []string{"id", "name", "description", "dataset_count", "image_count", "annotated_count",
		"empty_count", "invalid_count", "object_count", "total_bytes", "covers", "created_at", "updated_at"}
	coverKeys        = []string{"dataset_id", "id", "version"}
	projectLabelKeys = []string{"name", "object_count", "image_count", "dataset_count"}
)

// scannedDataset creates a dataset whose folder holds the given files and
// waits for its first scan.
func (e *apiEnv) scannedDataset(t *testing.T, name string, files map[string][]byte) dataset.Dataset {
	t.Helper()
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]string{"name": name}), http.StatusCreated)
	dir := filepath.Join(e.st.Dir(storage.Datasets), name)
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	job := want[dataset.Import](t, e.do("POST", fmt.Sprintf("/api/v1/datasets/%d/scan", ds.ID), nil), http.StatusAccepted)
	if job = e.waitImport(t, ds.ID, job.ID); job.Status != dataset.StatusSucceeded {
		t.Fatalf("scan of %s = %+v", name, job)
	}
	return want[dataset.Dataset](t, e.do("GET", fmt.Sprintf("/api/v1/datasets/%d", ds.ID), nil), http.StatusOK)
}

func datasetIDsOf(list List[dataset.Dataset]) []int64 {
	ids := []int64{}
	for _, d := range list.Items {
		ids = append(ids, d.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestProjectAPI(t *testing.T) {
	e := newAPIEnv(t, true)

	jpg := testJPEG(t, 32, 24)
	d1 := e.scannedDataset(t, "划痕", map[string][]byte{
		"a.jpg": jpg, "a.xml": []byte(testVOC("a.jpg", "scratch", "scratch", "dent")),
		"b.jpg": jpg, "b.xml": []byte(testVOC("b.jpg")),
	})
	d2 := e.scannedDataset(t, "凹坑", map[string][]byte{
		"c.jpg": jpg, "c.xml": []byte(testVOC("c.jpg", "dent")),
	})

	// Create: the response follows the contract.
	rec := e.do("POST", "/api/v1/projects", map[string]any{"name": " 外观检测 ", "description": "产线 A", "extra": 1})
	raw := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "project", raw, projectKeys...)
	if raw["name"] != "外观检测" || raw["dataset_count"] != 0.0 {
		t.Errorf("created project = %v", raw)
	}
	if covers, ok := raw["covers"].([]any); !ok || len(covers) != 0 {
		t.Errorf("covers = %#v, want []", raw["covers"])
	}
	id := int64(raw["id"].(float64))
	base := "/api/v1/projects/" + strconv.FormatInt(id, 10)

	wantError(t, e.do("POST", "/api/v1/projects", map[string]string{"name": "外观检测"}), 409, codeConflict, "已存在同名项目")
	wantError(t, e.do("POST", "/api/v1/projects", map[string]string{"name": ""}), 422, codeValidationFailed, "名称不能为空")
	wantError(t, e.do("POST", "/api/v1/projects", map[string]any{"name": "x", "dataset_ids": []int64{d1.ID, 99999}}),
		422, codeValidationFailed, "所选数据集不存在")
	wantError(t, e.do("POST", "/api/v1/projects", map[string]any{"name": "x", "dataset_ids": []any{1.5}}),
		400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("POST", "/api/v1/projects", "{"), 400, codeBadRequest, "请求格式不正确")

	// Add datasets: the totals and covers follow.
	p := want[project.Project](t, e.do("POST", base+"/datasets", map[string]any{"dataset_ids": []int64{d1.ID, d2.ID}}), 200)
	if p.DatasetCount != 2 || p.ImageCount != 3 || p.AnnotatedCount != 2 || p.EmptyCount != 1 || p.ObjectCount != 4 ||
		p.TotalBytes != d1.TotalBytes+d2.TotalBytes || len(p.Covers) != 3 {
		t.Errorf("project with datasets = %+v", p)
	}
	rawProject := want[map[string]any](t, e.do("GET", base, nil), 200)
	wantKeys(t, "cover", rawProject["covers"].([]any)[0].(map[string]any), coverKeys...)
	wantError(t, e.do("POST", base+"/datasets", map[string]any{"dataset_ids": []int64{}}), 422, codeValidationFailed, "请选择要添加的数据集")
	wantError(t, e.do("POST", "/api/v1/projects/99999/datasets", map[string]any{"dataset_ids": []int64{d1.ID}}), 404, codeNotFound, "项目不存在")

	// The datasets of the project, from both endpoints; datasets name their projects.
	list := want[List[dataset.Dataset]](t, e.do("GET", base+"/datasets", nil), 200)
	if got := datasetIDsOf(list); list.Total != 2 || !slices.Equal(got, []int64{d1.ID, d2.ID}) {
		t.Errorf("project datasets = %v (total %d)", got, list.Total)
	}
	if ps := list.Items[0].Projects; len(ps) != 1 || ps[0] != (dataset.ProjectRef{ID: id, Name: "外观检测"}) {
		t.Errorf("dataset projects = %+v", ps)
	}
	rawList := want[map[string]any](t, e.do("GET", "/api/v1/datasets?project_id="+strconv.FormatInt(id, 10), nil), 200)
	if rawList["total"] != 2.0 {
		t.Errorf("filtered list = %v", rawList)
	}
	wantKeys(t, "project ref", rawList["items"].([]any)[0].(map[string]any)["projects"].([]any)[0].(map[string]any), "id", "name")
	if l := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?project_id=none", nil), 200); l.Total != 0 {
		t.Errorf("datasets in no project = %+v", l)
	}
	if l := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?project_id=99999", nil), 200); l.Total != 0 {
		t.Errorf("datasets of a missing project = %+v", l)
	}
	// IDs beyond int4 are valid bigint project IDs, not server errors.
	if l := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?project_id=3000000000", nil), 200); l.Total != 0 {
		t.Errorf("datasets of a large missing project ID = %+v", l)
	}
	for _, v := range []string{"0", "-1", "abc", "1.5", "None"} {
		wantError(t, e.do("GET", "/api/v1/datasets?project_id="+v, nil), 400, codeBadRequest, "参数 project_id 无效")
	}
	wantError(t, e.do("GET", "/api/v1/projects/99999/datasets", nil), 404, codeNotFound, "项目不存在")

	// Labels over the project's datasets.
	rawLabels := want[map[string]any](t, e.do("GET", base+"/labels", nil), 200)
	wantKeys(t, "label", rawLabels["items"].([]any)[0].(map[string]any), projectLabelKeys...)
	labels := want[List[project.Label]](t, e.do("GET", base+"/labels", nil), 200)
	if wantLabels := []project.Label{
		{Name: "dent", ObjectCount: 2, ImageCount: 2, DatasetCount: 2},
		{Name: "scratch", ObjectCount: 2, ImageCount: 1, DatasetCount: 1},
	}; !slices.Equal(labels.Items, wantLabels) {
		t.Errorf("labels = %+v, want %+v", labels.Items, wantLabels)
	}
	wantError(t, e.do("GET", "/api/v1/projects/99999/labels", nil), 404, codeNotFound, "项目不存在")

	// The dataset side: replace the projects of a dataset.
	other := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": "其它"}), 201)
	dsURL := fmt.Sprintf("/api/v1/datasets/%d/projects", d2.ID)
	got := want[dataset.Dataset](t, e.do("PUT", dsURL, map[string]any{"project_ids": []int64{other.ID}}), 200)
	if got.ID != d2.ID || len(got.Projects) != 1 || got.Projects[0].ID != other.ID {
		t.Errorf("dataset after PUT projects = %+v", got.Projects)
	}
	wantError(t, e.do("PUT", dsURL, map[string]any{}), 422, codeValidationFailed, "缺少 project_ids")
	wantError(t, e.do("PUT", dsURL, map[string]any{"project_ids": []int64{99999}}), 422, codeValidationFailed, "所选项目不存在")
	wantError(t, e.do("PUT", "/api/v1/datasets/99999/projects", map[string]any{"project_ids": []int64{}}), 404, codeNotFound, "数据集不存在")
	if l := want[List[dataset.Dataset]](t, e.do("GET", base+"/datasets", nil), 200); !slices.Equal(datasetIDsOf(l), []int64{d1.ID}) {
		t.Errorf("project datasets after moving d2 = %v", datasetIDsOf(l))
	}
	got = want[dataset.Dataset](t, e.do("PUT", dsURL, map[string]any{"project_ids": []int64{}}), 200)
	if len(got.Projects) != 0 || got.Projects == nil {
		t.Errorf("projects after clearing = %#v, want []", got.Projects)
	}
	if l := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?project_id=none&q=凹", nil), 200); !slices.Equal(datasetIDsOf(l), []int64{d2.ID}) {
		t.Errorf("datasets in no project matching 凹 = %v", datasetIDsOf(l))
	}

	// Remove a dataset from the project.
	if rec := e.do("DELETE", fmt.Sprintf("%s/datasets/%d", base, d1.ID), nil); rec.Code != 204 {
		t.Fatalf("remove dataset: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", fmt.Sprintf("%s/datasets/%d", base, d1.ID), nil); rec.Code != 204 {
		t.Errorf("removing again: %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("DELETE", base+"/datasets/99999", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("DELETE", base+"/datasets/x", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("DELETE", fmt.Sprintf("/api/v1/projects/99999/datasets/%d", d1.ID), nil), 404, codeNotFound, "项目不存在")

	// Update.
	renamed := want[project.Project](t, e.do("PATCH", base, map[string]any{"name": "外观检测-2"}), 200)
	if renamed.Name != "外观检测-2" || renamed.Description != "产线 A" {
		t.Errorf("renamed = %+v", renamed)
	}
	wantError(t, e.do("PATCH", base, map[string]any{"name": "其它"}), 409, codeConflict, "已存在同名项目")
	wantError(t, e.do("PATCH", base, map[string]any{"description": strings.Repeat("字", 501)}), 422, codeValidationFailed, "描述")
	wantError(t, e.do("PATCH", "/api/v1/projects/99999", map[string]any{"name": "y"}), 404, codeNotFound, "项目不存在")

	// List: most recently updated first, with search.
	projects := want[List[project.Project]](t, e.do("GET", "/api/v1/projects", nil), 200)
	if projects.Total != 2 || projects.Items[0].ID != id {
		t.Errorf("projects = %+v", projects)
	}
	if l := want[List[project.Project]](t, e.do("GET", "/api/v1/projects?q=其", nil), 200); l.Total != 1 || l.Items[0].ID != other.ID {
		t.Errorf("search = %+v", l)
	}

	// Delete: the datasets stay.
	if rec := e.do("DELETE", base, nil); rec.Code != 204 {
		t.Fatalf("delete project: %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "项目不存在")
	wantError(t, e.do("DELETE", base, nil), 404, codeNotFound, "项目不存在")
	want[dataset.Dataset](t, e.do("GET", fmt.Sprintf("/api/v1/datasets/%d", d1.ID), nil), 200)
}

func TestProjectAPIRouting(t *testing.T) {
	e := newAPIEnv(t, false)
	for _, target := range []string{"/api/v1/projects/0", "/api/v1/projects/-1", "/api/v1/projects/abc",
		"/api/v1/projects/99999999999999999999", "/api/v1/projects/42", "/api/v1/projects/42/labels",
		"/api/v1/projects/42/datasets"} {
		wantError(t, e.do("GET", target, nil), 404, codeNotFound, "项目不存在")
	}
	wantError(t, e.do("PUT", "/api/v1/projects", nil), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("GET", "/api/v1/projects/1/bogus", nil), 404, codeNotFound, "")
	if list := want[List[project.Project]](t, e.do("GET", "/api/v1/projects", nil), 200); list.Total != 0 || list.Items == nil {
		t.Errorf("empty list = %+v", list)
	}
}
