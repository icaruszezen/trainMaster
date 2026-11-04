package httpapi

import (
	"fmt"
	"slices"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

func TestProjectClassesAPI(t *testing.T) {
	e := newAPIEnv(t, false)
	p := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": "classes"}), 201)
	later := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": "later"}), 201)
	base := fmt.Sprintf("/api/v1/projects/%d/classes", p.ID)

	wantKeys(t, "class list", want[map[string]any](t, e.do("GET", base, nil), 200), "items", "total", "version")
	empty := want[classListResponse](t, e.do("GET", base, nil), 200)
	if empty.Total != 0 || empty.Items == nil || empty.Version == "" {
		t.Errorf("empty classes = %+v", empty)
	}
	raw := want[map[string]any](t, e.do("PUT", base, map[string]any{"classes": []any{
		map[string]any{"name": " 划痕 ", "color": "#AABBCC"}, map[string]any{"name": "dent"}}, "base_version": empty.Version}), 200)
	wantKeys(t, "class", raw["items"].([]any)[0].(map[string]any), "name", "color")
	list := want[classListResponse](t, e.do("GET", base, nil), 200)
	if list.Total != 2 || list.Items[0] != (project.Class{Name: "划痕", Color: "#aabbcc"}) || list.Items[1] != (project.Class{Name: "dent"}) ||
		list.Version != raw["version"] || list.Version == empty.Version {
		t.Errorf("classes = %+v", list)
	}
	// Changing the list changes the project: it moves to the top of the
	// project list (most recently updated first).
	projects := want[List[project.Project]](t, e.do("GET", "/api/v1/projects", nil), 200)
	if len(projects.Items) != 2 || projects.Items[0].ID != p.ID || projects.Items[1].ID != later.ID {
		t.Errorf("projects after changing the classes of %d = %+v", p.ID, projects.Items)
	}

	// A replacement from an older version is refused.
	wantError(t, e.do("PUT", base, map[string]any{"classes": []any{}, "base_version": empty.Version}),
		409, codeConflict, "类别列表已被其他人修改，请载入最新的列表后再保存")
	wantError(t, e.do("PUT", base, map[string]any{"classes": []any{}, "base_version": ""}), 409, codeConflict, "类别列表已被其他人修改")
	if got := want[classListResponse](t, e.do("GET", base, nil), 200); got.Version != list.Version {
		t.Errorf("a refused replacement changed the list: %+v", got)
	}

	// Adding one class keeps the others.
	added := want[classListResponse](t, e.do("POST", base, map[string]any{"name": " 孔洞 "}), 200)
	if added.Total != 3 || added.Items[2] != (project.Class{Name: "孔洞"}) || added.Version == list.Version {
		t.Errorf("added = %+v", added)
	}
	if again := want[classListResponse](t, e.do("POST", base, map[string]any{"name": "孔洞", "color": "#123456"}), 200); again.Version != added.Version || again.Total != 3 {
		t.Errorf("added again = %+v", again)
	}
	wantError(t, e.do("POST", base, map[string]any{}), 422, codeValidationFailed, "缺少 name")
	wantError(t, e.do("POST", base, map[string]any{"name": " "}), 422, codeValidationFailed, "类别名称为空")
	wantError(t, e.do("POST", base, map[string]any{"name": "x", "color": "blue"}), 422, codeValidationFailed, "#rrggbb")
	wantError(t, e.do("POST", base, "{"), 400, codeBadRequest, "请求格式不正确")

	wantError(t, e.do("PUT", base, map[string]any{}), 422, codeValidationFailed, "缺少 classes")
	wantError(t, e.do("PUT", base, map[string]any{"classes": []any{map[string]any{"name": "a", "color": "blue"}}}),
		422, codeValidationFailed, "#rrggbb")
	wantError(t, e.do("PUT", base, map[string]any{"classes": []any{map[string]any{"name": "a"}, map[string]any{"name": "a"}}}),
		422, codeValidationFailed, "重复")
	// Without base_version the list is replaced whatever it is.
	if cleared := want[classListResponse](t, e.do("PUT", base, map[string]any{"classes": []any{}}), 200); cleared.Total != 0 ||
		cleared.Items == nil || cleared.Version != empty.Version {
		t.Errorf("cleared = %+v", cleared)
	}

	for _, target := range []string{"/api/v1/projects/999999/classes", "/api/v1/projects/0/classes", "/api/v1/projects/x/classes"} {
		wantError(t, e.do("GET", target, nil), 404, codeNotFound, "项目不存在")
		wantError(t, e.do("PUT", target, map[string]any{"classes": []any{}}), 404, codeNotFound, "项目不存在")
		wantError(t, e.do("POST", target, map[string]any{"name": "a"}), 404, codeNotFound, "项目不存在")
	}
	wantError(t, e.do("DELETE", base, nil), 405, codeMethodNotAllowed, "")
}

func TestDatasetClassesAPI(t *testing.T) {
	e := newAPIEnv(t, false)
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "a"}), 201)
	lone := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "lone"}), 201)
	setClasses := func(name string, classes ...map[string]any) {
		t.Helper()
		p := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": name, "dataset_ids": []int64{ds.ID}}), 201)
		want[classListResponse](t, e.do("PUT", fmt.Sprintf("/api/v1/projects/%d/classes", p.ID), map[string]any{"classes": classes}), 200)
	}
	// The projects of the dataset by name: Alpha before beta.
	setClasses("beta", map[string]any{"name": "dent", "color": "#222222"}, map[string]any{"name": "crack"})
	setClasses("Alpha", map[string]any{"name": "scratch", "color": "#111111"}, map[string]any{"name": "dent"})

	base := fmt.Sprintf("/api/v1/datasets/%d/classes", ds.ID)
	// Read-only: no version, the lists are edited per project.
	raw := want[map[string]any](t, e.do("GET", base, nil), 200)
	wantKeys(t, "dataset classes", raw, "items", "total")
	wantKeys(t, "class", raw["items"].([]any)[0].(map[string]any), "name", "color")
	list := want[List[project.Class]](t, e.do("GET", base, nil), 200)
	merged := []project.Class{{Name: "scratch", Color: "#111111"}, {Name: "dent"}, {Name: "crack"}}
	if list.Total != len(merged) || !slices.Equal(list.Items, merged) {
		t.Errorf("dataset classes = %+v, want %+v", list, merged)
	}

	// A dataset in no project has none: an empty list, not null.
	raw = want[map[string]any](t, e.do("GET", fmt.Sprintf("/api/v1/datasets/%d/classes", lone.ID), nil), 200)
	if items, ok := raw["items"].([]any); !ok || len(items) != 0 || raw["total"] != float64(0) {
		t.Errorf("classes of a dataset in no project = %v", raw)
	}

	wantError(t, e.do("GET", "/api/v1/datasets/999999/classes", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("GET", "/api/v1/datasets/x/classes", nil), 404, codeNotFound, "数据集不存在")
	// The class lists are edited in the projects, no longer per dataset.
	wantError(t, e.do("PUT", base, map[string]any{"classes": []any{}}), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("POST", base, map[string]any{"name": "a"}), 405, codeMethodNotAllowed, "")
}
