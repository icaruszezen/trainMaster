package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

var (
	reviewUploadKeys        = []string{"id", "review_id", "file_name", "overwrite", "summary", "problems", "created_at"}
	reviewUploadSummaryKeys = []string{"images", "applied", "status_changes", "annotation_changes", "unchanged", "conflicts",
		"invalid", "unknown"}
	reviewMergeKeys = []string{"id", "review_id", "dataset_ids", "statuses", "on_existing", "note", "versions", "status",
		"total", "done", "summary", "problems", "error", "created_at", "finished_at"}
	reviewMergeSummaryKeys = []string{"merged", "skipped_existing", "skipped_empty", "skipped_changed", "conflicts", "failed"}
	datasetVersionKeys     = []string{"id", "dataset_id", "kind", "name", "major", "minor", "note", "status", "image_count",
		"needs_sample_count", "approved_count", "object_count", "review", "merge_id", "source", "created_at", "updated_at",
		"finished_at"}
)

// inferReview runs the inference of a review as a node would: one box on
// every image.
func (e *reviewEnv) inferReview(t *testing.T, reviewID int64) {
	t.Helper()
	want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("infer-node", "infer-node", "null", "null")), 200)
	node := []string{"X-Node-Key", "infer-node"}
	claim := want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/tasks/claim", `{"kinds": ["review_inference"]}`,
		node...), 200)
	if object(t, claim["task"], "task")["id"] != float64(reviewID) {
		t.Fatalf("claim = %v", claim)
	}
	agent := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d", reviewID)
	page := want[review.TaskImages](t, e.do("GET", agent+"/images", nil, node...), 200)
	var results []map[string]any
	for _, img := range page.Items {
		results = append(results, map[string]any{"id": img.ID, "ok": true, "boxes": []any{
			map[string]any{"name": "scratch", "confidence": 0.9, "xmin": 1, "ymin": 1, "xmax": 20, "ymax": 20}}})
	}
	want[map[string]any](t, e.do("POST", agent+"/results", map[string]any{"results": results}, node...), 200)
	want[map[string]any](t, e.do("POST", agent+"/complete", map[string]any{"result": map[string]any{}}, node...), 200)
}

// readZip returns the entries of a zip by name.
func readZip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

