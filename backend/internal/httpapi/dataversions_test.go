package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// dataVersionEnv is the HTTP handler with every service a data version
// touches. In newDataVersionEnv the services are not started (no import
// runner, no builder): the versions of those tests are inserted as a
// finished build leaves them. startedDataVersionEnv runs real builds.
type dataVersionEnv struct {
	*apiEnv
	db       *database.DB
	versions *dataversion.Service
	models   *model.Service
}

func newDataVersionEnv(t *testing.T) *dataVersionEnv {
	t.Helper()
	return newDataVersionEnvWith(t, false)
}

func newDataVersionEnvWith(t *testing.T, start bool) *dataVersionEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	datasets := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log, Inspect: fakeInspect})
	versions := dataversion.NewService(dataversion.Options{DB: db.Pool, Storage: st, Logger: log, Datasets: datasets,
		Projects: projects})
	t.Cleanup(versions.WaitBackground)
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		if err := datasets.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); datasets.Stop() })
		if err := versions.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(versions.Stop)
	}
	h := NewHandler(Options{Logger: log, DB: fakeDB{}, Storage: st, StartedAt: time.Now(), Datasets: datasets,
		Projects: projects, Models: models, DataVersions: versions,
		Compute: compute.NewService(compute.Options{DB: db.Pool, Logger: log, HeartbeatInterval: 10 * time.Second})})
	return &dataVersionEnv{apiEnv: &apiEnv{h: h, st: st}, db: db, versions: versions, models: models}
}

// classProject creates a project with a class list through the API.
func (e *dataVersionEnv) classProject(t *testing.T, name string, classes ...string) project.Project {
	t.Helper()
	p := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": name}), http.StatusCreated)
	list := make([]map[string]string, len(classes))
	for i, c := range classes {
		list[i] = map[string]string{"name": c, "color": ""}
	}
	want[map[string]any](t, e.do("PUT", fmt.Sprintf("/api/v1/projects/%d/classes", p.ID), map[string]any{"classes": list}),
		http.StatusOK)
	return p
}

// testVersionImage is an image of a version inserted by insertDataVersion.
type testVersionImage struct {
	path      string // in the version folder
	split     string
	objects   []dataset.AnnotationObject
	data      []byte // nil: a 32×24 JPEG
	augmented bool   // an augmented copy of the image before it
}

// testDataVersion is an inserted version.
type testDataVersion struct {
	id       int64
	dir      string
	imageIDs []int64
}

