package httpapi

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// registerFolder writes files into datasets/<name>, registers the folder
// as a dataset and waits for its scan.
func (e *apiEnv) registerFolder(t *testing.T, name string, files map[string]string) dataset.Dataset {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(e.st.Dir(storage.Datasets), name, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": name, "directory": name}), 201)
	if job := e.waitImport(t, ds.ID, ds.ActiveImport.ID); job.Status != dataset.StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	return ds
}

func TestAnnotationAPI(t *testing.T) {
	e := newAPIEnv(t, true)
	ds := e.registerFolder(t, "line", map[string]string{
		"a.png": string(testPNG(t, 64, 48)),
		"b.png": string(testPNG(t, 64, 48)),
		"b.xml": testVOC("b.png", "dent"),
		"c.png": string(testPNG(t, 64, 48)),
	})
	base := fmt.Sprintf("/api/v1/datasets/%d", ds.ID)
	images := want[List[dataset.Image]](t, e.do("GET", base+"/images", nil), 200).Items
	a, b, c := images[0], images[1], images[2]
	imgURL := func(img dataset.Image, sub string) string { return fmt.Sprintf("%s/images/%d%s", base, img.ID, sub) }

	// Neighbors.
	raw := want[map[string]any](t, e.do("GET", imgURL(b, "/neighbors"), nil), 200)
	wantKeys(t, "neighbors", raw, "prev", "next", "position", "matches", "total")
	wantKeys(t, "neighbor image", raw["prev"].(map[string]any), imageKeys...)
	n := want[dataset.ImageNeighbors](t, e.do("GET", imgURL(b, "/neighbors"), nil), 200)
	if n.Prev == nil || n.Prev.ID != a.ID || n.Next == nil || n.Next.ID != c.ID || n.Position != 1 || !n.Matches || n.Total != 3 {
		t.Errorf("neighbors = %+v", n)
	}
	n = want[dataset.ImageNeighbors](t, e.do("GET", imgURL(b, "/neighbors?status=unannotated"), nil), 200)
	if n.Prev == nil || n.Prev.ID != a.ID || n.Next == nil || n.Next.ID != c.ID || n.Position != 1 || n.Matches || n.Total != 2 {
		t.Errorf("unannotated neighbors = %+v", n)
	}
	if n := want[dataset.ImageNeighbors](t, e.do("GET", imgURL(a, "/neighbors?label=dent&q=B"), nil), 200); n.Prev != nil ||
		n.Next == nil || n.Next.ID != b.ID || n.Matches || n.Total != 1 {
		t.Errorf("label neighbors = %+v", n)
	}
	wantError(t, e.do("GET", imgURL(a, "/neighbors?status=bogus"), nil), 400, codeBadRequest, "参数 status 无效")
	wantError(t, e.do("GET", base+"/images/999999/neighbors", nil), 404, codeNotFound, "图片不存在")
	wantError(t, e.do("GET", base+"/images/x/neighbors", nil), 404, codeNotFound, "图片不存在")

	// Save a new annotation.
	box := map[string]any{"name": "scratch", "xmin": 1, "ymin": 2, "xmax": 30, "ymax": 40, "difficult": false, "truncated": false}
	rawSaved := want[map[string]any](t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{
		"objects": []any{box}, "base_version": ""}), 200)
	wantKeys(t, "saved image", rawSaved, append(append([]string{}, imageKeys...), "objects", "annotation_error", "annotation_version", "annotation_unsupported", "annotation_blocked")...)
	saved := want[dataset.ImageDetail](t, e.do("GET", imgURL(a, ""), nil), 200)
	if saved.AnnotationState != "annotated" || saved.AnnotationVersion == "" || saved.AnnotationVersion != rawSaved["annotation_version"] ||
		len(saved.Objects) != 1 || saved.Objects[0].Name != "scratch" {
		t.Errorf("saved = %+v", saved)
	}
	if data, err := os.ReadFile(filepath.Join(e.st.Dir(storage.Datasets), "line", "a.xml")); err != nil ||
		!strings.Contains(string(data), "<name>scratch</name>") {
		t.Errorf("a.xml = %s, %v", data, err)
	}
	if d := want[dataset.Dataset](t, e.do("GET", base, nil), 200); d.AnnotatedCount != 2 || d.ObjectCount != 2 {
		t.Errorf("dataset after save = %+v", d)
	}

	// Conflicts and validation.
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{"objects": []any{}, "base_version": ""}),
		409, codeAnnotationConflict, "已被修改")
	if rec := e.do("PUT", imgURL(a, "/annotation"), map[string]any{"objects": []any{}, "force": true}); rec.Code != 200 {
		t.Errorf("forced save: %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{"base_version": ""}), 422, codeValidationFailed, "缺少 objects")
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{"objects": []any{}}), 422, codeValidationFailed, "缺少 base_version")
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{"objects": []any{map[string]any{"name": "x", "xmin": 1.5}}, "force": true}),
		400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), map[string]any{
		"objects": []any{map[string]any{"name": "x", "xmin": 5, "ymin": 5, "xmax": 5, "ymax": 9}}, "force": true}),
		422, codeValidationFailed, "宽或高为 0")
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), "{"), 400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("PUT", base+"/images/999999/annotation", map[string]any{"objects": []any{}, "force": true}), 404, codeNotFound, "图片不存在")
	wantError(t, e.do("PUT", "/api/v1/datasets/999999/images/1/annotation", map[string]any{"objects": []any{}, "force": true}),
		404, codeNotFound, "数据集不存在")
	big := `{"force": true, "objects": [` + strings.Repeat(`{"name": "x", "xmin": 1, "ymin": 1, "xmax": 2, "ymax": 2},`, 200_000) + `{}]}`
	wantError(t, e.do("PUT", imgURL(a, "/annotation"), big), 413, codePayloadTooLarge, "")

	// Delete.
	bDetail := want[dataset.ImageDetail](t, e.do("GET", imgURL(b, ""), nil), 200)
	wantError(t, e.do("DELETE", imgURL(b, "/annotation"), nil), 422, codeValidationFailed, "缺少参数 base_version")
	wantError(t, e.do("DELETE", imgURL(b, "/annotation?base_version="), nil), 409, codeAnnotationConflict, "已被修改")
	wantError(t, e.do("DELETE", imgURL(b, "/annotation?force=maybe"), nil), 400, codeBadRequest, "参数 force 无效")
	gone := want[dataset.ImageDetail](t, e.do("DELETE", imgURL(b, "/annotation?base_version="+url.QueryEscape(bDetail.AnnotationVersion)), nil), 200)
	if gone.AnnotationState != "none" || gone.AnnotationPath != nil || gone.AnnotationVersion != "" {
		t.Errorf("deleted = %+v", gone)
	}
	if _, err := os.Stat(filepath.Join(e.st.Dir(storage.Datasets), "line", "b.xml")); err == nil {
		t.Error("b.xml still exists")
	}
}

