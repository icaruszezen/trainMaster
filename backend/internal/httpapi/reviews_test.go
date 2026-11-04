package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

var (
	reviewKeys = []string{"id", "name", "description", "model", "model_exists", "project", "options", "status", "stage",
		"attempts", "node", "error", "error_detail", "result", "queue_position", "workers_online", "counts", "datasets",
		"last_upload", "active_merge", "last_merge", "created_at", "updated_at", "started_at", "finished_at"}
	reviewCountKeys = []string{"total", "inferred", "inference_failed", "pending", "approved", "needs_sample", "edited",
		"merged"}
	reviewDatasetKeys  = []string{"id", "name", "folder", "counts"}
	reviewModelKeys    = []string{"id", "project_id", "project_name", "version", "yolo_version", "task", "file_name", "class_names"}
	reviewOptionKeys   = []string{"conf", "iou", "imgsz", "max_det"}
	reviewImageKeyList = []string{"id", "review_id", "dataset_id", "image_id", "position", "path", "file_name",
		"package_path", "annotation_path", "width", "height", "image_version", "inferred", "inference_error",
		"object_count", "labels", "prediction_count", "edited", "status", "revision", "reviewed_at", "merged_at",
		"annotation_version"}
	reviewTaskKeys = []string{"kind", "id", "lease_seconds", "model", "options", "image_count", "remaining", "model_url",
		"images_url", "progress_url", "results_url", "complete_url", "fail_url"}
	reviewTaskModelKeys = []string{"id", "project_id", "version", "yolo_version", "task", "file_name", "size_bytes",
		"sha256", "class_names"}
)

// reviewEnv serves the dataset, project, model, compute and review APIs on
// a private database.
type reviewEnv struct {
	*apiEnv
	models  *model.Service
	reviews *review.Service
}

func newReviewEnv(t *testing.T, token string) *reviewEnv {
	t.Helper()
	return newReviewEnvWith(t, token, nil)
}

// newReviewEnvWith is newReviewEnv with options of the handler changed by
// adjust (when not nil).
func newReviewEnvWith(t *testing.T, token string, adjust func(o *Options)) *reviewEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	datasets := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	if err := datasets.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); datasets.Stop() })
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	e := &reviewEnv{}
	e.models = model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log, Inspect: fakeInspect})
	e.reviews = review.NewService(review.Options{DB: db.Pool, Storage: st, Logger: log, Datasets: datasets,
		Models: e.models, Projects: projects})
	o := Options{Logger: log, DB: fakeDB{}, Storage: st, StartedAt: time.Now(),
		Datasets: datasets, Projects: projects,
		Compute: compute.NewService(compute.Options{DB: db.Pool, Logger: log, HeartbeatInterval: 10 * time.Second}),
		Models:  e.models, Reviews: e.reviews, NodeToken: token}
	if adjust != nil {
		adjust(&o)
	}
	h := NewHandler(o)
	t.Cleanup(e.reviews.Stop) // waits for merges
	e.apiEnv = &apiEnv{h: h, st: st}
	return e
}

// dataset registers a folder of PNG images as a dataset and waits for its
// scan.
func (e *reviewEnv) dataset(t *testing.T, name string, files ...string) dataset.Dataset {
	t.Helper()
	dir := filepath.Join(e.st.Dir(storage.Datasets), name)
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, testPNG(t, 64, 48), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": name, "directory": name}), 201)
	if job := e.waitImport(t, ds.ID, ds.ActiveImport.ID); job.Status != dataset.StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	return ds
}

func (e *reviewEnv) project(t *testing.T, name string) project.Project {
	t.Helper()
	return want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]string{"name": name}), http.StatusCreated)
}

func (e *reviewEnv) uploadModel(t *testing.T, projectID int64, task string) (model.Model, []byte) {
	t.Helper()
	data := fakeWeights(t, "yolo11", task, task)
	up := want[model.Upload](t, e.do("POST", "/api/v1/models/uploads", map[string]any{"project_id": projectID,
		"yolo_version": "yolo11", "file_name": "best.pt", "file_size": len(data)}), http.StatusCreated)
	want[model.Upload](t, e.do("PUT", fmt.Sprintf("/api/v1/models/uploads/%d?offset=0", up.ID), data), http.StatusOK)
	return want[model.Model](t, e.do("POST", fmt.Sprintf("/api/v1/models/uploads/%d/commit", up.ID), nil),
		http.StatusCreated), data
}

