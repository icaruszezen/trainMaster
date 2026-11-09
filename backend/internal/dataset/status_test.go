package dataset

import (
	"context"
	"slices"
	"testing"
)

func TestNormalizeDatasetStatus(t *testing.T) {
	for _, v := range append([]string{DatasetStatusNone}, DatasetStatuses...) {
		if got, err := NormalizeDatasetStatus(v); err != nil || got != v {
			t.Errorf("NormalizeDatasetStatus(%q) = %q, %v", v, got, err)
		}
	}
	for _, v := range []string{"none", "Trainable", " trainable", "trainable ", "done", "可训练"} {
		_, err := NormalizeDatasetStatus(v)
		wantKind(t, err, ErrInvalid, "数据集状态无效")
	}
	if len(DatasetStatuses) != 5 || slices.Contains(DatasetStatuses, DatasetStatusNone) {
		t.Errorf("DatasetStatuses = %q", DatasetStatuses)
	}
}

func TestIntegrationDatasetStatus(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	a, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != DatasetStatusNone || a.StatusUpdatedAt != nil {
		t.Fatalf("a new dataset is not marked: %+v", a)
	}

	// Setting the status: status_updated_at is set, updated_at (the order
	// of the list) is not touched.
	annotating := DatasetStatusAnnotating
	got, err := e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Status: &annotating})
	if err != nil || got.Status != DatasetStatusAnnotating || got.StatusUpdatedAt == nil {
		t.Fatalf("UpdateDataset(status) = %+v, %v", got, err)
	}
	if !got.UpdatedAt.Equal(a.UpdatedAt) || got.Name != "a" {
		t.Errorf("changing the status changed other fields: %+v (was %+v)", got, a)
	}
	if got.StatusUpdatedAt.Location().String() != "UTC" {
		t.Errorf("status_updated_at is not UTC: %v", got.StatusUpdatedAt)
	}
	first := *got.StatusUpdatedAt

	// The same status again is no change.
	again, err := e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Status: &annotating})
	if err != nil || !again.StatusUpdatedAt.Equal(first) {
		t.Errorf("setting the same status again = %+v, %v; want status_updated_at %v", again, err, first)
	}

	// Another status moves status_updated_at.
	trainable := DatasetStatusTrainable
	got, err = e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Status: &trainable})
	if err != nil || got.Status != DatasetStatusTrainable || !got.StatusUpdatedAt.After(first) {
		t.Errorf("UpdateDataset(trainable) = %+v, %v", got, err)
	}

	// Invalid statuses change nothing.
	for _, bad := range []string{"done", "none", "Trainable"} {
		_, err := e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Status: &bad})
		wantKind(t, err, ErrInvalid, "数据集状态无效")
	}
	// A bad name rolls back a status given with it.
	reviewing, badName := DatasetStatusReviewing, "a/b"
	_, err = e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Name: &badName, Status: &reviewing})
	wantKind(t, err, ErrInvalid, "名称不能包含")
	if got, _ := e.svc.GetDataset(ctx, a.ID); got.Status != DatasetStatusTrainable {
		t.Errorf("status after a failed update = %q", got.Status)
	}
	// With a name, updated_at is bumped as before.
	name := "a2"
	renamed, err := e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Name: &name, Status: &reviewing})
	if err != nil || renamed.Name != "a2" || renamed.Status != DatasetStatusReviewing || !renamed.UpdatedAt.After(a.UpdatedAt) {
		t.Errorf("UpdateDataset(name, status) = %+v, %v", renamed, err)
	}
	_, err = e.svc.UpdateDataset(ctx, 999999, UpdateDatasetInput{Status: &reviewing})
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// Filtering.
	ids := func(q DatasetQuery) []int64 {
		t.Helper()
		list, err := e.svc.ListDatasets(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, d := range list {
			out = append(out, d.ID)
		}
		return out
	}
	none := DatasetStatusNone
	pending := DatasetStatusPendingReview
	for _, c := range []struct {
		q    DatasetQuery
		want []int64
	}{
		{DatasetQuery{}, []int64{a.ID, b.ID}},
		{DatasetQuery{Status: &reviewing}, []int64{a.ID}},
		{DatasetQuery{Status: &none}, []int64{b.ID}},
		{DatasetQuery{Status: &pending}, nil},
		{DatasetQuery{Status: &reviewing, Search: "b"}, nil},
	} {
		if got := ids(c.q); !slices.Equal(got, c.want) {
			t.Errorf("ListDatasets(%+v) = %v, want %v", c.q, got, c.want)
		}
	}

	// Clearing it.
	got, err = e.svc.UpdateDataset(ctx, a.ID, UpdateDatasetInput{Status: &none})
	if err != nil || got.Status != DatasetStatusNone || got.StatusUpdatedAt == nil {
		t.Errorf("clearing the status = %+v, %v", got, err)
	}

	// The status survives the recycle bin.
	if _, err := e.svc.UpdateDataset(ctx, b.ID, UpdateDatasetInput{Status: &pending}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteDataset(ctx, b.ID, false); err != nil {
		t.Fatal(err)
	}
	trashed, err := e.svc.ListTrashedDatasets(ctx)
	if err != nil || len(trashed) != 1 || trashed[0].Status != DatasetStatusPendingReview {
		t.Fatalf("ListTrashedDatasets = %+v, %v", trashed, err)
	}
	restored, err := e.svc.RestoreDataset(ctx, b.ID, RestoreInput{})
	if err != nil || restored.Status != DatasetStatusPendingReview {
		t.Errorf("RestoreDataset = %+v, %v", restored, err)
	}
}
