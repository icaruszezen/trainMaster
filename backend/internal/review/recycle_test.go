package review

import (
	"context"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// A dataset in the recycle bin leaves its reviews; restored, its review
// images come back unchanged, and a review that finished without them
// infers them.
func TestIntegrationReviewDatasetRecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds1 := e.addDataset(t, "ds1", "ds1", testImage{rel: "a.png"}, testImage{rel: "b.png"})
	ds2 := e.addDataset(t, "ds2", "ds2", testImage{rel: "c.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})
	node := e.node(t, "a")
	if task := e.claim(t, node); task == nil || task.ID != r.ID {
		t.Fatalf("claim = %+v", task)
	}
	before := e.images(t, r.ID, ImageQuery{})

	// Deleted while the review is inferred: it finishes without it.
	if err := e.datasets.DeleteDataset(ctx, ds2, false); err != nil {
		t.Fatal(err)
	}
	e.inferAll(t, r.ID, node)
	got := e.review(t, r.ID)
	if got.Status != StatusReady || got.Counts.Total != 2 || got.Counts.Inferred != 2 || len(got.Datasets) != 1 {
		t.Fatalf("review without the dataset = %+v", got)
	}
	ds, err := e.svc.ListDatasetReviews(ctx, ds2)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	if ds != nil {
		t.Errorf("reviews of a dataset in the bin = %+v", ds)
	}

	// Restored: the same review images, not inferred yet, so the review is
	// queued again and infers them.
	if _, err := e.datasets.RestoreDataset(ctx, ds2, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	got = e.review(t, r.ID)
	if got.Status != StatusQueued || got.Counts.Total != 3 || got.Counts.Inferred != 2 || len(got.Datasets) != 2 {
		t.Fatalf("review after the restore = %+v", got)
	}
	after := e.images(t, r.ID, ImageQuery{})
	if len(after) != len(before) {
		t.Fatalf("images after the restore = %+v", after)
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Position != before[i].Position || after[i].PackagePath != before[i].PackagePath {
			t.Errorf("image %d: %+v, before %+v", i, after[i], before[i])
		}
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID || task.Remaining != 1 {
		t.Fatalf("claim after the restore = %+v", task)
	}
	e.inferAll(t, r.ID, node)
	if got = e.review(t, r.ID); got.Status != StatusReady || got.Counts.Inferred != 3 {
		t.Fatalf("review after inferring the restored images = %+v", got)
	}

	// What was reviewed stays as it was.
	var c Image
	for _, img := range e.images(t, r.ID, ImageQuery{}) {
		if img.DatasetID == ds2 {
			c = img
		}
	}
	if _, err := e.svc.SetStatus(ctx, r.ID, c.ID, reviewfile.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if err := e.datasets.DeleteDataset(ctx, ds2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.datasets.RestoreDataset(ctx, ds2, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if d := e.detail(t, r.ID, c.ID); d.Status != reviewfile.StatusApproved || !d.Inferred {
		t.Errorf("restored image = %+v", d.Image)
	}
	if got = e.review(t, r.ID); got.Status != StatusReady {
		t.Errorf("a review whose images are all inferred must stay ready: %+v", got)
	}

	// Purged: the review keeps the other datasets.
	if err := e.datasets.DeleteDataset(ctx, ds2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM datasets WHERE id = $1", ds2); err != nil { // as the purge does
		t.Fatal(err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM recycled_review_datasets) + (SELECT count(*) FROM recycled_review_images)").
		Scan(&n); err != nil || n != 0 {
		t.Errorf("recycled rows left: %d, %v", n, err)
	}
	if got = e.review(t, r.ID); got.Counts.Total != 2 || len(got.Datasets) != 1 {
		t.Errorf("review after the purge = %+v", got)
	}
}

// Restored while a node infers the review: it stays with the node, whose
// next pass from the start lists the restored images too (even with the
// model in the bin meanwhile, as the node has its weights already).
func TestIntegrationReviewDatasetRestoreWhileRunning(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds1 := e.addDataset(t, "ds1", "ds1", testImage{rel: "a.png"})
	ds2 := e.addDataset(t, "ds2", "ds2", testImage{rel: "b.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})
	if err := e.datasets.DeleteDataset(ctx, ds1, false); err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "a")
	if task := e.claim(t, node); task == nil || task.ID != r.ID || task.Remaining != 1 {
		t.Fatalf("claim = %+v", task)
	}
	page, err := e.svc.ListTaskImages(ctx, r.ID, node, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("first pass = %+v, %v", page, err)
	}
	if err := e.models.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.datasets.RestoreDataset(ctx, ds1, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	got := e.review(t, r.ID)
	if got.Status != StatusRunning || got.Attempts != 1 || got.Node == nil || got.Counts.Total != 2 {
		t.Fatalf("review after the restore = %+v", got)
	}
	page, err = e.svc.ListTaskImages(ctx, r.ID, node, 0, 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("next pass = %+v, %v", page, err)
	}
	e.inferAll(t, r.ID, node)
	if got = e.review(t, r.ID); got.Status != StatusReady || got.Counts.Inferred != 2 {
		t.Fatalf("review after inferring the restored images = %+v", got)
	}
}

// Restored after the node's last (empty) listing: completing is refused
// and the review goes back to the queue at once; after maxAttempts claims
// it fails instead.
func TestIntegrationReviewDatasetRestoreBeforeComplete(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds1 := e.addDataset(t, "ds1", "ds1", testImage{rel: "a.png"})
	ds2 := e.addDataset(t, "ds2", "ds2", testImage{rel: "b.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})
	if err := e.datasets.DeleteDataset(ctx, ds1, false); err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "a")
	// Infer what is listed, list again (empty), restore, complete.
	finish := func(reviewID, restore int64) error {
		t.Helper()
		page, err := e.svc.ListTaskImages(ctx, reviewID, node, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var results []ImageResult
		for _, img := range page.Items {
			results = append(results, ImageResult{ID: img.ID, OK: true})
		}
		if len(results) > 0 {
			if _, err := e.svc.SubmitResults(ctx, reviewID, node, results); err != nil {
				t.Fatal(err)
			}
		}
		if page, err := e.svc.ListTaskImages(ctx, reviewID, node, 0, 10); err != nil || len(page.Items) != 0 {
			t.Fatalf("last pass = %+v, %v", page, err)
		}
		if _, err := e.datasets.RestoreDataset(ctx, restore, dataset.RestoreInput{}); err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.CompleteTask(ctx, reviewID, node, nil)
		return err
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID {
		t.Fatalf("claim = %+v", task)
	}
	wantKind(t, finish(r.ID, ds1), ErrConflict, "还有 1 张图片没有识别，审图任务已重新排队")
	if got := e.review(t, r.ID); got.Status != StatusQueued || got.Node != nil || got.Attempts != 1 || got.Counts.Inferred != 1 {
		t.Fatalf("review after the refused completion = %+v", got)
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID || task.Remaining != 1 {
		t.Fatalf("claim after the refused completion = %+v", task)
	}
	e.inferAll(t, r.ID, node)
	if got := e.review(t, r.ID); got.Status != StatusReady || got.Counts.Inferred != 2 {
		t.Fatalf("review after inferring the restored images = %+v", got)
	}

	// On the last attempt it fails instead, and can be retried.
	ds3 := e.addDataset(t, "ds3", "ds3", testImage{rel: "c.png"})
	ds4 := e.addDataset(t, "ds4", "ds4", testImage{rel: "d.png"})
	r2 := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds3, ds4}})
	if err := e.datasets.DeleteDataset(ctx, ds3, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE reviews SET attempts = $2 WHERE id = $1", r2.ID, maxAttempts-1); err != nil {
		t.Fatal(err)
	}
	if task := e.claim(t, node); task == nil || task.ID != r2.ID {
		t.Fatalf("last claim = %+v", task)
	}
	err := finish(r2.ID, ds3)
	wantKind(t, err, ErrConflict, "还有 1 张图片没有识别")
	if strings.Contains(err.Error(), "重新排队") {
		t.Errorf("the last attempt must not queue it again: %v", err)
	}
	if got := e.review(t, r2.ID); got.Status != StatusFailed || got.Error != msgIncomplete || got.FinishedAt == nil {
		t.Fatalf("review after the last refused completion = %+v", got)
	}
	if retried, err := e.svc.RetryReview(ctx, r2.ID); err != nil || retried.Status != StatusQueued {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
}

// A review whose model is in the recycle bin waits for it instead of
// failing; a review cannot be created with it.
func TestIntegrationReviewModelRecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	if err := e.models.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	node := e.node(t, "a")
	if task := e.claim(t, node); task != nil {
		t.Fatalf("claimed %+v with its model in the bin", task)
	}
	if got := e.review(t, r.ID); got.Status != StatusQueued || got.ModelExists || got.QueuePosition != nil {
		t.Errorf("waiting review = %+v", got)
	}
	// It is not ahead of the others in the queue.
	other := e.addModel(t, e.project(t, "q"), testModel{})
	r3 := e.create(t, CreateInput{ModelID: other.ID, DatasetIDs: []int64{ds}})
	if got := e.review(t, r3.ID); got.QueuePosition == nil || *got.QueuePosition != 1 {
		t.Errorf("review queued after the waiting one = %+v", got)
	}
	if task := e.claim(t, node); task == nil || task.ID != r3.ID {
		t.Fatalf("claim = %+v", task)
	}
	e.inferAll(t, r3.ID, node)
	_, err := e.svc.CreateReview(ctx, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	wantKind(t, err, ErrInvalid, "所选模型不存在或已被删除")

	if _, err := e.models.RestoreModel(ctx, m.ID, model.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, r.ID); !got.ModelExists || got.QueuePosition == nil || *got.QueuePosition != 1 {
		t.Errorf("review after the restore = %+v", got)
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID {
		t.Fatalf("claim after the restore = %+v", task)
	}

	// Moved to the bin while the node holds the review: the download fails
	// it, and retrying asks for the restore first.
	if err := e.models.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenTaskModel(ctx, r.ID, node)
	wantKind(t, err, ErrNotFound, msgModelInBin)
	if got := e.review(t, r.ID); got.Status != StatusFailed || got.Error != msgModelInBin {
		t.Errorf("review after its model went to the bin = %+v", got)
	}
	_, err = e.svc.RetryReview(ctx, r.ID)
	wantKind(t, err, ErrConflict, "请先在回收站中恢复模型")

	// Restoring a dataset queues a review whose model is in the bin: it
	// waits for the model like a new one.
	if _, err := e.models.RestoreModel(ctx, m.ID, model.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RetryReview(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.datasets.DeleteDataset(ctx, ds, false); err != nil {
		t.Fatal(err)
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID {
		t.Fatalf("claim = %+v", task)
	}
	e.inferAll(t, r.ID, node)
	if err := e.models.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.datasets.RestoreDataset(ctx, ds, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, r.ID); got.Status != StatusQueued || got.ModelExists || got.Counts.Total != 1 || got.Counts.Inferred != 0 {
		t.Fatalf("review after the restore = %+v", got)
	}
	if task := e.claim(t, node); task != nil {
		t.Fatalf("claimed %+v with its model in the bin", task)
	}
	if _, err := e.models.RestoreModel(ctx, m.ID, model.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if task := e.claim(t, node); task == nil || task.ID != r.ID || task.Remaining != 1 {
		t.Fatalf("claim after restoring the model = %+v", task)
	}
	e.inferAll(t, r.ID, node)
	if got := e.review(t, r.ID); got.Status != StatusReady || got.Counts.Inferred != 1 {
		t.Fatalf("review after inferring the restored images = %+v", got)
	}

	// Purged: the review fails like one whose model was deleted.
	r2 := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	if err := e.models.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM models WHERE id = $1", m.ID); err != nil { // as the purge does
		t.Fatal(err)
	}
	e.claim(t, node)
	if got := e.review(t, r2.ID); got.Status != StatusFailed || got.Error != msgModelDeleted {
		t.Errorf("review of a purged model = %+v", got)
	}
}

// A merge does not list the versions of a dataset in the recycle bin; they
// come back with it.
func TestIntegrationMergeVersionsRecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
	e.mark(t, r.ID, reviewfile.StatusApproved, images[5])
	m := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{reviewfile.StatusApproved}, Note: "x"})
	if m.Status != MergeSucceeded || len(m.Versions) != 1 || m.Versions[0].DatasetID != ds[1] {
		t.Fatalf("merge = %+v", m)
	}
	versions := func() []dataset.VersionRef {
		t.Helper()
		got, err := e.svc.GetMerge(ctx, r.ID, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Versions
	}
	if err := e.datasets.DeleteDataset(ctx, ds[1], false); err != nil {
		t.Fatal(err)
	}
	if v := versions(); len(v) != 0 {
		t.Errorf("versions with the dataset in the bin = %+v", v)
	}
	if _, err := e.datasets.RestoreDataset(ctx, ds[1], dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if v := versions(); len(v) != 1 || v[0].ID != m.Versions[0].ID {
		t.Errorf("versions after the restore = %+v", v)
	}
}
