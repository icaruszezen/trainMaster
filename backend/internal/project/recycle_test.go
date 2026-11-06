package project

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// trashModels moves models to the recycle bin as package model does.
func (e *testEnv) trashModels(t *testing.T, ids ...int64) {
	t.Helper()
	if _, err := e.db.Exec(context.Background(), "UPDATE models SET deleted_at = now() WHERE id = ANY($1)", ids); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) restore(ctx context.Context, id int64, name *string) (string, error) {
	var got string
	err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error {
		var err error
		got, err = RestoreInTx(ctx, tx, id, name)
		return err
	})
	return got, err
}

// A project whose models are all in the recycle bin goes there with them
// (out of use, its name free) and comes back when one of them is restored.
func TestIntegrationProjectRecycleBin(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	a := e.addDataset(t, "a", image{"1.jpg", map[string]int{"dent": 1}})
	b := e.addDataset(t, "b")
	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测", DatasetIDs: []int64{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	m1 := e.addModel(t, p.ID, 1, "yolov8")
	m2 := e.addModel(t, p.ID, 2, "yolo11")

	// Models in the bin do not count.
	e.trashModels(t, m2)
	if got, err := e.svc.GetProject(ctx, p.ID); err != nil || got.ModelCount != 1 || got.LatestModel == nil ||
		got.LatestModel.ID != m1 {
		t.Errorf("project with a model in the bin = %+v, %v", got, err)
	}
	wantKind(t, e.svc.DeleteProject(ctx, p.ID), ErrHasModels, "项目中有 1 个模型")

	e.trashModels(t, m1)
	if err := e.datasets.DeleteDataset(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO model_uploads (project_id, yolo_version, file_name, file_size, status)
		VALUES ($1, 'yolo11', 'best.pt', 10, 'uploading')`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	_, err = e.svc.GetProject(ctx, p.ID)
	wantKind(t, err, ErrNotFound, "项目不存在")
	wantKind(t, e.svc.DeleteProject(ctx, p.ID), ErrNotFound, "项目不存在")
	wantKind(t, e.svc.Exists(ctx, p.ID), ErrNotFound, "项目不存在")
	_, err = e.svc.AddDatasets(ctx, p.ID, []int64{a})
	wantKind(t, err, ErrNotFound, "项目不存在")
	_, err = e.svc.ListClasses(ctx, p.ID)
	wantKind(t, err, ErrNotFound, "项目不存在")
	if list, err := e.svc.ListProjects(ctx, ""); err != nil || len(list) != 0 {
		t.Errorf("ListProjects = %+v, %v", list, err)
	}
	if got := e.datasetProjects(t, a); len(got) != 0 {
		t.Errorf("projects of a = %q", got)
	}
	var kept bool
	var recycled, uploads int
	if err := e.db.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,
			(SELECT count(*) FROM recycled_project_datasets WHERE project_id = $1),
			(SELECT count(*) FROM model_uploads WHERE project_id = $1)
		FROM projects WHERE id = $1`, p.ID).Scan(&kept, &recycled, &uploads); err != nil || !kept || recycled != 2 || uploads != 0 {
		t.Errorf("project row kept %v, recycled links %d, uploads %d, %v", kept, recycled, uploads, err)
	}

	// Its name is free meanwhile; restoring it then needs another one.
	if _, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测"}); err != nil {
		t.Fatalf("the name of a project in the bin must be free: %v", err)
	}
	_, err = e.restore(ctx, p.ID, nil)
	wantKind(t, err, ErrNameTaken, "已存在同名项目“外观检测”")
	bad := " "
	_, err = e.restore(ctx, p.ID, &bad)
	wantKind(t, err, ErrInvalid, "名称不能为空")
	name := "外观检测（旧）"
	if got, err := e.restore(ctx, p.ID, &name); err != nil || got != name {
		t.Fatalf("restore = %q, %v", got, err)
	}
	got, err := e.svc.GetProject(ctx, p.ID)
	if err != nil || got.Name != name || got.DatasetCount != 1 || got.ModelCount != 0 {
		t.Errorf("restored project = %+v, %v", got, err)
	}
	if got := e.datasetProjects(t, a); !slices.Equal(got, []string{name}) {
		t.Errorf("projects of a = %q", got)
	}
	// The dataset that was in the bin too comes back into it when restored.
	if _, err := e.datasets.RestoreDataset(ctx, b, dataset.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if got := e.datasetProjects(t, b); !slices.Equal(got, []string{name}) {
		t.Errorf("projects of b = %q", got)
	}
	// A project in use is left as it is.
	if got, err := e.restore(ctx, p.ID, nil); err != nil || got != name {
		t.Errorf("restore of a project in use = %q, %v", got, err)
	}

	// Without models at all, a project is deleted for good.
	if _, err := e.db.Exec(ctx, "DELETE FROM models WHERE project_id = $1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM projects WHERE id = $1", p.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("project rows left: %d, %v", n, err)
	}
}
