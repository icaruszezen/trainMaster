package httpapi

import (
	"context"
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
	"github.com/icaruszezen/trainmaster/backend/internal/training"
)

type trainingEnv struct {
	*apiEnv
	db           *database.DB
	projects     *project.Service
	dataVersions *dataversion.Service
	models       *model.Service
	training     *training.Service
}

func newTrainingEnv(t *testing.T, token string) *trainingEnv {
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
	tr := training.NewService(training.Options{
		DB:           db.Pool,
		Storage:      st,
		Logger:       log,
		Projects:     projects,
		DataVersions: dataVersions,
		Models:       models,
	})
	t.Cleanup(func() {
		tr.WaitBackground()
		dataVersions.WaitBackground()
	})
	h := NewHandler(Options{
		Logger:       log,
		DB:           fakeDB{},
		Storage:      st,
		StartedAt:    time.Now(),
		Datasets:     datasets,
		Projects:     projects,
		DataVersions: dataVersions,
		Models:       models,
		Training:     tr,
		Compute:      compute.NewService(compute.Options{DB: db.Pool, Logger: log, HeartbeatInterval: 10 * time.Second}),
		NodeToken:    token,
	})
	return &trainingEnv{
		apiEnv:       &apiEnv{h: h, st: st},
		db:           db,
		projects:     projects,
		dataVersions: dataVersions,
		models:       models,
		training:     tr,
	}
}

