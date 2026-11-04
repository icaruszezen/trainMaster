package review

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// writeDatasetVOC writes a VOC file into a dataset folder.
func (e *testEnv) writeDatasetVOC(t *testing.T, dirName, rel, imageName string, objects ...dataset.AnnotationObject) []byte {
	t.Helper()
	data, err := dataset.EncodeVOC("", imageName, 40, 30, objects)
	if err != nil {
		t.Fatal(err)
	}
	p := e.datasetPath(dirName, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// datasetObjects returns the boxes of a dataset image as the dataset
// reads them now, and its annotation state.
func (e *testEnv) datasetObjects(t *testing.T, datasetID, imageID int64) ([]dataset.AnnotationObject, string) {
	t.Helper()
	d, err := e.datasets.GetImage(context.Background(), datasetID, imageID)
	if err != nil {
		t.Fatalf("dataset GetImage: %v", err)
	}
	return d.Objects, d.AnnotationState
}

// mergeAndWait starts a merge, waits for it and returns it finished.
func (e *testEnv) mergeAndWait(t *testing.T, reviewID int64, in MergeInput) Merge {
	t.Helper()
	m, err := e.svc.StartMerge(context.Background(), reviewID, in)
	if err != nil {
		t.Fatalf("StartMerge: %v", err)
	}
	if m.Status != MergeRunning || m.Done != 0 {
		t.Errorf("started merge = %+v", m)
	}
	e.svc.jobs.Wait()
	m, err = e.svc.GetMerge(context.Background(), reviewID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *testEnv) mark(t *testing.T, reviewID int64, status string, images ...Image) {
	t.Helper()
	for _, img := range images {
		if _, err := e.svc.SetStatus(context.Background(), reviewID, img.ID, status); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationMerge(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
	b, a, c, d, g := images[0], images[1], images[2], images[3], images[5]
	// a has an annotation in its dataset already.
	old := e.writeDatasetVOC(t, "line_a", "a.xml", "a.png", obj("old", 1, 1, 4, 4))
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, b, a, c, d)
	e.mark(t, r.ID, reviewfile.StatusApproved, g)

	// Validation.
	for _, tt := range []struct {
		in   MergeInput
		kind error
		msg  string
	}{
		{MergeInput{OnExisting: MergeSkip}, ErrInvalid, "请至少选择一种"},
		{MergeInput{Statuses: []string{"x"}, OnExisting: MergeSkip}, ErrInvalid, `"x" 无效`},
		{MergeInput{Statuses: []string{"approved"}}, ErrInvalid, "on_existing"},
		{MergeInput{Statuses: []string{"approved"}, OnExisting: MergeSkip, DatasetIDs: []int64{999}}, ErrInvalid, "数据集 #999 不在"},
		{MergeInput{Statuses: []string{"pending"}, OnExisting: MergeSkip}, ErrConflict, "没有符合条件的图片可以合并"},
	} {
		_, err := e.svc.StartMerge(ctx, r.ID, tt.in)
		wantKind(t, err, tt.kind, tt.msg)
	}
	_, err := e.svc.StartMerge(ctx, 999, MergeInput{Statuses: []string{"approved"}, OnExisting: MergeSkip})
	wantKind(t, err, ErrNotFound, "审图任务不存在")

	// needs_sample, skipping images that have an annotation: b and c are
	// merged (c as an empty annotation), a is skipped, d has no VOC file.
	m := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample", "needs_sample"}, OnExisting: MergeSkip})
	if m.Status != MergeSucceeded || m.Total != 4 || m.Done != 4 || m.FinishedAt == nil || m.Error != "" ||
		m.Summary != (MergeSummary{Merged: 2, SkippedExisting: 1, SkippedEmpty: 1}) || len(m.Problems) != 0 ||
		!slices.Equal(m.DatasetIDs, ds[:]) || !slices.Equal(m.Statuses, []string{"needs_sample"}) || m.OnExisting != MergeSkip {
		t.Errorf("merge = %+v", m)
	}
	if objects, state := e.datasetObjects(t, ds[0], b.ImageID); state != "annotated" ||
		!reviewfile.SameObjects(objects, e.detail(t, r.ID, b.ID).Objects) {
		t.Errorf("b in its dataset: %s %+v", state, objects)
	}
	if objects, state := e.datasetObjects(t, ds[0], c.ImageID); state != "empty" || len(objects) != 0 {
		t.Errorf("c in its dataset: %s %+v", state, objects)
	}
	if !bytes.Equal(readFile(t, e.datasetPath("line_a", "a.xml")), old) || exists(e.datasetPath("line:a", "d.xml")) {
		t.Error("a skipped image's dataset annotation changed")
	}
	var mergedRevision *int
	var mergedAt *time.Time
	if err := e.db.QueryRow(ctx, "SELECT merged_revision, merged_at FROM review_images WHERE id = $1", b.ID).
		Scan(&mergedRevision, &mergedAt); err != nil {
		t.Fatal(err)
	}
	if mergedAt == nil || mergedRevision == nil || *mergedRevision != e.detail(t, r.ID, b.ID).Revision {
		t.Errorf("b merged_at %v, merged_revision %v", mergedAt, mergedRevision)
	}
	if rv := e.review(t, r.ID); rv.Counts.Merged != 2 || rv.LastMerge == nil || rv.LastMerge.ID != m.ID || rv.ActiveMerge != nil ||
		rv.Datasets[0].Counts.Merged != 2 {
		t.Errorf("review after the merge = %+v", rv)
	}
	// The review keeps its own annotations.
	if got := e.detail(t, r.ID, b.ID); got.ObjectCount != 2 {
		t.Errorf("b in the review = %+v", got)
	}

	// Overwriting, also approved images, of the first dataset only.
	m2 := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"approved", "needs_sample"}, OnExisting: MergeOverwrite,
		DatasetIDs: []int64{ds[0], ds[0]}})
	if m2.Status != MergeSucceeded || m2.Total != 3 || m2.Summary != (MergeSummary{Merged: 3}) || !slices.Equal(m2.DatasetIDs, ds[:1]) {
		t.Errorf("overwriting merge = %+v", m2)
	}
	if objects, _ := e.datasetObjects(t, ds[0], a.ImageID); !reviewfile.SameObjects(objects, objs(obj("scratch", 3, 3, 20, 20))) {
		t.Errorf("a in its dataset after overwriting = %+v", objects)
	}

	// Lists.
	merges, total, err := e.svc.ListMerges(ctx, r.ID)
	if err != nil || len(merges) != 2 || merges[0].ID != m2.ID || total != 2 {
		t.Errorf("ListMerges = %+v, %d, %v", merges, total, err)
	}
	_, _, err = e.svc.ListMerges(ctx, 999)
	wantKind(t, err, ErrNotFound, "审图任务不存在")
	_, err = e.svc.GetMerge(ctx, r.ID, 999)
	wantKind(t, err, ErrNotFound, "合并记录不存在")
	_, err = e.svc.GetMerge(ctx, 999, m.ID)
	wantKind(t, err, ErrNotFound, "审图任务不存在")

	// The review records of the datasets.
	other := e.create(t, CreateInput{ModelID: r.Model.ID, DatasetIDs: []int64{ds[1]}})
	recs, err := e.svc.ListDatasetReviews(ctx, ds[1])
	if err != nil || len(recs) != 2 || recs[0].ID != other.ID || recs[1].ID != r.ID {
		t.Fatalf("ListDatasetReviews = %+v, %v", recs, err)
	}
	if rec := recs[1]; rec.Dataset.ID != ds[1] || rec.Dataset.Counts.Total != 3 || rec.Dataset.Counts.NeedsSample != 1 ||
		len(rec.DatasetMerges) != 1 || rec.DatasetMerges[0].ID != m.ID || rec.Counts.Total != 6 {
		t.Errorf("dataset review = %+v", rec)
	}
	if rec := recs[0]; rec.Dataset.ID != ds[1] || rec.DatasetMerges == nil || len(rec.DatasetMerges) != 0 {
		t.Errorf("dataset review without merges = %+v", rec)
	}
	if recs, err := e.svc.ListDatasetReviews(ctx, ds[0]); err != nil || len(recs) != 1 || len(recs[0].DatasetMerges) != 2 ||
		recs[0].DatasetMerges[0].ID != m2.ID {
		t.Errorf("ListDatasetReviews(ds1) = %+v, %v", recs, err)
	}
	empty := e.addDataset(t, "empty", "empty")
	if recs, err := e.svc.ListDatasetReviews(ctx, empty); err != nil || recs == nil || len(recs) != 0 {
		t.Errorf("ListDatasetReviews(empty) = %+v, %v", recs, err)
	}
	_, err = e.svc.ListDatasetReviews(ctx, 9999)
	wantKind(t, err, ErrNotFound, "数据集不存在")
}

func TestIntegrationMergeConflictsAndLayouts(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	// A VOC layout: images in JPEGImages/, annotations in Annotations/.
	ds := e.addDataset(t, "voc", "voc", testImage{rel: "JPEGImages/x.png"}, testImage{rel: "JPEGImages/y.png"},
		testImage{rel: "JPEGImages/z.png"})
	e.writeDatasetVOC(t, "voc", "Annotations/x.xml", "x.png", obj("old", 1, 1, 4, 4))
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	node := e.node(t, "n")
	e.claim(t, node)
	e.inferAll(t, r.ID, node)
	images := e.images(t, r.ID, ImageQuery{})
	x, y, z := images[0], images[1], images[2]
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, x, y, z)

	// While z is merged somebody writes its dataset annotation: a conflict.
	// While the merge runs, no other merge starts and the review cannot be
	// deleted.
	release := make(chan struct{})
	var once sync.Once
	reached := make(chan struct{})
	e.svc.beforeMergeSave = func(mergeID, reviewImageID int64) {
		once.Do(func() { close(reached); <-release })
		if reviewImageID == z.ID {
			e.writeDatasetVOC(t, "voc", "Annotations/z.xml", "z.png", obj("someone", 2, 2, 8, 8))
		}
	}
	started, err := e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	_, err = e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeSkip})
	wantKind(t, err, ErrConflict, "正在合并到数据集")
	_, err = e.svc.DeleteReview(ctx, r.ID)
	wantKind(t, err, ErrConflict, "正在合并到数据集，请等合并完成后再删除")
	if rv := e.review(t, r.ID); rv.ActiveMerge == nil || rv.ActiveMerge.ID != started.ID || rv.LastMerge != nil {
		t.Errorf("review while merging = %+v", rv)
	}
	close(release)
	e.svc.jobs.Wait()
	done, err := e.svc.GetMerge(ctx, r.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Summary != (MergeSummary{Merged: 2, Conflicts: 1}) || len(done.Problems) != 1 ||
		done.Problems[0].Path != z.PackagePath || done.Problems[0].Kind != ProblemConflict ||
		!strings.Contains(done.Problems[0].Message, "被其他人修改") {
		t.Errorf("merge = %+v", done)
	}
	// x replaced its file in Annotations/, y got a new one there.
	for _, img := range []Image{x, y} {
		objects, state := e.datasetObjects(t, ds, img.ImageID)
		if state != "annotated" || !reviewfile.SameObjects(objects, objs(obj("scratch", 1, 1, 11, 11))) {
			t.Errorf("%s in its dataset: %s %+v", img.Path, state, objects)
		}
	}
	if !exists(e.datasetPath("voc", "Annotations/y.xml")) || exists(e.datasetPath("voc", "JPEGImages/y.xml")) {
		t.Error("y's annotation is not in Annotations/")
	}
	if objects, _ := e.datasetObjects(t, ds, z.ImageID); !reviewfile.SameObjects(objects, objs(obj("someone", 2, 2, 8, 8))) {
		t.Errorf("z in its dataset = %+v", objects)
	}

	// A dataset image whose file is gone fails; the merge goes on.
	e.svc.beforeMergeSave = nil
	if err := os.Remove(e.datasetPath("voc", "JPEGImages/x.png")); err != nil {
		t.Fatal(err)
	}
	m2 := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeOverwrite})
	if m2.Status != MergeSucceeded || m2.Summary != (MergeSummary{Merged: 2, Failed: 1}) || len(m2.Problems) != 1 ||
		m2.Problems[0].Kind != ProblemFailed || m2.Problems[0].Path != x.PackagePath || m2.Problems[0].Message == "" {
		t.Errorf("merge with a missing image = %+v", m2)
	}
}

