package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// testEnv is a project Service and a dataset Service on a private database.
type testEnv struct {
	svc      *Service
	datasets *dataset.Service
	db       *database.DB
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	return &testEnv{
		svc:      NewService(Options{DB: db.Pool, Logger: log}),
		datasets: dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log}),
		db:       db,
	}
}

// image is an indexed image row; labels are the object counts per label.
type image struct {
	path   string
	labels map[string]int
}

// addDataset inserts a dataset and its image index directly (the dataset
// package's own tests cover importing), with matching counters.
func (e *testEnv) addDataset(t *testing.T, name string, images ...image) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := e.db.QueryRow(ctx, `INSERT INTO datasets (name, dir_name, origin) VALUES ($1, $1, 'upload') RETURNING id`,
		name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for i, img := range images {
		state, objects := "none", 0
		if img.labels != nil {
			state = "empty"
			for _, n := range img.labels {
				objects += n
			}
			if objects > 0 {
				state = "annotated"
			}
		} else {
			img.labels = map[string]int{}
		}
		if _, err := e.db.Exec(ctx, `INSERT INTO dataset_images (dataset_id, rel_path, size_bytes, mod_time,
				annotation_state, object_count, label_counts) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, img.path, 1000+i, time.Unix(1_700_000_000+int64(i), 0), state, objects, img.labels); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(ctx, `UPDATE datasets d SET image_count = s.n, annotated_count = s.annotated,
			empty_count = s.empty, object_count = s.objects, total_bytes = s.bytes
		FROM (SELECT count(*) AS n, count(*) FILTER (WHERE annotation_state = 'annotated') AS annotated,
			count(*) FILTER (WHERE annotation_state = 'empty') AS empty,
			coalesce(sum(object_count), 0) AS objects, coalesce(sum(size_bytes), 0) AS bytes
			FROM dataset_images WHERE dataset_id = $1) s
		WHERE d.id = $1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func wantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || !errors.Is(err, kind) || !strings.Contains(pe.Message, msgPart) {
		t.Errorf("err = %v, want kind %v containing %q", err, kind, msgPart)
	}
}

// datasetProjects returns the names of a dataset's projects.
func (e *testEnv) datasetProjects(t *testing.T, datasetID int64) []string {
	t.Helper()
	d, err := e.datasets.GetDataset(context.Background(), datasetID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range d.Projects {
		names = append(names, p.Name)
	}
	return names
}

// listIDs returns the sorted IDs of the datasets q selects.
func (e *testEnv) listIDs(t *testing.T, q dataset.DatasetQuery) []int64 {
	t.Helper()
	ds, err := e.datasets.ListDatasets(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestIntegrationProjectCRUD(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "  外观检测 ", Description: " 产线 A "})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Name != "外观检测" || p.Description != "产线 A" || p.DatasetCount != 0 || p.Covers == nil ||
		p.CreatedAt.Location() != time.UTC {
		t.Errorf("created = %+v", p)
	}

	_, err = e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测"})
	wantKind(t, err, ErrConflict, "已存在同名项目")
	_, err = e.svc.CreateProject(ctx, CreateProjectInput{Name: " "})
	wantKind(t, err, ErrInvalid, "名称不能为空")
	_, err = e.svc.CreateProject(ctx, CreateProjectInput{Name: "x", Description: strings.Repeat("字", 501)})
	wantKind(t, err, ErrInvalid, "描述不能超过 500 个字符")
	_, err = e.svc.CreateProject(ctx, CreateProjectInput{Name: "x", DatasetIDs: []int64{424242}})
	wantKind(t, err, ErrInvalid, "所选数据集不存在")
	if list, err := e.svc.ListProjects(ctx, ""); err != nil || len(list) != 1 {
		t.Errorf("a failed create must leave nothing behind: %+v, %v", list, err)
	}

	other, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "Scratch"})
	if err != nil {
		t.Fatal(err)
	}
	// Names are unique ignoring case, also when renaming.
	upper := "SCRATCH"
	_, err = e.svc.UpdateProject(ctx, p.ID, UpdateProjectInput{Name: &upper})
	wantKind(t, err, ErrConflict, "已存在同名项目")
	if _, err := e.svc.UpdateProject(ctx, other.ID, UpdateProjectInput{Name: &upper}); err != nil {
		t.Errorf("changing the case of the own name: %v", err)
	}
	desc := "  新描述 "
	updated, err := e.svc.UpdateProject(ctx, p.ID, UpdateProjectInput{Description: &desc})
	if err != nil || updated.Name != "外观检测" || updated.Description != "新描述" || !updated.UpdatedAt.After(p.UpdatedAt) {
		t.Errorf("UpdateProject = %+v, %v", updated, err)
	}
	empty := ""
	_, err = e.svc.UpdateProject(ctx, p.ID, UpdateProjectInput{Name: &empty})
	wantKind(t, err, ErrInvalid, "名称不能为空")
	_, err = e.svc.UpdateProject(ctx, 999999, UpdateProjectInput{Name: &desc})
	wantKind(t, err, ErrNotFound, "项目不存在")

	// Most recently updated first; search covers name and description.
	list, err := e.svc.ListProjects(ctx, "")
	if err != nil || len(list) != 2 || list[0].ID != p.ID || list[1].ID != other.ID {
		t.Errorf("ListProjects = %+v, %v", list, err)
	}
	for q, want := range map[string]int{"外观": 1, "新描": 1, "scr": 1, "zzz": 0, " ": 2} {
		if list, err := e.svc.ListProjects(ctx, q); err != nil || len(list) != want {
			t.Errorf("ListProjects(%q) returned %d projects, %v; want %d", q, len(list), err, want)
		}
	}

	if err := e.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	wantKind(t, e.svc.DeleteProject(ctx, p.ID), ErrNotFound, "项目不存在")
	_, err = e.svc.GetProject(ctx, p.ID)
	wantKind(t, err, ErrNotFound, "项目不存在")
	// The name is free again.
	if _, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "外观检测"}); err != nil {
		t.Errorf("reusing the name of a deleted project: %v", err)
	}
}

