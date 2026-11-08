package dataversion

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// versionColumns are the columns scanVersion reads, from data_versions v
// joined with projects p.
const versionColumns = `v.id, v.project_id, p.name, v.version, v.note, v.status, v.error, v.stage,
	v.progress_done, v.progress_total, v.classes, v.config, v.sources, v.image_count, v.object_count,
	v.train_count, v.val_count, v.test_count, v.augmented_count, v.total_bytes, v.stats,
	v.created_at, v.updated_at, v.started_at, v.finished_at`

// versionFrom is the FROM clause of versionColumns.
const versionFrom = ` FROM data_versions v JOIN projects p ON p.id = v.project_id`

// versionLive is the condition of a version in use: neither it nor its
// project is in the recycle bin. (A project is in the bin only while it
// has no versions in use, see project/recycle.go.)
const versionLive = `v.deleted_at IS NULL AND p.deleted_at IS NULL`

// newestFirst is the order of version lists.
const newestFirst = ` ORDER BY v.created_at DESC, v.id DESC`

func scanVersion(row pgx.Row) (DataVersion, error) { return scanVersionWith(row) }

// scanVersionWith scans versionColumns followed by the extra columns.
func scanVersionWith(row pgx.Row, extra ...any) (DataVersion, error) {
	var v DataVersion
	var stage string
	var done, total int
	err := row.Scan(append([]any{&v.ID, &v.ProjectID, &v.ProjectName, &v.Version, &v.Note, &v.Status, &v.Error,
		&stage, &done, &total, &v.Classes, &v.Config, &v.Sources, &v.ImageCount, &v.ObjectCount,
		&v.TrainCount, &v.ValCount, &v.TestCount, &v.AugmentedCount, &v.TotalBytes, &v.Stats,
		&v.CreatedAt, &v.UpdatedAt, &v.StartedAt, &v.FinishedAt}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errVersionNotFound
	}
	if err != nil {
		return v, err
	}
	v.ClassVersion = classVersion(v.Classes)
	if v.Status == StatusBuilding {
		v.Progress = &Progress{Stage: stage, Done: done, Total: total}
	}
	normalizeVersion(&v)
	return v, nil
}

// normalizeVersion makes the slices of v non-nil and its times UTC.
func normalizeVersion(v *DataVersion) {
	if v.Classes == nil {
		v.Classes = []Class{}
	}
	if v.Sources == nil {
		v.Sources = []Source{}
	}
	for i := range v.Sources {
		if v.Sources[i].Versions == nil {
			v.Sources[i].Versions = []SourceVersion{}
		}
	}
	if v.Config.Augmentation != nil && v.Config.Augmentation.Methods == nil {
		v.Config.Augmentation.Methods = []string{}
	}
	if v.Config.IgnoreLabels == nil {
		v.Config.IgnoreLabels = []string{}
	}
	if v.Stats.Labels == nil {
		v.Stats.Labels = []LabelStats{}
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	v.StartedAt, v.FinishedAt = utcPtr(v.StartedAt), utcPtr(v.FinishedAt)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func collectVersions(rows pgx.Rows, err error) ([]DataVersion, error) {
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DataVersion, error) { return scanVersion(row) })
	if out == nil && err == nil {
		out = []DataVersion{}
	}
	return out, err
}

// queryVersion reads a version in use; forUpdate locks its row.
func queryVersion(ctx context.Context, q querier, id int64, forUpdate bool) (DataVersion, error) {
	sql := "SELECT " + versionColumns + versionFrom + " WHERE v.id = $1 AND " + versionLive
	if forUpdate {
		sql += " FOR UPDATE OF v"
	}
	return scanVersion(q.QueryRow(ctx, sql, id))
}

// List returns the versions in use, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]DataVersion, error) {
	sql := "SELECT " + versionColumns + versionFrom + " WHERE " + versionLive
	var args []any
	if f.ProjectID != 0 {
		args = append(args, f.ProjectID)
		sql += " AND v.project_id = $" + strconv.Itoa(len(args))
	}
	versions, err := collectVersions(s.db.Query(ctx, sql+newestFirst, args...))
	if err != nil {
		return nil, err
	}
	if err := s.decorate(ctx, s.db, versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// Get returns a version in use.
func (s *Service) Get(ctx context.Context, id int64) (DataVersion, error) {
	v, err := queryVersion(ctx, s.db, id, false)
	if err != nil {
		return v, err
	}
	vs := []DataVersion{v}
	if err := s.decorate(ctx, s.db, vs); err != nil {
		return v, err
	}
	return vs[0], nil
}

// decorate sets what is computed when versions are read: the queue
// position of queued versions and whether the source datasets are still in
// use.
func (s *Service) decorate(ctx context.Context, q querier, versions []DataVersion) error {
	var datasetIDs []int64
	queued := false
	for _, v := range versions {
		queued = queued || v.Status == StatusQueued
		for _, src := range v.Sources {
			if !slices.Contains(datasetIDs, src.DatasetID) {
				datasetIDs = append(datasetIDs, src.DatasetID)
			}
		}
	}
	live := map[int64]bool{}
	if len(datasetIDs) > 0 {
		rows, err := q.Query(ctx, "SELECT id FROM datasets WHERE id = ANY($1) AND deleted_at IS NULL", datasetIDs)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			live[id] = true
		}
	}
	positions := map[int64]int{}
	if queued {
		// The builder takes the oldest queued version first; a building one
		// is ahead of all of them.
		rows, err := q.Query(ctx, "SELECT id FROM data_versions WHERE status = 'queued' ORDER BY queued_at, id")
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for i, id := range ids {
			positions[id] = i + 1
		}
	}
	for i := range versions {
		v := &versions[i]
		for j := range v.Sources {
			v.Sources[j].DatasetDeleted = !live[v.Sources[j].DatasetID]
		}
		if p, ok := positions[v.ID]; ok && v.Status == StatusQueued {
			v.QueuePosition = &p
		}
	}
	return nil
}

// classVersion is the version of a frozen class list, computed like the
// project's (project.ClassListVersion), so a client can tell whether the
// project's list is still the one a version froze.
func classVersion(classes []Class) string {
	pc := make([]project.Class, len(classes))
	for i, c := range classes {
		pc[i] = project.Class{Name: c.Name, Color: c.Color}
	}
	return project.ClassListVersion(pc)
}