func TestIntegrationMergeInterrupted(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _, _ := e.exchangeReview(t)
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, images[0], images[1])

	// A merge a previous run left running fails when the service starts.
	var leftover int64
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, on_existing, status, total, done)
		VALUES ($1, '{1}', '{needs_sample}', 'skip', 'running', 10, 3) RETURNING id`, r.ID).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	svcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	if m, err := e.svc.GetMerge(ctx, r.ID, leftover); err != nil || m.Status != MergeFailed || m.Error != msgMergeInterrupted ||
		m.FinishedAt == nil || m.Done != 3 {
		t.Errorf("leftover merge = %+v, %v", m, err)
	}

	// Stopping the service ends a running merge as failed.
	release := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	e.svc.beforeMergeSave = func(int64, int64) { once.Do(func() { close(reached); <-release }) }
	m, err := e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeSkip})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	stopped := make(chan struct{})
	go func() { e.svc.Stop(); close(stopped) }()
	<-e.svc.base.Done()
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	if m, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || m.Status != MergeFailed ||
		!strings.Contains(m.Error, "服务停止，合并被中断") || m.FinishedAt == nil {
		t.Errorf("merge stopped by the service = %+v, %v", m, err)
	}
	// After a restart it can be merged again.
	e.svc.beforeMergeSave = nil
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	defer e.svc.Stop()
	if m := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeSkip}); m.Status != MergeSucceeded {
		t.Errorf("merge after the restart = %+v", m)
	}
}

// TestIntegrationMergeEndRetried makes recording the end of a merge fail
// (the database away for a moment): it is tried again, so the merge does
// not stay running; when the service stops meanwhile, the next start fails
// the merge.
func TestIntegrationMergeEndRetried(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _, _ := e.exchangeReview(t)
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, images[0], images[1])
	retry := mergeEndRetry
	t.Cleanup(func() { mergeEndRetry, mergeEndFault = retry, nil })
	mergeEndRetry = [2]time.Duration{time.Millisecond, 4 * time.Millisecond}
	var attempts atomic.Int32
	mergeEndFault = func(attempt int) error {
		attempts.Store(int32(attempt))
		if attempt <= 3 {
			return errors.New("database unavailable")
		}
		return nil
	}
	in := MergeInput{Statuses: []string{"needs_sample"}, OnExisting: MergeSkip}
	if m := e.mergeAndWait(t, r.ID, in); m.Status != MergeSucceeded || m.FinishedAt == nil || m.Summary.Merged != 2 ||
		attempts.Load() != 4 {
		t.Errorf("merge = %+v after %d attempts", m, attempts.Load())
	}
	if m := e.mergeAndWait(t, r.ID, in); m.Status != MergeSucceeded {
		t.Errorf("next merge = %+v", m)
	}

	// It never works: the service stopping ends the attempts.
	attempts.Store(0)
	mergeEndFault = func(attempt int) error {
		attempts.Store(int32(attempt))
		return errors.New("database unavailable")
	}
	svcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.StartMerge(ctx, r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); attempts.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatal("recording the end of the merge was not retried")
		}
		time.Sleep(time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() { e.svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	if got, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || got.Status != MergeRunning {
		t.Errorf("merge after stopping = %+v, %v", got, err)
	}
	mergeEndFault = nil
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	defer e.svc.Stop()
	if got, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || got.Status != MergeFailed || got.Error != msgMergeInterrupted {
		t.Errorf("merge after the restart = %+v, %v", got, err)
	}
}
