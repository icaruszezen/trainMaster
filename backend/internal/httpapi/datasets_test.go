package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// apiEnv is the HTTP handler over a real dataset.Service on a private
// database (skipped without TM_TEST_DATABASE_URL).
type apiEnv struct {
	h  http.Handler
	st *storage.Storage
}

func newAPIEnv(t *testing.T, start bool) *apiEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	svc := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		if err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); svc.Stop() })
	}
	h := NewHandler(Options{Logger: log, DB: fakeDB{}, Storage: st, StartedAt: time.Now(), Datasets: svc, Projects: projects})
	return &apiEnv{h: h, st: st}
}

// do sends a request; body may be nil, a string, []byte or any other
// value, which is sent as JSON.
func (e *apiEnv) do(method, target string, body any, header ...string) *httptest.ResponseRecorder {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	case []byte:
		r = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, r)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// want checks the status and decodes the JSON body into T.
func want[T any](t *testing.T, rec *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body)
	}
	return decode[T](t, rec)
}

// wantError checks an error envelope.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, msgPart string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d; body %s", rec.Code, status, rec.Body)
		return
	}
	body := decode[errorBody](t, rec)
	if body.Error.Code != code || !strings.Contains(body.Error.Message, msgPart) {
		t.Errorf("error = %+v, want code %s containing %q", body.Error, code, msgPart)
	}
}

// wantKeys checks that a JSON object has exactly the given keys.
func wantKeys(t *testing.T, what string, obj map[string]any, keys ...string) {
	t.Helper()
	got := slices.Sorted(maps.Keys(obj))
	slices.Sort(keys)
	if !slices.Equal(got, keys) {
		t.Errorf("%s keys = %v, want %v", what, got, keys)
	}
}

var (
	datasetKeys = []string{"id", "name", "description", "dir_name", "path", "origin", "directory_missing",
		"image_count", "annotated_count", "empty_count", "invalid_count", "object_count", "label_count", "total_bytes",
		"covers", "projects", "active_import", "scanned_at", "created_at", "updated_at"}
	importKeys = []string{"id", "dataset_id", "source", "status", "on_conflict", "annotation_format", "classes", "file_name", "file_size",
		"file_count", "received_bytes", "received_files", "stage", "progress", "summary", "error",
		"created_at", "updated_at", "started_at", "finished_at"}
	imageKeys = []string{"id", "dataset_id", "path", "file_name", "size_bytes", "width", "height",
		"mod_time", "version", "annotation_state", "annotation_path", "annotation"}
)

