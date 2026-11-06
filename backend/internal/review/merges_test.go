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

	"github.com/jackc/pgx/v5/pgconn"

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

// mergeAndWait starts a merge, waits for it and returns it finished.
func (e *testEnv) mergeAndWait(t *testing.T, reviewID int64, in MergeInput) Merge {
	t.Helper()
	m, err := e.svc.StartMerge(context.Background(), reviewID, in)
	if err != nil {
		t.Fatalf("StartMerge: %v", err)
	}
	if m.Status != MergeRunning || m.Done != 0 || m.Versions == nil || len(m.Versions) != 0 {
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

// wantDatasetKind fails unless err is a dataset *Error of the kind whose
// message contains msg.
func wantDatasetKind(t *testing.T, err, kind error, msg string) {
	t.Helper()
	var de *dataset.Error
	if !errors.As(err, &de) || de.Kind != kind || !strings.Contains(de.Message, msg) {
		t.Errorf("error = %v (%T), want %v containing %q", err, err, kind, msg)
	}
}

// versions returns the versions of a dataset, newest first.
func (e *testEnv) versions(t *testing.T, datasetID int64) []dataset.Version {
	t.Helper()
	v, err := e.datasets.ListVersions(context.Background(), datasetID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// versionNames are the names of versions (made by merges), in order.
func versionNames(refs []dataset.VersionRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

// mergedAt returns when a review image was last merged (nil: never).
func (e *testEnv) mergedAt(t *testing.T, id int64) (*time.Time, *int) {
	t.Helper()
	var at *time.Time
	var revision *int
	if err := e.db.QueryRow(context.Background(), "SELECT merged_at, merged_revision FROM review_images WHERE id = $1", id).
		Scan(&at, &revision); err != nil {
		t.Fatal(err)
	}
	return at, revision
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
		{MergeInput{Note: "x"}, ErrInvalid, "请至少选择一种"},
		{MergeInput{Statuses: []string{"x"}, Note: "x"}, ErrInvalid, `"x" 无效`},
		{MergeInput{Statuses: []string{"approved"}}, ErrInvalid, "请填写版本备注"},
		{MergeInput{Statuses: []string{"approved"}, Note: " \t\n"}, ErrInvalid, "请填写版本备注"},
		{MergeInput{Statuses: []string{"approved"}, Note: "a\x00b"}, ErrInvalid, "版本备注包含无效字符"},
		{MergeInput{Statuses: []string{"approved"}, Note: strings.Repeat("备", 501)}, ErrInvalid, "不能超过 500 个字符"},
		{MergeInput{Statuses: []string{"approved"}, Note: "x", DatasetIDs: []int64{999}}, ErrInvalid, "数据集 #999 不在"},
		{MergeInput{Statuses: []string{"pending"}, Note: "x"}, ErrConflict, "没有符合条件的图片可以合并"},
	} {
		_, err := e.svc.StartMerge(ctx, r.ID, tt.in)
		wantKind(t, err, tt.kind, tt.msg)
	}
	_, err := e.svc.StartMerge(ctx, 999, MergeInput{Statuses: []string{"approved"}, Note: "x"})
	wantKind(t, err, ErrNotFound, "审图任务不存在")
	if rv := e.review(t, r.ID); rv.Datasets[0].NextVersion != "审图1.0" || rv.Datasets[1].NextVersion != "审图1.0" {
		t.Errorf("next versions before merging = %+v", rv.Datasets)
	}

	// needs_sample: b, a and c go into a new version of the first dataset
	// (c as a negative sample), d has no annotation (its inference failed),
	// so the second dataset gets none.
	m := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample", "needs_sample"}, Note: "  第一轮：划痕  \n"})
	if m.Status != MergeSucceeded || m.Total != 4 || m.Done != 4 || m.FinishedAt == nil || m.Error != "" ||
		m.Summary != (MergeSummary{Merged: 3, SkippedEmpty: 1}) || len(m.Problems) != 0 ||
		!slices.Equal(m.DatasetIDs, ds[:]) || !slices.Equal(m.Statuses, []string{"needs_sample"}) || m.OnExisting != nil ||
		m.Note != "第一轮：划痕" || len(m.Versions) != 1 || m.Versions[0].DatasetID != ds[0] ||
		m.Versions[0].Name != "审图1.0" || m.Versions[0].Status != dataset.VersionReady || m.Versions[0].ImageCount != 3 {
		t.Errorf("merge = %+v", m)
	}
	// The dataset's files are not touched.
	if !bytes.Equal(readFile(t, e.datasetPath("line_a", "a.xml")), old) || exists(e.datasetPath("line_a", "NG/b.xml")) ||
		exists(e.datasetPath("line_a", "c.xml")) {
		t.Error("the merge changed the dataset folder")
	}
	vs := e.versions(t, ds[0])
	if len(vs) != 1 {
		t.Fatalf("versions = %+v", vs)
	}
	v := vs[0]
	if v.ID != m.Versions[0].ID || v.Name != "审图1.0" || v.Major == nil || *v.Major != 1 || *v.Minor != 0 ||
		v.Note != "第一轮：划痕" || v.Kind != dataset.VersionKindReview || v.Status != dataset.VersionReady ||
		v.ImageCount != 3 || v.NeedsSampleCount != 3 || v.ApprovedCount != 0 || v.ObjectCount != 3 ||
		v.Review == nil || v.Review.ID != r.ID || v.Review.Name != r.Name || v.MergeID == nil || *v.MergeID != m.ID ||
		v.FinishedAt == nil || v.Source.ReviewID != r.ID || v.Source.ReviewName != r.Name || v.Source.MergeID != m.ID ||
		!slices.Equal(v.Source.Statuses, []string{"needs_sample"}) || v.Source.Model.ID != r.Model.ID ||
		v.Source.Model.ProjectName != "外观检测" {
		t.Errorf("version = %+v", v)
	}
	if vs := e.versions(t, ds[1]); len(vs) != 0 {
		t.Errorf("versions of the second dataset = %+v", vs)
	}
	if dd, err := e.datasets.GetDataset(ctx, ds[0]); err != nil || dd.VersionCount != 1 {
		t.Errorf("dataset = %+v, %v", dd, err)
	}
	// The merged images are marked, with the revision merged.
	if at, revision := e.mergedAt(t, b.ID); at == nil || revision == nil || *revision != e.detail(t, r.ID, b.ID).Revision {
		t.Errorf("b merged_at %v, merged_revision %v", at, revision)
	}
	if at, _ := e.mergedAt(t, d.ID); at != nil {
		t.Errorf("d (not merged) merged_at %v", at)
	}
	if rv := e.review(t, r.ID); rv.Counts.Merged != 3 || rv.LastMerge == nil || rv.LastMerge.ID != m.ID || rv.ActiveMerge != nil ||
		rv.Datasets[0].Counts.Merged != 3 || rv.Datasets[0].NextVersion != "审图1.1" || rv.Datasets[1].NextVersion != "审图1.0" ||
		len(rv.LastMerge.Versions) != 1 {
		t.Errorf("review after the merge = %+v", rv)
	}
	// The review keeps its own annotations.
	if got := e.detail(t, r.ID, b.ID); got.ObjectCount != 2 {
		t.Errorf("b in the review = %+v", got)
	}

	// Approved and needs_sample images of the first dataset only: its next
	// version.
	m2 := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"approved", "needs_sample"}, Note: "第二轮",
		DatasetIDs: []int64{ds[0], ds[0]}})
	if m2.Status != MergeSucceeded || m2.Total != 3 || m2.Summary != (MergeSummary{Merged: 3}) || !slices.Equal(m2.DatasetIDs, ds[:1]) ||
		!slices.Equal(versionNames(m2.Versions), []string{"审图1.1"}) {
		t.Errorf("second merge = %+v", m2)
	}
	// Both datasets: the second gets its first version, the first its next.
	m3 := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"approved", "needs_sample"}, Note: "第三轮"})
	if m3.Status != MergeSucceeded || m3.Summary != (MergeSummary{Merged: 4, SkippedEmpty: 1}) ||
		!slices.Equal(versionNames(m3.Versions), []string{"审图1.2", "审图1.0"}) || m3.Versions[1].DatasetID != ds[1] {
		t.Errorf("third merge = %+v", m3)
	}
	if vs := e.versions(t, ds[1]); len(vs) != 1 || vs[0].ApprovedCount != 1 || vs[0].NeedsSampleCount != 0 || vs[0].ObjectCount != 1 {
		t.Errorf("versions of the second dataset = %+v", vs)
	}
	// A deleted version's number is not given again.
	if err := e.datasets.DeleteVersion(ctx, ds[0], m3.Versions[0].ID); err != nil {
		t.Fatal(err)
	}
	m4 := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample"}, Note: "第四轮", DatasetIDs: []int64{ds[0]}})
	if !slices.Equal(versionNames(m4.Versions), []string{"审图1.3"}) {
		t.Errorf("merge after deleting a version = %+v", m4)
	}
	if names := func() (out []string) {
		for _, v := range e.versions(t, ds[0]) {
			out = append(out, v.Name)
		}
		return out
	}(); !slices.Equal(names, []string{"审图1.3", "审图1.1", "审图1.0"}) {
		t.Errorf("versions of the first dataset = %v", names)
	}
	// The merge lists what is left of its versions.
	if got, err := e.svc.GetMerge(ctx, r.ID, m3.ID); err != nil || !slices.Equal(versionNames(got.Versions), []string{"审图1.0"}) {
		t.Errorf("third merge after deleting its version = %+v, %v", got, err)
	}

	// Lists.
	merges, total, err := e.svc.ListMerges(ctx, r.ID)
	if err != nil || len(merges) != 4 || merges[0].ID != m4.ID || total != 4 || len(merges[3].Versions) != 1 {
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
		rec.Dataset.NextVersion != "审图1.1" || len(rec.DatasetMerges) != 2 || rec.DatasetMerges[0].ID != m3.ID ||
		rec.Counts.Total != 6 || !slices.Equal(versionNames(rec.DatasetMerges[0].Versions), []string{"审图1.0"}) {
		t.Errorf("dataset review = %+v", rec)
	}
	if rec := recs[0]; rec.Dataset.ID != ds[1] || rec.DatasetMerges == nil || len(rec.DatasetMerges) != 0 {
		t.Errorf("dataset review without merges = %+v", rec)
	}
	if recs, err := e.svc.ListDatasetReviews(ctx, ds[0]); err != nil || len(recs) != 1 || len(recs[0].DatasetMerges) != 4 ||
		recs[0].DatasetMerges[0].ID != m4.ID {
		t.Errorf("ListDatasetReviews(ds1) = %+v, %v", recs, err)
	}
	empty := e.addDataset(t, "empty", "empty")
	if recs, err := e.svc.ListDatasetReviews(ctx, empty); err != nil || recs == nil || len(recs) != 0 {
		t.Errorf("ListDatasetReviews(empty) = %+v, %v", recs, err)
	}
	_, err = e.svc.ListDatasetReviews(ctx, 9999)
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// Deleting the review keeps the versions, with where they came from.
	if _, err := e.svc.DeleteReview(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if vs := e.versions(t, ds[0]); len(vs) != 3 || vs[2].Review != nil || vs[2].MergeID != nil ||
		vs[2].Source.ReviewName != r.Name || vs[2].Note != "第一轮：划痕" {
		t.Errorf("versions after deleting the review = %+v", vs)
	}
}

func TestIntegrationMergeWhileRunning(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
	b, a, c, g := images[0], images[1], images[2], images[5]
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, b, a, c, g)

	// While the merge runs: no other merge starts, the review cannot be
	// deleted, the version being built cannot be changed; a status changed
	// meanwhile is not merged; a dataset deleted meanwhile gets no version.
	release := make(chan struct{})
	var once sync.Once
	reached := make(chan struct{})
	e.svc.beforeMergeSave = func(mergeID, reviewImageID int64) {
		once.Do(func() { close(reached); <-release })
	}
	started, err := e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, Note: "x"})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	_, err = e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, Note: "x"})
	wantKind(t, err, ErrConflict, "正在合并到数据集")
	_, err = e.svc.DeleteReview(ctx, r.ID)
	wantKind(t, err, ErrConflict, "正在合并到数据集，请等合并完成后再删除")
	rv := e.review(t, r.ID)
	if rv.ActiveMerge == nil || rv.ActiveMerge.ID != started.ID || rv.LastMerge != nil || len(rv.ActiveMerge.Versions) != 1 ||
		rv.ActiveMerge.Versions[0].Status != dataset.VersionBuilding || rv.ActiveMerge.Versions[0].Name != "" {
		t.Fatalf("review while merging = %+v", rv.ActiveMerge)
	}
	building := rv.ActiveMerge.Versions[0].ID
	if vs := e.versions(t, ds[0]); len(vs) != 1 || vs[0].Status != dataset.VersionBuilding || vs[0].Name != "" {
		t.Errorf("versions while merging = %+v", vs)
	}
	note := "y"
	_, err = e.datasets.UpdateVersion(ctx, ds[0], building, dataset.UpdateVersionInput{Note: &note})
	wantDatasetKind(t, err, dataset.ErrConflict, "正在生成")
	wantDatasetKind(t, e.datasets.DeleteVersion(ctx, ds[0], building), dataset.ErrConflict, "正在生成")
	e.mark(t, r.ID, reviewfile.StatusApproved, c)
	if err := e.datasets.DeleteDataset(ctx, ds[1], false); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.svc.jobs.Wait()
	done, err := e.svc.GetMerge(ctx, r.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != MergeSucceeded || done.Summary != (MergeSummary{Merged: 2, SkippedChanged: 1, Failed: 1}) ||
		len(done.Problems) != 2 || done.Problems[0].Path != c.PackagePath || done.Problems[0].Kind != ProblemSkipped ||
		!strings.Contains(done.Problems[0].Message, "审核状态在合并时被改为其它状态") ||
		done.Problems[1].Path != g.PackagePath || done.Problems[1].Kind != ProblemFailed ||
		!slices.Equal(versionNames(done.Versions), []string{"审图1.0"}) || done.Versions[0].ImageCount != 2 {
		t.Errorf("merge = %+v", done)
	}
	if at, _ := e.mergedAt(t, c.ID); at != nil {
		t.Errorf("c (changed) merged_at %v", at)
	}
}