func TestIntegrationProjectDatasets(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	a := e.addDataset(t, "a", image{"1.jpg", map[string]int{"scratch": 2, "dent": 1}}, image{"2.jpg", map[string]int{}},
		image{"3.jpg", nil})
	b := e.addDataset(t, "b", image{"x.jpg", map[string]int{"scratch": 1}})
	c := e.addDataset(t, "c")

	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "P", DatasetIDs: []int64{b, a, a}})
	if err != nil {
		t.Fatalf("CreateProject with datasets: %v", err)
	}
	// Totals over a and b.
	if p.DatasetCount != 2 || p.ImageCount != 4 || p.AnnotatedCount != 2 || p.EmptyCount != 1 ||
		p.ObjectCount != 4 || p.TotalBytes != 1000+1001+1002+1000 {
		t.Errorf("project totals = %+v", p)
	}
	// Covers mix the datasets: a (added first, same transaction: by ID), then b.
	var coverDatasets []int64
	for _, c := range p.Covers {
		coverDatasets = append(coverDatasets, c.DatasetID)
		if c.ID == 0 || c.Version == "" {
			t.Errorf("cover = %+v", c)
		}
	}
	if !slices.Equal(coverDatasets, []int64{a, b, a, a}) {
		t.Errorf("cover datasets = %v, want [a b a a] = %v", coverDatasets, []int64{a, b, a, a})
	}

	q, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "Q"})
	if err != nil {
		t.Fatal(err)
	}
	if q.DatasetCount != 0 || len(q.Covers) != 0 || q.ImageCount != 0 {
		t.Errorf("empty project = %+v", q)
	}

	// Adding: idempotent, validated, touches the project only on a change.
	before, _ := e.svc.GetProject(ctx, p.ID)
	same, err := e.svc.AddDatasets(ctx, p.ID, []int64{a})
	if err != nil || same.DatasetCount != 2 || !same.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("adding a member again = %+v, %v", same, err)
	}
	p2, err := e.svc.AddDatasets(ctx, p.ID, []int64{c, a})
	if err != nil || p2.DatasetCount != 3 || !p2.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("AddDatasets = %+v, %v", p2, err)
	}
	_, err = e.svc.AddDatasets(ctx, p.ID, nil)
	wantKind(t, err, ErrInvalid, "请选择要添加的数据集")
	_, err = e.svc.AddDatasets(ctx, p.ID, []int64{a, 777777})
	wantKind(t, err, ErrInvalid, "所选数据集不存在")
	_, err = e.svc.AddDatasets(ctx, 888888, []int64{777777})
	wantKind(t, err, ErrNotFound, "项目不存在")
	_, err = e.svc.AddDatasets(ctx, p.ID, []int64{0})
	wantKind(t, err, ErrInvalid, "数据集 ID 无效")

	// The dataset side: its projects by name, and the list filters.
	if got := e.datasetProjects(t, a); !slices.Equal(got, []string{"P"}) {
		t.Errorf("projects of a = %v", got)
	}
	if err := e.svc.SetDatasetProjects(ctx, a, []int64{q.ID, p.ID}); err != nil {
		t.Fatalf("SetDatasetProjects: %v", err)
	}
	if got := e.datasetProjects(t, a); !slices.Equal(got, []string{"P", "Q"}) {
		t.Errorf("projects of a = %v", got)
	}
	if got := e.listIDs(t, dataset.DatasetQuery{ProjectID: p.ID}); !slices.Equal(got, []int64{a, b, c}) {
		t.Errorf("datasets of P = %v", got)
	}
	if got := e.listIDs(t, dataset.DatasetQuery{ProjectID: q.ID, Search: "A"}); !slices.Equal(got, []int64{a}) {
		t.Errorf("datasets of Q matching a = %v", got)
	}
	if got := e.listIDs(t, dataset.DatasetQuery{ProjectID: 999999}); len(got) != 0 {
		t.Errorf("datasets of a missing project = %v", got)
	}

	// Replacing the set removes and adds, and touches both projects.
	pBefore, _ := e.svc.GetProject(ctx, p.ID)
	if err := e.svc.SetDatasetProjects(ctx, c, []int64{q.ID}); err != nil {
		t.Fatal(err)
	}
	if got := e.datasetProjects(t, c); !slices.Equal(got, []string{"Q"}) {
		t.Errorf("projects of c = %v", got)
	}
	if pAfter, _ := e.svc.GetProject(ctx, p.ID); pAfter.DatasetCount != 2 || !pAfter.UpdatedAt.After(pBefore.UpdatedAt) {
		t.Errorf("P after c left = %+v", pAfter)
	}
	if err := e.svc.SetDatasetProjects(ctx, c, []int64{}); err != nil {
		t.Fatal(err)
	}
	if got := e.listIDs(t, dataset.DatasetQuery{NoProject: true}); !slices.Equal(got, []int64{c}) {
		t.Errorf("datasets in no project = %v", got)
	}
	wantKind(t, e.svc.SetDatasetProjects(ctx, c, []int64{q.ID, 999999}), ErrInvalid, "所选项目不存在")
	if got := e.datasetProjects(t, c); len(got) != 0 {
		t.Errorf("a rejected change must not apply: %v", got)
	}
	wantKind(t, e.svc.SetDatasetProjects(ctx, 999999, nil), ErrNotFound, "数据集不存在")

	// Removing: idempotent for an existing dataset.
	if err := e.svc.RemoveDataset(ctx, p.ID, b); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveDataset(ctx, p.ID, b); err != nil {
		t.Errorf("removing a dataset that is not in the project: %v", err)
	}
	wantKind(t, e.svc.RemoveDataset(ctx, p.ID, 999999), ErrNotFound, "数据集不存在")
	wantKind(t, e.svc.RemoveDataset(ctx, 999999, b), ErrNotFound, "项目不存在")
	if got := e.listIDs(t, dataset.DatasetQuery{ProjectID: p.ID}); !slices.Equal(got, []int64{a}) {
		t.Errorf("datasets of P after removing b = %v", got)
	}

	// Labels over the project's datasets.
	if _, err := e.svc.AddDatasets(ctx, p.ID, []int64{b}); err != nil {
		t.Fatal(err)
	}
	labels, err := e.svc.ListLabels(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Label{{"scratch", 3, 2, 2}, {"dent", 1, 1, 1}}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %+v, want %+v", labels, want)
	}
	if labels, err := e.svc.ListLabels(ctx, q.ID); err != nil || len(labels) != 2 {
		t.Errorf("labels of Q (only a) = %+v, %v", labels, err)
	}
	_, err = e.svc.ListLabels(ctx, 999999)
	wantKind(t, err, ErrNotFound, "项目不存在")

	// Deleting a dataset removes it from its projects (which counts as a
	// change to them); deleting a project keeps its datasets.
	beforeDelete, err := e.svc.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.datasets.DeleteDataset(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetProject(ctx, p.ID); got.DatasetCount != 1 || got.ImageCount != 3 ||
		!got.UpdatedAt.After(beforeDelete.UpdatedAt) {
		t.Errorf("P after deleting b = %+v, updated before %v", got, beforeDelete.UpdatedAt)
	}
	if err := e.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.datasetProjects(t, a); !slices.Equal(got, []string{"Q"}) {
		t.Errorf("projects of a after deleting P = %v", got)
	}
	if _, err := e.datasets.GetDataset(ctx, a); err != nil {
		t.Errorf("datasets survive their project: %v", err)
	}
}