func (e *apiEnv) waitImport(t *testing.T, datasetID, importID int64) dataset.Import {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		job := want[dataset.Import](t, e.do("GET", fmt.Sprintf("/api/v1/datasets/%d/imports/%d", datasetID, importID), nil), 200)
		switch job.Status {
		case dataset.StatusSucceeded, dataset.StatusFailed, dataset.StatusCanceled:
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("import still %s after 15s", job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testVOC(filename string, labels ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<annotation><filename>%s</filename>", filename)
	for _, l := range labels {
		fmt.Fprintf(&b, "<object><name>%s</name><difficult>1</difficult><bndbox><xmin>1</xmin><ymin>2</ymin><xmax>30</xmax><ymax>40</ymax></bndbox></object>", l)
	}
	return b.String() + "</annotation>"
}

func testZip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, f[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDatasetAPIArchiveImport(t *testing.T) {
	e := newAPIEnv(t, true)

	// Create: unknown fields are fine, the response follows the contract.
	rec := e.do("POST", "/api/v1/datasets", map[string]any{"name": " 划痕-A ", "description": "产线 1", "extra": true})
	raw := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "dataset", raw, datasetKeys...)
	if raw["name"] != "划痕-A" || raw["dir_name"] != "划痕-A" || raw["origin"] != "upload" ||
		raw["active_import"] != nil || raw["scanned_at"] != nil {
		t.Errorf("created dataset = %v", raw)
	}
	if covers, ok := raw["covers"].([]any); !ok || len(covers) != 0 {
		t.Errorf("covers = %#v, want []", raw["covers"])
	}
	if projects, ok := raw["projects"].([]any); !ok || len(projects) != 0 {
		t.Errorf("projects = %#v, want []", raw["projects"])
	}
	id := int64(raw["id"].(float64))
	dsPath := filepath.Join(e.st.Dir(storage.Datasets), "划痕-A")
	if raw["path"] != dsPath {
		t.Errorf("path = %v, want %s", raw["path"], dsPath)
	}
	if fi, err := os.Stat(dsPath); err != nil || !fi.IsDir() {
		t.Fatalf("dataset folder: %v", err)
	}
	base := "/api/v1/datasets/" + strconv.FormatInt(id, 10)

	wantError(t, e.do("POST", "/api/v1/datasets", map[string]string{"name": "划痕-a"}), 409, codeConflict, "")
	wantError(t, e.do("POST", "/api/v1/datasets", map[string]string{"name": " "}), 422, codeValidationFailed, "名称不能为空")
	wantError(t, e.do("POST", "/api/v1/datasets", "{"), 400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("POST", "/api/v1/datasets", `{"name":"a"}{}`), 400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("POST", "/api/v1/datasets", `{"name":5}`), 400, codeBadRequest, "请求格式不正确")
	wantError(t, e.do("POST", "/api/v1/datasets", `{"name":"`+strings.Repeat("a", 2<<20)+`"}`), 413, codePayloadTooLarge, "")

	// The archive: images and VOC files in one top-level folder, with junk,
	// a non-image and a zip-slip entry.
	jpg, pngData := testJPEG(t, 64, 48), testPNG(t, 20, 10)
	archive := testZip(t, [][2]string{
		{"batch1/a.jpg", string(jpg)},
		{"batch1/a.xml", testVOC("a.jpg", "scratch", "dent")},
		{"batch1/sub/b.png", string(pngData)},
		{"batch1/sub/b.xml", testVOC("b.png")},
		{"batch1/c.jpg", "not really a jpeg"},
		{"batch1/notes.txt", "hello"},
		{"__MACOSX/batch1/._a.jpg", "junk"},
		{"batch1/Thumbs.db", "junk"},
		{"../evil.jpg", string(jpg)},
	})

	wantError(t, e.do("POST", base+"/imports", map[string]any{"source": "archive", "file_name": "batch1.zip"}), 422, codeValidationFailed, "")
	wantError(t, e.do("POST", base+"/imports", map[string]any{"source": "archive", "file_name": "x.zip", "file_size": int64(1) << 49}), 507, codeInsufficientStorage, "磁盘剩余空间不足")
	rec = e.do("POST", base+"/imports", map[string]any{"source": "archive", "file_name": "batch1.zip", "file_size": len(archive)})
	rawJob := want[map[string]any](t, rec, http.StatusCreated)
	wantKeys(t, "import", rawJob, importKeys...)
	if rawJob["status"] != "uploading" || rawJob["on_conflict"] != "skip" || rawJob["stage"] != "" {
		t.Errorf("created import = %v", rawJob)
	}
	if s, ok := rawJob["summary"].(map[string]any); !ok || len(s) != 0 {
		t.Errorf("summary = %#v, want {}", rawJob["summary"])
	}
	jobURL := fmt.Sprintf("%s/imports/%d", base, int64(rawJob["id"].(float64)))

	ds := want[dataset.Dataset](t, e.do("GET", base, nil), 200)
	if ds.ActiveImport == nil || ds.ActiveImport.Status != dataset.StatusUploading {
		t.Errorf("active_import = %+v", ds.ActiveImport)
	}

	half := len(archive) / 2
	wantError(t, e.do("PUT", jobURL+"/archive", archive[:half]), 400, codeBadRequest, "offset")
	wantError(t, e.do("PUT", jobURL+"/archive?offset=-1", archive[:half]), 400, codeBadRequest, "offset")
	job := want[dataset.Import](t, e.do("PUT", jobURL+"/archive?offset=0", archive[:half]), 200)
	if job.ReceivedBytes != int64(half) {
		t.Errorf("received_bytes = %d", job.ReceivedBytes)
	}
	wantError(t, e.do("PUT", jobURL+"/archive?offset=0", archive[:half]), 409, codeUploadOffsetMismatch, fmt.Sprintf("服务器已接收 %d 字节", half))
	wantError(t, e.do("POST", jobURL+"/commit", nil), 409, codeConflict, "尚未上传完成")
	wantError(t, e.do("POST", jobURL+"/files", nil), 400, codeBadRequest, "")
	want[dataset.Import](t, e.do("PUT", fmt.Sprintf("%s/archive?offset=%d", jobURL, half), archive[half:]), 200)
	job = want[dataset.Import](t, e.do("POST", jobURL+"/commit", nil), http.StatusAccepted)
	if job.Status != dataset.StatusQueued && job.Status != dataset.StatusRunning && job.Status != dataset.StatusSucceeded {
		t.Errorf("committed import = %+v", job)
	}

	job = e.waitImport(t, id, job.ID)
	if job.Status != dataset.StatusSucceeded || job.Error != "" || job.FinishedAt == nil {
		t.Fatalf("import = %+v", job)
	}
	if job.Progress.Total == 0 || job.Progress.Done != job.Progress.Total {
		t.Errorf("final progress = %+v", job.Progress)
	}
	for k, v := range map[string]int64{"images_added": 3, "annotations_matched": 2, "entries_unsafe": 1, "files_ignored": 3} {
		if job.Summary[k] != v {
			t.Errorf("summary[%s] = %d, want %d (%v)", k, job.Summary[k], v, job.Summary)
		}
	}
	for _, rel := range []string{"a.jpg", "a.xml", "sub/b.png", "sub/b.xml", "c.jpg"} {
		if _, err := os.Stat(filepath.Join(dsPath, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	for _, p := range []string{filepath.Join(dsPath, "notes.txt"), filepath.Join(dsPath, "batch1"), filepath.Join(e.st.Dir(storage.Datasets), "evil.jpg")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s should not exist", p)
		}
	}

	// Dataset aggregates.
	ds = want[dataset.Dataset](t, e.do("GET", base, nil), 200)
	if ds.ImageCount != 3 || ds.AnnotatedCount != 1 || ds.EmptyCount != 1 || ds.ObjectCount != 2 ||
		ds.LabelCount != 2 || ds.ActiveImport != nil || ds.ScannedAt == nil || len(ds.Covers) != 3 {
		t.Errorf("dataset after import = %+v", ds)
	}
	list := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?q="+url.QueryEscape("产线"), nil), 200)
	if list.Total != 1 || len(list.Items) != 1 {
		t.Errorf("search by description = %+v", list)
	}
	if list := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets?q=nothing", nil), 200); list.Total != 0 || list.Items == nil {
		t.Errorf("empty search = %+v", list)
	}

	// Images.
	rawList := want[map[string]any](t, e.do("GET", base+"/images", nil), 200)
	items := rawList["items"].([]any)
	if rawList["total"] != float64(3) || len(items) != 3 {
		t.Fatalf("images = %v", rawList)
	}
	wantKeys(t, "image", items[0].(map[string]any), imageKeys...)
	images := want[List[dataset.Image]](t, e.do("GET", base+"/images", nil), 200).Items
	if paths := []string{images[0].Path, images[1].Path, images[2].Path}; !slices.Equal(paths, []string{"a.jpg", "c.jpg", "sub/b.png"}) {
		t.Errorf("image order = %v", paths)
	}
	a := images[0]
	if a.Width == nil || *a.Width != 64 || *a.Height != 48 || a.Annotation == nil || a.Annotation.Path != "a.xml" ||
		a.Annotation.ObjectCount != 2 || len(a.Annotation.Labels) != 2 || a.Annotation.Labels[0].Name != "dent" {
		t.Errorf("a.jpg = %+v / %+v", a, a.Annotation)
	}
	if a.AnnotationState != "annotated" || a.AnnotationPath == nil || *a.AnnotationPath != "a.xml" {
		t.Errorf("a.jpg annotation state = %q, path %v", a.AnnotationState, a.AnnotationPath)
	}
	if c := images[1]; c.Width != nil || c.Annotation != nil || c.AnnotationState != "none" || c.AnnotationPath != nil {
		t.Errorf("c.jpg = %+v", c)
	}
	if raw := items[1].(map[string]any); raw["annotation_path"] != nil || raw["annotation_state"] != "none" {
		t.Errorf("c.jpg JSON = %v", raw)
	}
	for query, wantPaths := range map[string][]string{
		"status=annotated":            {"a.jpg"},
		"status=empty":                {"sub/b.png"},
		"status=unannotated":          {"c.jpg"},
		"status=invalid":              {},
		"label=scratch":               {"a.jpg"},
		"label=Scratch":               {},
		"q=SUB":                       {"sub/b.png"},
		"q=" + url.QueryEscape("%"):   {},
		"sort=-objects&limit=1":       {"a.jpg"},
		"limit=2&offset=2":            {"sub/b.png"},
		"status=all&sort=-mod_time&v": {"a.jpg", "c.jpg", "sub/b.png"},
	} {
		got := want[List[dataset.Image]](t, e.do("GET", base+"/images?"+query, nil), 200)
		var paths []string
		for _, img := range got.Items {
			paths = append(paths, img.Path)
		}
		slices.Sort(paths)
		if query == "sort=-objects&limit=1" || query == "limit=2&offset=2" {
			if got.Total != 3 {
				t.Errorf("%s: total = %d, want 3", query, got.Total)
			}
		}
		if !slices.Equal(paths, wantPaths) && !(len(paths) == 0 && len(wantPaths) == 0) {
			t.Errorf("%s: images %v, want %v", query, paths, wantPaths)
		}
	}
	for _, query := range []string{"limit=0", "limit=201", "offset=-1", "limit=x", "status=bogus", "sort=mod_time"} {
		wantError(t, e.do("GET", base+"/images?"+query, nil), 400, codeBadRequest, "参数")
	}

	imgURL := fmt.Sprintf("%s/images/%d", base, a.ID)
	rawDetail := want[map[string]any](t, e.do("GET", imgURL, nil), 200)
	wantKeys(t, "image detail", rawDetail, append(slices.Clone(imageKeys), "objects", "annotation_error")...)
	detail := want[dataset.ImageDetail](t, e.do("GET", imgURL, nil), 200)
	if len(detail.Objects) != 2 || detail.AnnotationError != "" || !detail.Objects[0].Difficult ||
		detail.Objects[0].Xmin != 1 || detail.Objects[0].Ymax != 40 {
		t.Errorf("detail = %+v", detail)
	}
	if d := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images/%d", base, images[1].ID), nil), 200); d["annotation_error"] != "" {
		t.Errorf("c.jpg detail = %v", d)
	} else if objs, ok := d["objects"].([]any); !ok || len(objs) != 0 {
		t.Errorf("objects = %#v, want []", d["objects"])
	}
	wantError(t, e.do("GET", base+"/images/99999", nil), 404, codeNotFound, "图片不存在")
	wantError(t, e.do("GET", base+"/images/abc", nil), 404, codeNotFound, "图片不存在")

	// The original file.
	rec = e.do("GET", imgURL+"/file", nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), jpg) {
		t.Fatalf("file: status %d, %d bytes", rec.Code, rec.Body.Len())
	}
	h := rec.Header()
	if h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "private, no-cache" ||
		h.Get("Content-Disposition") != `inline; filename="a.jpg"; filename*=UTF-8''a.jpg` ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Last-Modified") == "" {
		t.Errorf("file headers = %v", h)
	}
	rec = e.do("GET", imgURL+"/file?v="+a.Version, nil, "Range", "bytes=0-9")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 || rec.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Errorf("ranged file: status %d, %d bytes, headers %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	if rec := e.do("GET", imgURL+"/file?v=stale", nil); rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("a stale version must not be cached for good: %v", rec.Header())
	}
	if rec := e.do("GET", imgURL+"/file", nil, "If-Modified-Since", h.Get("Last-Modified")); rec.Code != http.StatusNotModified {
		t.Errorf("conditional request: status %d", rec.Code)
	}
	if rec := e.do("HEAD", imgURL+"/file", nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD file: status %d, %d bytes", rec.Code, rec.Body.Len())
	}

	// Thumbnails.
	for _, tc := range []struct {
		query string
		side  int
	}{{"", 64}, {"?size=sm&v=" + a.Version, 64}, {"?size=lg", 64}} {
		rec := e.do("GET", imgURL+"/thumbnail"+tc.query, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("thumbnail%s: status %d, %v, %s", tc.query, rec.Code, rec.Header(), rec.Body)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
		if err != nil || cfg.Width != tc.side {
			t.Errorf("thumbnail%s: %+v, %v", tc.query, cfg, err)
		}
		if strings.Contains(tc.query, "v=") != strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("thumbnail%s Cache-Control = %q", tc.query, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("thumbnail%s has Content-Disposition", tc.query)
		}
	}
	wantError(t, e.do("GET", imgURL+"/thumbnail?size=xl", nil), 400, codeBadRequest, "size")
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail", base, images[1].ID), nil), 422, codeUnsupportedImage, "")

	// Labels and jobs.
	labels := want[List[dataset.Label]](t, e.do("GET", base+"/labels", nil), 200)
	if labels.Total != 2 || labels.Items[0] != (dataset.Label{Name: "dent", ObjectCount: 1, ImageCount: 1}) {
		t.Errorf("labels = %+v", labels)
	}
	jobs := want[List[dataset.Import]](t, e.do("GET", base+"/imports?limit=1", nil), 200)
	if jobs.Total != 1 || len(jobs.Items) != 1 || jobs.Items[0].ID != job.ID {
		t.Errorf("imports = %+v", jobs)
	}
	wantError(t, e.do("GET", base+"/imports?limit=101", nil), 400, codeBadRequest, "limit")
	if c := want[dataset.Import](t, e.do("POST", jobURL+"/commit", nil), http.StatusAccepted); c.Status != dataset.StatusSucceeded {
		t.Errorf("commit of a finished import = %+v", c)
	}
	if c := want[dataset.Import](t, e.do("POST", jobURL+"/cancel", nil), 200); c.Status != dataset.StatusSucceeded {
		t.Errorf("cancel of a finished import = %+v", c)
	}

	// Rescan: nothing changed.
	scan := want[dataset.Import](t, e.do("POST", base+"/scan", nil), http.StatusAccepted)
	if scan.Source != dataset.SourceScan {
		t.Errorf("scan = %+v", scan)
	}
	if scan = e.waitImport(t, id, scan.ID); scan.Status != dataset.StatusSucceeded || scan.Summary["images_unchanged"] != 3 {
		t.Errorf("rescan = %+v", scan)
	}

	// Update.
	upd := want[dataset.Dataset](t, e.do("PATCH", base, map[string]any{"name": "划痕 B"}), 200)
	if upd.Name != "划痕 B" || upd.Description != "产线 1" || upd.DirName != "划痕-A" {
		t.Errorf("updated = %+v", upd)
	}
	wantError(t, e.do("PATCH", base, map[string]any{"description": strings.Repeat("长", 501)}), 422, codeValidationFailed, "500")
	wantError(t, e.do("PATCH", base, "[]"), 400, codeBadRequest, "")

	// Delete with files.
	wantError(t, e.do("DELETE", base+"?delete_files=maybe", nil), 400, codeBadRequest, "delete_files")
	if rec := e.do("DELETE", base+"?delete_files=true", nil); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete: status %d, %s", rec.Code, rec.Body)
	}
	if _, err := os.Lstat(dsPath); !os.IsNotExist(err) {
		t.Errorf("dataset folder remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.st.Dir(storage.Cache), "thumbs", strconv.FormatInt(id, 10))); !os.IsNotExist(err) {
		t.Errorf("thumbnails remain: %v", err)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "数据集不存在")
	wantError(t, e.do("GET", imgURL+"/file", nil), 404, codeNotFound, "数据集不存在")
}

// multipartBody builds a folder upload request body from parts; a part
// with a file name is a file field.
type formPart struct{ name, fileName, value string }

func multipartBody(t *testing.T, parts ...formPart) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.fileName != "" {
			w, err = mw.CreateFormFile(p.name, p.fileName)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, p.value)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func TestDatasetAPIFolderImport(t *testing.T) {
	e := newAPIEnv(t, true)
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]string{"name": "folder"}), 201)
	base := fmt.Sprintf("/api/v1/datasets/%d", ds.ID)
	job := want[dataset.Import](t, e.do("POST", base+"/imports", map[string]any{
		"source": "folder", "file_name": "line2", "file_size": 100, "file_count": 3, "on_conflict": "overwrite",
	}), 201)
	if job.OnConflict != dataset.ConflictOverwrite || job.FileCount != 3 {
		t.Errorf("created = %+v", job)
	}
	jobURL := fmt.Sprintf("%s/imports/%d", base, job.ID)
	jpg := string(testJPEG(t, 8, 8))

	// Path fields carry the folder structure; the part's file name does not.
	body, ct := multipartBody(t,
		formPart{name: "path", value: "day1/cam 1/a.jpg"},
		formPart{name: "file", fileName: "a.jpg", value: jpg},
		formPart{name: "comment", value: "ignored field"},
		formPart{name: "path", value: "day1/cam 1/a.xml"},
		formPart{name: "file", fileName: "a.xml", value: testVOC("a.jpg", "spot")},
		formPart{name: "path", value: "readme.md"},
		formPart{name: "file", fileName: "readme.md", value: "not stored"},
	)
	job = want[dataset.Import](t, e.do("POST", jobURL+"/files", body, "Content-Type", ct), 200)
	if job.ReceivedFiles != 2 || job.Status != dataset.StatusUploading {
		t.Errorf("after upload = %+v", job)
	}

	for name, parts := range map[string][]formPart{
		"file before path":  {{name: "file", fileName: "b.jpg", value: jpg}},
		"path without file": {{name: "path", value: "b.jpg"}},
		"path too long":     {{name: "path", value: strings.Repeat("a", 5000)}, {name: "file", fileName: "b.jpg", value: jpg}},
	} {
		body, ct := multipartBody(t, parts...)
		rec := e.do("POST", jobURL+"/files", body, "Content-Type", ct)
		if rec.Code != 400 {
			t.Errorf("%s: status %d, %s", name, rec.Code, rec.Body)
		}
	}
	body, ct = multipartBody(t, formPart{name: "path", value: "../up.jpg"}, formPart{name: "file", fileName: "up.jpg", value: jpg})
	wantError(t, e.do("POST", jobURL+"/files", body, "Content-Type", ct), 400, codeBadRequest, "文件路径无效")
	wantError(t, e.do("POST", jobURL+"/files", "plain", "Content-Type", "text/plain"), 400, codeBadRequest, "multipart")
	wantError(t, e.do("POST", jobURL+"/files", "--x\r\nbroken", "Content-Type", "multipart/form-data; boundary=x"), 400, codeBadRequest, "")
	wantError(t, e.do("PUT", jobURL+"/archive?offset=0", "x"), 400, codeBadRequest, "")

	// Declared sizes over the limits are refused before reading.
	req := httptest.NewRequest("POST", jobURL+"/files", strings.NewReader(""))
	req.ContentLength = dataset.MaxFolderRequestBytes + 1
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	wantError(t, rec, 413, codePayloadTooLarge, "256 MiB")

	job = want[dataset.Import](t, e.do("POST", jobURL+"/commit", nil), 202)
	body, ct = multipartBody(t, formPart{name: "path", value: "late.jpg"}, formPart{name: "file", fileName: "late.jpg", value: jpg})
	wantError(t, e.do("POST", jobURL+"/files", body, "Content-Type", ct), 409, codeConflict, "")
	job = e.waitImport(t, ds.ID, job.ID)
	if job.Status != dataset.StatusSucceeded || job.Summary["images_added"] != 1 || job.Summary["annotations_matched"] != 1 {
		t.Fatalf("import = %+v", job)
	}
	images := want[List[dataset.Image]](t, e.do("GET", base+"/images", nil), 200)
	if images.Total != 1 || images.Items[0].Path != "day1/cam 1/a.jpg" || images.Items[0].Annotation == nil {
		t.Errorf("images = %+v", images)
	}
	if rec := e.do("GET", fmt.Sprintf("%s/images/%d/file", base, images.Items[0].ID), nil); rec.Code != 200 {
		t.Errorf("file: status %d", rec.Code)
	}
}