// insertDataVersion inserts a version as a finished build leaves it (its
// files in the version folder, its rows; migration 0013).
func (e *dataVersionEnv) insertDataVersion(t *testing.T, projectID int64, version int, status string,
	images ...testVersionImage) testDataVersion {
	t.Helper()
	ctx := context.Background()
	dirName := fmt.Sprintf("data-versions/project-%d/v%d", projectID, version)
	v := testDataVersion{dir: filepath.Join(e.st.Dir(storage.Exports), filepath.FromSlash(dirName))}
	classes := []dataversion.Class{{Name: "scratch", Color: "#ef4444"}, {Name: "dent", Color: ""}}
	sources := []dataversion.Source{{DatasetID: 1, DatasetName: "产线A", DirName: "line_a", IncludeBase: true,
		Versions: []dataversion.SourceVersion{{ID: 5, Name: "审图1.2"}}}}
	config := dataversion.Config{Split: dataversion.SplitConfig{Train: 80, Val: 20, Seed: 42, GroupBy: "stem"},
		IgnoreLabels: []string{}}
	var finished *time.Time
	if status == dataversion.StatusReady || status == dataversion.StatusFailed {
		now := time.Now()
		finished = &now
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO data_versions (project_id, version, dir_name, note, status, classes,
			config, sources, image_count, finished_at)
		VALUES ($1, $2, $3, '第一版', $4, $5, $6, $7, $8, $9) RETURNING id`,
		projectID, version, dirName, status, classes, config, sources, len(images), finished).Scan(&v.id); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(v.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, img := range images {
		data := img.data
		if data == nil {
			data = testJPEG(t, 32, 24)
		}
		file := filepath.Join(v.dir, filepath.FromSlash(img.path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if img.objects == nil {
			img.objects = []dataset.AnnotationObject{}
		}
		labels := map[string]int{}
		for _, o := range img.objects {
			labels[o.Name]++
		}
		var from *int64
		stored, augment := "link", []string{}
		if img.augmented {
			from, stored, augment = &v.imageIDs[i-1], "generated", []string{"hflip"}
		}
		var id int64
		if err := e.db.QueryRow(ctx, `INSERT INTO data_version_images (version_id, position, split, split_by, path,
				size_bytes, mod_time, width, height, objects, object_count, labels, dataset_id, source_path, source_size,
				source_mod_time, annotation_source, augmented_from, augment, stored)
			VALUES ($1, $2, $3, 'ratio', $4, $5, $6, 32, 24, $7, $8, $9, 1, $10, $5, $6, 5, $11, $12, $13) RETURNING id`,
			v.id, i, img.split, img.path, fi.Size(), fi.ModTime(), img.objects, len(img.objects), labels,
			strings.TrimPrefix(img.path, "images/line_a/"), from, augment, stored).Scan(&id); err != nil {
			t.Fatal(err)
		}
		v.imageIDs = append(v.imageIDs, id)
	}
	return v
}

// trashDataVersion moves a ready version to the recycle bin as Delete
// does.
func (e *dataVersionEnv) trashDataVersion(t *testing.T, id int64) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(), "UPDATE data_versions SET deleted_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
}

var (
	dataVersionKeys = []string{"id", "project_id", "project_name", "version", "note", "status", "error", "progress",
		"queue_position", "classes", "class_version", "config", "sources", "image_count", "object_count", "train_count",
		"val_count", "test_count", "augmented_count", "total_bytes", "stats", "created_at", "updated_at", "started_at",
		"finished_at"}
	dataVersionImageKeys = []string{"id", "position", "split", "split_by", "path", "file_name", "width", "height",
		"size_bytes", "object_count", "labels", "dataset_id", "dataset_name", "source_path", "annotation_source",
		"augmented_from", "augment", "stored", "thumbnail_url", "file_url"}
)

func TestDataVersionAPIRouting(t *testing.T) {
	e := newDataVersionEnv(t)
	const base = "/api/v1/data-versions"
	for _, id := range []string{"0", "-1", "1e3", "007x", "123456789012345678901", "preview"} {
		wantError(t, e.do("GET", base+"/"+id, nil), 404, codeNotFound, "数据版本不存在")
		wantError(t, e.do("GET", base+"/"+id+"/images", nil), 404, codeNotFound, "数据版本不存在")
	}
	for _, id := range []string{"0", "x"} {
		wantError(t, e.do("GET", base+"/1/images/"+id, nil), 404, codeNotFound, "图片不存在")
		wantError(t, e.do("GET", base+"/1/images/"+id+"/file", nil), 404, codeNotFound, "图片不存在")
	}
	wantError(t, e.do("GET", base+"/1/nope", nil), 404, codeNotFound, "")
	wantError(t, e.do("PUT", base, nil), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("GET", base+"?project_id=x", nil), 400, codeBadRequest, "参数 project_id 无效")
	wantError(t, e.do("GET", base+"?project_id=0", nil), 400, codeBadRequest, "参数 project_id 无效")
	rec := e.do("GET", base+"?project_id=424242", nil)
	if l := want[List[map[string]any]](t, rec, 200); l.Total != 0 || l.Items == nil {
		t.Errorf("unknown project = %+v", l)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	wantError(t, e.do("GET", base+"/424242", nil), 404, codeNotFound, "数据版本不存在")
	wantError(t, e.do("POST", base+"/424242/exports", map[string]any{"format": "yolo"}), 404, codeNotFound, "数据版本不存在")
	wantError(t, e.do("GET", base+"/424242/exports/AAAA", nil), 404, codeNotFound, "下载链接已失效")
	wantError(t, e.do("POST", "/api/v1/recycle-bin/data-versions/x/restore", nil), 404, codeNotFound, "回收站中没有这个数据版本")
}

func TestDataVersionAPIList(t *testing.T) {
	e := newDataVersionEnv(t)
	p := e.classProject(t, "外观检测", "scratch", "dent")
	q := e.classProject(t, "q", "x")
	v1 := e.insertDataVersion(t, p.ID, 1, dataversion.StatusReady)
	v2 := e.insertDataVersion(t, p.ID, 2, dataversion.StatusFailed)
	e.insertDataVersion(t, q.ID, 1, dataversion.StatusReady)

	all := want[List[map[string]any]](t, e.do("GET", "/api/v1/data-versions", nil), 200)
	if all.Total != 3 || len(all.Items) != 3 {
		t.Fatalf("list = %+v", all)
	}
	wantKeys(t, "data version", all.Items[0], dataVersionKeys...)
	l := want[List[dataversion.DataVersion]](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions?project_id=%d", p.ID), nil), 200)
	if l.Total != 2 || l.Items[0].ID != v2.id || l.Items[1].ID != v1.id || l.Items[0].ProjectName != "外观检测" {
		t.Errorf("project list = %+v", l)
	}
	got := want[dataversion.DataVersion](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d", v1.id), nil), 200)
	if got.ID != v1.id || got.Version != 1 || got.Status != dataversion.StatusReady || len(got.Classes) != 2 ||
		got.ClassVersion == "" || len(got.Sources) != 1 || !got.Sources[0].DatasetDeleted {
		t.Errorf("get = %+v", got)
	}

	// The project reports its versions.
	raw := want[map[string]any](t, e.do("GET", fmt.Sprintf("/api/v1/projects/%d", p.ID), nil), 200)
	wantKeys(t, "project", raw, projectKeys...)
	latest, ok := raw["latest_data_version"].(map[string]any)
	if raw["data_version_count"] != 2.0 || !ok {
		t.Fatalf("project = %v", raw)
	}
	wantKeys(t, "latest data version", latest, "id", "version", "status", "created_at")
	if latest["id"] != float64(v2.id) || latest["version"] != 2.0 || latest["status"] != "failed" {
		t.Errorf("latest data version = %v", latest)
	}
	empty := e.classProject(t, "empty")
	if raw := want[map[string]any](t, e.do("GET", fmt.Sprintf("/api/v1/projects/%d", empty.ID), nil), 200); raw["data_version_count"] != 0.0 ||
		raw["latest_data_version"] != nil {
		t.Errorf("project without data versions = %v", raw)
	}

	// In the bin: neither listed nor found.
	e.trashDataVersion(t, v1.id)
	wantError(t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d", v1.id), nil), 404, codeNotFound, "数据版本不存在")
	if l := want[List[dataversion.DataVersion]](t, e.do("GET", "/api/v1/data-versions", nil), 200); l.Total != 2 {
		t.Errorf("list with a version in the bin = %+v", l)
	}
}

func TestDataVersionAPIImages(t *testing.T) {
	e := newDataVersionEnv(t)
	p := e.classProject(t, "p", "scratch", "dent")
	v := e.insertDataVersion(t, p.ID, 1, dataversion.StatusReady,
		testVersionImage{path: "images/line_a/NG/a.jpg", split: "train",
			objects: []dataset.AnnotationObject{{Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 9, Ymax: 9}}},
		testVersionImage{path: "images/line_a/NG/a-aug1.jpg", split: "train", augmented: true,
			objects: []dataset.AnnotationObject{{Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 9, Ymax: 9}}},
		testVersionImage{path: "images/line_a/OK/b.png", split: "val", data: testPNG(t, 40, 20)},
		testVersionImage{path: "images/line_a/OK/broken.jpg", split: "test", data: []byte("broken")},
	)
	base := fmt.Sprintf("/api/v1/data-versions/%d", v.id)

	raw := want[map[string]any](t, e.do("GET", base+"/images", nil), 200)
	items := raw["items"].([]any)
	if raw["total"] != 4.0 || len(items) != 4 {
		t.Fatalf("images = %v", raw)
	}
	first := items[0].(map[string]any)
	wantKeys(t, "data version image", first, dataVersionImageKeys...)
	if first["dataset_name"] != "产线A" || first["annotation_source"].(map[string]any)["name"] != "审图1.2" ||
		first["thumbnail_url"] != fmt.Sprintf("%s/images/%d/thumbnail?size=sm", base, v.imageIDs[0]) {
		t.Errorf("first image = %v", first)
	}
	ids := func(query string) []int64 {
		t.Helper()
		l := want[List[dataversion.Image]](t, e.do("GET", base+"/images"+query, nil), 200)
		out := []int64{}
		for _, img := range l.Items {
			out = append(out, img.ID)
		}
		return out
	}
	for query, want := range map[string][]int64{
		"?split=val":                {v.imageIDs[2]},
		"?label=scratch":            {v.imageIDs[0], v.imageIDs[1]},
		"?augmented=true":           {v.imageIDs[1]},
		"?augmented=false&split=":   {v.imageIDs[0], v.imageIDs[2], v.imageIDs[3]},
		"?dataset_id=1&limit=1":     {v.imageIDs[0]},
		"?dataset_id=2":             {},
		"?q=OK%2F&offset=1":         {v.imageIDs[3]},
		"?q=%25":                    {},
		"?offset=4":                 {},
		"?limit=200&q=":             {v.imageIDs[0], v.imageIDs[1], v.imageIDs[2], v.imageIDs[3]},
		"?label=%E5%88%92%E7%97%95": {},
	} {
		if got := ids(query); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}
	for _, query := range []string{"?split=trainval", "?augmented=maybe", "?limit=0", "?limit=201", "?offset=-1",
		"?dataset_id=0", "?dataset_id=x", "?q=%00", "?label=%FF"} {
		wantError(t, e.do("GET", base+"/images"+query, nil), 400, codeBadRequest, "参数")
	}

	// Detail: objects always present.
	d := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images/%d", base, v.imageIDs[0]), nil), 200)
	wantKeys(t, "image detail", d, append(dataVersionImageKeys, "objects")...)
	if objects := d["objects"].([]any); len(objects) != 1 {
		t.Errorf("objects = %v", d["objects"])
	}
	if d := want[map[string]any](t, e.do("GET", fmt.Sprintf("%s/images/%d", base, v.imageIDs[2]), nil), 200); d["objects"] == nil ||
		len(d["objects"].([]any)) != 0 {
		t.Errorf("objects of a negative = %v", d["objects"])
	}
	wantError(t, e.do("GET", base+"/images/424242", nil), 404, codeNotFound, "图片不存在")

	// Files and thumbnails may be cached for good.
	rec := e.do("GET", fmt.Sprintf("%s/images/%d/file", base, v.imageIDs[2]), nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `inline; filename="b.png"`) ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Body.Len() == 0 {
		t.Errorf("file = %d %v (%d bytes)", rec.Code, rec.Header(), rec.Body.Len())
	}
	if rec := e.do("HEAD", fmt.Sprintf("%s/images/%d/file", base, v.imageIDs[2]), nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD file = %d (%d bytes)", rec.Code, rec.Body.Len())
	}
	if rec := e.do("GET", fmt.Sprintf("%s/images/%d/file", base, v.imageIDs[0]), nil, "Range", "bytes=0-1"); rec.Code != 206 ||
		rec.Body.Len() != 2 {
		t.Errorf("range = %d (%d bytes)", rec.Code, rec.Body.Len())
	}
	for _, size := range []string{"", "?size=sm", "?size=lg"} {
		rec := e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail%s", base, v.imageIDs[0], size), nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" ||
			rec.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" || rec.Body.Len() == 0 {
			t.Errorf("thumbnail%s = %d %v", size, rec.Code, rec.Header())
		}
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail?size=xl", base, v.imageIDs[0]), nil), 400, codeBadRequest, "size")
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/thumbnail", base, v.imageIDs[3]), nil), 422, codeUnsupportedImage, "缩略图")
	if err := os.Remove(filepath.Join(v.dir, "images", "line_a", "OK", "b.png")); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/file", base, v.imageIDs[2]), nil), 404, codeNotFound, "图片文件不存在")

	// Not ready: 409; in the bin: 404.
	building := e.insertDataVersion(t, p.ID, 2, dataversion.StatusBuilding)
	wantError(t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d/images", building.id), nil), 409, codeConflict,
		"数据版本还没有生成完成")
	wantError(t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d/images/1/thumbnail", building.id), nil), 409,
		codeConflict, "数据版本还没有生成完成")
	e.trashDataVersion(t, v.id)
	wantError(t, e.do("GET", base+"/images", nil), 404, codeNotFound, "数据版本不存在")
	wantError(t, e.do("GET", fmt.Sprintf("%s/images/%d/file", base, v.imageIDs[0]), nil), 404, codeNotFound, "数据版本不存在")
}

func TestDataVersionAPIExport(t *testing.T) {
	e := newDataVersionEnv(t)
	p := e.classProject(t, "外观检测", "scratch", "dent")
	v := e.insertDataVersion(t, p.ID, 3, dataversion.StatusReady,
		testVersionImage{path: "images/line_a/a.jpg", split: "train",
			objects: []dataset.AnnotationObject{{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 16, Ymax: 12}}},
		testVersionImage{path: "images/line_a/b.jpg", split: "val"})
	base := fmt.Sprintf("/api/v1/data-versions/%d", v.id)

	wantError(t, e.do("POST", base+"/exports", map[string]any{"format": "zip"}), 422, codeValidationFailed, "下载格式")
	wantError(t, e.do("POST", base+"/exports", "{"), 400, codeBadRequest, "")
	raw := want[map[string]any](t, e.do("POST", base+"/exports", map[string]any{"format": "yolo"}), 201)
	wantKeys(t, "export", raw, "url", "file_name", "expires_at")
	var ticket exportResponse
	data, _ := json.Marshal(raw)
	if err := json.Unmarshal(data, &ticket); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ticket.URL, base+"/exports/") || ticket.FileName != "外观检测-数据版本v3-yolo.zip" ||
		time.Until(ticket.ExpiresAt) < 59*time.Minute {
		t.Errorf("ticket = %+v", ticket)
	}
	for range 2 { // can be fetched again
		rec := e.do("GET", ticket.URL, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" ||
			rec.Header().Get("Content-Disposition") != attachmentDisposition(ticket.FileName) ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("download = %d %v", rec.Code, rec.Header())
		}
		files := readZip(t, rec.Body.Bytes())
		top := "外观检测-数据版本v3/"
		if string(files[top+"labels/train/line_a/a.txt"]) != "1 0.250000 0.250000 0.500000 0.500000\n" ||
			files[top+"images/val/line_a/b.jpg"] == nil || !strings.Contains(string(files[top+"data.yaml"]), "nc: 2\n") ||
			len(files) != 6 {
			t.Errorf("archive = %v", files)
		}
	}
	if rec := e.do("HEAD", ticket.URL, nil); rec.Code != 200 || rec.Body.Len() != 0 ||
		rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("HEAD = %d (%d bytes) %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	token := ticket.URL[strings.LastIndexByte(ticket.URL, '/')+1:]
	for _, bad := range []string{base + "/exports/AAAA", base + "/exports/not-a-token",
		fmt.Sprintf("/api/v1/data-versions/%d/exports/%s", v.id+1, token)} {
		wantError(t, e.do("GET", bad, nil), 404, codeNotFound, "下载链接已失效")
	}
	for _, format := range []string{"voc", "coco"} {
		raw := want[exportResponse](t, e.do("POST", base+"/exports", map[string]any{"format": format,
			"include_augmented": true}), 201)
		if rec := e.do("GET", raw.URL, nil); rec.Code != 200 || len(readZip(t, rec.Body.Bytes())) == 0 {
			t.Errorf("%s download = %d", format, rec.Code)
		}
	}

	// Moved to the recycle bin after the ticket: gone.
	e.trashDataVersion(t, v.id)
	wantError(t, e.do("GET", ticket.URL, nil), 404, codeNotFound, "数据版本不存在")
	wantError(t, e.do("POST", base+"/exports", map[string]any{"format": "yolo"}), 404, codeNotFound, "数据版本不存在")
	failed := e.insertDataVersion(t, p.ID, 4, dataversion.StatusFailed)
	wantError(t, e.do("POST", fmt.Sprintf("/api/v1/data-versions/%d/exports", failed.id), map[string]any{"format": "voc"}),
		409, codeConflict, "数据版本还没有生成完成")
}

func TestDataVersionAPIRecycleBin(t *testing.T) {
	e := newDataVersionEnv(t)
	const bin = "/api/v1/recycle-bin"
	if raw := want[map[string]any](t, e.do("GET", bin, nil), 200); raw["data_versions"] == nil ||
		len(raw["data_versions"].([]any)) != 0 {
		t.Errorf("empty recycle bin = %v", raw)
	}
	p := e.classProject(t, "外观检测", "scratch")
	v1 := e.insertDataVersion(t, p.ID, 1, dataversion.StatusReady)
	v2 := e.insertDataVersion(t, p.ID, 2, dataversion.StatusReady)
	e.trashDataVersion(t, v1.id)

	raw := want[map[string]any](t, e.do("GET", bin, nil), 200)
	wantKeys(t, "recycle bin", raw, "retention_days", "datasets", "versions", "models", "data_versions")
	items := raw["data_versions"].([]any)
	if len(items) != 1 {
		t.Fatalf("data versions in the bin = %v", items)
	}
	wantKeys(t, "trashed data version", items[0].(map[string]any), append(dataVersionKeys, "deleted_at", "purge_at",
		"project_deleted")...)
	restore := fmt.Sprintf("%s/data-versions/%d/restore", bin, v1.id)
	wantError(t, e.do("POST", restore, "{"), 400, codeBadRequest, "")
	got := want[dataversion.DataVersion](t, e.do("POST", restore, nil), 200)
	if got.ID != v1.id || got.ProjectName != "外观检测" {
		t.Errorf("restored = %+v", got)
	}
	wantError(t, e.do("POST", restore, nil), 404, codeNotFound, "回收站中没有这个数据版本")

	// Deleted with its project: restoring it restores the project, under
	// another name when its own is taken.
	e.trashDataVersion(t, v1.id)
	e.trashDataVersion(t, v2.id)
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d", p.ID), nil); rec.Code != 204 {
		t.Fatalf("delete project = %d %s", rec.Code, rec.Body)
	}
	if !dirExists(filepath.Dir(v1.dir)) {
		t.Error("the folder of a project with versions in the bin must stay")
	}
	l := want[recycleBin](t, e.do("GET", bin, nil), 200)
	if len(l.DataVersions) != 2 || !l.DataVersions[0].ProjectDeleted {
		t.Fatalf("recycle bin = %+v", l.DataVersions)
	}
	e.classProject(t, "外观检测")
	wantError(t, e.do("POST", restore, nil), 409, codeNameTaken, "已存在同名项目“外观检测”")
	wantError(t, e.do("POST", restore, map[string]any{"project_name": " "}), 422, codeValidationFailed, "名称不能为空")
	got = want[dataversion.DataVersion](t, e.do("POST", restore, map[string]any{"project_name": "外观检测（旧）"}), 200)
	if got.ProjectID != p.ID || got.ProjectName != "外观检测（旧）" {
		t.Errorf("restored with its project = %+v", got)
	}
}

func TestDataVersionAPIProjectDelete(t *testing.T) {
	e := newDataVersionEnv(t)
	p := e.classProject(t, "p", "scratch")
	v := e.insertDataVersion(t, p.ID, 1, dataversion.StatusReady)
	base := fmt.Sprintf("/api/v1/projects/%d", p.ID)

	wantError(t, e.do("DELETE", base, nil), 409, codeConflict, "项目中有 1 个数据版本，请先删除数据版本，或选择同时删除数据版本")
	wantError(t, e.do("DELETE", base+"?delete_models=true", nil), 409, codeConflict, "项目中有 1 个数据版本")
	wantError(t, e.do("DELETE", base+"?delete_data_versions=maybe", nil), 400, codeBadRequest,
		"参数 delete_data_versions 无效，应为 true 或 false")
	wantError(t, e.do("DELETE", base+"?delete_models=x", nil), 400, codeBadRequest, "参数 delete_models 无效")

	// The models are checked first, and a refused delete changes nothing:
	// the data versions are not touched.
	m := (&modelEnv{apiEnv: e.apiEnv}).uploadModel(t, p.ID, "yolo11", fakeWeights(t, "yolo11", "detect", "1"))
	wantError(t, e.do("DELETE", base+"?delete_data_versions=true", nil), 409, codeConflict, "项目中有 1 个模型")
	wantError(t, e.do("DELETE", base+"?delete_models=true", nil), 409, codeConflict, "项目中有 1 个数据版本")
	want[model.Model](t, e.do("GET", fmt.Sprintf("/api/v1/models/%d", m.ID), nil), 200)
	want[dataversion.DataVersion](t, e.do("GET", fmt.Sprintf("/api/v1/data-versions/%d", v.id), nil), 200)

	// With the version in the bin, the models go there too and the project
	// stays with them.
	e.trashDataVersion(t, v.id)
	if rec := e.do("DELETE", base+"?delete_models=true", nil); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", base, nil), 404, codeNotFound, "项目不存在")
	if !dirExists(v.dir) {
		t.Error("the version folder must stay while the version is in the bin")
	}

	// Deleted for good: the empty folder of its (failed, removed) versions
	// goes with it.
	q := e.classProject(t, "q", "scratch")
	emptyDir := filepath.Join(e.st.Dir(storage.Exports), "data-versions", fmt.Sprintf("project-%d", q.ID))
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_data_versions=false", q.ID), nil); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if dirExists(emptyDir) {
		t.Error("the empty data versions folder of a deleted project must be removed")
	}
}
