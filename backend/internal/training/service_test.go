package training

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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

type testEnv struct {
	db           *database.DB
	st           *storage.Storage
	projects     *project.Service
	datasets     *dataset.Service
	dataVersions *dataversion.Service
	models       *model.Service
	svc          *Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	datasets := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	dataVersions := dataversion.NewService(dataversion.Options{
		DB: db.Pool, Storage: st, Logger: log, Datasets: datasets, Projects: projects,
	})
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log})
	svc := NewService(Options{
		DB:           db.Pool,
		Storage:      st,
		Logger:       log,
		Projects:     projects,
		DataVersions: dataVersions,
		Models:       models,
	})
	t.Cleanup(func() {
		svc.WaitBackground()
		dataVersions.WaitBackground()
	})
	return &testEnv{
		db:           db,
		st:           st,
		projects:     projects,
		datasets:     datasets,
		dataVersions: dataVersions,
		models:       models,
		svc:          svc,
	}
}

func testJPG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	img.Set(0, 0, color.RGBA{R: 200, G: 80, B: 40, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *testEnv) createProject(t *testing.T, name string) project.Project {
	t.Helper()
	p, err := e.projects.CreateProject(context.Background(), project.CreateProjectInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *testEnv) insertNode(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(),
		`INSERT INTO compute_nodes (node_key, name, hostname, hardware) VALUES ($1, $2, 'gpu-host-01', '{}') RETURNING id`,
		"node-key-"+name, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *testEnv) insertReadyDataVersion(t *testing.T, projectID int64, ver int) int64 {
	t.Helper()
	ctx := context.Background()
	dirName := fmt.Sprintf("data-versions/project-%d/v%d", projectID, ver)
	dir := filepath.Join(e.st.Dir(storage.Exports), filepath.FromSlash(dirName))
	classes := []dataversion.Class{{Name: "scratch", Color: "#ef4444"}, {Name: "dent", Color: "#3b82f6"}}
	sources := []dataversion.Source{{DatasetID: 1, DatasetName: "line_a", DirName: "line_a", IncludeBase: true}}
	cfg := dataversion.Config{
		Split:        dataversion.SplitConfig{Train: 80, Val: 20, Seed: 42, GroupBy: "stem"},
		IgnoreLabels: []string{},
	}
	now := time.Now()
	var dvID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO data_versions (
			project_id, version, dir_name, note, status, classes, config, sources,
			image_count, object_count, train_count, val_count, test_count, finished_at
		) VALUES ($1, $2, $3, '测试版本', 'ready', $4, $5, $6, 2, 2, 1, 1, 0, $7) RETURNING id`,
		projectID, ver, dirName, classes, cfg, sources, now).Scan(&dvID); err != nil {
		t.Fatal(err)
	}
	jpg := testJPG(t)
	splits := []struct {
		split string
		rel   string
	}{
		{"train", "images/line_a/a.jpg"},
		{"val", "images/line_a/b.jpg"},
	}
	for i, sp := range splits {
		fp := filepath.Join(dir, filepath.FromSlash(sp.rel))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, jpg, 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(fp)
		if err != nil {
			t.Fatal(err)
		}
		objs := []dataset.AnnotationObject{{Name: "scratch", Xmin: 2, Ymin: 2, Xmax: 20, Ymax: 18}}
		labels := map[string]int{"scratch": 1}
		if _, err := e.db.Exec(ctx, `INSERT INTO data_version_images (
				version_id, position, split, split_by, path, size_bytes, mod_time, width, height,
				objects, object_count, labels, dataset_id, source_path, source_size, source_mod_time,
				annotation_source, augment, stored
			) VALUES ($1, $2, $3, 'ratio', $4, $5, $6, 32, 24, $7, 1, $8, 1, $9, $5, $6, 0, '[]', 'link')`,
			dvID, i, sp.split, sp.rel, fi.Size(), fi.ModTime(), objs, labels, filepath.Base(sp.rel)); err != nil {
			t.Fatal(err)
		}
	}
	return dvID
}

func checkpointFixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "model", "checkpoint", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing fixture %s: %v", p, err)
	}
	return p
}

func (e *testEnv) registerFixtureModel(t *testing.T, projectID int64, fixture string) model.Model {
	t.Helper()
	src := checkpointFixturePath(t, fixture)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	runsRel := fmt.Sprintf("fixture-%s", fixture)
	dst := filepath.Join(e.st.Dir(storage.Runs), runsRel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := e.models.RegisterTrainedModel(context.Background(), model.TrainedModelInput{
		ProjectID:     projectID,
		TrainingJobID: 1,
		YOLOVersion:   "yolo11",
		WeightsPath:   runsRel,
		FileName:      fixture,
		Description:   "fixture " + fixture,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTrainingLifecycleAndAutoRegister(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	proj := e.createProject(t, "缺陷检测产线")
	dvID := e.insertReadyDataVersion(t, proj.ID, 1)
	nodeID := e.insertNode(t, "gpu-node-1")

	// 1. Create scratch job with custom epochs & lr0; defaults populated automatically.
	job, err := e.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitScratch,
		YOLOVersion:   "yolo11",
		ModelScale:    "n",
		Params: map[string]any{
			"epochs": 10,
			"batch":  8,
			"lr0":    0.005,
		},
	})
	if err != nil {
		t.Fatalf("Create scratch job failed: %v", err)
	}
	if job.Status != StatusQueued || job.TotalEpochs != 10 || !job.AutoRegister || job.QueuePosition == nil || *job.QueuePosition != 1 {
		t.Fatalf("unexpected created job: %+v", job)
	}
	if job.Params["epochs"] != float64(10) && job.Params["epochs"] != 10 {
		t.Errorf("params[epochs] = %#v", job.Params["epochs"])
	}

	// 2. DataVersion cannot be deleted while referenced by training job.
	if err := e.dataVersions.Delete(ctx, dvID); !errors.Is(err, dataversion.ErrConflict) {
		t.Fatalf("expected dataversion.ErrConflict when deleting referenced dv, got %v", err)
	}
	// Project CheckDeleteAll blocks unless withTrainingJobs is true.
	if err := e.projects.CheckDeleteAll(ctx, proj.ID, true, true, false); !errors.Is(err, project.ErrHasTrainingJobs) {
		t.Fatalf("expected ErrHasTrainingJobs, got %v", err)
	}

	// 3. Node claims the job via model.Service.ClaimTask (registered via AddTaskSource).
	claimed, err := e.models.ClaimTask(ctx, nodeID, []string{TaskKind}, 0)
	if err != nil {
		t.Fatalf("ClaimTask error: %v", err)
	}
	task, ok := claimed.(*Task)
	if !ok || task == nil {
		t.Fatalf("ClaimTask = %#v, want *Task", claimed)
	}
	if task.ID != job.ID || task.ArchYAML != "yolo11n.yaml" || task.InitMode != InitScratch {
		t.Fatalf("unexpected claimed task: %+v", task)
	}

	// 4. Node downloads dataset zip via OpenTaskDataset.
	exp, err := e.svc.OpenTaskDataset(ctx, job.ID, nodeID)
	if err != nil {
		t.Fatalf("OpenTaskDataset error: %v", err)
	}
	var zipBuf bytes.Buffer
	if err := exp.Write(ctx, &zipBuf); err != nil {
		exp.Close()
		t.Fatalf("write dataset zip: %v", err)
	}
	exp.Close()
	zr, err := zip.NewReader(bytes.NewReader(zipBuf.Bytes()), int64(zipBuf.Len()))
	if err != nil {
		t.Fatalf("open dataset zip: %v", err)
	}
	hasDataYAML := false
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "/data.yaml") || zf.Name == "data.yaml" {
			hasDataYAML = true
		}
	}
	if !hasDataYAML {
		t.Errorf("dataset zip missing data.yaml")
	}

	// 5. Node streams progress, epochs, and logs.
	f1, f2, m50, m95, boxL := 0.42, 0.78, 0.85, 0.77, 1.12
	curEp, totEp, bDone, bTot, eta := 2, 10, 8, 8, 25
	if _, err := e.svc.ReportProgress(ctx, job.ID, nodeID, ProgressInput{
		Stage:        StageTraining,
		CurrentEpoch: &curEp,
		TotalEpochs:  &totEp,
		BatchDone:    &bDone,
		BatchTotal:   &bTot,
		ETASeconds:   &eta,
		Epochs: []EpochMetrics{
			{Epoch: 1, TotalEpochs: 10, TrainBoxLoss: &boxL, MAP50: &f1, MAP50_95: &f1, Fitness: &f1},
			{Epoch: 2, TotalEpochs: 10, TrainBoxLoss: &boxL, MAP50: &m50, MAP50_95: &m95, Fitness: &f2},
		},
		LogChunk: "Epoch 1/10: box_loss=1.12\nEpoch 2/10: mAP50=0.85\n",
	}); err != nil {
		t.Fatalf("ReportProgress error: %v", err)
	}

	logs, err := e.svc.ReadLogs(ctx, job.ID, 0, 4096)
	if err != nil {
		t.Fatalf("ReadLogs error: %v", err)
	}
	if !strings.Contains(logs.Content, "mAP50=0.85") || logs.NextOffset <= 0 {
		t.Errorf("unexpected logs: %+v", logs)
	}

	// 6. Node uploads best.pt, last.pt, and results.csv.
	bestData, err := os.ReadFile(checkpointFixturePath(t, "yolo11n-best.pt"))
	if err != nil {
		t.Fatal(err)
	}
	lastData, err := os.ReadFile(checkpointFixturePath(t, "yolo11n-last.pt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UploadArtifact(ctx, job.ID, nodeID, "weights/best.pt", bytes.NewReader(bestData)); err != nil {
		t.Fatalf("UploadArtifact best.pt error: %v", err)
	}
	if _, err := e.svc.UploadArtifact(ctx, job.ID, nodeID, "weights/last.pt", bytes.NewReader(lastData)); err != nil {
		t.Fatalf("UploadArtifact last.pt error: %v", err)
	}
	if _, err := e.svc.UploadArtifact(ctx, job.ID, nodeID, "results.csv", strings.NewReader("epoch,train/box_loss\n1,1.12\n")); err != nil {
		t.Fatalf("UploadArtifact results.csv error: %v", err)
	}
	if _, err := e.svc.UploadArtifact(ctx, job.ID, nodeID, "../evil.pt", bytes.NewReader(bestData)); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected path traversal UploadArtifact to fail with ErrInvalid, got %v", err)
	}

	// 7. CompleteTask triggers auto-registration of weights/best.pt into models!
	doneJob, err := e.svc.CompleteTask(ctx, job.ID, nodeID, json.RawMessage(`{"ultralytics_version":"8.4.198","device":"cuda:0"}`))
	if err != nil {
		t.Fatalf("CompleteTask error: %v", err)
	}
	if doneJob.Status != StatusSucceeded || doneJob.BestModel == nil {
		t.Fatalf("expected succeeded job with auto-registered BestModel, got %+v", doneJob)
	}
	if doneJob.Metrics.BestEpoch == nil || *doneJob.Metrics.BestEpoch != 2 {
		t.Errorf("BestEpoch = %v, want 2", doneJob.Metrics.BestEpoch)
	}
	if len(doneJob.Epochs) != 2 {
		t.Errorf("len(Epochs) = %d, want 2", len(doneJob.Epochs))
	}

	// Verify registered model metadata in model repository.
	regBest, err := e.models.GetModel(ctx, doneJob.BestModel.ID)
	if err != nil {
		t.Fatalf("GetModel(best) error: %v", err)
	}
	if regBest.Source != model.SourceTraining || regBest.TrainingJobID == nil || *regBest.TrainingJobID != job.ID {
		t.Errorf("unexpected registered model source/job_id: %+v", regBest)
	}

	// 8. Manually register last.pt as well, and verify resume validation!
	regLast, err := e.svc.RegisterModel(ctx, job.ID, RegisterModelInput{Variant: VariantLast, Description: "断点检查点"})
	if err != nil {
		t.Fatalf("RegisterModel(last) error: %v", err)
	}
	if !regLast.Info.TrainingCheckpoint {
		t.Errorf("expected last.pt to have TrainingCheckpoint=true")
	}
	// Duplicate registration of last.pt returns already-registered model or conflict? Let's verify ErrConflict!
	if _, err := e.svc.RegisterModel(ctx, job.ID, RegisterModelInput{Variant: VariantLast}); !errors.Is(err, ErrConflict) {
		t.Errorf("expected second RegisterModel(last) to return ErrConflict, got %v", err)
	}

	// Resume mode rejects best.pt (no optimizer checkpoint) and accepts last.pt!
	if _, err := e.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitResume,
		BaseModelID:   &regBest.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected InitResume with best.pt to fail with ErrInvalid, got %v", err)
	}
	resumeJob, err := e.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitResume,
		BaseModelID:   &regLast.ID,
	})
	if err != nil {
		t.Fatalf("Create resume job with last.pt failed: %v", err)
	}
	if resumeJob.Params["resume"] != true {
		t.Errorf("resumeJob.Params[resume] = %v, want true", resumeJob.Params["resume"])
	}

	// Artifact download works.
	af, err := e.svc.OpenArtifact(ctx, job.ID, "results.csv")
	if err != nil {
		t.Fatalf("OpenArtifact error: %v", err)
	}
	csvBytes, _ := io.ReadAll(af.Content)
	af.Close()
	if !strings.Contains(string(csvBytes), "train/box_loss") {
		t.Errorf("unexpected artifact content: %q", string(csvBytes))
	}
}

func TestCancelRetryAndDelete(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	proj := e.createProject(t, "取消与重试测试")
	dvID := e.insertReadyDataVersion(t, proj.ID, 1)
	nodeID := e.insertNode(t, "gpu-node-2")

	job, err := e.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitScratch,
		YOLOVersion:   "yolo26",
		ModelScale:    "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Claim so status becomes running.
	if _, err := e.svc.Claim(ctx, nodeID); err != nil {
		t.Fatal(err)
	}

	// Retrying a running job is rejected with ErrConflict.
	if _, err := e.svc.Retry(ctx, job.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Retry running job = %v, want ErrConflict", err)
	}

	// Cancel running job -> node's next ReportProgress gets ErrConflict (409).
	canceled, err := e.svc.Cancel(ctx, job.ID)
	if err != nil {
		t.Fatalf("Cancel error: %v", err)
	}
	if canceled.Status != StatusCanceled {
		t.Fatalf("Status = %q, want canceled", canceled.Status)
	}
	if _, err := e.svc.ReportProgress(ctx, job.ID, nodeID, ProgressInput{Stage: StageTraining}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ReportProgress after cancel = %v, want ErrConflict", err)
	}

	// Retry puts job back to queued.
	retried, err := e.svc.Retry(ctx, job.ID)
	if err != nil {
		t.Fatalf("Retry error: %v", err)
	}
	if retried.Status != StatusQueued {
		t.Fatalf("Status after Retry = %q, want queued", retried.Status)
	}

	// Delete job succeeds (and any stale node progress gets ErrNotFound), and DataVersion can now be deleted cleanly!
	if err := e.svc.Delete(ctx, job.ID); err != nil {
		t.Fatalf("Delete job error: %v", err)
	}
	if _, err := e.svc.ReportProgress(ctx, job.ID, nodeID, ProgressInput{Stage: StageTraining}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReportProgress after delete = %v, want ErrNotFound", err)
	}
	if err := e.dataVersions.Delete(ctx, dvID); err != nil {
		t.Fatalf("DataVersion Delete after job removal failed: %v", err)
	}
}