func TestDatasetAPIDirectoriesAndJobs(t *testing.T) {
	e := newAPIEnv(t, false) // jobs stay queued
	root := e.st.Dir(storage.Datasets)
	for rel, data := range map[string]string{
		"existing/x/a.png":    string(testPNG(t, 4, 4)),
		"existing/x/a.xml":    testVOC("a.png", "k"),
		"existing/b.png":      string(testPNG(t, 4, 4)),
		".hidden/c.png":       "",
		"other/readme.txt":    "",
		"some-file-at-top.md": "",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "existing"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	rawDirs := want[map[string]any](t, e.do("GET", "/api/v1/datasets/directories", nil), 200)
	if rawDirs["total"] != float64(2) {
		t.Fatalf("directories = %v", rawDirs)
	}
	wantKeys(t, "directory", rawDirs["items"].([]any)[0].(map[string]any), "name", "path", "registered", "dataset_id",
		"image_count", "annotation_count", "total_bytes", "approximate", "modified_at")
	dirs := want[List[dataset.Directory]](t, e.do("GET", "/api/v1/datasets/directories", nil), 200).Items
	if dirs[0].Name != "existing" || dirs[0].ImageCount != 2 || dirs[0].AnnotationCount != 1 || dirs[0].Registered ||
		dirs[0].DatasetID != nil || dirs[0].Path != filepath.Join(root, "existing") || dirs[1].Name != "other" {
		t.Errorf("directories = %+v", dirs)
	}

	for i, tc := range []struct {
		dir    string
		status int
	}{{"missing", 404}, {".hidden", 404}, {"linked", 404}, {"some-file-at-top.md", 404}, {"../existing", 404}, {"x/..", 404}, {"", 422}} {
		dir, status := tc.dir, tc.status
		rec := e.do("POST", "/api/v1/datasets", map[string]any{"name": fmt.Sprintf("n%d", i), "directory": dir})
		if rec.Code != status {
			t.Errorf("directory %q: status %d, want %d (%s)", dir, rec.Code, status, rec.Body)
		}
	}
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "Existing data", "directory": "existing"}), 201)
	if ds.Origin != dataset.OriginDirectory || ds.DirName != "existing" || ds.ActiveImport == nil ||
		ds.ActiveImport.Source != dataset.SourceScan || ds.ActiveImport.Status != dataset.StatusQueued {
		t.Errorf("registered = %+v", ds)
	}
	wantError(t, e.do("POST", "/api/v1/datasets", map[string]any{"name": "again", "directory": "existing"}), 409, codeConflict, "Existing data")
	dirs = want[List[dataset.Directory]](t, e.do("GET", "/api/v1/datasets/directories", nil), 200).Items
	if dirs[0].Name != "other" || dirs[1].Name != "existing" || !dirs[1].Registered || dirs[1].DatasetID == nil || *dirs[1].DatasetID != ds.ID {
		t.Errorf("directories after register = %+v", dirs)
	}

	base := fmt.Sprintf("/api/v1/datasets/%d", ds.ID)
	wantError(t, e.do("POST", base+"/scan", nil), 409, codeConflict, "正在导入或扫描")
	wantError(t, e.do("DELETE", base, nil), 409, codeConflict, "请等待完成或取消后再删除")
	scanURL := fmt.Sprintf("%s/imports/%d", base, ds.ActiveImport.ID)
	if c := want[dataset.Import](t, e.do("POST", scanURL+"/cancel", nil), 200); c.Status != dataset.StatusCanceled {
		t.Errorf("cancel = %+v", c)
	}
	wantError(t, e.do("POST", scanURL+"/commit", nil), 409, codeConflict, "")
	wantError(t, e.do("GET", base+"/imports/77777", nil), 404, codeNotFound, "导入任务不存在")
	wantError(t, e.do("GET", base+"/imports/x", nil), 404, codeNotFound, "导入任务不存在")
	wantError(t, e.do("POST", base+"/imports/77777/cancel", nil), 404, codeNotFound, "导入任务不存在")
	wantError(t, e.do("GET", "/api/v1/datasets/88888/imports/"+strconv.FormatInt(ds.ActiveImport.ID, 10), nil), 404, codeNotFound, "数据集不存在")

	// Delete keeps the folder by default.
	if rec := e.do("DELETE", base, nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "existing", "b.png")); err != nil {
		t.Errorf("files must stay: %v", err)
	}
}

