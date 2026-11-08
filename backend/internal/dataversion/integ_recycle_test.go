package dataversion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

func (e *integEnv) integTrashed(t *testing.T) []TrashedDataVersion {
	t.Helper()
	items, err := e.svc.ListTrashed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// integSetDeleted moves the deletion time of a version in the bin.
func (e *integEnv) integSetDeleted(t *testing.T, id int64, ago time.Duration) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(), "UPDATE data_versions SET deleted_at = now() - $2 * interval '1 second' WHERE id = $1",
		id, int64(ago/time.Second)); err != nil {
		t.Fatal(err)
	}
}

// integTrashedModel inserts a model of a project in the recycle bin.
func (e *integEnv) integTrashedModel(t *testing.T, projectID int64) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), `INSERT INTO models (project_id, version, source, yolo_version, task,
			file_name, dir_name, size_bytes, sha256, deleted_at)
		VALUES ($1, 1, 'upload', 'yolo11', 'detect', 'best.pt', $2, 1, 'x', now()) RETURNING id`,
		projectID, fmt.Sprintf("project-%d/v1", projectID)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntegrationDataVersionRecycleBin(t *testing.T) {
	e := integNewService(t)
	ctx := context.Background()
	pid := e.integProject(t, "外观检测", "scratch")
	sources := []Source{{DatasetID: 999999, DatasetName: "已删除的数据集", DirName: "gone", IncludeBase: true}}
	v1 := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Version: 1, Sources: sources,
		Images: []integImageSpec{{Path: "images/gone/a.jpg"}}})
	v2 := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Version: 2})
	v3 := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Version: 3, Status: StatusFailed})

	// Only ready versions go to the bin.
	integWantKind(t, e.svc.moveToBin(ctx, v3.ID), ErrNotFound, "数据版本不存在")
	integWantKind(t, e.svc.moveToBin(ctx, 1<<40), ErrNotFound, "数据版本不存在")
	if err := e.svc.moveToBin(ctx, v1.ID); err != nil {
		t.Fatal(err)
	}
	integWantKind(t, e.svc.moveToBin(ctx, v1.ID), ErrNotFound, "数据版本不存在")
	if _, err := e.svc.Get(ctx, v1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of a version in the bin = %v", err)
	}
	if list, err := e.svc.List(ctx, ListFilter{}); err != nil || len(list) != 2 {
		t.Errorf("List = %d versions, %v", len(list), err)
	}
	if !integExists(v1.Dir) {
		t.Error("the folder of a version in the bin must stay")
	}
	if err := e.svc.moveToBin(ctx, v2.ID); err != nil {
		t.Fatal(err)
	}
	e.integSetDeleted(t, v1.ID, time.Hour)

	items := e.integTrashed(t)
	if len(items) != 2 || items[0].ID != v2.ID || items[1].ID != v1.ID {
		t.Fatalf("recycle bin = %+v", items)
	}
	it := items[1]
	if it.ProjectDeleted || it.ProjectName != "外观检测" || it.DeletedAt.Location() != time.UTC ||
		time.Since(it.DeletedAt) < 59*time.Minute || !it.PurgeAt.Equal(recycle.PurgeAt(it.DeletedAt)) ||
		len(it.Sources) != 1 || !it.Sources[0].DatasetDeleted || it.Status != StatusReady || it.ImageCount != 1 {
		t.Errorf("trashed version = %+v", it)
	}

	// Restore: back in use, with the folder of a purged dataset.
	got, err := e.svc.Restore(ctx, v1.ID, RestoreInput{})
	if err != nil || got.ID != v1.ID || got.ProjectName != "外观检测" || !got.Sources[0].DatasetDeleted {
		t.Fatalf("Restore = %+v, %v", got, err)
	}
	if _, err := e.svc.Restore(ctx, v1.ID, RestoreInput{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Restore of a version in use = %v", err)
	} else {
		integWantKind(t, err, ErrNotFound, "回收站中没有这个数据版本")
	}
	if _, _, err := e.svc.ListImages(ctx, v1.ID, ImageFilter{}); err != nil {
		t.Errorf("images after the restore: %v", err)
	}

	// The project deleted with its versions in the bin goes there too; a
	// restore brings it back, under another name when its own is taken.
	if _, err := e.db.Exec(ctx, "DELETE FROM data_versions WHERE id = $1", v3.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.moveToBin(ctx, v1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.projects.DeleteProject(ctx, pid); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	items = e.integTrashed(t)
	if len(items) != 2 || !items[0].ProjectDeleted || !items[1].ProjectDeleted {
		t.Fatalf("recycle bin with the project deleted = %+v", items)
	}
	if _, err := e.projects.CreateProject(ctx, project.CreateProjectInput{Name: "外观检测"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Restore(ctx, v1.ID, RestoreInput{})
	var pe *project.Error
	if !errors.As(err, &pe) || !errors.Is(err, project.ErrNameTaken) {
		t.Fatalf("Restore with the project name taken = %v", err)
	}
	bad := " "
	if _, err := e.svc.Restore(ctx, v1.ID, RestoreInput{ProjectName: &bad}); !errors.Is(err, project.ErrInvalid) {
		t.Errorf("Restore with an empty project name = %v", err)
	}
	if len(e.integTrashed(t)) != 2 {
		t.Error("a refused restore must change nothing")
	}
	name := "外观检测（旧）"
	got, err = e.svc.Restore(ctx, v1.ID, RestoreInput{ProjectName: &name})
	if err != nil || got.ProjectID != pid || got.ProjectName != name {
		t.Fatalf("Restore with a new project name = %+v, %v", got, err)
	}
	if p, err := e.projects.GetProject(ctx, pid); err != nil || p.Name != name || p.DataVersionCount != 1 {
		t.Errorf("restored project = %+v, %v", p, err)
	}
	if items := e.integTrashed(t); len(items) != 1 || items[0].ID != v2.ID || items[0].ProjectDeleted ||
		items[0].ProjectName != name {
		t.Errorf("recycle bin = %+v", items)
	}
	if got, err := e.svc.Restore(ctx, v2.ID, RestoreInput{ProjectName: &bad}); err != nil || got.ID != v2.ID {
		t.Errorf("Restore with the project in use (the name is ignored) = %+v, %v", got, err)
	}
	if items := e.integTrashed(t); items == nil || len(items) != 0 {
		t.Errorf("empty recycle bin = %#v", items)
	}
}

func TestIntegrationDataVersionJanitor(t *testing.T) {
	e := integNewService(t)
	ctx := context.Background()
	root := e.svc.rootDir()

	// Project p: an expired version, one in the bin for a day and one in use.
	p := e.integProject(t, "p", "scratch")
	expired := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: p, Version: 1,
		Images: []integImageSpec{{Path: "images/d/a.jpg"}, {Path: "images/d/a-aug1.jpg", AugmentedFrom: 1}}})
	recent := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: p, Version: 2})
	live := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: p, Version: 3, Images: []integImageSpec{{Path: "images/d/b.jpg"}}})
	for _, v := range []integVersion{expired, live} {
		th, err := e.svc.OpenThumbnail(ctx, v.ID, v.ImageIDs[0], "sm")
		if err != nil {
			t.Fatal(err)
		}
		th.File.Close()
	}
	for _, v := range []integVersion{expired, recent} {
		if err := e.svc.moveToBin(ctx, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.integSetDeleted(t, expired.ID, recycle.Retention+time.Hour)
	e.integSetDeleted(t, recent.ID, 24*time.Hour)

	// Project q: deleted with a model and a data version in the bin; purged
	// only once both are gone. Project r: with a data version only.
	q := e.integProject(t, "q", "scratch")
	qv := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: q, Version: 1, Images: []integImageSpec{{Path: "images/d/c.jpg"}}})
	qm := e.integTrashedModel(t, q)
	r := e.integProject(t, "r", "scratch")
	rv := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: r, Version: 1})
	for _, x := range []struct {
		project int64
		version integVersion
	}{{q, qv}, {r, rv}} {
		if err := e.svc.moveToBin(ctx, x.version.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.projects.DeleteProject(ctx, x.project); err != nil {
			t.Fatal(err)
		}
		e.integSetDeleted(t, x.version.ID, recycle.Retention+time.Minute)
	}

	// Leftovers on disk.
	writeFile := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldDeleting := filepath.Join(root, fmt.Sprintf(".deleting-77-%d", time.Now().Add(-time.Hour).UnixNano()))
	writeFile(filepath.Join(oldDeleting, "images", "d", "a.jpg"))
	noTime := filepath.Join(root, ".deleting-78")
	writeFile(filepath.Join(noTime, "x"))
	youngDeleting := filepath.Join(root, fmt.Sprintf(".deleting-79-%d", time.Now().UnixNano()))
	writeFile(filepath.Join(youngDeleting, "x"))
	orphanThumbs := filepath.Join(e.st.Dir(storage.Cache), "data-versions", "999999")
	writeFile(filepath.Join(orphanThumbs, "1-sm.jpg"))
	goneProject := filepath.Join(root, "project-888888")
	if err := os.MkdirAll(goneProject, 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "project-777777", "v1") // no .pending: maybe from a backup
	writeFile(filepath.Join(backup, "version.json"))
	interrupted := filepath.Join(root, "project-"+strconv.FormatInt(p, 10), "v9") // .pending without a version
	writeFile(filepath.Join(interrupted, pendingMarker))
	building := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: p, Version: 10, Status: StatusBuilding})
	writeFile(filepath.Join(building.Dir, pendingMarker))
	notAProject := filepath.Join(root, "project-x")
	if err := os.MkdirAll(notAProject, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.janitorPass(ctx); err != nil {
		t.Fatalf("janitorPass: %v", err)
	}
	e.svc.WaitBackground()

	// The expired version is purged with its rows, folder and thumbnails.
	if n := e.integCount(t, "SELECT count(*) FROM data_versions WHERE id = $1", expired.ID); n != 0 {
		t.Error("the expired version must be purged")
	}
	if n := e.integCount(t, "SELECT count(*) FROM data_version_images WHERE version_id = $1", expired.ID); n != 0 {
		t.Errorf("%d image rows of the purged version left", n)
	}
	if integExists(expired.Dir) || integExists(e.svc.thumbDir(expired.ID)) {
		t.Error("the folder and the thumbnails of the purged version must be removed")
	}
	if items := e.integTrashed(t); len(items) != 1 || items[0].ID != recent.ID {
		t.Errorf("recycle bin = %+v", items)
	}
	if !integExists(recent.Dir) || !integExists(live.Dir) || !integExists(e.svc.thumbDir(live.ID)) {
		t.Error("versions not expired must keep their folders and thumbnails")
	}
	// q waits for its model; r is purged with its folder.
	if n := e.integCount(t, "SELECT count(*) FROM projects WHERE id = $1", q); n != 1 {
		t.Error("a project with a model in the bin must stay")
	}
	if n := e.integCount(t, "SELECT count(*) FROM data_versions WHERE project_id = ANY($1)", []int64{q, r}); n != 0 {
		t.Errorf("%d expired versions of the deleted projects left", n)
	}
	if n := e.integCount(t, "SELECT count(*) FROM projects WHERE id = $1", r); n != 0 {
		t.Error("the deleted project without models must be purged with its last version")
	}
	if integExists(filepath.Join(root, "project-"+strconv.FormatInt(r, 10))) {
		t.Error("the folder of the purged project must be removed")
	}
	// Reconciliation.
	for _, gone := range []string{oldDeleting, noTime, orphanThumbs, goneProject, interrupted} {
		if integExists(gone) {
			t.Errorf("%s must be removed", gone)
		}
	}
	for _, kept := range []string{youngDeleting, backup, building.Dir, notAProject} {
		if !integExists(kept) {
			t.Errorf("%s must stay", kept)
		}
	}

	// Package model purges q's model; the next pass purges q.
	if _, err := e.db.Exec(ctx, "DELETE FROM models WHERE id = $1", qm); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.janitorPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.integCount(t, "SELECT count(*) FROM projects WHERE id = $1", q); n != 0 {
		t.Error("the deleted project must be purged once its model is gone too")
	}
	if integExists(filepath.Join(root, "project-"+strconv.FormatInt(q, 10))) {
		t.Error("the folder of the purged project must be removed")
	}
	if n := e.integCount(t, "SELECT count(*) FROM projects WHERE id = $1", p); n != 1 {
		t.Error("a project in use must stay")
	}
}

