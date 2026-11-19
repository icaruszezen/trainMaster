package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/icaruszezen/trainmaster/backend/internal/evaluation"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

type evalEnv struct {
	*apiEnv
	db           *database.DB
	datasets     *dataset.Service
	projects     *project.Service
	dataVersions *dataversion.Service
	models       *model.Service
	evaluations  *evaluation.Service
}

func newEvalEnv(t *testing.T, token string) *evalEnv {
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
	dataVersions := dataversion.NewService(dataversion.Options{
		DB: db.Pool, Storage: st, Logger: log, Datasets: datasets, Projects: projects,
	})
	// Custom inspect that reads class names encoded in fake weights JSON.
	inspect := func(r io.ReaderAt, size int64) (*checkpoint.Info, error) {
		buf := make([]byte, size)
		if _, err := r.ReadAt(buf, 0); err != nil && err != io.EOF {
			return nil, err
		}
		var meta struct {
			YOLOVersion string   `json:"yolo_version"`
			Task        string   `json:"task"`
			Scale       string   `json:"scale"`
			ClassNames  []string `json:"class_names"`
		}
		if err := json.Unmarshal(buf, &meta); err != nil {
			return fakeInspect(r, size)
		}
		return &checkpoint.Info{
			YOLOVersion: meta.YOLOVersion,
			Task:        meta.Task,
			Scale:       meta.Scale,
			ImageSize:   640,
			ClassNames:  meta.ClassNames,
		}, nil
	}
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log, Inspect: inspect})
	evals := evaluation.NewService(evaluation.ServiceOptions{
		DB:           db.Pool,
		Storage:      st,
		Logger:       log,
		Projects:     projects,
		Datasets:     datasets,
		DataVersions: dataVersions,
		Models:       models,
	})
	t.Cleanup(func() {
		evals.Stop()
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
		Evaluations:  evals,
		Compute:      compute.NewService(compute.Options{DB: db.Pool, Logger: log, HeartbeatInterval: 10 * time.Second}),
		NodeToken:    token,
	})
	return &evalEnv{
		apiEnv:       &apiEnv{h: h, st: st},
		db:           db,
		datasets:     datasets,
		projects:     projects,
		dataVersions: dataVersions,
		models:       models,
		evaluations:  evals,
	}
}

func (e *evalEnv) uploadModelWithClasses(t *testing.T, projectID int64, task, scale string, classes []string) model.Model {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"yolo_version": "yolo11",
		"task":         task,
		"scale":        scale,
		"class_names":  classes,
	})
	if err != nil {
		t.Fatal(err)
	}
	up := want[model.Upload](t, e.do("POST", "/api/v1/models/uploads", map[string]any{
		"project_id":   projectID,
		"yolo_version": "yolo11",
		"file_name":    "best.pt",
		"file_size":    len(payload),
	}), http.StatusCreated)
	want[model.Upload](t, e.do("PUT", fmt.Sprintf("/api/v1/models/uploads/%d?offset=0", up.ID), payload), http.StatusOK)
	return want[model.Model](t, e.do("POST", fmt.Sprintf("/api/v1/models/uploads/%d/commit", up.ID), nil), http.StatusCreated)
}

