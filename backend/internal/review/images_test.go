package review

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// inferredReview creates a review of two datasets (ds1: a, b, NG/c, d;
// ds2: e, f), infers it with a few boxes and returns it with its images in
// review order.
func (e *testEnv) inferredReview(t *testing.T, prefix string) (Review, []Image, int64) {
	t.Helper()
	ctx := context.Background()
	m := e.addModel(t, e.project(t, prefix+"p"), testModel{})
	ds1 := e.addDataset(t, prefix+"ds1", prefix+"ds1", testImage{rel: "a.png"}, testImage{rel: "b.png"},
		testImage{rel: "NG/c.png"}, testImage{rel: "d.png"})
	ds2 := e.addDataset(t, prefix+"ds2", prefix+"ds2", testImage{rel: "e.png"}, testImage{rel: "f.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})
	node := e.node(t, prefix+"node")
	e.claim(t, node)
	images := e.images(t, r.ID, ImageQuery{})
	// NG/c, a, b, d, e, f
	results := []ImageResult{
		{ID: images[0].ID, OK: true, Boxes: []ResultBox{box("scratch", 0.9, 1, 1, 10, 10), box("dent", 0.8, 2, 2, 12, 12)}},
		{ID: images[1].ID, OK: true, Boxes: []ResultBox{box("scratch", 0.7, 1, 1, 10, 10)}},
		{ID: images[2].ID, OK: true},
		{ID: images[3].ID, OK: false, Error: "无法读取图片"},
		{ID: images[4].ID, OK: true, Boxes: []ResultBox{box("dent", 0.6, 5, 5, 20, 20)}},
	}
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, results); err != nil {
		t.Fatal(err)
	}
	return r, e.images(t, r.ID, ImageQuery{}), node
}

func paths(images []Image) []string {
	var out []string
	for _, img := range images {
		out = append(out, img.Path)
	}
	return out
}

func TestIntegrationImageFilters(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _ := e.inferredReview(t, "")
	if got := paths(images); !slices.Equal(got, []string{"NG/c.png", "a.png", "b.png", "d.png", "e.png", "f.png"}) {
		t.Fatalf("images = %v", got)
	}
	if images[0].ObjectCount != 2 || images[0].PredictionCount != 2 ||
		!slices.Equal(images[0].Labels, []dataset.LabelCount{{Name: "dent", Count: 1}, {Name: "scratch", Count: 1}}) {
		t.Errorf("first image = %+v", images[0])
	}
	// Mark some.
	for i, status := range map[int]string{0: "approved", 1: "needs_sample", 4: "needs_sample"} {
		if _, err := e.svc.SetStatus(ctx, r.ID, images[i].ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.SaveAnnotation(ctx, r.ID, images[2].ID, SaveInput{Force: true, Objects: []dataset.AnnotationObject{
		{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}}); err != nil {
		t.Fatal(err)
	}
	ds2 := images[4].DatasetID
	for _, tt := range []struct {
		q    ImageQuery
		want []string
	}{
		{ImageQuery{Status: "all"}, []string{"NG/c.png", "a.png", "b.png", "d.png", "e.png", "f.png"}},
		{ImageQuery{Status: "pending"}, []string{"b.png", "d.png", "f.png"}},
		{ImageQuery{Status: "approved"}, []string{"NG/c.png"}},
		{ImageQuery{Status: "needs_sample"}, []string{"a.png", "e.png"}},
		{ImageQuery{Status: "reviewed"}, []string{"NG/c.png", "a.png", "e.png"}},
		{ImageQuery{DatasetID: ds2}, []string{"e.png", "f.png"}},
		{ImageQuery{Label: "dent"}, []string{"NG/c.png", "b.png", "e.png"}},
		{ImageQuery{Label: "scratch", Status: "needs_sample"}, []string{"a.png"}},
		{ImageQuery{Search: "NG/"}, []string{"NG/c.png"}},
		{ImageQuery{Search: "ng/"}, []string{"NG/c.png"}},
		{ImageQuery{Search: "ng"}, []string{"NG/c.png", "a.png", "b.png", "d.png", "e.png", "f.png"}}, // .png
		{ImageQuery{Edited: "edited"}, []string{"b.png"}},
		{ImageQuery{Edited: "unedited", DatasetID: ds2}, []string{"e.png", "f.png"}},
		{ImageQuery{Boxes: "with"}, []string{"NG/c.png", "a.png", "b.png", "e.png"}},
		{ImageQuery{Boxes: "without"}, []string{"d.png", "f.png"}},
		{ImageQuery{Inference: "failed"}, []string{"d.png"}},
	} {
		if got := paths(e.images(t, r.ID, tt.q)); !slices.Equal(got, tt.want) {
			t.Errorf("%+v: %v, want %v", tt.q, got, tt.want)
		}
	}
	page, total, err := e.svc.ListImages(ctx, r.ID, ImageQuery{Offset: 1, Limit: 2})
	if err != nil || total != 6 || !slices.Equal(paths(page), []string{"a.png", "b.png"}) {
		t.Errorf("page = %v, %d, %v", paths(page), total, err)
	}
	for _, q := range []ImageQuery{{Status: "x"}, {Edited: "x"}, {Boxes: "x"}, {Inference: "x"}, {Limit: 201}, {Offset: -1}} {
		if _, _, err := e.svc.ListImages(ctx, r.ID, q); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%+v: %v", q, err)
		}
	}
	if _, _, err := e.svc.ListImages(ctx, 999, ImageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown review: %v", err)
	}

	// Neighbors: the image itself need not match.
	n, err := e.svc.ImageNeighbors(ctx, r.ID, images[2].ID, ImageQuery{Status: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Prev == nil || n.Prev.Path != "a.png" || n.Next == nil || n.Next.Path != "e.png" || n.Position != 2 || n.Matches ||
		n.Total != 3 {
		t.Errorf("neighbors = %+v", n)
	}
	n, err = e.svc.ImageNeighbors(ctx, r.ID, images[0].ID, ImageQuery{})
	if err != nil || n.Prev != nil || n.Next == nil || n.Next.Path != "a.png" || n.Position != 0 || !n.Matches || n.Total != 6 {
		t.Errorf("first neighbors = %+v, %v", n, err)
	}
	n, err = e.svc.ImageNeighbors(ctx, r.ID, images[5].ID, ImageQuery{Status: "pending"})
	if err != nil || n.Prev == nil || n.Prev.Path != "d.png" || n.Next != nil || n.Position != 2 || !n.Matches {
		t.Errorf("last neighbors = %+v, %v", n, err)
	}
	if _, err := e.svc.ImageNeighbors(ctx, r.ID, 99999, ImageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown image: %v", err)
	}

	// Labels of the review annotations.
	labels, err := e.svc.ListLabels(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(labels, []Label{{Name: "dent", ObjectCount: 3, ImageCount: 3}, {Name: "scratch", ObjectCount: 2, ImageCount: 2}}) {
		t.Errorf("labels = %+v", labels)
	}
	got := e.review(t, r.ID)
	if got.Counts != (Counts{Total: 6, Inferred: 5, InferenceFailed: 1, Pending: 3, Approved: 1, NeedsSample: 2, Edited: 1}) {
		t.Errorf("counts = %+v", got.Counts)
	}
	if got.Datasets[1].Counts != (Counts{Total: 2, Inferred: 1, NeedsSample: 1, Pending: 1}) {
		t.Errorf("dataset counts = %+v", got.Datasets[1].Counts)
	}
}

func TestIntegrationAnnotationsAndStatus(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _ := e.inferredReview(t, "")
	c := e.detail(t, r.ID, images[0].ID)
	if c.Revision != 0 || c.Edited || len(c.Objects) != 2 || c.AnnotationVersion == "" {
		t.Fatalf("detail = %+v", c)
	}

	// A stale base version is refused, unless forced.
	edit := []dataset.AnnotationObject{{Name: " scratch ", Xmin: -3, Ymin: 1, Xmax: 10, Ymax: 100, Difficult: true}}
	_, err := e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Objects: edit, BaseVersion: "stale"})
	wantKind(t, err, ErrAnnotationChanged, "已被修改")
	saved, err := e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Objects: edit, BaseVersion: c.AnnotationVersion})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || !saved.Edited || saved.ObjectCount != 1 || len(saved.Objects) != 1 ||
		saved.Objects[0] != (dataset.AnnotationObject{Name: "scratch", Xmin: 0, Ymin: 1, Xmax: 10, Ymax: 30, Difficult: true}) ||
		saved.AnnotationVersion == c.AnnotationVersion || len(saved.Predictions) != 2 ||
		!slices.Equal(saved.Labels, []dataset.LabelCount{{Name: "scratch", Count: 1}}) {
		t.Errorf("saved = %+v", saved)
	}
	if reviewfile.AnnotationVersion(readFile(t, e.reviewFile(r.ID, saved.AnnotationPath))) != saved.AnnotationVersion {
		t.Error("the file is not the saved version")
	}
	// The same boxes again: nothing changes.
	same, err := e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Objects: saved.Objects, BaseVersion: saved.AnnotationVersion})
	if err != nil || same.Revision != 1 || same.AnnotationVersion != saved.AnnotationVersion {
		t.Errorf("unchanged save = %+v, %v", same, err)
	}
	// Back to the predictions (in another order): not edited any more.
	back, err := e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Force: true, Objects: []dataset.AnnotationObject{
		{Name: "dent", Xmin: 2, Ymin: 2, Xmax: 12, Ymax: 12}, {Name: "scratch", Xmin: 1, Ymin: 1, Xmax: 10, Ymax: 10, Truncated: true}}})
	if err != nil || back.Edited || back.Revision != 2 || back.ObjectCount != 2 {
		t.Errorf("back to predictions = %+v, %v", back, err)
	}
	// Invalid boxes.
	_, err = e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Force: true, Objects: []dataset.AnnotationObject{{Name: "", Xmax: 1, Ymax: 1}}})
	wantKind(t, err, ErrInvalid, "没有类别")
	_, err = e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Force: true, Objects: []dataset.AnnotationObject{{Name: "x", Xmin: 50, Xmax: 60, Ymax: 5}}})
	wantKind(t, err, ErrInvalid, "宽或高为 0")
	// An empty annotation is fine.
	emptied, err := e.svc.SaveAnnotation(ctx, r.ID, c.ID, SaveInput{Force: true})
	if err != nil || emptied.ObjectCount != 0 || !emptied.Edited || len(emptied.Objects) != 0 || emptied.AnnotationVersion == "" {
		t.Errorf("emptied = %+v, %v", emptied, err)
	}

	// The image whose inference failed can be edited: the save creates its file.
	failed := e.detail(t, r.ID, images[3].ID)
	if failed.AnnotationVersion != "" || exists(e.reviewFile(r.ID, failed.AnnotationPath)) {
		t.Fatalf("failed image = %+v", failed)
	}
	created, err := e.svc.SaveAnnotation(ctx, r.ID, failed.ID, SaveInput{Objects: []dataset.AnnotationObject{
		{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 5, Ymax: 5}}})
	if err != nil || created.AnnotationVersion == "" || !created.Edited || created.InferenceError == "" ||
		!exists(e.reviewFile(r.ID, created.AnnotationPath)) {
		t.Errorf("created = %+v, %v", created, err)
	}

	// Status changes.
	approved, err := e.svc.SetStatus(ctx, r.ID, c.ID, "approved")
	if err != nil || approved.Status != "approved" || approved.ReviewedAt == nil || approved.Revision != emptied.Revision+1 {
		t.Errorf("approved = %+v, %v", approved, err)
	}
	if again, err := e.svc.SetStatus(ctx, r.ID, c.ID, "approved"); err != nil || again.Revision != approved.Revision {
		t.Errorf("approved again = %+v, %v", again, err)
	}
	pending, err := e.svc.SetStatus(ctx, r.ID, c.ID, "pending")
	if err != nil || pending.Status != "pending" || pending.ReviewedAt != nil || pending.Revision != approved.Revision+1 {
		t.Errorf("pending = %+v, %v", pending, err)
	}
	_, err = e.svc.SetStatus(ctx, r.ID, c.ID, "done")
	wantKind(t, err, ErrInvalid, "status")
	if _, err := e.svc.SetStatus(ctx, r.ID, 99999, "approved"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown image: %v", err)
	}
	other, _, _ := e.inferredReview(t, "other-")
	if _, err := e.svc.SetStatus(ctx, other.ID, c.ID, "approved"); !errors.Is(err, ErrNotFound) {
		t.Errorf("image of another review: %v", err)
	}

	// Not inferred yet: refused.
	uninferred := images[5]
	_, err = e.svc.SetStatus(ctx, r.ID, uninferred.ID, "approved")
	wantKind(t, err, ErrConflict, "还没有识别完成")
	_, err = e.svc.SaveAnnotation(ctx, r.ID, uninferred.ID, SaveInput{})
	wantKind(t, err, ErrConflict, "还没有识别完成")

	// A file that cannot be parsed shows as an error; a forced save
	// replaces it. A missing file shows no boxes.
	bad := images[1]
	if err := os.WriteFile(e.reviewFile(r.ID, bad.AnnotationPath), []byte("<html/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	bd := e.detail(t, r.ID, bad.ID)
	if bd.AnnotationError == "" || len(bd.Objects) != 0 || bd.AnnotationVersion == "" {
		t.Errorf("unparseable = %+v", bd)
	}
	if _, err := e.svc.SaveAnnotation(ctx, r.ID, bad.ID, SaveInput{BaseVersion: bd.AnnotationVersion}); err != nil {
		t.Errorf("save over an unparseable file: %v", err)
	}
	if err := os.Remove(e.reviewFile(r.ID, bad.AnnotationPath)); err != nil {
		t.Fatal(err)
	}
	if md := e.detail(t, r.ID, bad.ID); md.AnnotationVersion != "" || len(md.Objects) != 0 || md.AnnotationError != "" {
		t.Errorf("missing file = %+v", md)
	}

	// Concurrent saves of one image are serialized: one wins per version.
	cur := e.detail(t, r.ID, images[4].ID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, conflicts int
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.SaveAnnotation(ctx, r.ID, cur.ID, SaveInput{BaseVersion: cur.AnnotationVersion,
				Objects: []dataset.AnnotationObject{{Name: "dent", Xmin: 1, Ymin: 1, Xmax: 2 + i, Ymax: 5}}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrAnnotationChanged):
				conflicts++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflicts != 7 {
		t.Errorf("concurrent saves: %d ok, %d conflicts", ok, conflicts)
	}
}

func TestIntegrationClassesAndFiles(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.project(t, "p")
	m := e.addModel(t, p, testModel{classes: []string{"scratch", "dent", "scratch", " "}})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})

	// The model's names while the project has no class list.
	list, err := e.svc.ListClasses(ctx, r.ID)
	if err != nil || list.Total != 2 || list.Version != "" || list.Items[0] != (Class{Name: "scratch"}) ||
		list.Items[1] != (Class{Name: "dent"}) {
		t.Errorf("model classes = %+v, %v", list, err)
	}
	pl, err := e.projects.SetClasses(ctx, p, []project.Class{{Name: "划痕", Color: "#ff0000"}, {Name: "凹坑"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.ListClasses(ctx, r.ID)
	if err != nil || list.Total != 2 || list.Version != pl.Version || list.Items[0] != (Class{Name: "划痕", Color: "#ff0000"}) {
		t.Errorf("project classes = %+v, %v", list, err)
	}
	if _, err := e.svc.ListClasses(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown review: %v", err)
	}

	// Image files and thumbnails come from the dataset.
	img := e.images(t, r.ID, ImageQuery{})[0]
	f, err := e.svc.OpenImage(ctx, r.ID, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f.File)
	f.File.Close()
	if len(data) == 0 || f.Version != img.ImageVersion {
		t.Errorf("image file: %d bytes, version %q (want %q)", len(data), f.Version, img.ImageVersion)
	}
	th, err := e.svc.OpenThumbnail(ctx, r.ID, img.ID, "sm")
	if err != nil {
		t.Fatal(err)
	}
	th.File.Close()
	if th.ContentType != "image/jpeg" {
		t.Errorf("thumbnail = %+v", th)
	}
	if _, err := e.svc.OpenThumbnail(ctx, r.ID, img.ID, "xl"); !errors.Is(err, dataset.ErrBadRequest) {
		t.Errorf("bad size: %v", err)
	}
	if _, err := e.svc.OpenImage(ctx, r.ID, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown image: %v", err)
	}
	if _, err := e.svc.OpenImage(ctx, 999, img.ID); !errors.Is(err, errReviewNotFound) {
		t.Errorf("unknown review: %v", err)
	}
	if err := os.Remove(e.datasetPath("ds", "a.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.OpenImage(ctx, r.ID, img.ID); !errors.Is(err, dataset.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	// The detail still shows.
	if d := e.detail(t, r.ID, img.ID); d.ID != img.ID {
		t.Errorf("detail of an image whose file is missing = %+v", d)
	}
}

func TestIntegrationListUpdateDelete(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p1, p2 := e.project(t, "产线A"), e.project(t, "产线B")
	m1, m2 := e.addModel(t, p1, testModel{}), e.addModel(t, p2, testModel{})
	ds1 := e.addDataset(t, "ds1", "ds1", testImage{rel: "a.png"})
	ds2 := e.addDataset(t, "ds2", "ds2", testImage{rel: "b.png"}, testImage{rel: "c.png"})
	r1 := e.create(t, CreateInput{Name: "首轮", Description: "白班样本", ModelID: m1.ID, DatasetIDs: []int64{ds1}})
	r2 := e.create(t, CreateInput{ModelID: m2.ID, DatasetIDs: []int64{ds1, ds2}})
	r3 := e.create(t, CreateInput{ModelID: m1.ID, DatasetIDs: []int64{ds2}})
	node := e.node(t, "n")
	e.claim(t, node)
	e.inferAll(t, r1.ID, node)

	ids := func(q ListQuery) []int64 {
		t.Helper()
		list, err := e.svc.ListReviews(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, r := range list {
			out = append(out, r.ID)
		}
		return out
	}
	for _, tt := range []struct {
		q    ListQuery
		want []int64
	}{
		{ListQuery{}, []int64{r3.ID, r2.ID, r1.ID}},
		{ListQuery{Search: "白班"}, []int64{r1.ID}},
		{ListQuery{Search: "产线b"}, []int64{r2.ID}},
		{ListQuery{ProjectID: p1}, []int64{r3.ID, r1.ID}},
		{ListQuery{DatasetID: ds2}, []int64{r3.ID, r2.ID}},
		{ListQuery{ModelID: m2.ID}, []int64{r2.ID}},
		{ListQuery{Status: StatusReady}, []int64{r1.ID}},
		{ListQuery{Status: StatusQueued}, []int64{r3.ID, r2.ID}},
	} {
		if got := ids(tt.q); !slices.Equal(got, tt.want) {
			t.Errorf("%+v: %v, want %v", tt.q, got, tt.want)
		}
	}
	if _, err := e.svc.ListReviews(ctx, ListQuery{Status: "x"}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("bad status: %v", err)
	}
	list, _ := e.svc.ListReviews(ctx, ListQuery{})
	if list[0].QueuePosition == nil || *list[0].QueuePosition != 2 || *list[1].QueuePosition != 1 || list[2].QueuePosition != nil ||
		len(list[1].Datasets) != 2 || list[1].Counts.Total != 3 || list[2].Status != StatusReady {
		t.Errorf("list = %+v", list)
	}

	// Uploads and merges show on the review.
	if _, err := e.db.Exec(ctx, `INSERT INTO review_uploads (review_id, file_name, overwrite, summary, problems)
		VALUES ($1, 'old.zip', false, '{"images": 1}', '[]'), ($1, 'new.zip', true, '{"images": 2, "applied": 1}',
			'[{"path": "a.png", "kind": "conflict", "message": "x"}]')`, r1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, on_existing, status, total,
			done, summary, finished_at)
		VALUES ($1, $2, '{needs_sample}', 'skip', 'succeeded', 1, 1, '{"merged": 1}', now()),
			($1, $2, '{approved,needs_sample}', 'overwrite', 'running', 1, 0, '{}', NULL)`, r1.ID, []int64{ds1}); err != nil {
		t.Fatal(err)
	}
	got := e.review(t, r1.ID)
	if got.LastUpload == nil || got.LastUpload.FileName != "new.zip" || !got.LastUpload.Overwrite ||
		got.LastUpload.Summary != (UploadSummary{Images: 2, Applied: 1}) || len(got.LastUpload.Problems) != 1 ||
		got.ActiveMerge == nil || got.ActiveMerge.OnExisting == nil || *got.ActiveMerge.OnExisting != "overwrite" ||
		got.ActiveMerge.Versions == nil || len(got.ActiveMerge.Versions) != 0 ||
		!slices.Equal(got.ActiveMerge.Statuses, []string{"approved", "needs_sample"}) ||
		got.LastMerge == nil || got.LastMerge.Summary.Merged != 1 || got.LastMerge.FinishedAt == nil {
		t.Errorf("uploads and merges = %+v / %+v / %+v", got.LastUpload, got.ActiveMerge, got.LastMerge)
	}

	// Update.
	up, err := e.svc.UpdateReview(ctx, r1.ID, UpdateInput{Name: ptr(" 第二轮 "), Description: ptr("")})
	if err != nil || up.Name != "第二轮" || up.Description != "" {
		t.Errorf("updated = %+v, %v", up, err)
	}
	_, err = e.svc.UpdateReview(ctx, r1.ID, UpdateInput{Name: ptr(" ")})
	wantKind(t, err, ErrInvalid, "名称不能为空")
	if _, err := e.svc.UpdateReview(ctx, 999, UpdateInput{Name: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of unknown review: %v", err)
	}

	// Delete: refused while a merge runs.
	_, err = e.svc.DeleteReview(ctx, r1.ID)
	wantKind(t, err, ErrConflict, "正在合并到数据集")
	if _, err := e.db.Exec(ctx, "UPDATE review_merges SET status = 'failed' WHERE status = 'running'"); err != nil {
		t.Fatal(err)
	}
	dir := e.svc.reviewDir(r1.ID)
	if res, err := e.svc.DeleteReview(ctx, r1.ID); err != nil || res != nil {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if exists(dir) {
		t.Error("the review folder is still there")
	}
	if _, err := e.svc.GetReview(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted review: %v", err)
	}
	if _, err := e.svc.DeleteReview(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}

	// A running inference is abandoned: the node gets "not found".
	task := e.claim(t, node)
	if task == nil || task.ID != r2.ID {
		t.Fatalf("claimed %+v", task)
	}
	if _, err := e.svc.DeleteReview(ctx, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReportProgress(ctx, r2.ID, node, ProgressInput{Stage: StageInferring}); !errors.Is(err, errTaskGone) {
		t.Errorf("progress of a deleted review: %v", err)
	}
	if _, err := e.svc.SubmitResults(ctx, r2.ID, node, []ImageResult{{ID: 1, OK: true}}); !errors.Is(err, errTaskGone) {
		t.Errorf("results of a deleted review: %v", err)
	}
	if _, err := e.svc.CompleteTask(ctx, r2.ID, node, nil); !errors.Is(err, errTaskGone) {
		t.Errorf("completion of a deleted review: %v", err)
	}
	if _, err := e.svc.FailTask(ctx, r2.ID, node, FailInput{}); !errors.Is(err, errTaskGone) {
		t.Errorf("failure of a deleted review: %v", err)
	}

	// A folder that cannot be removed completely: the review is gone, the
	// rest of the folder keeps its name.
	e.svc.removeAll = func(string) error { return os.ErrPermission }
	res, err := e.svc.DeleteReview(ctx, r3.ID)
	if err != nil || res == nil || res.FilesRemoved || !strings.Contains(res.Message, "权限不足") ||
		!strings.Contains(res.Message, e.svc.reviewDir(r3.ID)) || !exists(e.svc.reviewDir(r3.ID)) {
		t.Errorf("partial delete = %+v, %v", res, err)
	}
	if _, err := e.svc.GetReview(ctx, r3.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("partly deleted review: %v", err)
	}
}

func TestIntegrationDatasetChangesCascade(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds1 := e.addDataset(t, "ds1", "ds1", testImage{rel: "a.png"}, testImage{rel: "b.png"})
	ds2 := e.addDataset(t, "ds2", "ds2", testImage{rel: "c.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})

	// A dataset image removed by a rescan takes its review image along.
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_images WHERE dataset_id = $1 AND rel_path = 'a.png'", ds1); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, r.ID); got.Counts.Total != 2 || got.Datasets[0].Counts.Total != 1 {
		t.Errorf("after removing an image: %+v", got.Counts)
	}
	// A dataset in the recycle bin leaves the review, and comes back when
	// restored.
	if err := e.datasets.DeleteDataset(ctx, ds2, false); err != nil {
		t.Fatal(err)
	}
	got := e.review(t, r.ID)
	if got.Counts.Total != 1 || len(got.Datasets) != 1 || got.Datasets[0].ID != ds1 {
		t.Errorf("after deleting a dataset: %+v %+v", got.Counts, got.Datasets)
	}
	if _, err := e.datasets.RestoreDataset(ctx, ds2, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	got = e.review(t, r.ID)
	if got.Counts.Total != 2 || len(got.Datasets) != 2 || got.Datasets[1].ID != ds2 {
		t.Errorf("after restoring the dataset: %+v %+v", got.Counts, got.Datasets)
	}
	// A dataset renamed shows its current name.
	if _, err := e.db.Exec(ctx, "UPDATE datasets SET name = '新名字' WHERE id = $1", ds1); err != nil {
		t.Fatal(err)
	}
	if got := e.review(t, r.ID); got.Datasets[0].Name != "新名字" {
		t.Errorf("dataset name = %q", got.Datasets[0].Name)
	}
	// The project of the model deleted (to the recycle bin with the
	// model): the snapshot stays.
	if err := e.models.DeleteProjectModels(ctx, m.Project.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.projects.DeleteProject(ctx, m.Project.ID); err != nil {
		t.Fatal(err)
	}
	got = e.review(t, r.ID)
	if got.ModelExists || got.Project != nil || got.Model.ProjectName != "p" || got.Model.ID != m.ID {
		t.Errorf("after deleting the project: %+v", got)
	}
}

func TestIntegrationCleanup(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "p"), testModel{})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	root := e.st.Dir(storage.Reviews)
	mk := func(p string) string {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	trash := mk(filepath.Join(root, ".deleting-77"))
	orphan := mk(filepath.Join(root, "review-999"))
	busy := mk(filepath.Join(root, "review-1000"))
	other := mk(filepath.Join(root, "notes"))
	odd := mk(filepath.Join(root, "review-007"))
	e.svc.busy.add(busy)
	uploads := mk(e.svc.uploadsTempDir())
	stale := filepath.Join(uploads, "old.zip")
	fresh := filepath.Join(uploads, "new.zip")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("zip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.t = time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(fresh, e.clock.t, e.clock.t); err != nil {
		t.Fatal(err)
	}
	e.svc.cleanup(ctx)
	for p, want := range map[string]bool{trash: false, orphan: false, busy: true, other: true, odd: true,
		e.svc.reviewDir(r.ID): true, stale: false, fresh: true} {
		if exists(p) != want {
			t.Errorf("%s exists = %v, want %v", p, !want, want)
		}
	}
}