func TestIntegrationMergeInterrupted(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
	e.mark(t, r.ID, reviewfile.StatusNeedsSample, images[0], images[1])

	// A merge a previous run left running fails when the service starts,
	// and the version it was building goes.
	var leftover int64
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, note, status, total, done)
		VALUES ($1, $2, '{needs_sample}', 'x', 'running', 10, 3) RETURNING id`, r.ID, []int64{ds[0]}).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO dataset_versions (dataset_id, kind, note, status, review_id, merge_id, source)
		VALUES ($1, 'review', 'x', 'building', $2, $3, '{}')`, ds[0], r.ID, leftover); err != nil {
		t.Fatal(err)
	}
	svcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	if m, err := e.svc.GetMerge(ctx, r.ID, leftover); err != nil || m.Status != MergeFailed || m.Error != msgMergeInterrupted ||
		m.FinishedAt == nil || m.Done != 3 || len(m.Versions) != 0 || !strings.Contains(m.Error, "没有生成新的版本") {
		t.Errorf("leftover merge = %+v, %v", m, err)
	}
	if vs := e.versions(t, ds[0]); len(vs) != 0 {
		t.Errorf("versions after the start = %+v", vs)
	}

	// Stopping the service ends a running merge as failed: nothing of it is
	// kept.
	release := make(chan struct{})
	reached := make(chan struct{})
	var calls atomic.Int32
	e.svc.beforeMergeSave = func(int64, int64) {
		if calls.Add(1) == 2 { // the first image is in the version
			close(reached)
			<-release
		}
	}
	m, err := e.svc.StartMerge(ctx, r.ID, MergeInput{Statuses: []string{"needs_sample"}, Note: "x"})
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
	if m, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || m.Status != MergeFailed || m.Error != msgMergeStopped ||
		m.FinishedAt == nil || len(m.Versions) != 0 {
		t.Errorf("merge stopped by the service = %+v, %v", m, err)
	}
	if vs := e.versions(t, ds[0]); len(vs) != 0 {
		t.Errorf("versions after stopping = %+v", vs)
	}
	if at, _ := e.mergedAt(t, images[0].ID); at != nil {
		t.Errorf("image of the stopped merge marked merged at %v", at)
	}
	if rv := e.review(t, r.ID); rv.Counts.Merged != 0 || rv.Datasets[0].NextVersion != "审图1.0" {
		t.Errorf("review after the stopped merge = %+v", rv)
	}
	// After a restart it can be merged again: the first version is 1.0.
	e.svc.beforeMergeSave = nil
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	defer e.svc.Stop()
	if m := e.mergeAndWait(t, r.ID, MergeInput{Statuses: []string{"needs_sample"}, Note: "x"}); m.Status != MergeSucceeded ||
		!slices.Equal(versionNames(m.Versions), []string{"审图1.0"}) {
		t.Errorf("merge after the restart = %+v", m)
	}
}

