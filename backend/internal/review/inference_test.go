package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// expireLease makes the lease of a running review run out.
func (e *testEnv) expireLease(t *testing.T, id int64) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(),
		"UPDATE reviews SET lease_expires_at = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationInference(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.project(t, "p")
	m := e.addModel(t, p, testModel{classes: []string{"scratch", "dent"}})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png", w: 100, h: 80}, testImage{rel: "b.png"},
		testImage{rel: "c.png", w: -1}, testImage{rel: "d.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	node, other := e.node(t, "a"), e.node(t, "b")

	// A node offering other kinds gets nothing.
	if task, err := e.models.ClaimTask(ctx, node, []string{model.TaskModelExport}, 0); task != nil || err != nil {
		t.Fatalf("export-only claim = %#v, %v", task, err)
	}
	task := e.claim(t, node)
	if task == nil {
		t.Fatal("no task")
	}
	base := "/api/v1/compute/agent/review-inferences/" + strings.TrimSpace(formatID(r.ID)) + "/"
	if task.Kind != TaskKind || task.ID != r.ID || task.LeaseSeconds != 60 || task.ImageCount != 4 || task.Remaining != 4 ||
		task.Options != r.Options || task.ModelURL != base+"model" || task.ImagesURL != base+"images" ||
		task.ProgressURL != base+"progress" || task.ResultsURL != base+"results" || task.CompleteURL != base+"complete" ||
		task.FailURL != base+"fail" {
		t.Errorf("task = %+v", task)
	}
	if task.Model.ID != m.ID || task.Model.SizeBytes != m.SizeBytes || task.Model.SHA256 != m.SHA256 ||
		!slices.Equal(task.Model.ClassNames, []string{"scratch", "dent"}) || task.Model.Task != "detect" {
		t.Errorf("task model = %+v", task.Model)
	}
	if again := e.claim(t, other); again != nil {
		t.Errorf("second claim = %+v", again)
	}
	got := e.review(t, r.ID)
	if got.Status != StatusRunning || got.Stage != StageDownloading || got.Attempts != 1 || got.Node == nil ||
		got.Node.ID != node || got.Node.Name != "a" || got.StartedAt == nil || got.QueuePosition != nil ||
		got.WorkersOnline != 2 {
		t.Errorf("running review = %+v", got)
	}

	// The weights.
	f, err := e.svc.OpenTaskModel(ctx, r.ID, node)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f.Content)
	f.Close()
	if int64(len(data)) != m.SizeBytes || f.Size != m.SizeBytes {
		t.Errorf("weights: %d bytes", len(data))
	}
	if _, err := e.svc.OpenTaskModel(ctx, r.ID, other); !errors.Is(err, errTaskNotYours) {
		t.Errorf("other node's download: %v", err)
	}

	// Paging the images.
	page, err := e.svc.ListTaskImages(ctx, r.ID, node, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Remaining != 4 || page.Items[0].FileName != "a.png" || *page.Items[0].Width != 100 ||
		page.Items[2].Width != nil || page.Items[0].SizeBytes <= 0 ||
		page.Items[0].URL != base+"images/"+formatID(page.Items[0].ID)+"/file" {
		t.Errorf("first page = %+v", page)
	}
	rest, err := e.svc.ListTaskImages(ctx, r.ID, node, page.Items[2].ID, 0)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].FileName != "d.png" {
		t.Errorf("second page = %+v, %v", rest, err)
	}
	if _, err := e.svc.ListTaskImages(ctx, r.ID, node, 0, 501); !errors.Is(err, ErrBadRequest) {
		t.Errorf("limit 501: %v", err)
	}
	ids := []int64{page.Items[0].ID, page.Items[1].ID, page.Items[2].ID, rest.Items[0].ID}

	// An image file.
	img, err := e.svc.OpenTaskImage(ctx, r.ID, node, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	img.File.Close()
	if img.Name != "a.png" || img.ContentType != "image/png" {
		t.Errorf("image file = %+v", img)
	}
	if _, err := e.svc.OpenTaskImage(ctx, r.ID, node, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown image: %v", err)
	}
	if err := os.Remove(e.datasetPath("ds", "d.png")); err != nil {
		t.Fatal(err)
	}
	// A file that cannot be read is not found, with the dataset's message
	// (the node fails that image and goes on), also when the dataset folder
	// is missing (a conflict would tell the node it lost the review).
	_, err = e.svc.OpenTaskImage(ctx, r.ID, node, ids[3])
	wantKind(t, err, ErrNotFound, "图片文件不存在")
	dsDir := filepath.Join(e.st.Dir(storage.Datasets), "ds")
	if err := os.Rename(dsDir, dsDir+"-moved"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenTaskImage(ctx, r.ID, node, ids[0])
	wantKind(t, err, ErrNotFound, "数据集文件夹不存在")
	if err := os.Rename(dsDir+"-moved", dsDir); err != nil {
		t.Fatal(err)
	}

	// Progress.
	if _, err := e.svc.ReportProgress(ctx, r.ID, node, ProgressInput{Stage: "converting"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad stage: %v", err)
	}
	if lease, err := e.svc.ReportProgress(ctx, r.ID, node, ProgressInput{Stage: StageInferring, Message: "已识别 0 / 4"}); err != nil || lease != 60 {
		t.Errorf("progress = %d, %v", lease, err)
	}
	if got := e.review(t, r.ID); got.Stage != StageInferring {
		t.Errorf("stage = %q", got.Stage)
	}

	// Results: clamped, cleaned, ordered by confidence; a failed image; ids
	// that are not the review's are ignored, repeats count once.
	results := []ImageResult{
		{ID: ids[0], OK: true, Width: 999, Height: 999, Boxes: []ResultBox{
			box("dent", 0.5, -5, 10.4, 50.6, 200),          // clamped to 0,10–51,80
			box("  scratch\n", 1.7, 10, 10, 20, 20),        // name cleaned, confidence 1
			box("", 0.9, 1, 1, 5, 5),                       // no name: dropped
			box("dent", 0.4, 120, 10, 130, 20),             // outside: no area
			box("dent", 0.123456, 1, 1, 1.4, 5),            // rounds to no width
			box(strings.Repeat("长", 300), 0.3, 1, 1, 9, 9), // long name cut
		}},
		{ID: ids[1], OK: false, Error: "无法读取图片（文件损坏）"},
		{ID: ids[1], OK: true, Boxes: []ResultBox{box("scratch", 0.9, 1, 1, 5, 5)}}, // repeat: ignored
		{ID: 99999, OK: true},
		{ID: ids[2], OK: true, Width: 64, Height: 48, Boxes: []ResultBox{box("scratch", 0.8, -1, 2, 1000, 47)}},
	}
	receipt, err := e.svc.SubmitResults(ctx, r.ID, node, results)
	if err != nil {
		t.Fatal(err)
	}
	if receipt != (ResultsReceipt{LeaseSeconds: 60, Accepted: 3, Remaining: 1}) {
		t.Errorf("receipt = %+v", receipt)
	}
	a := e.detail(t, r.ID, ids[0])
	wantPred := []Prediction{
		{Name: "scratch", Confidence: 1, Xmin: 10, Ymin: 10, Xmax: 20, Ymax: 20},
		{Name: "dent", Confidence: 0.5, Xmin: 0, Ymin: 10, Xmax: 51, Ymax: 80},
		{Name: strings.Repeat("长", 255), Confidence: 0.3, Xmin: 1, Ymin: 1, Xmax: 9, Ymax: 9},
	}
	if !slices.Equal(a.Predictions, wantPred) {
		t.Errorf("predictions = %+v", a.Predictions)
	}
	if !a.Inferred || a.InferenceError != "" || a.ObjectCount != 3 || a.PredictionCount != 3 || a.Edited ||
		len(a.Objects) != 3 || a.Objects[1].Name != "dent" || a.Objects[1].Xmax != 51 || a.AnnotationError != "" ||
		a.AnnotationVersion == "" || a.Width == nil || *a.Width != 100 || a.Revision != 0 || a.DatasetName != "ds" {
		t.Errorf("inferred image = %+v", a)
	}
	file := readFile(t, e.reviewFile(r.ID, a.AnnotationPath))
	if reviewfile.AnnotationVersion(file) != a.AnnotationVersion ||
		!strings.Contains(string(file), "<filename>a.png</filename>") || !strings.Contains(string(file), "<width>100</width>") {
		t.Errorf("VOC file:\n%s", file)
	}
	b := e.detail(t, r.ID, ids[1])
	if !b.Inferred || b.InferenceError != "无法读取图片（文件损坏）" || b.AnnotationVersion != "" || len(b.Objects) != 0 ||
		exists(e.reviewFile(r.ID, b.AnnotationPath)) {
		t.Errorf("failed image = %+v", b)
	}
	c := e.detail(t, r.ID, ids[2])
	if c.Width == nil || *c.Width != 64 || *c.Height != 48 || len(c.Predictions) != 1 ||
		c.Predictions[0] != (Prediction{Name: "scratch", Confidence: 0.8, Xmin: 0, Ymin: 2, Xmax: 64, Ymax: 47}) {
		t.Errorf("image of unknown size = %+v", c)
	}

	// Sending the same results again changes nothing.
	if again, err := e.svc.SubmitResults(ctx, r.ID, node, results); err != nil || again.Accepted != 0 || again.Remaining != 1 {
		t.Errorf("repeated results = %+v, %v", again, err)
	}
	if a2 := e.detail(t, r.ID, ids[0]); a2.AnnotationVersion != a.AnnotationVersion {
		t.Error("repeated results changed the file")
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, other, results); !errors.Is(err, errTaskNotYours) {
		t.Errorf("other node's results: %v", err)
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{{ID: 0}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, make([]ImageResult, 501)); !errors.Is(err, ErrInvalid) {
		t.Errorf("501 results: %v", err)
	}

	// Completing early is refused and queues it again (the node gives up
	// on the 409); the next claim finishes it.
	_, err = e.svc.CompleteTask(ctx, r.ID, node, nil)
	wantKind(t, err, ErrConflict, "还有 1 张图片没有识别，审图任务已重新排队")
	if got := e.review(t, r.ID); got.Status != StatusQueued || got.Node != nil || got.Attempts != 1 || got.FinishedAt != nil {
		t.Fatalf("review completed early = %+v", got)
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID || task.Remaining != 1 {
		t.Fatalf("claim after completing early = %+v", task)
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{{ID: ids[3], OK: false, Error: "图片文件不存在"}}); err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.CompleteTask(ctx, r.ID, node, json.RawMessage(`{"device": "cuda:0", "torch_version": "2.5.1",
		"ultralytics_version": "8.3.40", "node_name": "gpu-1", "elapsed_seconds": 12.34567, "images_per_second": -1, "extra": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	var res InferenceResult
	if err := json.Unmarshal(done.Result, &res); err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusReady || done.Stage != "" || done.FinishedAt == nil || res.Device != "cuda:0" ||
		res.TorchVersion != "2.5.1" || res.NodeName != "gpu-1" || res.ElapsedSeconds == nil || *res.ElapsedSeconds != 12.346 ||
		res.ImagesPerSecond != nil {
		t.Errorf("done = %+v, result %+v", done, res)
	}
	if done.Counts != (Counts{Total: 4, Inferred: 4, InferenceFailed: 2, Pending: 4}) {
		t.Errorf("counts = %+v", done.Counts)
	}
	// The node no longer holds it.
	if _, err := e.svc.ReportProgress(ctx, r.ID, node, ProgressInput{Stage: StageInferring}); !errors.Is(err, errTaskNotYours) {
		t.Errorf("progress after completion: %v", err)
	}
	if _, err := e.svc.CompleteTask(ctx, r.ID, node, nil); !errors.Is(err, errTaskNotYours) {
		t.Errorf("second completion: %v", err)
	}
}

func formatID(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

func (e *testEnv) datasetPath(dirName, rel string) string {
	return strings.Join([]string{e.st.Dir("datasets"), dirName, rel}, string(os.PathSeparator))
}

// replaceDatasetImage replaces a dataset image file by a w×h one and
// updates its index as a rescan does (the row and its ID stay).
func (e *testEnv) replaceDatasetImage(t *testing.T, datasetID int64, dirName, rel string, w, h int) {
	t.Helper()
	p := e.datasetPath(dirName, rel)
	if err := os.WriteFile(p, pngImage(t, w, h), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(context.Background(), `UPDATE dataset_images SET width = $3, height = $4, size_bytes = $5,
			mod_time = $6
		WHERE dataset_id = $1 AND rel_path = $2`, datasetID, rel, w, h, fi.Size(), time.UnixMicro(fi.ModTime().UnixMicro())); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationInferenceAfterRescan: an image replaced by a larger one
// (and rescanned) after the review was created takes the size of the new
// file, which is the one the node infers and people see.
func TestIntegrationInferenceAfterRescan(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png", w: 100, h: 80}, testImage{rel: "b.png", w: 100, h: 80})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	node := e.node(t, "a")
	if e.claim(t, node) == nil {
		t.Fatal("no task")
	}
	page, err := e.svc.ListTaskImages(ctx, r.ID, node, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := page.Items[0], page.Items[1]
	// b is inferred at the old size, then both files are replaced.
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{{ID: b.ID, OK: true, Width: 100, Height: 80,
		Boxes: []ResultBox{box("scratch", 0.9, 10, 10, 50, 50)}}}); err != nil {
		t.Fatal(err)
	}
	e.replaceDatasetImage(t, ds, "ds", "a.png", 200, 160)
	e.replaceDatasetImage(t, ds, "ds", "b.png", 200, 160)

	page, err = e.svc.ListTaskImages(ctx, r.ID, node, 0, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || *page.Items[0].Width != 200 || *page.Items[0].Height != 160 {
		t.Fatalf("images to infer = %+v, %v", page, err)
	}
	// The node infers the new file; what it reports is not what counts (the
	// index is).
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{{ID: a.ID, OK: true, Width: 999, Height: 999,
		Boxes: []ResultBox{box("scratch", 0.9, 10, 10, 50, 50), box("dent", 0.8, 120, 100, 190, 150), box("dent", 0.7, 120, 100, 300, 300)}}}); err != nil {
		t.Fatal(err)
	}
	got := e.detail(t, r.ID, a.ID)
	want := []Prediction{
		{Name: "scratch", Confidence: 0.9, Xmin: 10, Ymin: 10, Xmax: 50, Ymax: 50},
		{Name: "dent", Confidence: 0.8, Xmin: 120, Ymin: 100, Xmax: 190, Ymax: 150},
		{Name: "dent", Confidence: 0.7, Xmin: 120, Ymin: 100, Xmax: 200, Ymax: 160},
	}
	if !slices.Equal(got.Predictions, want) || got.Width == nil || *got.Width != 200 || *got.Height != 160 {
		t.Errorf("image inferred after a rescan = %+v", got)
	}
	file := string(readFile(t, e.reviewFile(r.ID, got.AnnotationPath)))
	if !strings.Contains(file, "<width>200</width>") || !strings.Contains(file, "<height>160</height>") {
		t.Errorf("VOC file:\n%s", file)
	}

	// b was inferred at the old size: a box drawn on the new image is kept,
	// and the image takes the new size.
	saved, err := e.svc.SaveAnnotation(ctx, r.ID, b.ID, SaveInput{Force: true, Objects: []dataset.AnnotationObject{
		{Name: "dent", Xmin: 120, Ymin: 100, Xmax: 190, Ymax: 150}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Objects) != 1 || saved.Objects[0].Xmax != 190 || saved.Width == nil || *saved.Width != 200 || *saved.Height != 160 {
		t.Errorf("saved after a rescan = %+v", saved)
	}
	file = string(readFile(t, e.reviewFile(r.ID, saved.AnnotationPath)))
	if !strings.Contains(file, "<width>200</width>") || !strings.Contains(file, "<xmax>190</xmax>") {
		t.Errorf("VOC file:\n%s", file)
	}
	if again := e.detail(t, r.ID, b.ID); again.Width == nil || *again.Width != 200 || again.AnnotationVersion != saved.AnnotationVersion {
		t.Errorf("image after the save = %+v", again)
	}
}

func TestIntegrationLeasesAndRetries(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"}, testImage{rel: "b.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	node := e.node(t, "a")

	// Lost leases queue the review again, until the third claim.
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		task := e.claim(t, node)
		if task == nil || task.ID != r.ID {
			t.Fatalf("attempt %d: claimed %+v", attempt, task)
		}
		if attempt == 1 {
			page, _ := e.svc.ListTaskImages(ctx, r.ID, node, 0, 0)
			if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{{ID: page.Items[0].ID, OK: true}}); err != nil {
				t.Fatal(err)
			}
		}
		e.expireLease(t, r.ID)
		if err := e.svc.expireLeases(ctx); err != nil {
			t.Fatal(err)
		}
		got := e.review(t, r.ID)
		if attempt < maxAttempts && (got.Status != StatusQueued || got.Node != nil || got.Attempts != attempt) {
			t.Errorf("after lost lease %d: %+v", attempt, got)
		}
		if attempt == maxAttempts && (got.Status != StatusFailed || got.Error != msgLeaseLost || got.FinishedAt == nil) {
			t.Errorf("after the last lost lease: %+v", got)
		}
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, nil); !errors.Is(err, errTaskNotYours) {
		t.Errorf("results after failure: %v", err)
	}

	// A retry queues it again and continues with the image left.
	if _, err := e.svc.RetryReview(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("retry of unknown review: %v", err)
	}
	retried, err := e.svc.RetryReview(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != StatusQueued || retried.Attempts != 0 || retried.Error != "" || retried.FinishedAt != nil ||
		retried.StartedAt != nil || retried.QueuePosition == nil || *retried.QueuePosition != 1 {
		t.Errorf("retried = %+v", retried)
	}
	_, err = e.svc.RetryReview(ctx, r.ID)
	wantKind(t, err, ErrConflict, "只有识别失败")
	task := e.claim(t, node)
	if task == nil || task.Remaining != 1 || task.ImageCount != 2 {
		t.Fatalf("claim after retry = %+v", task)
	}

	// A retryable failure queues it again; others fail it.
	got, err := e.svc.FailTask(ctx, r.ID, node, FailInput{Error: "网络错误", Retryable: true})
	if err != nil || got.Status != StatusQueued || got.Error != "" {
		t.Errorf("retryable failure = %+v, %v", got, err)
	}
	e.claim(t, node)
	got, err = e.svc.FailTask(ctx, r.ID, node, FailInput{Error: " 显存不足\x00 ", Detail: strings.Repeat("x", 10<<10)})
	if err != nil || got.Status != StatusFailed || got.Error != "显存不足" || len(got.ErrorDetail) != maxDetailBytes {
		t.Errorf("failure = %+v, %v", got.Status, err)
	}
	if _, err := e.svc.FailTask(ctx, r.ID, node, FailInput{}); !errors.Is(err, errTaskNotYours) {
		t.Errorf("second failure: %v", err)
	}
	r2, _ := e.svc.RetryReview(ctx, r.ID)
	e.claim(t, node)
	if got, _ := e.svc.FailTask(ctx, r2.ID, node, FailInput{}); got.Error != "识别失败（训练节点没有给出原因）" {
		t.Errorf("failure without a message = %q", got.Error)
	}
}

func TestIntegrationClaimSkipsUnusableModels(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.project(t, "p")
	deleted := e.addModel(t, p, testModel{})
	lost := e.addModel(t, p, testModel{})
	good := e.addModel(t, p, testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	r1 := e.create(t, CreateInput{ModelID: deleted.ID, DatasetIDs: []int64{ds}})
	r2 := e.create(t, CreateInput{ModelID: lost.ID, DatasetIDs: []int64{ds}})
	r3 := e.create(t, CreateInput{ModelID: good.ID, DatasetIDs: []int64{ds}})
	if err := e.models.DeleteModel(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.modelPath(lost)); err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "a")
	if task := e.claim(t, node); task == nil || task.ID != r3.ID {
		t.Fatalf("claimed %+v, want review %d", task, r3.ID)
	}
	// The review of a model in the recycle bin waits for it.
	if got := e.review(t, r1.ID); got.Status != StatusQueued || got.ModelExists {
		t.Errorf("review of a model in the bin = %+v", got)
	}
	if got := e.review(t, r2.ID); got.Status != StatusFailed || got.Error != msgModelFileMissing || !got.ModelExists {
		t.Errorf("review of a lost model file = %+v", got)
	}
	_, err := e.svc.RetryReview(ctx, r2.ID)
	wantKind(t, err, ErrConflict, "模型文件不存在")

	// The weights disappearing while the node holds the review fail it.
	if err := os.Remove(e.modelPath(good)); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenTaskModel(ctx, r3.ID, node)
	wantKind(t, err, ErrNotFound, msgModelFileMissing)
	if got := e.review(t, r3.ID); got.Status != StatusFailed || got.Error != msgModelFileMissing {
		t.Errorf("review after its weights vanished = %+v", got)
	}
}

func (e *testEnv) modelPath(m model.Model) string {
	return strings.Join([]string{e.st.Dir("models"), "project-" + formatID(m.Project.ID), "v" + formatID(int64(m.Version)),
		"model.pt"}, string(os.PathSeparator))
}

func TestIntegrationWaitingClaimAndBackground(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	node := e.node(t, "a")

	// A merge left running by a previous process fails at start; a review
	// whose node vanished is queued again by the background loop.
	old := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	var mergeID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, on_existing, status)
		VALUES ($1, $2, '{needs_sample}', 'skip', 'running') RETURNING id`, old.ID, []int64{ds}).Scan(&mergeID); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, old.ID); got.ActiveMerge == nil || got.ActiveMerge.ID != mergeID || got.ActiveMerge.Done != 0 {
		t.Errorf("active merge = %+v", got.ActiveMerge)
	}
	if e.claim(t, node) == nil {
		t.Fatal("the review was not claimed")
	}
	e.expireLease(t, old.ID)
	if err := e.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e.models.Start(ctx)
	defer e.models.Stop()
	defer e.svc.Stop()
	got := e.review(t, old.ID)
	if got.ActiveMerge != nil || got.LastMerge == nil || got.LastMerge.Status != MergeFailed ||
		got.LastMerge.Error != msgMergeInterrupted || got.LastMerge.FinishedAt == nil ||
		!slices.Equal(got.LastMerge.DatasetIDs, []int64{ds}) || !slices.Equal(got.LastMerge.Statuses, []string{"needs_sample"}) {
		t.Errorf("merge after restart = %+v", got.LastMerge)
	}
	claimed := make(chan any, 1)
	go func() {
		task, err := e.models.ClaimTask(ctx, node, []string{TaskKind}, 20*time.Second)
		if err != nil {
			t.Error(err)
		}
		claimed <- task
	}()
	select {
	case task := <-claimed:
		if rt, ok := task.(*Task); !ok || rt.ID != old.ID {
			t.Errorf("claim after expiry = %#v", task)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the background loop did not queue the review again")
	}

	// A waiting claim is woken by a new review.
	go func() {
		task, err := e.models.ClaimTask(ctx, node, []string{TaskKind}, 20*time.Second)
		if err != nil {
			t.Error(err)
		}
		claimed <- task
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	select {
	case task := <-claimed:
		if rt, ok := task.(*Task); !ok || rt.ID != r.ID || time.Since(start) > 5*time.Second {
			t.Errorf("woken claim = %#v", task)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("creating a review did not wake the claim")
	}
}

func TestSanitizeBoxes(t *testing.T) {
	boxes := []ResultBox{
		box("a", 0.2, 0, 0, 10, 10),
		box("b", math.Inf(1), 0, 0, 10, 10),
		box("c", 0.9, math.NaN(), 0, 10, 10),
		box("d\u0085x", -1, 0.49, 0.5, 2.5, 3),
	}
	got := sanitizeBoxes(boxes, 0, 0)
	want := []Prediction{
		{Name: "a", Confidence: 0.2, Xmin: 0, Ymin: 0, Xmax: 10, Ymax: 10},
		{Name: "dx", Confidence: 0, Xmin: 0, Ymin: 1, Xmax: 3, Ymax: 3},
	}
	if !slices.Equal(got, want) {
		t.Errorf("sanitizeBoxes = %+v", got)
	}
	var many []ResultBox
	for i := range 1500 {
		many = append(many, box("x", float64(i)/1500, 0, 0, 5, 5))
	}
	got = sanitizeBoxes(many, 0, 0)
	if len(got) != maxBoxes || got[0].Confidence != roundTo(1499.0/1500, 4) || got[999].Confidence < 0.33 {
		t.Errorf("kept %d boxes, first %v, last %v", len(got), got[0], got[len(got)-1])
	}
	if huge := sanitizeBoxes([]ResultBox{box("x", 1, 0, 0, 1e20, 1e20)}, 0, 0); huge[0].Xmax != math.MaxInt32 {
		t.Errorf("huge box = %+v", huge)
	}
}
