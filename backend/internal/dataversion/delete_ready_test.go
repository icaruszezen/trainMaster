package dataversion

// Deleting ready versions (they go to the recycle bin: moveToBin).

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
)

func TestCoreDeleteReady(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 10))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	v1 := e.coreCreateBuilt(t, in)
	v2 := e.coreCreateBuilt(t, in)

	// Two deletes at once: one moves it to the bin, the other finds
	// nothing.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.svc.Delete(ctx, v2.ID)
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), ErrNotFound) {
		t.Errorf("deletes = %v", errs)
	}
	if _, err := e.svc.Get(ctx, v2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v", err)
	}
	e.svc.WaitBackground()
	if _, err := os.Stat(e.coreDir(t, v2)); err != nil {
		t.Errorf("the folder of a version in the bin: %v", err)
	}
	trashed, err := e.svc.ListTrashed(ctx)
	if err != nil || len(trashed) != 1 || trashed[0].ID != v2.ID {
		t.Errorf("ListTrashed = %+v, %v", trashed, err)
	}
	note := "x"
	if _, err := e.svc.Update(ctx, v2.ID, UpdateInput{Note: &note}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update = %v", err)
	}
	if _, err := e.svc.Retry(ctx, v2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Retry = %v", err)
	}
	// The preview and the next version compare with v1, the newest ready
	// version in use.
	pre, err := e.svc.Preview(ctx, in)
	if err != nil || pre.Compare == nil || pre.Compare.ID != v1.ID {
		t.Errorf("preview compare = %+v, %v", pre.Compare, err)
	}
	v3 := e.coreCreateBuilt(t, in)
	if v3.Version != 3 || v3.Stats.Compare == nil || v3.Stats.Compare.ID != v1.ID {
		t.Errorf("v3 = %d, compare %+v", v3.Version, v3.Stats.Compare)
	}

	// DeleteProjectVersions: ready ones to the bin, the others removed.
	queued, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	failed := coreFailedVersion(t, e, in)
	if err := e.svc.DeleteProjectVersions(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := e.svc.List(ctx, ListFilter{ProjectID: p.ID}); err != nil || len(list) != 0 {
		t.Errorf("List = %+v, %v", list, err)
	}
	trashed, err = e.svc.ListTrashed(ctx)
	if err != nil || len(trashed) != 3 {
		t.Errorf("ListTrashed = %+v, %v", trashed, err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM data_versions WHERE id = ANY($1)",
		[]int64{queued.ID, failed.ID}).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d unbuilt versions left (%v)", n, err)
	}
	for _, v := range []DataVersion{v1, v3} {
		if _, err := os.Stat(e.coreDir(t, v)); err != nil {
			t.Errorf("folder of v%d: %v", v.Version, err)
		}
	}
}