func TestClassesAPI(t *testing.T) {
	e := newAPIEnv(t, false)
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "classes"}), 201)
	base := fmt.Sprintf("/api/v1/datasets/%d/classes", ds.ID)

	wantKeys(t, "class list", want[map[string]any](t, e.do("GET", base, nil), 200), "items", "total", "version")
	empty := want[classListResponse](t, e.do("GET", base, nil), 200)
	if empty.Total != 0 || empty.Items == nil || empty.Version == "" {
		t.Errorf("empty classes = %+v", empty)
	}
	raw := want[map[string]any](t, e.do("PUT", base, map[string]any{"classes": []any{
		map[string]any{"name": " 划痕 ", "color": "#AABBCC"}, map[string]any{"name": "dent"}}, "base_version": empty.Version}), 200)
	wantKeys(t, "class", raw["items"].([]any)[0].(map[string]any), "name", "color")
	list := want[classListResponse](t, e.do("GET", base, nil), 200)
	if list.Total != 2 || list.Items[0] != (dataset.Class{Name: "划痕", Color: "#aabbcc"}) || list.Items[1] != (dataset.Class{Name: "dent"}) ||
		list.Version != raw["version"] || list.Version == empty.Version {
		t.Errorf("classes = %+v", list)
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
	if added.Total != 3 || added.Items[2] != (dataset.Class{Name: "孔洞"}) || added.Version == list.Version {
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
	if cleared := want[classListResponse](t, e.do("PUT", base, map[string]any{"classes": []any{}}), 200); cleared.Total != 0 || cleared.Version != empty.Version {
		t.Errorf("cleared = %+v", cleared)
	}
	wantError(t, e.do("GET", "/api/v1/datasets/999999/classes", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("PUT", "/api/v1/datasets/999999/classes", map[string]any{"classes": []any{}}), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("POST", "/api/v1/datasets/999999/classes", map[string]any{"name": "a"}), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("DELETE", base, nil), 405, codeMethodNotAllowed, "")
}
