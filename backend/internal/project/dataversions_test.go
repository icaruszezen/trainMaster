package project

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// addDataVersion inserts a data version row directly (package dataversion
// builds them); trashed puts it in the recycle bin (ready versions only).
func (e *testEnv) addDataVersion(t *testing.T, projectID int64, version int, status string, trashed bool) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(context.Background(), `INSERT INTO data_versions (project_id, version, dir_name, note,
			status, classes, config, sources, deleted_at)
		VALUES ($1, $2, $3, 'note', $4, '[]', '{}', '[]', CASE WHEN $5 THEN now() END) RETURNING id`,
		projectID, version, fmt.Sprintf("data-versions/project-%d/v%d", projectID, version), status, trashed).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *testEnv) projectRowExists(t *testing.T, id int64) bool {
	t.Helper()
	var n int
	if err := e.db.QueryRow(context.Background(), "SELECT count(*) FROM projects WHERE id = $1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// A project reports its data versions not in the recycle bin (any state)
// and the one with the highest version.
func TestIntegrationProjectDataVersionStats(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if p.DataVersionCount != 0 || p.LatestDataVersion != nil {
		t.Errorf("new project: %d data versions, latest %+v", p.DataVersionCount, p.LatestDataVersion)
	}
	e.addDataVersion(t, p.ID, 1, "ready", false)
	failed := e.addDataVersion(t, p.ID, 3, "failed", false)
	e.addDataVersion(t, p.ID, 2, "ready", false)
	e.addDataVersion(t, p.ID, 4, "ready", true) // in the bin: not counted, not the latest
	otherLatest := e.addDataVersion(t, other.ID, 1, "queued", false)

	got, err := e.svc.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DataVersionCount != 3 || got.LatestDataVersion == nil || got.LatestDataVersion.ID != failed ||
		got.LatestDataVersion.Version != 3 || got.LatestDataVersion.Status != "failed" ||
		got.LatestDataVersion.CreatedAt.IsZero() || got.LatestDataVersion.CreatedAt.Location() != time.UTC {
		t.Errorf("project = %d data versions, latest %+v", got.DataVersionCount, got.LatestDataVersion)
	}
	list, err := e.svc.ListProjects(ctx, "")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListProjects = %+v, %v", list, err)
	}
	for _, x := range list {
		want, wantLatest := 3, failed
		if x.ID == other.ID {
			want, wantLatest = 1, otherLatest
		}
		if x.DataVersionCount != want || x.LatestDataVersion == nil || x.LatestDataVersion.ID != wantLatest {
			t.Errorf("listed %s: %d data versions, latest %+v", x.Name, x.DataVersionCount, x.LatestDataVersion)
		}
	}
}

// A project with data versions in use cannot be deleted (the models are
// checked first); one whose data versions are in the bin goes there with
// them, and is purged once neither models nor data versions are left.
func TestIntegrationProjectDeleteWithDataVersions(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	a := e.addDataset(t, "a", image{"1.jpg", map[string]int{"dent": 1}})
	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测", DatasetIDs: []int64{a}})
	if err != nil {
		t.Fatal(err)
	}
	m := e.addModel(t, p.ID, 1, "yolo11")
	v1 := e.addDataVersion(t, p.ID, 1, "ready", false)
	e.addDataVersion(t, p.ID, 2, "building", false)

	// Models first, so that the existing message stays.
	wantKind(t, e.svc.DeleteProject(ctx, p.ID), ErrHasModels, "项目中有 1 个模型，请先删除模型，或选择同时删除模型")
	wantKind(t, e.svc.CheckDelete(ctx, p.ID, false, false), ErrHasModels, "项目中有 1 个模型")
	wantKind(t, e.svc.CheckDelete(ctx, p.ID, false, true), ErrHasModels, "项目中有 1 个模型")
	wantKind(t, e.svc.CheckDelete(ctx, p.ID, true, false), ErrHasDataVersions,
		"项目中有 2 个数据版本，请先删除数据版本，或选择同时删除数据版本")
	if err := e.svc.CheckDelete(ctx, p.ID, true, true); err != nil {
		t.Errorf("CheckDelete with both: %v", err)
	}
	wantKind(t, e.svc.CheckDelete(ctx, 1<<40, true, true), ErrNotFound, "项目不存在")

	e.trashModels(t, m)
	wantKind(t, e.svc.DeleteProject(ctx, p.ID), ErrHasDataVersions, "项目中有 2 个数据版本")
	if !e.projectRowExists(t, p.ID) {
		t.Fatal("a refused delete must keep the project")
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM data_versions WHERE project_id = $1 AND status = 'building'", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE data_versions SET deleted_at = now() WHERE id = $1", v1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CheckDelete(ctx, p.ID, false, false); err != nil {
		t.Errorf("CheckDelete with everything in the bin: %v", err)
	}
	if err := e.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := e.svc.GetProject(ctx, p.ID); err == nil {
		t.Error("the deleted project must be out of use")
	}
	if !e.projectRowExists(t, p.ID) {
		t.Fatal("a project with a model and a data version in the bin must stay")
	}

	// Purged only once both the model and the data version are gone.
	if ids, err := PurgeEmptyProjects(ctx, e.db.Pool); err != nil || len(ids) != 0 {
		t.Errorf("PurgeEmptyProjects with both = %v, %v", ids, err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM models WHERE id = $1", m); err != nil {
		t.Fatal(err)
	}
	if ids, err := PurgeEmptyProjects(ctx, e.db.Pool); err != nil || len(ids) != 0 {
		t.Errorf("PurgeEmptyProjects with the data version left = %v, %v", ids, err)
	}
	if !e.projectRowExists(t, p.ID) {
		t.Fatal("purged while a data version is in the bin")
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM data_versions WHERE id = $1", v1); err != nil {
		t.Fatal(err)
	}
	ids, err := PurgeEmptyProjects(ctx, e.db.Pool)
	if err != nil || !slices.Equal(ids, []int64{p.ID}) {
		t.Errorf("PurgeEmptyProjects = %v, %v", ids, err)
	}
	if e.projectRowExists(t, p.ID) {
		t.Error("the empty project must be purged")
	}
	if ids, err := PurgeEmptyProjects(ctx, e.db.Pool); err != nil || ids == nil || len(ids) != 0 {
		t.Errorf("PurgeEmptyProjects again = %#v, %v", ids, err)
	}

	// Only data versions in the bin (no models at all): soft delete too;
	// without any, a hard delete.
	q, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "q"})
	if err != nil {
		t.Fatal(err)
	}
	e.addDataVersion(t, q.ID, 1, "ready", true)
	if err := e.svc.DeleteProject(ctx, q.ID); err != nil || !e.projectRowExists(t, q.ID) {
		t.Errorf("DeleteProject with a data version in the bin: %v, row kept %v", err, e.projectRowExists(t, q.ID))
	}
	r, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteProject(ctx, r.ID); err != nil || e.projectRowExists(t, r.ID) {
		t.Errorf("DeleteProject without anything: %v, row kept %v", err, e.projectRowExists(t, r.ID))
	}
	// A project in use is never purged.
	s, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := PurgeEmptyProjects(ctx, e.db.Pool); err != nil || slices.Contains(ids, s.ID) {
		t.Errorf("PurgeEmptyProjects = %v, %v", ids, err)
	}
}