// Concurrent membership changes from both sides must neither deadlock nor
// lose links.
func TestIntegrationProjectMembershipConcurrency(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var datasets, projects []int64
	for i := range 4 {
		datasets = append(datasets, e.addDataset(t, fmt.Sprintf("d%d", i)))
		p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: fmt.Sprintf("p%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, p.ID)
	}
	reversed := slices.Clone(datasets)
	slices.Reverse(reversed)

	var wg sync.WaitGroup
	errs := make(chan error, 128)
	var created atomic.Int64
	for round := range 8 {
		for i, p := range projects {
			// A new project with the datasets and a rename of p race for
			// the same name: one of them gets ErrConflict, neither may fail
			// otherwise (e.g. with a deadlock).
			name := fmt.Sprintf("c%d-%d", round, i)
			wg.Add(4)
			go func() {
				defer wg.Done()
				_, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: name, DatasetIDs: reversed})
				if err == nil {
					created.Add(1)
				} else if !errors.Is(err, ErrConflict) {
					errs <- fmt.Errorf("CreateProject: %w", err)
				}
			}()
			go func() {
				defer wg.Done()
				upper := strings.ToUpper(name)
				if _, err := e.svc.UpdateProject(ctx, p, UpdateProjectInput{Name: &upper}); err != nil &&
					!errors.Is(err, ErrConflict) {
					errs <- fmt.Errorf("UpdateProject: %w", err)
				}
			}()
			go func() {
				defer wg.Done()
				ids := datasets
				if (round+i)%2 == 1 {
					ids = reversed
				}
				if _, err := e.svc.AddDatasets(ctx, p, ids); err != nil {
					errs <- fmt.Errorf("AddDatasets: %w", err)
				}
			}()
			go func() {
				defer wg.Done()
				if err := e.svc.SetDatasetProjects(ctx, datasets[i], projects); err != nil {
					errs <- fmt.Errorf("SetDatasetProjects: %w", err)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	all, err := e.svc.ListProjects(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := len(projects) + int(created.Load()); len(all) != want {
		t.Errorf("%d projects, want %d", len(all), want)
	}
	// SetDatasetProjects may have taken datasets out of the new projects,
	// but not out of the original ones.
	for _, p := range projects {
		if got, err := e.svc.GetProject(ctx, p); err != nil || got.DatasetCount != len(datasets) {
			t.Errorf("project %d = %+v, %v; want all %d datasets", p, got, err, len(datasets))
		}
	}
}
