package dataversion

// Test helpers of the backend-core tests (create, preview, build). Every
// top-level identifier starts with "core" (the integration tests of this
// package use "integ").

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// coreEnv is a Service on a private database and data directory, with
// the dataset service running (scans) and the data version service not
// started (tests build with buildNext).
type coreEnv struct {
	svc      *Service
	datasets *dataset.Service
	projects *project.Service
	db       *database.DB
	st       *storage.Storage
}

func coreNewEnv(t *testing.T) *coreEnv {
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
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	svc := NewService(Options{DB: db.Pool, Storage: st, Logger: log, Datasets: datasets, Projects: projects})
	t.Cleanup(func() {
		svc.WaitBackground()
		cancel()
		datasets.Stop()
	})
	return &coreEnv{svc: svc, datasets: datasets, projects: projects, db: db, st: st}
}

// corePNG is a PNG image of w x h pixels; seed varies its content (and
// size).
func corePNG(t *testing.T, w, h, seed int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x*7 + seed), uint8(y*5 + seed*3), uint8((x + y + seed) * 11), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// coreBox is a box of a VOC file.
type coreBox struct {
	label                  string
	xmin, ymin, xmax, ymax int
}

// coreVOC is a VOC annotation of an image of w x h pixels.
func coreVOC(filename string, w, h int, boxes ...coreBox) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "<annotation><filename>%s</filename><size><width>%d</width><height>%d</height><depth>3</depth></size>",
		filename, w, h)
	for _, o := range boxes {
		fmt.Fprintf(&b, "<object><name>%s</name><bndbox><xmin>%d</xmin><ymin>%d</ymin><xmax>%d</xmax><ymax>%d</ymax></bndbox></object>",
			o.label, o.xmin, o.ymin, o.xmax, o.ymax)
	}
	b.WriteString("</annotation>")
	return []byte(b.String())
}

// coreLabeled is an image with a VOC file with one 10 x 10 box per label
// (files: <stem>.png and <stem>.xml).
func coreLabeled(t *testing.T, files map[string][]byte, rel string, seed int, labels ...string) {
	t.Helper()
	var boxes []coreBox
	for i, l := range labels {
		boxes = append(boxes, coreBox{l, 2 + i, 2, 12 + i, 12})
	}
	files[rel] = corePNG(t, 32, 24, seed)
	files[strings.TrimSuffix(rel, filepath.Ext(rel))+".xml"] = coreVOC(filepath.Base(rel), 32, 24, boxes...)
}

