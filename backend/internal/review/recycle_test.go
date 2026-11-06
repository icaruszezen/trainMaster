package review

import (
	"context"
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
	if got := e.review(t, r.ID); got.Status != StatusQueued || got.ModelExists {
		t.Errorf("waiting review = %+v", got)
	}
	_, err := e.svc.CreateReview(ctx, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	wantKind(t, err, ErrInvalid, "所选模型不存在或已被删除")

	if _, err := e.models.RestoreModel(ctx, m.ID, model.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, r.ID); !got.ModelExists {
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

	// Restoring a dataset does not queue a review whose model is in the bin.
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
	if got := e.review(t, r.ID); got.Status != StatusReady || got.Counts.Total != 1 || got.Counts.Inferred != 0 {
		t.Errorf("review after the restore = %+v", got)
	}

	// Purged: the review fails like one whose model was deleted.
	if _, err := e.models.RestoreModel(ctx, m.ID, model.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
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
