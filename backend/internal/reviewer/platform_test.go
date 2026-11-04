package reviewer

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

	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// platform is a trainMaster platform (its review service on a test
// database) to download packages from and upload results to.
type platform struct {
	st     *storage.Storage
	svc    *review.Service
	models *model.Service
}

func newPlatform(t *testing.T) (*platform, *review.Service, func(sql string, args ...any) int64) {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	datasets := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log})
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	svc := review.NewService(review.Options{DB: db.Pool, Storage: st, Logger: log, Datasets: datasets, Models: models,
		Projects: projects})
	p := &platform{st: st, svc: svc, models: models}
	insert := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	return p, svc, insert
}

// TestIntegrationPlatformRoundTrip downloads a package from the platform,
// reviews it in the local reviewer, exports the results and uploads them
// back: the platform applies exactly the local changes.
func TestIntegrationPlatformRoundTrip(t *testing.T) {
	p, svc, insert := newPlatform(t)
	ctx := context.Background()

	// A project with a model, two datasets (one with a folder name that is
	// not safe on Windows) and a review of both.
	projectID := insert("INSERT INTO projects (name) VALUES ('外观检测') RETURNING id")
	insert("UPDATE projects SET model_version_seq = 1 WHERE id = $1 RETURNING id", projectID)
	dirName := fmt.Sprintf("project-%d/v1", projectID)
	weights := []byte("weights")
	modelID := insert(`INSERT INTO models (project_id, version, source, yolo_version, task, file_name, dir_name, size_bytes,
			sha256, info)
		VALUES ($1, 1, 'upload', 'yolo11', 'detect', 'best.pt', $2, $3, $4, $5) RETURNING id`,
		projectID, dirName, len(weights), strings.Repeat("ab", 32), map[string]any{"task": "detect", "class_names": []string{"scratch", "dent"}})
	modelDir := filepath.Join(p.st.Dir(storage.Models), filepath.FromSlash(dirName))
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.pt"), weights, 0o644); err != nil {
		t.Fatal(err)
	}
	addDataset := func(name, dir string, rels ...string) int64 {
		id := insert("INSERT INTO datasets (name, dir_name, origin) VALUES ($1, $2, 'upload') RETURNING id", name, dir)
		for _, rel := range rels {
			fp := filepath.Join(p.st.Dir(storage.Datasets), dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fp, pngBytes(t, 40, 30), 0o644); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(fp)
			if err != nil {
				t.Fatal(err)
			}
			insert(`INSERT INTO dataset_images (dataset_id, rel_path, size_bytes, mod_time, width, height, sort_key)
				VALUES ($1, $2, $3, $4, 40, 30, $5) RETURNING id`, id, rel, fi.Size(),
				time.UnixMicro(fi.ModTime().UnixMicro()), dataset.ImageSortKey(rel))
		}
		return id
	}
	ds1 := addDataset("line-a", "line_a", "a.png", "NG/b.png", "c.png")
	ds2 := addDataset("line-a 夜班", "line:a", "d.png", "e.png")
	r, err := svc.CreateReview(ctx, review.CreateInput{Name: "外观 审图", ModelID: modelID, DatasetIDs: []int64{ds1, ds2}})
	if err != nil {
		t.Fatal(err)
	}
	images, _, err := svc.ListImages(ctx, r.ID, review.ImageQuery{Limit: review.MaxImageLimit})
	if err != nil || len(images) != 5 {
		t.Fatalf("images = %v, %v", images, err)
	}
	byPath := map[string]review.Image{}
	for _, img := range images {
		byPath[img.PackagePath] = img
	}

	// Inference by a node: d fails.
	node := insert("INSERT INTO compute_nodes (node_key, name, hardware) VALUES ('n1', 'n1', '{}') RETURNING id")
	if _, err := p.models.ClaimTask(ctx, node, []string{review.TaskKind}, 0); err != nil {
		t.Fatal(err)
	}
	var results []review.ImageResult
	for _, img := range images {
		res := review.ImageResult{ID: img.ID, OK: true, Boxes: []review.ResultBox{
			{Name: "scratch", Confidence: 0.9, Xmin: 1, Ymin: 1, Xmax: 11, Ymax: 11}}}
		if img.Path == "d.png" {
			res = review.ImageResult{ID: img.ID, OK: false, Error: "无法读取图片"}
		}
		results = append(results, res)
	}
	if _, err := svc.SubmitResults(ctx, r.ID, node, results); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteTask(ctx, r.ID, node, json.RawMessage(`{"device": "cpu"}`)); err != nil {
		t.Fatal(err)
	}
	// The platform approves c before the download.
	c := byPath["line_a/c.png"]
	if _, err := svc.SetStatus(ctx, r.ID, c.ID, reviewfile.StatusApproved); err != nil {
		t.Fatal(err)
	}

	// The package, downloaded into the user's downloads folder.
	ticket, err := svc.CreatePackage(ctx, r.ID, review.PackageInput{})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := svc.OpenPackage(ctx, r.ID, ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	downloads := t.TempDir()
	zipPath := filepath.Join(downloads, ticket.FileName)
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pkg.Write(ctx, f); err != nil {
		t.Fatal(err)
	}
	pkg.Close()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Reviewed locally.
	ts := newTestServer(t)
	s := ts.open(zipPath)
	if s.Package.Counts.Total != 5 || s.Package.Counts.Approved != 1 || s.Package.Counts.InferenceFailed != 1 ||
		s.Package.MissingImages != 0 || s.Package.Review.ID != r.ID {
		t.Fatalf("package = %+v", s.Package)
	}
	var local list[Image]
	ts.do(http.MethodGet, "/api/review/images", nil, &local)
	localByPath := map[string]Image{}
	for _, img := range local.Items {
		localByPath[img.PackagePath] = img
		pi := byPath[img.PackagePath]
		if img.ID != pi.ID || img.ImageID != pi.ImageID || img.AnnotationPath != pi.AnnotationPath || img.Path != pi.Path {
			t.Errorf("local image %+v, the platform's %+v", img, pi)
		}
	}
	for _, path := range []string{"line_a/a.png", "line_a/NG/b.png", "line_a/c.png", "line_a~" + fmt.Sprint(ds2) + "/d.png"} {
		if _, ok := localByPath[path]; !ok {
			t.Errorf("%s is not in the package (it has %v)", path, localByPath)
		}
	}
	a, b := localByPath["line_a/a.png"], localByPath["line_a/NG/b.png"]
	d := localByPath["line_a~"+fmt.Sprint(ds2)+"/d.png"]
	// a approved; b needs samples with another box; d (inference failed)
	// gets a box.
	ts.do(http.MethodPut, fmt.Sprintf("/api/review/images/%d/status", a.ID), map[string]string{"status": "approved"}, nil)
	ts.do(http.MethodPut, fmt.Sprintf("/api/review/images/%d/status", b.ID), map[string]string{"status": "needs_sample"}, nil)
	for _, edit := range []struct {
		img     Image
		objects []dataset.AnnotationObject
	}{
		{b, []dataset.AnnotationObject{obj("scratch", 1, 1, 11, 11), obj("dent", 20, 5, 39, 29)}},
		{d, []dataset.AnnotationObject{obj("dent", 2, 2, 30, 20)}},
	} {
		img, objects := edit.img, edit.objects
		var got ImageDetail
		if code := ts.do(http.MethodPut, fmt.Sprintf("/api/review/images/%d/annotation", img.ID), map[string]any{
			"objects": objects, "base_version": img.AnnotationVersion}, &got); code != http.StatusOK {
			t.Fatalf("save %s: %d", img.Path, code)
		}
	}
	var exported ExportResult
	if code := ts.do(http.MethodPost, "/api/export", nil, &exported); code != http.StatusOK {
		t.Fatalf("export: %d", code)
	}
	if exported.StatusChanges != 2 || exported.AnnotationChanges != 2 || exported.Unchanged != 2 {
		t.Errorf("export = %+v", exported)
	}

	// Uploaded to the platform.
	results2, err := os.Open(exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer results2.Close()
	up, err := svc.UploadResults(ctx, r.ID, review.ResultsInput{FileName: exported.FileName, Size: exported.SizeBytes, Body: results2})
	if err != nil {
		t.Fatal(err)
	}
	if up.Summary != (review.UploadSummary{Images: 5, Applied: 3, StatusChanges: 2, AnnotationChanges: 2, Unchanged: 2}) ||
		len(up.Problems) != 0 {
		t.Errorf("upload = %+v", up)
	}
	for _, tt := range []struct {
		path    string
		status  string
		objects []dataset.AnnotationObject
		edited  bool
	}{
		{"line_a/a.png", reviewfile.StatusApproved, []dataset.AnnotationObject{obj("scratch", 1, 1, 11, 11)}, false},
		{"line_a/NG/b.png", reviewfile.StatusNeedsSample,
			[]dataset.AnnotationObject{obj("scratch", 1, 1, 11, 11), obj("dent", 20, 5, 39, 29)}, true},
		{"line_a/c.png", reviewfile.StatusApproved, []dataset.AnnotationObject{obj("scratch", 1, 1, 11, 11)}, false},
		{d.PackagePath, reviewfile.StatusPending, []dataset.AnnotationObject{obj("dent", 2, 2, 30, 20)}, true},
	} {
		got, err := svc.GetImage(ctx, r.ID, byPath[tt.path].ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != tt.status || !reviewfile.SameObjects(got.Objects, tt.objects) || got.Edited != tt.edited {
			t.Errorf("%s on the platform = %+v", tt.path, got)
		}
	}

	// The same results again change nothing.
	if _, err := results2.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	again, err := svc.UploadResults(ctx, r.ID, review.ResultsInput{FileName: exported.FileName, Size: exported.SizeBytes, Body: results2})
	if err != nil || again.Summary != (review.UploadSummary{Images: 5, Unchanged: 5}) {
		t.Errorf("repeated upload = %+v, %v", again, err)
	}
}
