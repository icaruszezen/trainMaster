package model

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// A project in the recycle bin with a model and a data version there is
// purged only once both are gone (whichever cleanup purges it); the
// folders in models/ of projects that are gone are removed.
func TestIntegrationPurgeProjectWaitsForDataVersions(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	projects := project.NewService(project.Options{DB: e.db.Pool, Logger: slog.New(slog.DiscardHandler)})
	projectID := e.project(t, "p")
	m := e.addModel(t, projectID, "yolo11")
	var versionID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO data_versions (project_id, version, dir_name, note, status, classes,
			config, sources, deleted_at)
		VALUES ($1, 1, 'data-versions/project-1/v1', 'n', 'ready', '[]', '{}', '[]', now()) RETURNING id`,
		projectID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteProjectModels(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if err := projects.DeleteProject(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(e.st.Dir(storage.Models), fmt.Sprintf("project-%d", projectID))

	e.expireModels(t, projectID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetModel(ctx, m.ID); err == nil {
		t.Error("the expired model must be purged")
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM projects WHERE id = $1", projectID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("project purged while its data version is in the bin: %d rows, %v", n, err)
	}
	if !exists(projectDir) {
		t.Error("the folder of a project still in the bin was removed")
	}

	// Package dataversion purges the data version and then the project;
	// the model folder of the project is removed by the next cleanup.
	if _, err := e.db.Exec(ctx, "DELETE FROM data_versions WHERE id = $1", versionID); err != nil {
		t.Fatal(err)
	}
	if ids, err := project.PurgeEmptyProjects(ctx, e.db.Pool); err != nil || len(ids) != 1 {
		t.Fatalf("PurgeEmptyProjects = %v, %v", ids, err)
	}
	// A folder that is not a project folder and one that is not empty stay.
	other := filepath.Join(e.st.Dir(storage.Models), "project-x")
	kept := filepath.Join(e.st.Dir(storage.Models), "project-999999")
	for _, dir := range []string{other, filepath.Join(kept, "v1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(projectDir) {
		t.Error("the empty folder of a purged project must be removed")
	}
	if !exists(other) || !exists(kept) {
		t.Error("folders that are not empty project folders must stay")
	}
}