func TestReviewAPIFlow(t *testing.T) {
	const token = "s3cr3t-node-token-0123"
	auth := []string{"Authorization", "Bearer " + token}
	e := newReviewEnv(t, token)
	p := e.project(t, "外观检测")
	m, weights := e.uploadModel(t, p.ID, "detect")
	cls, _ := e.uploadModel(t, p.ID, "classify")
	ds := e.dataset(t, "line-a", "a.png", "NG/b.png", "c.png")

	// Creation.
	for _, tt := range []struct {
		body   any
		status int
		code   string
		msg    string
	}{
		{map[string]any{}, 422, codeValidationFailed, "请至少选择一个数据集"},
		{map[string]any{"model_id": m.ID, "dataset_ids": []int64{ds.ID}, "options": map[string]any{"imgsz": "640"}},
			422, codeValidationFailed, "imgsz"},
		{map[string]any{"model_id": 999, "dataset_ids": []int64{ds.ID}}, 422, codeValidationFailed, "所选模型不存在"},
		{map[string]any{"model_id": cls.ID, "dataset_ids": []int64{ds.ID}}, 422, codeValidationFailed, "分类模型没有检测框"},
		{map[string]any{"model_id": m.ID, "dataset_ids": []int64{ds.ID}, "options": map[string]any{"conf": 2}},
			422, codeValidationFailed, "conf"},
		{`{"model_id": "x"}`, 400, codeBadRequest, "请求格式不正确"},
	} {
		wantError(t, e.do("POST", "/api/v1/reviews", tt.body), tt.status, tt.code, tt.msg)
	}
	raw := want[map[string]any](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}, "options": map[string]any{"conf": 0.3, "imgsz": []int{480, 640}}}), 201)
	wantKeys(t, "review", raw, reviewKeys...)
	wantKeys(t, "review model", object(t, raw["model"], "model"), reviewModelKeys...)
	wantKeys(t, "review options", object(t, raw["options"], "options"), reviewOptionKeys...)
	wantKeys(t, "review counts", object(t, raw["counts"], "counts"), reviewCountKeys...)
	dsRaw := raw["datasets"].([]any)
	wantKeys(t, "review dataset", object(t, dsRaw[0], "dataset"), reviewDatasetKeys...)
	if raw["name"] != "外观检测 v1 审图" || raw["status"] != "queued" || raw["queue_position"] != 1.0 ||
		raw["node"] != nil || raw["result"] != nil || raw["last_upload"] != nil || raw["active_merge"] != nil ||
		raw["model_exists"] != true || object(t, raw["project"], "project")["name"] != "外观检测" ||
		fmt.Sprint(object(t, raw["options"], "options")) != "map[conf:0.3 imgsz:[480 640] iou:0.7 max_det:300]" {
		t.Errorf("review = %v", raw)
	}
	id := int64(raw["id"].(float64))
	base := fmt.Sprintf("/api/v1/reviews/%d", id)

	// List, get, update.
	if l := want[List[review.Review]](t, e.do("GET", "/api/v1/reviews?status=queued&dataset_id="+fmt.Sprint(ds.ID), nil), 200); l.Total != 1 || l.Items[0].ID != id {
		t.Errorf("list = %+v", l)
	}
	if l := want[List[review.Review]](t, e.do("GET", "/api/v1/reviews?q=nothing", nil), 200); l.Total != 0 || l.Items == nil {
		t.Errorf("empty list = %+v", l)
	}
	wantError(t, e.do("GET", "/api/v1/reviews?status=done", nil), 400, codeBadRequest, "status")
	wantError(t, e.do("GET", "/api/v1/reviews?project_id=x", nil), 400, codeBadRequest, "project_id")
	wantError(t, e.do("GET", "/api/v1/reviews/abc", nil), 404, codeNotFound, "审图任务不存在")
	wantError(t, e.do("GET", "/api/v1/reviews/999", nil), 404, codeNotFound, "审图任务不存在")
	if rv := want[review.Review](t, e.do("PATCH", base, map[string]any{"name": "首轮审图", "description": "白班"}), 200); rv.Name != "首轮审图" || rv.Description != "白班" {
		t.Errorf("updated = %+v", rv)
	}
	wantError(t, e.do("PATCH", base, map[string]any{"name": ""}), 422, codeValidationFailed, "名称不能为空")
	wantError(t, e.do("POST", base+"/retry", nil), 409, codeConflict, "只有识别失败")

	// Before inference: images are listed, but cannot be marked.
	images := want[List[review.Image]](t, e.do("GET", base+"/images", nil), 200)
	if images.Total != 3 || images.Items[0].Path != "NG/b.png" || images.Items[0].PackagePath != "line-a/NG/b.png" {
		t.Fatalf("images = %+v", images)
	}
	first := images.Items[0]
	imgURL := fmt.Sprintf("%s/images/%d", base, first.ID)
	wantError(t, e.do("PUT", imgURL+"/status", map[string]any{"status": "approved"}), 409, codeConflict, "还没有识别完成")

	// The node claims the inference.
	nodeKey := []string{"X-Node-Key", "gpu-server-01"}
	agentHeader := append(slices.Clone(auth), nodeKey...)
	nodeID := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("gpu-server-01", "gpu-server-01", "null", "null"), auth...), 200).NodeID
	claim := want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/tasks/claim",
		`{"node_key": "gpu-server-01", "kinds": ["model_export", "review_inference"]}`, auth...), 200)
	task := object(t, claim["task"], "task")
	wantKeys(t, "review task", task, reviewTaskKeys...)
	wantKeys(t, "review task model", object(t, task["model"], "task model"), reviewTaskModelKeys...)
	agent := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d", id)
	if task["kind"] != "review_inference" || task["id"] != float64(id) || task["image_count"] != 3.0 ||
		task["remaining"] != 3.0 || task["model_url"] != agent+"/model" || task["results_url"] != agent+"/results" ||
		fmt.Sprint(object(t, task["model"], "model")["class_names"]) != "[scratch dent]" {
		t.Errorf("task = %v", task)
	}
	if rv := want[review.Review](t, e.do("GET", base, nil), 200); rv.Status != "running" || rv.Node == nil ||
		rv.Node.ID != nodeID || rv.WorkersOnline != 1 {
		t.Errorf("running = %+v", rv)
	}

	// Agent calls: token and node key are checked.
	wantError(t, e.do("GET", agent+"/images", nil, nodeKey...), 401, codeUnauthorized, "")
	wantError(t, e.do("GET", agent+"/images", nil, auth...), 400, codeBadRequest, "X-Node-Key")
	wantError(t, e.do("GET", agent+"/images", nil, append(slices.Clone(auth), "X-Node-Key", "nobody")...), 404,
		codeUnknownNode, "")
	wantError(t, e.do("GET", "/api/v1/compute/agent/review-inferences/999/images", nil, agentHeader...), 404,
		codeNotFound, "审图任务不存在")

	rec := e.do("GET", agent+"/model", nil, agentHeader...)
	if rec.Code != 200 || rec.Body.String() != string(weights) || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("model download = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	page := want[map[string]any](t, e.do("GET", agent+"/images?limit=2", nil, agentHeader...), 200)
	items := page["items"].([]any)
	wantKeys(t, "task image", object(t, items[0], "task image"), "id", "file_name", "width", "height", "size_bytes", "url")
	if len(items) != 2 || page["remaining"] != 3.0 {
		t.Errorf("page = %v", page)
	}
	wantError(t, e.do("GET", agent+"/images?limit=501", nil, agentHeader...), 400, codeBadRequest, "limit")
	wantError(t, e.do("GET", agent+"/images?after=-1", nil, agentHeader...), 400, codeBadRequest, "after")
	fileURL := object(t, items[0], "task image")["url"].(string)
	rec = e.do("GET", fileURL, nil, agentHeader...)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() == 0 {
		t.Errorf("image file = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	wantError(t, e.do("GET", agent+"/images/99999/file", nil, agentHeader...), 404, codeNotFound, "图片不存在")
	wantError(t, e.do("POST", agent+"/progress", map[string]any{"stage": "x"}, agentHeader...), 422, codeValidationFailed, "stage")
	if l := want[map[string]any](t, e.do("POST", agent+"/progress", map[string]any{"stage": "inferring",
		"message": "已识别 0 / 3"}, agentHeader...), 200); l["lease_seconds"] != 60.0 {
		t.Errorf("progress = %v", l)
	}

	// Results.
	var ids []int64
	for _, it := range items {
		ids = append(ids, int64(object(t, it, "image")["id"].(float64)))
	}
	receipt := want[map[string]any](t, e.do("POST", agent+"/results", map[string]any{"results": []any{
		map[string]any{"id": ids[0], "ok": true, "width": 64, "height": 48, "boxes": []any{
			map[string]any{"name": "scratch", "class_id": 0, "confidence": 0.91, "xmin": 1.2, "ymin": 2, "xmax": 30, "ymax": 99}}},
		map[string]any{"id": ids[1], "ok": false, "error": "无法读取图片"},
	}}, agentHeader...), 200)
	wantKeys(t, "receipt", receipt, "lease_seconds", "accepted", "remaining")
	if receipt["accepted"] != 2.0 || receipt["remaining"] != 1.0 {
		t.Errorf("receipt = %v", receipt)
	}
	wantError(t, e.do("POST", agent+"/results", map[string]any{"results": make([]map[string]any, 501)}, agentHeader...),
		422, codeValidationFailed, "500")
	wantError(t, e.do("POST", agent+"/complete", map[string]any{"result": map[string]any{}}, agentHeader...), 409,
		codeConflict, "还有 1 张图片没有识别")
	rest := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images?after=%d", agent, ids[1]), nil, agentHeader...), 200)
	lastID := int64(object(t, rest["items"].([]any)[0], "image")["id"].(float64))
	want[map[string]any](t, e.do("POST", agent+"/results", map[string]any{"results": []any{
		map[string]any{"id": lastID, "ok": true, "boxes": []any{
			map[string]any{"name": "dent", "confidence": 0.5, "xmin": 3, "ymin": 3, "xmax": 9, "ymax": 9}}}}}, agentHeader...), 200)
	done := want[map[string]any](t, e.do("POST", agent+"/complete", map[string]any{"result": map[string]any{
		"device": "cuda:0", "elapsed_seconds": 2.5}}, agentHeader...), 200)
	wantKeys(t, "review", done, reviewKeys...)
	if done["status"] != "ready" || object(t, done["result"], "result")["device"] != "cuda:0" {
		t.Errorf("done = %v", done)
	}
	wantError(t, e.do("POST", agent+"/fail", map[string]any{"error": "x"}, agentHeader...), 409, codeConflict, "不属于该节点")

	// The review images.
	rawImages := want[map[string]any](t, e.do("GET", base+"/images?limit=1", nil), 200)
	wantKeys(t, "review image", object(t, rawImages["items"].([]any)[0], "image"), reviewImageKeyList...)
	if rawImages["total"] != 3.0 {
		t.Errorf("total = %v", rawImages["total"])
	}
	for _, q := range []string{"status=x", "edited=x", "boxes=x", "inference=x", "limit=201", "offset=-1", "dataset_id=0"} {
		wantError(t, e.do("GET", base+"/images?"+q, nil), 400, codeBadRequest, "")
	}
	if l := want[List[review.Image]](t, e.do("GET", base+"/images?inference=failed", nil), 200); l.Total != 1 || l.Items[0].ID != ids[1] {
		t.Errorf("failed images = %+v", l)
	}
	detail := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images/%d", base, ids[0]), nil), 200)
	wantKeys(t, "review image detail", detail, append(slices.Clone(reviewImageKeyList), "objects", "predictions",
		"dataset_name", "annotation_error")...)
	preds := detail["predictions"].([]any)
	if len(preds) != 1 || fmt.Sprint(preds[0]) != "map[confidence:0.91 name:scratch xmax:30 xmin:1 ymax:48 ymin:2]" ||
		detail["dataset_name"] != "line-a" {
		t.Errorf("detail = %v", detail)
	}
	version := detail["annotation_version"].(string)
	n := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images/%d/neighbors?status=pending", base, ids[0]), nil), 200)
	wantKeys(t, "neighbors", n, "prev", "next", "position", "matches", "total")
	wantError(t, e.do("GET", base+"/images/99999", nil), 404, codeNotFound, "图片不存在")
	wantError(t, e.do("GET", base+"/images/abc", nil), 404, codeNotFound, "图片不存在")

	// Annotation and status.
	annURL := fmt.Sprintf("%s/images/%d/annotation", base, ids[0])
	wantError(t, e.do("PUT", annURL, map[string]any{"base_version": version}), 422, codeValidationFailed, "objects")
	wantError(t, e.do("PUT", annURL, map[string]any{"objects": []any{}}), 422, codeValidationFailed, "base_version")
	wantError(t, e.do("PUT", annURL, map[string]any{"objects": []any{}, "base_version": "old"}), 409,
		codeAnnotationConflict, "已被修改")
	wantError(t, e.do("PUT", annURL, map[string]any{"objects": []any{map[string]any{"name": "", "xmax": 3, "ymax": 3}},
		"base_version": version}), 422, codeValidationFailed, "没有类别")
	saved := want[review.ImageDetail](t, e.do("PUT", annURL, map[string]any{"base_version": version, "objects": []any{
		map[string]any{"name": "dent", "xmin": 1, "ymin": 1, "xmax": 20, "ymax": 20}}}), 200)
	if !saved.Edited || saved.Revision != 1 || saved.ObjectCount != 1 {
		t.Errorf("saved = %+v", saved)
	}
	statusURL := fmt.Sprintf("%s/images/%d/status", base, ids[0])
	wantError(t, e.do("PUT", statusURL, map[string]any{"status": "ok"}), 422, codeValidationFailed, "status")
	marked := want[review.ImageDetail](t, e.do("PUT", statusURL, map[string]any{"status": "needs_sample"}), 200)
	if marked.Status != "needs_sample" || marked.ReviewedAt == nil || marked.Revision != 2 {
		t.Errorf("marked = %+v", marked)
	}

	// Files, labels, classes.
	rec = e.do("GET", fmt.Sprintf("%s/images/%d/file?v=%s", base, ids[0], saved.ImageVersion), nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("image = %d %v", rec.Code, rec.Header())
	}
	rec = e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail?size=sm", base, ids[0]), nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("thumbnail = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail?size=xl", base, ids[0]), nil), 400, codeBadRequest, "size")
	labels := want[map[string]any](t, e.do("GET", base+"/labels", nil), 200)
	if fmt.Sprint(labels["items"]) != "[map[image_count:2 name:dent object_count:2]]" || labels["total"] != 1.0 {
		t.Errorf("labels = %v", labels)
	}
	classes := want[map[string]any](t, e.do("GET", base+"/classes", nil), 200)
	wantKeys(t, "classes", classes, "items", "total", "version")
	if fmt.Sprint(classes["items"]) != "[map[color: name:scratch] map[color: name:dent]]" || classes["version"] != "" {
		t.Errorf("classes = %v", classes)
	}
	if err := os.Remove(filepath.Join(e.st.Dir(storage.Datasets), "line-a", "c.png")); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/file", base, lastID), nil), 404, codeNotFound, "图片文件不存在")

	// Delete; a node still calling about it gets 404.
	second := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	claim = want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/tasks/claim",
		`{"kinds": ["review_inference"]}`, agentHeader...), 200)
	if object(t, claim["task"], "task")["id"] != float64(second.ID) {
		t.Fatalf("claim = %v", claim)
	}
	rec = e.do("DELETE", fmt.Sprintf("/api/v1/reviews/%d", second.ID), nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("POST", fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d/progress", second.ID),
		map[string]any{"stage": "inferring"}, agentHeader...), 404, codeNotFound, "审图任务不存在")
	wantError(t, e.do("DELETE", fmt.Sprintf("/api/v1/reviews/%d", second.ID), nil), 404, codeNotFound, "审图任务不存在")

	// An empty claim is {"task": null}.
	if rec := e.do("POST", "/api/v1/compute/agent/tasks/claim", `{"kinds": ["review_inference"]}`, agentHeader...); rec.Code != 200 ||
		strings.TrimSpace(rec.Body.String()) != `{"task":null}` {
		t.Errorf("empty claim = %d %s", rec.Code, rec.Body)
	}
}

func TestReviewAPIFailAndRetry(t *testing.T) {
	e := newReviewEnv(t, "")
	p := e.project(t, "p")
	m, _ := e.uploadModel(t, p.ID, "detect")
	ds := e.dataset(t, "ds", "a.png")
	rv := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("n1", "n1", "null", "null")), 200)
	node := []string{"X-Node-Key", "n1"}
	want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/tasks/claim", `{"kinds": ["review_inference"]}`, node...), 200)
	agent := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d", rv.ID)
	failed := want[review.Review](t, e.do("POST", agent+"/fail", map[string]any{"error": "显存不足", "detail": "CUDA out of memory"},
		node...), 200)
	if failed.Status != "failed" || failed.Error != "显存不足" || failed.ErrorDetail != "CUDA out of memory" {
		t.Errorf("failed = %+v", failed)
	}
	retried := want[review.Review](t, e.do("POST", fmt.Sprintf("/api/v1/reviews/%d/retry", rv.ID), nil), 202)
	if retried.Status != "queued" || retried.Attempts != 0 || retried.Error != "" {
		t.Errorf("retried = %+v", retried)
	}
	wantError(t, e.do("POST", "/api/v1/reviews/999/retry", nil), 404, codeNotFound, "审图任务不存在")
}