// TestIntegrationMergeEndRetried makes recording the end of a merge fail
// (the database away for a moment): it is tried again, so the merge does
// not stay running; when the service stops meanwhile, the next start fails
// the merge. An error that would happen again fails the merge at once.
func TestIntegrationMergeEndRetried(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
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
	in := MergeInput{Statuses: []string{"needs_sample"}, Note: "x"}
	if m := e.mergeAndWait(t, r.ID, in); m.Status != MergeSucceeded || m.FinishedAt == nil || m.Summary.Merged != 2 ||
		attempts.Load() != 4 || !slices.Equal(versionNames(m.Versions), []string{"审图1.0"}) {
		t.Errorf("merge = %+v after %d attempts", m, attempts.Load())
	}

	// Not transient: the merge fails (once that is recorded), its version
	// goes, its images are not marked.
	mergeEndFault = func(attempt int) error {
		attempts.Store(int32(attempt))
		if attempt == 1 {
			return &pgconn.PgError{Code: "23505", Message: "duplicate key"}
		}
		return nil
	}
	if _, err := e.db.Exec(ctx, "UPDATE review_images SET merged_at = NULL, merged_revision = NULL"); err != nil {
		t.Fatal(err)
	}
	if m := e.mergeAndWait(t, r.ID, in); m.Status != MergeFailed || m.Error != msgMergeFailed || len(m.Versions) != 0 ||
		attempts.Load() != 2 {
		t.Errorf("merge failing for good = %+v after %d attempts", m, attempts.Load())
	}
	if vs := e.versions(t, ds[0]); len(vs) != 1 {
		t.Errorf("versions = %+v", vs)
	}
	if at, _ := e.mergedAt(t, images[0].ID); at != nil {
		t.Errorf("image of the failed merge marked merged at %v", at)
	}
	mergeEndFault = nil
	if m := e.mergeAndWait(t, r.ID, in); m.Status != MergeSucceeded || !slices.Equal(versionNames(m.Versions), []string{"审图1.1"}) {
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
	if got, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || got.Status != MergeRunning || len(got.Versions) != 1 ||
		got.Versions[0].Status != dataset.VersionBuilding {
		t.Errorf("merge after stopping = %+v, %v", got, err)
	}
	mergeEndFault = nil
	if err := e.svc.Start(svcCtx); err != nil {
		t.Fatal(err)
	}
	defer e.svc.Stop()
	if got, err := e.svc.GetMerge(ctx, r.ID, m.ID); err != nil || got.Status != MergeFailed || got.Error != msgMergeInterrupted ||
		len(got.Versions) != 0 {
		t.Errorf("merge after the restart = %+v, %v", got, err)
	}
	if vs := e.versions(t, ds[0]); len(vs) != 2 {
		t.Errorf("versions after the restart = %+v", vs)
	}
}

func TestTransientError(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{errors.New("conn closed"), true},
		{context.DeadlineExceeded, true},
		{&pgconn.PgError{Code: "40P01"}, true},
		{&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "08006"}, true},
		{&pgconn.PgError{Code: "53300"}, true},
		{&pgconn.PgError{Code: "23505"}, false},
		{&pgconn.PgError{Code: "XX000"}, false},
		{&pgconn.PgError{Code: "42P01"}, false},
	} {
		if got := transientError(tt.err); got != tt.want {
			t.Errorf("transientError(%v) = %v", tt.err, got)
		}
	}
}