func TestDatasetAPIRouting(t *testing.T) {
	e := newAPIEnv(t, false)
	for _, target := range []string{"/api/v1/datasets/0", "/api/v1/datasets/-1", "/api/v1/datasets/1e3", "/api/v1/datasets/007x",
		"/api/v1/datasets/99999999999999999999", "/api/v1/datasets/42", "/api/v1/datasets/42/images"} {
		wantError(t, e.do("GET", target, nil), 404, codeNotFound, "数据集不存在")
	}
	wantError(t, e.do("GET", "/api/v1/datasets/1/bogus", nil), 404, codeNotFound, "")
	wantError(t, e.do("PUT", "/api/v1/datasets", nil), 405, codeMethodNotAllowed, "")
	if list := want[List[dataset.Dataset]](t, e.do("GET", "/api/v1/datasets", nil), 200); list.Total != 0 || list.Items == nil {
		t.Errorf("empty list = %+v", list)
	}
	if rec := e.do("GET", "/api/v1/datasets", nil); rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("JSON responses must not be cached")
	}
}

func TestDatasetAPIAnnotationFormats(t *testing.T) {
	e := newAPIEnv(t, true)
	ds := want[dataset.Dataset](t, e.do("POST", "/api/v1/datasets", map[string]string{"name": "yolo"}), 201)
	base := "/api/v1/datasets/" + strconv.FormatInt(ds.ID, 10)

	// Import a YOLO archive.
	pngData := testPNG(t, 40, 20)
	archive := testZip(t, [][2]string{
		{"images/a.png", string(pngData)}, {"labels/a.txt", "1 0.5 0.5 0.5 0.5\n"}, {"labels/classes.txt", "ok\nng\n"},
	})
	req := map[string]any{"source": "archive", "file_name": "a.zip", "file_size": len(archive), "annotation_format": "yolo"}
	wantError(t, e.do("POST", base+"/imports", req), 422, codeValidationFailed, "YOLO 格式需要提供类别顺序")
	req["classes"] = []string{"ok", "ok"}
	wantError(t, e.do("POST", base+"/imports", req), 422, codeValidationFailed, "类别“ok”重复")
	req["annotation_format"] = "coco"
	wantError(t, e.do("POST", base+"/imports", req), 422, codeValidationFailed, "标注格式必须是")
	req["annotation_format"], req["classes"] = "yolo", []string{"ok", "ng"}
	raw := want[map[string]any](t, e.do("POST", base+"/imports", req), 201)
	wantKeys(t, "import", raw, importKeys...)
	if raw["annotation_format"] != "yolo" || fmt.Sprint(raw["classes"]) != "[ok ng]" {
		t.Errorf("import = %v", raw)
	}
	importID := int64(raw["id"].(float64))
	imp := fmt.Sprintf("%s/imports/%d", base, importID)
	want[dataset.Import](t, e.do("PUT", imp+"/archive?offset=0", archive), 200)
	want[dataset.Import](t, e.do("POST", imp+"/commit", nil), 202)
	job := e.waitImport(t, ds.ID, importID)
	if job.Status != dataset.StatusSucceeded || job.Summary["annotations_converted"] != 1 || job.Summary["annotations_matched"] != 1 {
		t.Fatalf("job = %+v", job)
	}
	labels := want[List[dataset.Label]](t, e.do("GET", base+"/labels", nil), 200)
	if len(labels.Items) != 1 || labels.Items[0].Name != "ng" {
		t.Errorf("labels = %+v", labels)
	}

	// Download it as X-AnyLabeling.
	wantError(t, e.do("POST", base+"/exports", map[string]any{"format": "yolo"}), 422, codeValidationFailed, "需要提供类别顺序")
	wantError(t, e.do("POST", base+"/exports", map[string]any{"format": "coco"}), 422, codeValidationFailed, "导出格式必须是")
	wantError(t, e.do("POST", "/api/v1/datasets/999/exports", map[string]any{"format": "voc"}), 404, codeNotFound, "数据集不存在")
	raw = want[map[string]any](t, e.do("POST", base+"/exports", map[string]any{"format": "xanylabeling"}), 201)
	wantKeys(t, "export", raw, "url", "file_name", "expires_at")
	url, _ := raw["url"].(string)
	if !strings.HasPrefix(url, base+"/exports/") || raw["file_name"] != "yolo-xanylabeling.zip" {
		t.Errorf("export = %v", raw)
	}
	rec := e.do("GET", url, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="yolo-xanylabeling.zip"; filename*=UTF-8''yolo-xanylabeling.zip` ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"yolo/images/a.png", "yolo/images/a.json"}) {
		t.Errorf("zip entries = %v", names)
	}
	// The link can be used again; HEAD sends only the headers.
	if rec := e.do("HEAD", url, nil); rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("HEAD = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := e.do("GET", url, nil); rec.Code != 200 {
		t.Errorf("second download = %d", rec.Code)
	}
	wantError(t, e.do("GET", base+"/exports/AAAA", nil), 404, codeNotFound, "下载链接已失效")
	wantError(t, e.do("GET", base+"/exports/not-a-token", nil), 404, codeNotFound, "下载链接已失效")
	wantError(t, e.do("GET", strings.Replace(url, base, "/api/v1/datasets/999", 1), nil), 404, codeNotFound, "下载链接已失效")

	// A folder that disappeared after the link was made: JSON error, not a broken zip.
	dir := filepath.Join(e.st.Dir(storage.Datasets), "yolo")
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", url, nil), 409, codeConflict, "数据集文件夹不存在")
}

func TestInlineDisposition(t *testing.T) {
	for name, want := range map[string]string{
		"a.jpg":         `inline; filename="a.jpg"; filename*=UTF-8''a.jpg`,
		"划痕 1.png":      `inline; filename="__ 1.png"; filename*=UTF-8''%E5%88%92%E7%97%95%201.png`,
		`q"u\o;te.jpg`:  `inline; filename="q_u_o;te.jpg"; filename*=UTF-8''q%22u%5Co%3Bte.jpg`,
		"tab\there.jpg": `inline; filename="tab_here.jpg"; filename*=UTF-8''tab%09here.jpg`,
	} {
		if got := inlineDisposition(name); got != want {
			t.Errorf("inlineDisposition(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestLimitBody(t *testing.T) {
	r := httptest.NewRequest("PUT", "/", strings.NewReader("0123456789"))
	body := limitBody(httptest.NewRecorder(), r, 4, "too big")
	_, err := io.ReadAll(body)
	var de *dataset.Error
	if !errors.As(err, &de) || de.Kind != dataset.ErrTooLarge || de.Message != "too big" {
		t.Errorf("err = %v", err)
	}
	status, code := datasetErrorStatus(de.Kind)
	if status != 413 || code != codePayloadTooLarge {
		t.Errorf("status = %d %s", status, code)
	}
}