// coreWriteFiles writes files below dir.
func coreWriteFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// coreDataset creates a dataset with files and indexes them.
func (e *coreEnv) coreDataset(t *testing.T, name string, files map[string][]byte) dataset.Dataset {
	t.Helper()
	ctx := context.Background()
	d, err := e.datasets.CreateDataset(ctx, dataset.CreateDatasetInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	coreWriteFiles(t, d.Path, files)
	e.coreScan(t, d.ID)
	if d, err = e.datasets.GetDataset(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	return d
}

// coreScan rescans a dataset and waits for the scan.
func (e *coreEnv) coreScan(t *testing.T, datasetID int64) {
	t.Helper()
	ctx := context.Background()
	job, err := e.datasets.StartScan(ctx, datasetID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := e.datasets.GetImport(ctx, datasetID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch j.Status {
		case dataset.StatusSucceeded:
			return
		case dataset.StatusFailed, dataset.StatusCanceled:
			t.Fatalf("scan %s: %s", j.Status, j.Error)
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// coreProject creates a project with datasets (in this order) and a class
// list.
func (e *coreEnv) coreProject(t *testing.T, name string, classes []string, datasetIDs ...int64) project.Project {
	t.Helper()
	ctx := context.Background()
	p, err := e.projects.CreateProject(ctx, project.CreateProjectInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range datasetIDs {
		if _, err := e.projects.AddDatasets(ctx, p.ID, []int64{id}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct link times: the source order
	}
	e.coreSetClasses(t, p.ID, classes...)
	return p
}

func (e *coreEnv) coreSetClasses(t *testing.T, projectID int64, names ...string) string {
	t.Helper()
	list := []project.Class{}
	for i, n := range names {
		list = append(list, project.Class{Name: n, Color: fmt.Sprintf("#%02x0000", 16+i)})
	}
	cl, err := e.projects.SetClasses(context.Background(), projectID, list, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cl.Version
}

func (e *coreEnv) coreClassVersion(t *testing.T, projectID int64) string {
	t.Helper()
	cl, err := e.projects.ListClasses(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return cl.Version
}

// coreVersionImage is an image of a review version made in a test.
type coreVersionImage struct {
	path, status string
	labels       []string // one box per label
}

// coreReviewVersion builds a ready review version of a dataset as a merge
// does.
func (e *coreEnv) coreReviewVersion(t *testing.T, datasetID int64, images ...coreVersionImage) dataset.Version {
	t.Helper()
	ctx := context.Background()
	var reviewID, mergeID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO reviews (name, model_ref, options, status) VALUES ('审图', '{}', '{}', 'ready')
		RETURNING id`).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, note, status)
		VALUES ($1, $2, '{needs_sample}', 'x', 'running') RETURNING id`, reviewID, []int64{datasetID}).Scan(&mergeID); err != nil {
		t.Fatal(err)
	}
	id, err := e.datasets.CreateReviewVersion(ctx, dataset.ReviewVersionDraft{DatasetID: datasetID, ReviewID: reviewID,
		MergeID: mergeID, Note: "审图"})
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range images {
		objects := []dataset.AnnotationObject{}
		for i, l := range img.labels {
			objects = append(objects, dataset.AnnotationObject{Name: l, Xmin: 1 + i, Ymin: 1, Xmax: 9 + i, Ymax: 9})
		}
		if err := e.datasets.AddVersionImage(ctx, id, dataset.VersionImage{Path: img.path, ReviewStatus: img.status,
			Objects: objects}); err != nil {
			t.Fatalf("AddVersionImage %s: %v", img.path, err)
		}
	}
	if err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error {
		_, err := e.datasets.FinishReviewVersions(ctx, tx, mergeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	v, err := e.datasets.GetVersion(ctx, datasetID, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// coreInput is a create input with the UI defaults.
func coreInput(projectID int64, classVersion string, sources ...SourceInput) CreateInput {
	return CreateInput{ProjectID: projectID, Note: "第一版", ClassVersion: classVersion, Sources: sources,
		Split: SplitConfig{Train: 80, Val: 20, Test: 0, Seed: 42, UseFolders: true, GroupBy: GroupByStem},
		Scope: ScopeAnnotated, IncludeEmpty: true, IgnoreLabels: []string{}}
}

// coreBase selects the base annotations of datasets.
func coreBase(ids ...int64) []SourceInput {
	var out []SourceInput
	for _, id := range ids {
		out = append(out, SourceInput{DatasetID: id, IncludeBase: true, VersionIDs: []int64{}})
	}
	return out
}

// coreBuildAll builds the queued versions.
func (e *coreEnv) coreBuildAll(t *testing.T) {
	t.Helper()
	for {
		built, err := e.svc.buildNext(context.Background())
		if err != nil {
			t.Fatalf("buildNext: %v", err)
		}
		if !built {
			return
		}
	}
}

// coreCreateBuilt creates a version and builds it; it must become ready.
func (e *coreEnv) coreCreateBuilt(t *testing.T, in CreateInput) DataVersion {
	t.Helper()
	v, err := e.svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	e.coreBuildAll(t)
	if v, err = e.svc.Get(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady {
		t.Fatalf("version %d is %s: %s", v.Version, v.Status, v.Error)
	}
	return v
}

// coreDir is the folder of a version.
func (e *coreEnv) coreDir(t *testing.T, v DataVersion) string {
	t.Helper()
	return e.svc.versionDir(versionDirName(v.ProjectID, v.Version))
}

func coreWantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	var ue *Error
	if !errors.As(err, &ue) || !errors.Is(err, kind) || !strings.Contains(ue.Message, msgPart) {
		t.Errorf("err = %v, want kind %v containing %q", err, kind, msgPart)
	}
}

// coreReadFile reads a file of a version folder ("" when missing).
func coreReadFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(data)
}

// coreImageRow is a data_version_images row as the tests read it.
type coreImageRow struct {
	Position         int
	Split            string
	SplitBy          string
	Path             string
	SizeBytes        int64
	ObjectCount      int
	Objects          []dataset.AnnotationObject
	SourcePath       string
	AnnotationSource *int64
	AugmentedFrom    *int64
	Augment          []string
	Stored           string
	ID               int64
	Width, Height    int
}

func (e *coreEnv) coreRows(t *testing.T, versionID int64) []coreImageRow {
	t.Helper()
	rows, err := e.db.Query(context.Background(), `SELECT position, split, split_by, path, size_bytes, object_count,
		objects, source_path, annotation_source, augmented_from, augment, stored, id, width, height
		FROM data_version_images WHERE version_id = $1 ORDER BY position`, versionID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[coreImageRow])
	if err != nil {
		t.Fatal(err)
	}
	return out
}