func (e *trainingEnv) insertReadyVersion(t *testing.T, projectID int64) int64 {
	t.Helper()
	ctx := context.Background()
	dirName := fmt.Sprintf("data-versions/project-%d/v1", projectID)
	dir := filepath.Join(e.st.Dir(storage.Exports), filepath.FromSlash(dirName))
	classes := []dataversion.Class{{Name: "scratch", Color: "#ef4444"}}
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
		) VALUES ($1, 1, $2, 'v1', 'ready', $3, $4, $5, 2, 2, 1, 1, 0, $6) RETURNING id`,
		projectID, dirName, classes, cfg, sources, now).Scan(&dvID); err != nil {
		t.Fatal(err)
	}
	jpg := testJPEG(t, 32, 24)
	for i, sp := range []struct{ split, rel string }{{"train", "images/line_a/a.jpg"}, {"val", "images/line_a/b.jpg"}} {
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
		objs := []dataset.AnnotationObject{{Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 16, Ymax: 16}}
		if _, err := e.db.Exec(ctx, `INSERT INTO data_version_images (
				version_id, position, split, split_by, path, size_bytes, mod_time, width, height,
				objects, object_count, labels, dataset_id, source_path, source_size, source_mod_time,
				annotation_source, augment, stored
			) VALUES ($1, $2, $3, 'ratio', $4, $5, $6, 32, 24, $7, 1, '{"scratch":1}', 1, $8, $5, $6, 0, '[]', 'link')`,
			dvID, i, sp.split, sp.rel, fi.Size(), fi.ModTime(), objs, filepath.Base(sp.rel)); err != nil {
			t.Fatal(err)
		}
	}
	return dvID
}

func TestTrainingHTTPFlow(t *testing.T) {
	const token = "s3cr3t-training-node-token"
	const nodeKey = "node-key-trainer-01"
	auth := []string{"Authorization", "Bearer " + token, "X-Node-Key", nodeKey}
	e := newTrainingEnv(t, token)

	// 1. GET /api/v1/training/params-schema returns all 86 YOLO parameters.
	schema := want[training.ParamsSchemaResponse](t, e.do("GET", "/api/v1/training/params-schema", nil), http.StatusOK)
	if len(schema.Specs) != 86 || len(schema.Groups) != 4 {
		t.Fatalf("params-schema = %d specs, %d groups; want 86, 4", len(schema.Specs), len(schema.Groups))
	}

	// 2. Create project + ready data version.
	proj := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": "PCB焊点检测"}), http.StatusCreated)
	dvID := e.insertReadyVersion(t, proj.ID)

	// Validation error on bad parameter.
	wantError(t, e.do("POST", "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dvID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"params":          map[string]any{"epochs": -5},
	}), http.StatusUnprocessableEntity, codeValidationFailed, "epochs")

	// 3. Create valid training job.
	job := want[training.Job](t, e.do("POST", "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dvID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"model_scale":     "n",
		"params": map[string]any{
			"epochs":    5,
			"batch":     16,
			"optimizer": "AdamW",
			"lr0":       0.001,
		},
	}), http.StatusCreated)
	if job.Status != training.StatusQueued || job.TotalEpochs != 5 {
		t.Fatalf("created job = %+v", job)
	}

	// List & PATCH job.
	list := want[List[training.Job]](t, e.do("GET", fmt.Sprintf("/api/v1/training/jobs?project_id=%d", proj.ID), nil), http.StatusOK)
	if list.Total != 1 || list.Items[0].ID != job.ID {
		t.Fatalf("list = %+v", list)
	}
	patched := want[training.Job](t, e.do("PATCH", fmt.Sprintf("/api/v1/training/jobs/%d", job.ID), map[string]any{
		"name":        "PCB焊点 YOLO11n 实验1",
		"description": "AdamW lr0=0.001",
	}), http.StatusOK)
	if patched.Name != "PCB焊点 YOLO11n 实验1" {
		t.Errorf("patched.Name = %q", patched.Name)
	}

	// 4. Register compute node and claim training_job task.
	want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/register", map[string]any{
		"node_key": nodeKey,
		"name":     "gpu-worker-1",
		"hostname": "gpu-box",
		"hardware": map[string]any{},
	}, "Authorization", "Bearer "+token), http.StatusOK)

	claimResp := want[struct {
		Task *training.Task `json:"task"`
	}](t, e.do("POST", "/api/v1/compute/agent/tasks/claim", map[string]any{
		"node_key":     nodeKey,
		"kinds":        []string{"training_job"},
		"wait_seconds": 0,
	}, "Authorization", "Bearer "+token), http.StatusOK)
	if claimResp.Task == nil || claimResp.Task.ID != job.ID {
		t.Fatalf("claimed task = %+v", claimResp.Task)
	}
	tk := claimResp.Task

	// 5. Download dataset zip, stream progress & logs, upload best.pt & plot, complete.
	dsRec := e.do("GET", tk.DatasetURL, nil, auth...)
	if dsRec.Code != http.StatusOK || dsRec.Body.Len() == 0 {
		t.Fatalf("dataset download code=%d len=%d", dsRec.Code, dsRec.Body.Len())
	}

	want[map[string]any](t, e.do("POST", tk.ProgressURL, map[string]any{
		"stage":         "training",
		"current_epoch": 5,
		"total_epochs":  5,
		"epochs": []map[string]any{
			{"epoch": 5, "total_epochs": 5, "map50": 0.91, "map50_95": 0.82},
		},
		"log_chunk": "5/5 epochs completed, mAP50=0.91\n",
	}, auth...), http.StatusOK)

	bestBytes, err := os.ReadFile(filepath.Join("..", "model", "checkpoint", "testdata", "yolo11n-best.pt"))
	if err != nil {
		t.Fatal(err)
	}
	want[training.Artifact](t, e.do("PUT", tk.ArtifactsURL+"?path=weights/best.pt", bestBytes, auth...), http.StatusOK)
	want[training.Artifact](t, e.do("PUT", tk.ArtifactsURL+"?path=results.png", testPNG(t, 64, 48), auth...), http.StatusOK)

	completed := want[training.Job](t, e.do("POST", tk.CompleteURL, map[string]any{
		"result": map[string]any{"ultralytics_version": "8.4.198"},
	}, auth...), http.StatusOK)
	if completed.Status != training.StatusSucceeded || completed.BestModel == nil {
		t.Fatalf("completed job = %+v", completed)
	}

	// 6. Public logs and artifact retrieval.
	logSlice := want[training.LogSlice](t, e.do("GET", fmt.Sprintf("/api/v1/training/jobs/%d/logs", job.ID), nil), http.StatusOK)
	if !strings.Contains(logSlice.Content, "mAP50=0.91") {
		t.Errorf("logs = %+v", logSlice)
	}
	plotRec := e.do("GET", fmt.Sprintf("/api/v1/training/jobs/%d/artifacts/results.png", job.ID), nil)
	if plotRec.Code != http.StatusOK || plotRec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("artifact GET status=%d ct=%q", plotRec.Code, plotRec.Header().Get("Content-Type"))
	}

	// 7. Project delete protection without delete_training_jobs=true -> 409; with flag -> 204!
	wantError(t, e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=true&delete_data_versions=true", proj.ID), nil),
		http.StatusConflict, codeConflict, "训练任务")
	delRec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=true&delete_data_versions=true&delete_training_jobs=true", proj.ID), nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE project with delete_training_jobs=true status=%d body=%s", delRec.Code, delRec.Body.String())
	}
}