func (e *evalEnv) insertEvalDataVersion(t *testing.T, projectID int64) int64 {
	t.Helper()
	ctx := context.Background()
	dirName := fmt.Sprintf("data-versions/project-%d/v1", projectID)
	dir := filepath.Join(e.st.Dir(storage.Exports), filepath.FromSlash(dirName))
	classes := []dataversion.Class{
		{Name: "scratch", Color: "#2563eb"},
		{Name: "dent", Color: "#10b981"},
	}
	sources := []dataversion.Source{{DatasetID: 1, DatasetName: "cam_top", DirName: "cam_top", IncludeBase: true}}
	cfg := dataversion.Config{
		Split:        dataversion.SplitConfig{Train: 50, Val: 50, Seed: 42, GroupBy: "stem"},
		IgnoreLabels: []string{},
	}
	now := time.Now()
	var dvID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO data_versions (
			project_id, version, dir_name, note, status, classes, config, sources,
			image_count, object_count, train_count, val_count, test_count, finished_at
		) VALUES ($1, 1, $2, 'v1', 'ready', $3, $4, $5, 3, 2, 0, 3, 0, $6) RETURNING id`,
		projectID, dirName, classes, cfg, sources, now).Scan(&dvID); err != nil {
		t.Fatal(err)
	}

	jpg := testJPEG(t, 128, 96)
	type dvSample struct {
		rel    string
		objs   []dataset.AnnotationObject
		labels string
	}
	dvSamples := []dvSample{
		{
			rel:    "images/val/ng_01.jpg",
			objs:   []dataset.AnnotationObject{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 60, Ymax: 60}},
			labels: `{"scratch":1}`,
		},
		{
			rel:    "images/val/ng_02.jpg",
			objs:   []dataset.AnnotationObject{{Name: "dent", Xmin: 20, Ymin: 20, Xmax: 80, Ymax: 70}},
			labels: `{"dent":1}`,
		},
		{
			rel:    "images/val/ok_01.jpg",
			objs:   []dataset.AnnotationObject{},
			labels: `{}`,
		},
	}
	for i, sp := range dvSamples {
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
		if _, err := e.db.Exec(ctx, `INSERT INTO data_version_images (
				version_id, position, split, split_by, path, size_bytes, mod_time, width, height,
				objects, object_count, labels, dataset_id, source_path, source_size, source_mod_time,
				annotation_source, augment, stored
			) VALUES ($1, $2, 'val', 'ratio', $3, $4, $5, 128, 96, $6, $7, $8::jsonb, 1, $9, $4, $5, 0, '[]', 'link')`,
			dvID, i, sp.rel, fi.Size(), fi.ModTime(), sp.objs, len(sp.objs), sp.labels, filepath.Base(sp.rel)); err != nil {
			t.Fatal(err)
		}
	}
	return dvID
}

func (e *evalEnv) postGzipJSON(t *testing.T, url string, payload any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestEvaluationFullHTTPFlow(t *testing.T) {
	const token = "eval-agent-token"
	const nodeKey = "eval-node-01"
	auth := []string{"Authorization", "Bearer " + token, "X-Node-Key", nodeKey}
	e := newEvalEnv(t, token)

	// 1. Setup project, 2 models (m2 has an extra model-only class), and a DataVersion.
	proj := want[project.Project](t, e.do("POST", "/api/v1/projects", map[string]any{"name": "金属外壳质检"}), http.StatusCreated)
	m1 := e.uploadModelWithClasses(t, proj.ID, "detect", "n", []string{"scratch", "dent"})
	m2 := e.uploadModelWithClasses(t, proj.ID, "detect", "m", []string{"scratch", "dent", "ghost"})
	dvID := e.insertEvalDataVersion(t, proj.ID)

	// 2. Preview checks sample counts and class alignment badges.
	prev := want[evaluation.PreviewResult](t, e.do("POST", "/api/v1/evaluations/preview", map[string]any{
		"model_ids": []int64{m1.ID, m2.ID},
		"source": map[string]any{
			"type":            "data_version",
			"data_version_id": dvID,
			"splits":          []string{"val"},
		},
	}), http.StatusOK)
	if prev.Source.TotalImages != 3 || prev.Source.OKImages != 1 || prev.Source.NGImages != 2 {
		t.Fatalf("preview source counts = %+v, want 3 total (1 OK, 2 NG)", prev.Source)
	}
	if len(prev.Models) != 2 || prev.Models[0].MatchedCount != 2 || prev.Models[1].ModelOnlyCount != 1 {
		t.Fatalf("preview alignment = %+v", prev.Models)
	}

	// 3. Batch create 2 evaluations on the same DataVersion.
	created := want[List[evaluation.Evaluation]](t, e.do("POST", "/api/v1/evaluations", map[string]any{
		"model_ids": []int64{m1.ID, m2.ID},
		"name":      "v1 验证对比",
		"source": map[string]any{
			"type":            "data_version",
			"data_version_id": dvID,
			"splits":          []string{"val"},
		},
		"options": map[string]any{"conf": 0.25, "iou": 0.50, "batch": 8},
	}), http.StatusCreated)
	if created.Total != 2 || created.Items[0].SourceFingerprint != created.Items[1].SourceFingerprint {
		t.Fatalf("batch create failed or fingerprint mismatch: %+v", created)
	}
	eval1ID, eval2ID := created.Items[0].ID, created.Items[1].ID

	// 4. Register compute node and execute both evaluation tasks via agent protocol.
	regRec := e.do("POST", "/api/v1/compute/agent/register", map[string]any{
		"node_key": nodeKey, "name": "GPU-01", "hostname": "gpu-01", "hardware": map[string]any{},
	}, "Authorization", "Bearer "+token)
	if regRec.Code != http.StatusOK {
		t.Fatalf("register node = %d: %s", regRec.Code, regRec.Body)
	}

	runAgent := func(wantEvalID int64, buildPreds func(imgs []evaluation.TaskImage) []evaluation.StoredImagePrediction) evaluation.Evaluation {
		claimResp := want[struct {
			Task *evaluation.Task `json:"task"`
		}](t, e.do("POST", "/api/v1/compute/agent/tasks/claim", map[string]any{
			"node_key":     nodeKey,
			"kinds":        []string{"model_evaluation"},
			"wait_seconds": 0,
		}, "Authorization", "Bearer "+token), http.StatusOK)
		if claimResp.Task == nil || claimResp.Task.Kind != evaluation.TaskKind || claimResp.Task.ID != wantEvalID {
			t.Fatalf("claimed task = %+v, want (%s, %d)", claimResp.Task, evaluation.TaskKind, wantEvalID)
		}
		task := *claimResp.Task
		// Download model weights.
		if rec := e.do("GET", task.ModelURL, nil, auth...); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("download model = %d len=%d", rec.Code, rec.Body.Len())
		}
		// Paginate images (limit=2 -> 2 pages for 3 images).
		var allImgs []evaluation.TaskImage
		var after int64
		for {
			page := want[evaluation.TaskImagePage](t, e.do("GET", fmt.Sprintf("%s?after=%d&limit=2", task.ImagesURL, after), nil, auth...), http.StatusOK)
			allImgs = append(allImgs, page.Items...)
			if !page.HasMore {
				break
			}
			after = page.NextID
		}
		if len(allImgs) != 3 {
			t.Fatalf("task images len = %d, want 3", len(allImgs))
		}
		// Download one image over HTTP.
		if rec := e.do("GET", allImgs[0].FileURL, nil, auth...); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
			t.Fatalf("download task image = %d ct=%s", rec.Code, rec.Header().Get("Content-Type"))
		}
		// Report progress.
		want[leaseResponse](t, e.do("POST", task.ProgressURL, map[string]any{
			"stage": "inferring", "progress_done": 2, "progress_total": 3, "eta_seconds": 1,
		}, auth...), http.StatusOK)

		// Upload gzipped predictions + speed metrics.
		preds := buildPreds(allImgs)
		resRec := e.postGzipJSON(t, task.ResultsURL, evaluation.UploadResultsPayload{
			Curves: evaluation.CurvesData{
				Speed: &evaluation.SpeedMetrics{PreprocessMs: 1.1, InferenceMs: 4.2, PostprocessMs: 0.7, TotalMs: 6.0, FPS: 166.7, Device: "cuda:0", BatchSize: 8, ImgSize: [2]int{640, 640}},
			},
			Predictions: preds,
		}, auth...)
		if resRec.Code != http.StatusOK {
			t.Fatalf("upload results = %d: %s", resRec.Code, resRec.Body)
		}
		return want[evaluation.Evaluation](t, e.do("POST", task.CompleteURL, map[string]any{"result": map[string]any{"ok": true}}, auth...), http.StatusOK)
	}

	// Eval 1 (baseline): hits scratch TP, misses dent FN (escape), and has one conf=0.40 FP on ok_01 (overkill).
	ev1 := runAgent(eval1ID, func(imgs []evaluation.TaskImage) []evaluation.StoredImagePrediction {
		return []evaluation.StoredImagePrediction{
			{SampleID: imgs[0].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 60, Ymax: 60, Conf: 0.90}}},
			{SampleID: imgs[1].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{}},                                                                    // Escape
			{SampleID: imgs[2].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{{Name: "scratch", Xmin: 5, Ymin: 5, Xmax: 30, Ymax: 30, Conf: 0.40}}}, // Overkill at 0.25
		}
	})
	if ev1.Status != evaluation.StatusSucceeded || ev1.Summary.OKNG.Overkill != 1 || ev1.Summary.OKNG.Escape != 1 {
		t.Fatalf("eval1 summary = %+v", ev1.Summary)
	}

	// Eval 2 (candidate): perfect on all 3 images!
	ev2 := runAgent(eval2ID, func(imgs []evaluation.TaskImage) []evaluation.StoredImagePrediction {
		return []evaluation.StoredImagePrediction{
			{SampleID: imgs[0].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 60, Ymax: 60, Conf: 0.95}}},
			{SampleID: imgs[1].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{{Name: "dent", Xmin: 20, Ymin: 20, Xmax: 80, Ymax: 70, Conf: 0.88}}},
			{SampleID: imgs[2].ID, Width: 128, Height: 96, Predictions: []evaluation.PredBox{}},
		}
	})
	if ev2.Status != evaluation.StatusSucceeded || ev2.Summary.TP != 2 || ev2.Summary.FP != 0 || ev2.Summary.FN != 0 {
		t.Fatalf("eval2 summary = %+v", ev2.Summary)
	}

	// 5. Real-time threshold sliding on eval1: conf=0.50 eliminates overkill on ok_01.
	anaLow := want[evaluation.AnalysisResult](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/%d/analysis?conf=0.25&iou=0.50", eval1ID), nil), http.StatusOK)
	anaHigh := want[evaluation.AnalysisResult](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/%d/analysis?conf=0.50&iou=0.50", eval1ID), nil), http.StatusOK)
	if anaLow.OKNG.Overkill != 1 || anaHigh.OKNG.Overkill != 0 {
		t.Fatalf("threshold slider mismatch: low overkill=%d high overkill=%d", anaLow.OKNG.Overkill, anaHigh.OKNG.Overkill)
	}

	// 6. Save tuned default threshold via PATCH and verify persisted summary updates.
	updated := want[evaluation.Evaluation](t, e.do("PATCH", fmt.Sprintf("/api/v1/evaluations/%d", eval1ID), map[string]any{
		"name":         "基线模型 v1 (已调优)",
		"default_conf": 0.55,
		"default_iou":  0.50,
	}), http.StatusOK)
	if updated.Name != "基线模型 v1 (已调优)" || updated.Options.Conf != 0.55 || updated.Summary.OKNG.Overkill != 0 {
		t.Fatalf("PATCH save default threshold failed: %+v", updated)
	}

	// 7. Sample filtering + confusion-matrix drilldown (background -> scratch FP at conf=0.25) + detail + image serving.
	fpSamples := want[List[evaluation.SampleItem]](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/%d/images?conf=0.25&gt_class=background&pred_class=scratch", eval1ID), nil), http.StatusOK)
	if fpSamples.Total != 1 || fpSamples.Items[0].FileName != "ok_01.jpg" {
		t.Fatalf("confusion drilldown = %+v, want ok_01.jpg", fpSamples)
	}
	sampleDetail := want[evaluation.SampleDetail](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/%d/images/%d?conf=0.25", eval1ID, fpSamples.Items[0].ID), nil), http.StatusOK)
	if len(sampleDetail.Predictions) != 1 || sampleDetail.Predictions[0].Match != evaluation.MatchFP {
		t.Fatalf("sample detail predictions = %+v", sampleDetail.Predictions)
	}
	if rec := e.do("GET", sampleDetail.FileURL, nil); rec.Code != http.StatusOK {
		t.Fatalf("serve sample image = %d", rec.Code)
	}
	if rec := e.do("GET", sampleDetail.ThumbnailURL+"?size=sm", nil); rec.Code != http.StatusOK {
		t.Fatalf("serve sample thumbnail = %d", rec.Code)
	}

	// 8. Multi-model comparison & same-source per-image diff.
	cmpRes := want[evaluation.CompareResult](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/compare?ids=%d,%d&mode=unified&conf=0.25&iou=0.50", eval1ID, eval2ID), nil), http.StatusOK)
	if !cmpRes.SameSource || len(cmpRes.Items) != 2 || cmpRes.Items[0].PRAll == nil {
		t.Fatalf("compare result invalid: %+v", cmpRes)
	}
	diffRes := want[evaluation.CompareDiffResult](t, e.do("GET", fmt.Sprintf("/api/v1/evaluations/compare/diff?base_id=%d&target_id=%d&mode=unified&conf=0.25&iou=0.50&diff_type=changed", eval1ID, eval2ID), nil), http.StatusOK)
	if diffRes.Total != 2 || diffRes.Counts["improved"] != 1 || diffRes.Counts["fp_reduced"] != 1 {
		t.Fatalf("compare diff = total %d counts=%+v", diffRes.Total, diffRes.Counts)
	}

	// 9. Report exports (single HTML/JSON/CSV + compare HTML/CSV).
	for _, tc := range []struct {
		url      string
		wantCT   string
		contains string
	}{
		{fmt.Sprintf("/api/v1/evaluations/%d/export?format=html&inline=true", eval1ID), "text/html", "<svg"},
		{fmt.Sprintf("/api/v1/evaluations/%d/export?format=json", eval1ID), "application/json", `"analysis"`},
		{fmt.Sprintf("/api/v1/evaluations/%d/export?format=classes_csv", eval1ID), "text/csv", "scratch"},
		{fmt.Sprintf("/api/v1/evaluations/%d/export?format=samples_csv&conf=0.25", eval1ID), "text/csv", "ok_01.jpg"},
		{fmt.Sprintf("/api/v1/evaluations/compare/export?ids=%d,%d&format=html", eval1ID, eval2ID), "text/html", "<svg"},
		{fmt.Sprintf("/api/v1/evaluations/compare/export?ids=%d,%d&format=csv", eval1ID, eval2ID), "text/csv", "mAP50"},
	} {
		rec := e.do("GET", tc.url, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), tc.wantCT) || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Fatalf("export %s failed: code=%d ct=%s", tc.url, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	// 10. Dataset-source evaluation + cancel + retry.
	voc := `<annotation><size><width>64</width><height>48</height></size><object><name>scratch</name><bndbox><xmin>4</xmin><ymin>4</ymin><xmax>30</xmax><ymax>30</ymax></bndbox></object></annotation>`
	ds := e.registerFolder(t, "line_b", map[string]string{
		"a.png": string(testPNG(t, 64, 48)),
		"a.xml": voc,
		"b.png": string(testPNG(t, 64, 48)), // unannotated OK background image
	})
	dsCreated := want[List[evaluation.Evaluation]](t, e.do("POST", "/api/v1/evaluations", map[string]any{
		"model_ids": []int64{m1.ID},
		"source": map[string]any{
			"type": "datasets",
			"datasets": []map[string]any{
				{"dataset_id": ds.ID, "include_base": true, "image_mode": "all"},
			},
		},
	}), http.StatusCreated)
	dsEvalID := dsCreated.Items[0].ID
	if dsCreated.Items[0].Source.TotalImages != 2 || dsCreated.Items[0].Source.OKImages != 1 || dsCreated.Items[0].Source.NGImages != 1 {
		t.Fatalf("dataset eval source counts = %+v", dsCreated.Items[0].Source)
	}
	canceled := want[evaluation.Evaluation](t, e.do("POST", fmt.Sprintf("/api/v1/evaluations/%d/cancel", dsEvalID), nil), http.StatusOK)
	if canceled.Status != evaluation.StatusCanceled {
		t.Fatalf("cancel status = %s", canceled.Status)
	}
	retried := want[evaluation.Evaluation](t, e.do("POST", fmt.Sprintf("/api/v1/evaluations/%d/retry", dsEvalID), nil), http.StatusOK)
	if retried.Status != evaluation.StatusQueued {
		t.Fatalf("retry status = %s", retried.Status)
	}

	// 11. Project deletion protection: fails without delete_evaluations=true, succeeds with it.
	wantError(t, e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=true&delete_data_versions=true", proj.ID), nil),
		http.StatusConflict, codeConflict, "评估任务")
	delRec := e.do("DELETE", fmt.Sprintf("/api/v1/projects/%d?delete_models=true&delete_data_versions=true&delete_evaluations=true", proj.ID), nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete project with evaluations = %d: %s", delRec.Code, delRec.Body)
	}
	// Verify runs/evaluations/eval-<id> dir was cleaned up.
	evalDir := filepath.Join(e.st.Dir(storage.Runs), "evaluations", fmt.Sprintf("eval-%d", eval1ID))
	if _, err := os.Stat(evalDir); !os.IsNotExist(err) {
		t.Fatalf("expected evaluation run dir %s to be deleted", evalDir)
	}
}