// The janitor runs at start and stops with its context.
func TestIntegrationDataVersionJanitorLoop(t *testing.T) {
	e := integNewService(t)
	v := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: e.integProject(t, "p", "scratch")})
	if err := e.svc.moveToBin(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	e.integSetDeleted(t, v.ID, recycle.Retention+time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.janitorLoop(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for e.integCount(t, "SELECT count(*) FROM data_versions WHERE id = $1", v.ID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the janitor did not purge at start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("janitorLoop did not stop")
	}
}

func TestDataVersionDeletingAge(t *testing.T) {
	now := time.Now()
	if d := deletingAge(fmt.Sprintf(".deleting-3-%d", now.Add(-time.Hour).UnixNano())); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("age = %v", d)
	}
	for _, name := range []string{".deleting-3", ".deleting-", ".deleting-3-x"} {
		if d := deletingAge(name); d < 24*time.Hour {
			t.Errorf("deletingAge(%q) = %v, want old", name, d)
		}
	}
	for _, tt := range []struct {
		name, prefix string
		id           int64
		ok           bool
	}{{"project-12", "project-", 12, true}, {"v3", "v", 3, true}, {"project-012", "project-", 0, false},
		{"project-0", "project-", 0, false}, {"project-", "project-", 0, false}, {"v3a", "v", 0, false}, {"12", "", 12, true}} {
		if id, ok := parseFolderID(tt.name, tt.prefix); id != tt.id || ok != tt.ok {
			t.Errorf("parseFolderID(%q) = %d, %v", tt.name, id, ok)
		}
	}
}