// uploadResults posts a zip as the raw body.
func (e *reviewEnv) uploadResults(target string, data []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", target, bytes.NewReader(data))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestReviewAPIExchange(t *testing.T) {
	e := newReviewEnv(t, "")
	p := e.project(t, "外观检测")
	m, _ := e.uploadModel(t, p.ID, "detect")
	ds := e.dataset(t, "line-a", "a.png", "NG/b.png")
	rv := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	base := fmt.Sprintf("/api/v1/reviews/%d", rv.ID)

	// Packages.
	wantError(t, e.do("POST", base+"/packages", nil), 409, codeConflict, "还没有识别完成的图片")
	e.inferReview(t, rv.ID)
	wantError(t, e.do("POST", base+"/packages", map[string]any{"statuses": []string{"ok"}}), 422, codeValidationFailed, "无效")
	wantError(t, e.do("POST", base+"/packages", map[string]any{"dataset_ids": []int64{999}}), 422, codeValidationFailed, "不在这个审图任务中")
	wantError(t, e.do("POST", base+"/packages", map[string]any{"statuses": []string{"approved"}}), 409, codeConflict, "没有符合条件")
	wantError(t, e.do("POST", "/api/v1/reviews/999/packages", nil), 404, codeNotFound, "审图任务不存在")
	wantError(t, e.do("POST", base+"/packages", `{"statuses": 1}`), 400, codeBadRequest, "")
	pkg := want[map[string]any](t, e.do("POST", base+"/packages", map[string]any{"statuses": []string{"pending"}}), 201)
	wantKeys(t, "package", pkg, "url", "file_name", "expires_at", "image_count")
	top := fmt.Sprintf("审图-%d-外观检测 v1 审图", rv.ID)
	url := pkg["url"].(string)
	if !strings.HasPrefix(url, base+"/packages/") || pkg["file_name"] != top+".zip" || pkg["image_count"] != 2.0 {
		t.Errorf("package = %v", pkg)
	}
	rec := e.do("GET", url, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
		t.Fatalf("package download = %d %v", rec.Code, rec.Header())
	}
	entries := readZip(t, rec.Body.Bytes())
	manifest, err := reviewfile.ReadManifest(bytes.NewReader(entries[top+"/review.json"]), 1<<20)
	if err != nil || len(manifest.Images) != 2 || len(entries) != 6 {
		t.Fatalf("package: %d entries, manifest %+v, %v", len(entries), manifest, err)
	}
	if rec := e.do("HEAD", url, nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	wantError(t, e.do("GET", base+"/packages/notatoken", nil), 404, codeNotFound, "下载链接已失效")
	wantError(t, e.do("GET", base+"/packages/ABCDEFGHIJKLMNOPQRSTUVWXYZ", nil), 404, codeNotFound, "下载链接已失效")
	wantError(t, e.do("GET", "/api/v1/reviews/999/packages/"+strings.TrimPrefix(url, base+"/packages/"), nil), 404,
		codeNotFound, "下载链接已失效")

	// Results: the first image approved locally.
	manifest.Kind = reviewfile.KindResults
	manifest.Images[0].Status = reviewfile.StatusApproved
	var mb bytes.Buffer
	if err := reviewfile.WriteManifest(&mb, manifest); err != nil {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create(top + "-审图结果/review.json")
	_, _ = w.Write(mb.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	results := zb.Bytes()
	up := want[map[string]any](t, e.uploadResults(base+"/results?file_name="+"审图结果.zip", results, "application/zip"), 200)
	wantKeys(t, "upload", up, reviewUploadKeys...)
	wantKeys(t, "upload summary", object(t, up["summary"], "summary"), reviewUploadSummaryKeys...)
	if fmt.Sprint(up["summary"]) != "map[annotation_changes:0 applied:1 conflicts:0 images:2 invalid:0 status_changes:1 unchanged:1 unknown:0]" ||
		up["file_name"] != "审图结果.zip" || up["overwrite"] != false || fmt.Sprint(up["problems"]) != "[]" {
		t.Errorf("upload = %v", up)
	}
	if img := want[review.ImageDetail](t, e.do("GET", fmt.Sprintf("%s/images/%d", base, manifest.Images[0].ID), nil), 200); img.Status != "approved" {
		t.Errorf("image after the upload = %+v", img)
	}
	up = want[map[string]any](t, e.uploadResults(base+"/results?overwrite=true", results, "application/octet-stream"), 200)
	if object(t, up["summary"], "summary")["unchanged"] != 2.0 || up["overwrite"] != true || up["file_name"] != "审图结果.zip" {
		t.Errorf("repeated upload = %v", up)
	}
	wantError(t, e.uploadResults(base+"/results?overwrite=maybe", results, "application/zip"), 400, codeBadRequest, "overwrite")
	wantError(t, e.uploadResults(base+"/results", results, "multipart/form-data; boundary=x"), 400, codeBadRequest, "不要用表单上传")
	wantError(t, e.uploadResults(base+"/results", []byte("PK? no"), "application/zip"), 422, codeValidationFailed, "不是有效的 zip")
	wantError(t, e.uploadResults(base+"/results", nil, "application/zip"), 422, codeValidationFailed, "上传的文件是空的")
	wantError(t, e.uploadResults("/api/v1/reviews/999/results", results, "application/zip"), 404, codeNotFound, "审图任务不存在")
	other := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	wantError(t, e.uploadResults(fmt.Sprintf("/api/v1/reviews/%d/results", other.ID), results, "application/zip"), 422,
		codeValidationFailed, fmt.Sprintf("这是审图任务 #%d 的审图结果，不是这个任务的", rv.ID))
	req := httptest.NewRequest("POST", base+"/results", io.NopCloser(bytes.NewReader(results)))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantError(t, rec, 400, codeBadRequest, "Content-Length")
	req = httptest.NewRequest("POST", base+"/results", bytes.NewReader(results))
	req.ContentLength = review.MaxResultsBytes + 1
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantError(t, rec, 413, codePayloadTooLarge, "4 GiB")
	uploads := want[map[string]any](t, e.do("GET", base+"/uploads", nil), 200)
	if uploads["total"] != 2.0 {
		t.Errorf("uploads = %v", uploads)
	}
	wantKeys(t, "listed upload", object(t, uploads["items"].([]any)[0], "upload"), reviewUploadKeys...)
	wantError(t, e.do("GET", "/api/v1/reviews/999/uploads", nil), 404, codeNotFound, "审图任务不存在")

	// Merges.
	xmlPath := filepath.Join(e.st.Dir(storage.Datasets), "line-a", strings.TrimSuffix(manifest.Images[0].SourcePath, ".png")+".xml")
	wantError(t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{}, "note": "x"}), 422,
		codeValidationFailed, "至少选择")
	wantError(t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{"approved"}}), 422,
		codeValidationFailed, "请填写版本备注")
	wantError(t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{"approved"}, "note": " \n "}), 422,
		codeValidationFailed, "请填写版本备注")
	wantError(t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{"approved"}, "note": strings.Repeat("备", 501)}),
		422, codeValidationFailed, "500")
	wantError(t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{"needs_sample"}, "note": "x"}), 409,
		codeConflict, "没有符合条件")
	wantError(t, e.do("POST", fmt.Sprintf("/api/v1/reviews/%d/merges", other.ID),
		map[string]any{"statuses": []string{"pending"}, "note": "x"}), 409, codeConflict, "还没有识别完成")
	wantError(t, e.do("POST", "/api/v1/reviews/999/merges", map[string]any{"statuses": []string{"approved"},
		"note": "x"}), 404, codeNotFound, "审图任务不存在")
	if rv := want[map[string]any](t, e.do("GET", base, nil), 200); object(t, rv["datasets"].([]any)[0], "dataset")["next_version"] != "审图1.0" {
		t.Errorf("review before merging = %v", rv)
	}
	started := want[map[string]any](t, e.do("POST", base+"/merges", map[string]any{"statuses": []string{"approved"},
		"note": " 第一轮复核 ", "dataset_ids": []int64{ds.ID}}), 202)
	wantKeys(t, "merge", started, reviewMergeKeys...)
	wantKeys(t, "merge summary", object(t, started["summary"], "summary"), reviewMergeSummaryKeys...)
	if started["status"] != "running" || started["total"] != 1.0 || fmt.Sprint(started["dataset_ids"]) != fmt.Sprintf("[%d]", ds.ID) ||
		fmt.Sprint(started["statuses"]) != "[approved]" || started["finished_at"] != nil || started["on_existing"] != nil ||
		started["note"] != "第一轮复核" {
		t.Errorf("started merge = %v", started)
	}
	mergeURL := fmt.Sprintf("%s/merges/%d", base, int64(started["id"].(float64)))
	var done review.Merge
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if done = want[review.Merge](t, e.do("GET", mergeURL, nil), 200); done.Status != review.MergeRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the merge did not finish")
		}
	}
	if done.Status != review.MergeSucceeded || done.Summary.Merged != 1 || done.Done != 1 || len(done.Versions) != 1 ||
		done.Versions[0].Name != "审图1.0" || done.Versions[0].DatasetID != ds.ID || done.Versions[0].ImageCount != 1 {
		t.Errorf("merge = %+v", done)
	}
	merged := want[map[string]any](t, e.do("GET", mergeURL, nil), 200)
	wantKeys(t, "merge version", object(t, merged["versions"].([]any)[0], "version"), "id", "dataset_id", "name", "status",
		"image_count")
	// The dataset's files are not touched: the merge made a version.
	if _, err := os.Stat(xmlPath); !os.IsNotExist(err) {
		t.Errorf("merge wrote the dataset annotation: %v", err)
	}
	if rv := want[map[string]any](t, e.do("GET", base, nil), 200); object(t, rv["datasets"].([]any)[0], "dataset")["next_version"] != "审图1.1" {
		t.Errorf("review after merging = %v", rv)
	}

	// The version on the dataset API.
	dsBase := fmt.Sprintf("/api/v1/datasets/%d", ds.ID)
	versions := want[map[string]any](t, e.do("GET", dsBase+"/versions", nil), 200)
	if versions["total"] != 1.0 || len(versions["items"].([]any)) != 1 {
		t.Fatalf("versions = %v", versions)
	}
	ver := object(t, versions["items"].([]any)[0], "version")
	wantKeys(t, "version", ver, datasetVersionKeys...)
	wantKeys(t, "version source", object(t, ver["source"], "source"), "review_id", "review_name", "merge_id", "statuses", "model")
	if ver["name"] != "审图1.0" || ver["note"] != "第一轮复核" || ver["status"] != "ready" || ver["approved_count"] != 1.0 ||
		ver["image_count"] != 1.0 || object(t, ver["review"], "review")["id"] != float64(rv.ID) || ver["merge_id"] != started["id"] {
		t.Errorf("version = %v", ver)
	}
	versionID := fmt.Sprint(int64(ver["id"].(float64)))
	verURL := dsBase + "/versions/" + versionID
	if got := want[map[string]any](t, e.do("GET", verURL, nil), 200); got["id"] != ver["id"] {
		t.Errorf("GET version = %v", got)
	}
	if got := want[map[string]any](t, e.do("PATCH", verURL, map[string]any{"note": " 复核后 "}), 200); got["note"] != "复核后" {
		t.Errorf("PATCH version = %v", got)
	}
	wantError(t, e.do("PATCH", verURL, map[string]any{"note": ""}), 422, codeValidationFailed, "请填写版本备注")
	wantError(t, e.do("PATCH", verURL, `{"note": 1}`), 400, codeBadRequest, "")
	wantError(t, e.do("GET", dsBase+"/versions/999", nil), 404, codeNotFound, "版本不存在")
	wantError(t, e.do("GET", dsBase+"/versions/x", nil), 404, codeNotFound, "版本不存在")
	wantError(t, e.do("GET", "/api/v1/datasets/999/versions", nil), 404, codeNotFound, "数据集不存在")
	if d := want[map[string]any](t, e.do("GET", dsBase, nil), 200); d["version_count"] != 1.0 {
		t.Errorf("dataset = %v", d)
	}

	// What a download of it contains, and the download.
	preview := want[map[string]any](t, e.do("GET", dsBase+"/exports/preview?version_ids="+versionID, nil), 200)
	wantKeys(t, "export preview", preview, "image_count", "from_versions", "annotated_count", "empty_count", "invalid_count",
		"unannotated_count", "object_count", "labels", "version_image_count", "missing_count", "changed_count")
	if preview["image_count"] != 2.0 || preview["from_versions"] != 1.0 || preview["object_count"] != 1.0 ||
		fmt.Sprint(preview["labels"]) != "[map[image_count:1 name:scratch object_count:1]]" {
		t.Errorf("preview = %v", preview)
	}
	// Without the base only needs_sample images: the image was approved.
	if p := want[map[string]any](t, e.do("GET", dsBase+"/exports/preview?include_base=false&version_ids="+versionID, nil), 200); p["image_count"] != 0.0 || p["version_image_count"] != 0.0 {
		t.Errorf("preview without the base = %v", p)
	}
	if p := want[map[string]any](t, e.do("GET", dsBase+"/exports/preview", nil), 200); p["image_count"] != 2.0 || p["from_versions"] != 0.0 {
		t.Errorf("preview of the base = %v", p)
	}
	wantError(t, e.do("GET", dsBase+"/exports/preview?include_base=maybe", nil), 400, codeBadRequest, "include_base")
	wantError(t, e.do("GET", dsBase+"/exports/preview?version_ids=1,x", nil), 400, codeBadRequest, "version_ids")
	wantError(t, e.do("GET", dsBase+"/exports/preview?include_base=false", nil), 422, codeValidationFailed, "请至少选择")
	wantError(t, e.do("GET", dsBase+"/exports/preview?version_ids=999", nil), 404, codeNotFound, "所选的版本不存在")
	wantError(t, e.do("GET", "/api/v1/datasets/999/exports/preview", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("POST", dsBase+"/exports", map[string]any{"format": "voc", "include_base": false,
		"version_ids": []string{versionID}}), 400, codeBadRequest, "")
	wantError(t, e.do("POST", dsBase+"/exports", map[string]any{"format": "voc", "include_base": false,
		"version_ids": []any{ver["id"]}}), 409, codeConflict, "所选版本中没有可以导出的图片")
	wantError(t, e.do("POST", dsBase+"/exports", map[string]any{"format": "voc", "include_base": false}), 422,
		codeValidationFailed, "请至少选择")
	ticket := want[map[string]any](t, e.do("POST", dsBase+"/exports", map[string]any{"format": "voc",
		"version_ids": []any{ver["id"]}}), 201)
	if ticket["file_name"] != "line-a-基础+审图1.0-voc.zip" {
		t.Errorf("export = %v", ticket)
	}
	rec = e.do("GET", ticket["url"].(string), nil)
	if rec.Code != 200 {
		t.Fatalf("download = %d %s", rec.Code, rec.Body)
	}
	files := readZip(t, rec.Body.Bytes())
	mergedXML := "line-a-基础+审图1.0/" + strings.TrimSuffix(manifest.Images[0].SourcePath, ".png") + ".xml"
	if !strings.Contains(string(files[mergedXML]), "<name>scratch</name>") || len(files) != 3 {
		t.Errorf("download: %d files, %s = %s", len(files), mergedXML, files[mergedXML])
	}
	if plain := want[map[string]any](t, e.do("POST", dsBase+"/exports", map[string]any{"format": "voc", "include_base": true,
		"version_ids": []any{}}), 201); plain["file_name"] != "line-a-voc.zip" {
		t.Errorf("export of the base = %v", plain)
	}
	if rec := e.do("DELETE", verURL, nil); rec.Code != 204 {
		t.Errorf("DELETE version = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("DELETE", verURL, nil), 404, codeNotFound, "版本不存在")
	wantError(t, e.do("GET", ticket["url"].(string), nil), 404, codeNotFound, "所选的版本已被删除")
	merges := want[map[string]any](t, e.do("GET", base+"/merges", nil), 200)
	if merges["total"] != 1.0 {
		t.Errorf("merges = %v", merges)
	}
	wantError(t, e.do("GET", base+"/merges/999", nil), 404, codeNotFound, "合并记录不存在")
	wantError(t, e.do("GET", base+"/merges/x", nil), 404, codeNotFound, "合并记录不存在")
	wantError(t, e.do("GET", "/api/v1/reviews/999/merges", nil), 404, codeNotFound, "审图任务不存在")
	if rv := want[map[string]any](t, e.do("GET", base, nil), 200); object(t, rv["last_merge"], "last_merge")["id"] != started["id"] ||
		object(t, rv["last_upload"], "last_upload")["overwrite"] != true || rv["active_merge"] != nil {
		t.Errorf("review = %v", rv)
	}

	// The review records of the dataset.
	recs := want[map[string]any](t, e.do("GET", fmt.Sprintf("/api/v1/datasets/%d/reviews", ds.ID), nil), 200)
	items := recs["items"].([]any)
	if recs["total"] != 2.0 || len(items) != 2 {
		t.Fatalf("dataset reviews = %v", recs)
	}
	rec0, rec1 := object(t, items[0], "dataset review"), object(t, items[1], "dataset review")
	wantKeys(t, "dataset review", rec1, append(slices.Clone(reviewKeys), "dataset", "dataset_merges")...)
	wantKeys(t, "dataset review dataset", object(t, rec1["dataset"], "dataset"), reviewDatasetKeys...)
	if rec0["id"] != float64(other.ID) || fmt.Sprint(rec0["dataset_merges"]) != "[]" || rec1["id"] != float64(rv.ID) ||
		len(rec1["dataset_merges"].([]any)) != 1 || object(t, rec1["dataset"], "dataset")["id"] != float64(ds.ID) {
		t.Errorf("dataset reviews = %v", recs)
	}
	wantKeys(t, "dataset merge", object(t, rec1["dataset_merges"].([]any)[0], "merge"), reviewMergeKeys...)
	wantError(t, e.do("GET", "/api/v1/datasets/999/reviews", nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("GET", "/api/v1/datasets/x/reviews", nil), 404, codeNotFound, "")
}

func TestReviewAPITool(t *testing.T) {
	// Not bundled.
	e := newReviewEnv(t, "")
	tool := want[map[string]any](t, e.do("GET", "/api/v1/reviews/tool", nil), 200)
	wantKeys(t, "tool", tool, "available", "file_name", "size_bytes", "version")
	if fmt.Sprint(tool) != "map[available:false file_name:trainmaster-reviewer.exe size_bytes:0 version:]" {
		t.Errorf("tool = %v", tool)
	}
	wantError(t, e.do("GET", "/api/v1/reviews/tool/download", nil), 404, codeNotFound, "平台没有附带本地审图程序")

	// Configured, but the file is not there (yet).
	exe := filepath.Join(t.TempDir(), "tools", "trainmaster-reviewer.exe")
	e = newReviewEnvWith(t, "", func(o *Options) { o.ReviewerTool = exe })
	if tool := want[map[string]any](t, e.do("GET", "/api/v1/reviews/tool", nil), 200); tool["available"] != false {
		t.Errorf("tool without its file = %v", tool)
	}
	wantError(t, e.do("GET", "/api/v1/reviews/tool/download", nil), 404, codeNotFound, "TM_REVIEWER_TOOL")
	if err := os.MkdirAll(exe, 0o755); err != nil { // a folder is no tool either
		t.Fatal(err)
	}
	if tool := want[map[string]any](t, e.do("GET", "/api/v1/reviews/tool", nil), 200); tool["available"] != false {
		t.Errorf("tool that is a folder = %v", tool)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	// Bundled.
	content := []byte("MZ" + strings.Repeat("x", 1000))
	if err := os.WriteFile(exe, content, 0o755); err != nil {
		t.Fatal(err)
	}
	tool = want[map[string]any](t, e.do("GET", "/api/v1/reviews/tool", nil), 200)
	if tool["available"] != true || tool["size_bytes"] != 1002.0 || tool["version"] == "" {
		t.Errorf("tool = %v", tool)
	}
	rec := e.do("GET", "/api/v1/reviews/tool/download", nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), content) || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="trainmaster-reviewer.exe"`) {
		t.Errorf("download = %d %v", rec.Code, rec.Header())
	}
	rec = e.do("GET", "/api/v1/reviews/tool/download", nil, "Range", "bytes=0-1")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "MZ" {
		t.Errorf("range download = %d %q", rec.Code, rec.Body.String())
	}
	// The tool download is read-only.
	wantError(t, e.do("POST", "/api/v1/reviews/tool/download", nil), 405, codeMethodNotAllowed, "")
}

// TestReviewAPIHistoryTotals checks that the upload and merge lists give
// the number of all records, not only of the newest 50 they hold.
func TestReviewAPIHistoryTotals(t *testing.T) {
	e := newReviewEnv(t, "")
	ctx := t.Context()
	p := e.project(t, "外观检测")
	m, _ := e.uploadModel(t, p.ID, "detect")
	ds := e.dataset(t, "line-a", "a.png")
	rv := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	base := fmt.Sprintf("/api/v1/reviews/%d", rv.ID)
	e.inferReview(t, rv.ID)
	pkg := want[map[string]any](t, e.do("POST", base+"/packages", nil), 201)
	entries := readZip(t, e.do("GET", pkg["url"].(string), nil).Body.Bytes())
	top := fmt.Sprintf("审图-%d-外观检测 v1 审图", rv.ID)
	manifest, err := reviewfile.ReadManifest(bytes.NewReader(entries[top+"/review.json"]), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Kind = reviewfile.KindResults
	manifest.Images[0].Status = reviewfile.StatusApproved
	var mb bytes.Buffer
	if err := reviewfile.WriteManifest(&mb, manifest); err != nil {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create(top + "-审图结果/review.json")
	_, _ = w.Write(mb.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for range 51 {
		if _, err := e.reviews.UploadResults(ctx, rv.ID, review.ResultsInput{FileName: "结果.zip", Size: int64(zb.Len()),
			Body: bytes.NewReader(zb.Bytes())}); err != nil {
			t.Fatal(err)
		}
	}
	for range 51 {
		started, err := e.reviews.StartMerge(ctx, rv.ID, review.MergeInput{Statuses: []string{"approved"}, Note: "复核"})
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			if m, err := e.reviews.GetMerge(ctx, rv.ID, started.ID); err != nil || m.Status != review.MergeRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the merge did not finish")
			}
		}
	}
	for _, path := range []string{"/uploads", "/merges"} {
		list := want[map[string]any](t, e.do("GET", base+path, nil), 200)
		if list["total"] != 51.0 || len(list["items"].([]any)) != 50 {
			t.Errorf("%s: total %v, %d items", path, list["total"], len(list["items"].([]any)))
		}
	}
}
