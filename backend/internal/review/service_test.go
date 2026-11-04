package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testEnv struct {
	db       *database.DB
	st       *storage.Storage
	clock    *testClock
	datasets *dataset.Service
	models   *model.Service
	projects *project.Service
	svc      *Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	e := &testEnv{db: db, st: st, clock: &testClock{t: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)}}
	e.datasets = dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	e.models = model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log, Now: e.clock.Now})
	e.projects = project.NewService(project.Options{DB: db.Pool, Logger: log})
	e.svc = NewService(Options{DB: db.Pool, Storage: st, Logger: log, Datasets: e.datasets, Models: e.models,
		Projects: e.projects, Now: e.clock.Now})
	return e
}

// project inserts a project and returns its ID.
func (e *testEnv) project(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), "INSERT INTO projects (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// testModel describes a model to insert.
type testModel struct {
	task      string
	classes   []string
	imageSize int
	noFile    bool
}

// addModel inserts a model of the project with its weights file and
// returns it.
func (e *testEnv) addModel(t *testing.T, projectID int64, tm testModel) model.Model {
	t.Helper()
	ctx := context.Background()
	if tm.task == "" {
		tm.task = "detect"
	}
	if tm.classes == nil {
		tm.classes = []string{"scratch", "dent"}
	}
	info := map[string]any{"task": tm.task, "class_names": tm.classes}
	if tm.imageSize > 0 {
		info["image_size"] = tm.imageSize
	}
	var version int
	if err := e.db.QueryRow(ctx, `UPDATE projects SET model_version_seq = model_version_seq + 1 WHERE id = $1
		RETURNING model_version_seq`, projectID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	dirName := fmt.Sprintf("project-%d/v%d", projectID, version)
	weights := []byte(fmt.Sprintf("weights of %s", dirName))
	var id int64
	if err := e.db.QueryRow(ctx, `INSERT INTO models (project_id, version, source, yolo_version, task, file_name, dir_name,
			size_bytes, sha256, info)
		VALUES ($1, $2, 'upload', 'yolo11', $3, 'best.pt', $4, $5, $6, $7) RETURNING id`,
		projectID, version, tm.task, dirName, len(weights), strings.Repeat("ab", 32), info).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if !tm.noFile {
		dir := filepath.Join(e.st.Dir(storage.Models), filepath.FromSlash(dirName))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "model.pt"), weights, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := e.models.GetModel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// pngImage encodes a w×h PNG.
func pngImage(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testImage is an image of a test dataset; size 0 means 40×30, and a
// negative width leaves the size unknown in the index.
type testImage struct {
	rel  string
	w, h int
}

// addDataset creates a dataset folder with PNG images and indexes them
// (as an import would), and returns the dataset ID.
func (e *testEnv) addDataset(t *testing.T, name, dirName string, images ...testImage) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := e.db.QueryRow(ctx, `INSERT INTO datasets (name, dir_name, origin) VALUES ($1, $2, 'upload') RETURNING id`,
		name, dirName).Scan(&id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.st.Dir(storage.Datasets), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, img := range images {
		e.addDatasetImage(t, id, dir, img)
	}
	return id
}

func (e *testEnv) addDatasetImage(t *testing.T, datasetID int64, dir string, img testImage) int64 {
	t.Helper()
	w, h := img.w, img.h
	if w == 0 {
		w, h = 40, 30
	}
	data := pngImage(t, max(w, 1), max(h, 1))
	p := filepath.Join(dir, filepath.FromSlash(img.rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	var width, height *int
	if w > 0 {
		width, height = &w, &h
	}
	var id int64
	if err := e.db.QueryRow(context.Background(), `INSERT INTO dataset_images (dataset_id, rel_path, size_bytes, mod_time,
			width, height, sort_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, datasetID, img.rel, fi.Size(),
		time.UnixMicro(fi.ModTime().UnixMicro()), width, height, dataset.ImageSortKey(img.rel)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// node inserts a registered training node and returns its ID.
func (e *testEnv) node(t *testing.T, key string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), `INSERT INTO compute_nodes (node_key, name, hardware)
		VALUES ($1, $1, '{}') RETURNING id`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// create creates a review and fails the test on an error.
func (e *testEnv) create(t *testing.T, in CreateInput) Review {
	t.Helper()
	r, err := e.svc.CreateReview(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	return r
}

// claim claims review inference for the node through the model service's
// claim, without waiting; nil when nothing is queued.
func (e *testEnv) claim(t *testing.T, nodeID int64) *Task {
	t.Helper()
	task, err := e.models.ClaimTask(context.Background(), nodeID, []string{TaskKind}, 0)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task == nil {
		return nil
	}
	rt, ok := task.(*Task)
	if !ok {
		t.Fatalf("claimed a %T", task)
	}
	return rt
}

func (e *testEnv) review(t *testing.T, id int64) Review {
	t.Helper()
	r, err := e.svc.GetReview(context.Background(), id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	return r
}

// images lists all images of a review.
func (e *testEnv) images(t *testing.T, reviewID int64, q ImageQuery) []Image {
	t.Helper()
	q.Limit = MaxImageLimit
	images, _, err := e.svc.ListImages(context.Background(), reviewID, q)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	return images
}

func (e *testEnv) detail(t *testing.T, reviewID, imageID int64) ImageDetail {
	t.Helper()
	d, err := e.svc.GetImage(context.Background(), reviewID, imageID)
	if err != nil {
		t.Fatalf("GetImage: %v", err)
	}
	return d
}

// box is a reported box.
func box(name string, conf, x1, y1, x2, y2 float64) ResultBox {
	return ResultBox{Name: name, Confidence: conf, Xmin: x1, Ymin: y1, Xmax: x2, Ymax: y2}
}

// inferAll reports a box for every image not inferred yet (one per image,
// "scratch" at 1,1–11,11) and completes the review.
func (e *testEnv) inferAll(t *testing.T, reviewID, nodeID int64) {
	t.Helper()
	ctx := context.Background()
	page, err := e.svc.ListTaskImages(ctx, reviewID, nodeID, 0, MaxTaskImageLimit)
	if err != nil {
		t.Fatal(err)
	}
	var results []ImageResult
	for _, img := range page.Items {
		results = append(results, ImageResult{ID: img.ID, OK: true, Boxes: []ResultBox{box("scratch", 0.9, 1, 1, 11, 11)}})
	}
	if _, err := e.svc.SubmitResults(ctx, reviewID, nodeID, results); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteTask(ctx, reviewID, nodeID, json.RawMessage(`{"device": "cpu"}`)); err != nil {
		t.Fatal(err)
	}
}

// wantKind fails unless err is a review *Error of the kind whose message
// contains msg.
func wantKind(t *testing.T, err, kind error, msg string) {
	t.Helper()
	var re *Error
	if !errors.As(err, &re) || !errors.Is(err, kind) || !strings.Contains(re.Message, msg) {
		t.Errorf("error = %v (%T), want %v containing %q", err, err, kind, msg)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (e *testEnv) reviewFile(id int64, rel string) string {
	return filepath.Join(e.svc.reviewDir(id), filepath.FromSlash(rel))
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
