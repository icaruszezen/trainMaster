package project

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// projectSelect reads projects with the totals over their datasets; append
// a WHERE clause, then projectGroupBy.
const projectSelect = `SELECT p.id, p.name, p.description, p.created_at, p.updated_at,
		count(d.id), coalesce(sum(d.image_count), 0), coalesce(sum(d.annotated_count), 0),
		coalesce(sum(d.empty_count), 0), coalesce(sum(d.invalid_count), 0), coalesce(sum(d.object_count), 0),
		coalesce(sum(d.total_bytes), 0)::bigint
	FROM projects p
	LEFT JOIN project_datasets pd ON pd.project_id = p.id
	LEFT JOIN datasets d ON d.id = pd.dataset_id`

const projectGroupBy = ` GROUP BY p.id`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt, &p.DatasetCount, &p.ImageCount,
		&p.AnnotatedCount, &p.EmptyCount, &p.InvalidCount, &p.ObjectCount, &p.TotalBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errProjectNotFound
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	p.Covers = []Cover{}
	return p, err
}

func queryProject(ctx context.Context, q querier, id int64) (Project, error) {
	return scanProject(q.QueryRow(ctx, projectSelect+" WHERE p.id = $1"+projectGroupBy, id))
}

func queryProjects(ctx context.Context, q querier, search string) ([]Project, error) {
	rows, err := q.Query(ctx, projectSelect+`
		WHERE $1 = '' OR strpos(lower(p.name), lower($1)) > 0 OR strpos(lower(p.description), lower($1)) > 0`+
		projectGroupBy+` ORDER BY p.updated_at DESC, p.id DESC`, search)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) { return scanProject(row) })
}

// Lock order: a transaction that changes project_datasets first locks the
// rows of the datasets involved, then those of the projects, each in ID
// order (FOR NO KEY UPDATE: the rows stay readable and referenceable). A
// link changes only while its dataset and its project are both locked, so
// membership changes serialize per dataset and per project, and cannot
// deadlock with each other.

// lockRows locks the rows of table (datasets or projects) with the given
// IDs, which must be sorted, and returns the IDs that exist.
func lockRows(ctx context.Context, q querier, table string, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, "SELECT id FROM "+table+" WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE", ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// lockOne locks one row of table and reports whether it exists.
func lockOne(ctx context.Context, q querier, table string, id int64) (bool, error) {
	found, err := lockRows(ctx, q, table, []int64{id})
	return len(found) == 1, err
}

// touchProjects marks locked projects as updated (their datasets changed).
func touchProjects(ctx context.Context, q querier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, "UPDATE projects SET updated_at = now() WHERE id = ANY($1)", ids)
	return err
}

// Name of the unique index on projects, used to explain violations.
const projectsNameKey = "projects_name_key"

// checkNameFree fails with errNameTaken if another project (not exceptID)
// already uses name, ignoring case.
func checkNameFree(ctx context.Context, q querier, name string, exceptID int64) error {
	var exists bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE lower(name) = lower($1) AND id <> $2)",
		name, exceptID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return errNameTaken
	}
	return nil
}

// uniqueViolation turns a violation of the unique name index (the check
// before the write can race) into errNameTaken.
func uniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == projectsNameKey {
		return errNameTaken
	}
	return err
}

// queryCovers returns up to 4 images per project for the card mosaic: the
// first images (by path) of its first datasets with images (in the order
// they were added), taken in turns so the mosaic shows several datasets.
func queryCovers(ctx context.Context, q querier, ids []int64) (map[int64][]Cover, error) {
	rows, err := q.Query(ctx, `SELECT ps.id, c.dataset_id, c.id, c.size_bytes, c.mod_time
		FROM unnest($1::bigint[]) AS ps (id)
		CROSS JOIN LATERAL (SELECT pd.dataset_id, pd.created_at FROM project_datasets pd
			JOIN datasets d ON d.id = pd.dataset_id
			WHERE pd.project_id = ps.id AND d.image_count > 0
			ORDER BY pd.created_at, pd.dataset_id LIMIT 4) ds
		CROSS JOIN LATERAL (SELECT dataset_id, id, size_bytes, mod_time, rel_path FROM dataset_images
			WHERE dataset_id = ds.dataset_id ORDER BY rel_path LIMIT 4) c
		ORDER BY ps.id, ds.created_at, ds.dataset_id, c.rel_path`, ids)
	if err != nil {
		return nil, err
	}
	// Per project, the images of each dataset in order.
	perDataset := make(map[int64][][]Cover)
	var projectID, datasetID, id, size int64
	var mod time.Time
	_, err = pgx.ForEachRow(rows, []any{&projectID, &datasetID, &id, &size, &mod}, func() error {
		groups := perDataset[projectID]
		if n := len(groups); n == 0 || groups[n-1][0].DatasetID != datasetID {
			groups = append(groups, nil)
		}
		last := len(groups) - 1
		groups[last] = append(groups[last], Cover{DatasetID: datasetID, ID: id, Version: dataset.ImageVersion(size, mod)})
		perDataset[projectID] = groups
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Cover, len(perDataset))
	for projectID, groups := range perDataset {
		out[projectID] = interleave(groups, 4)
	}
	return out, nil
}

// interleave takes one item from each group in turn until it has max
// items or the groups are exhausted.
func interleave[T any](groups [][]T, max int) []T {
	var out []T
	for i := 0; len(out) < max; i++ {
		took := false
		for _, g := range groups {
			if i < len(g) && len(out) < max {
				out = append(out, g[i])
				took = true
			}
		}
		if !took {
			break
		}
	}
	return out
}

func queryLabels(ctx context.Context, q querier, projectID int64) ([]Label, error) {
	rows, err := q.Query(ctx, `SELECT l.key, sum(l.value::bigint)::bigint, count(*), count(DISTINCT i.dataset_id)
		FROM project_datasets pd
		JOIN dataset_images i ON i.dataset_id = pd.dataset_id
		CROSS JOIN LATERAL jsonb_each_text(i.label_counts) AS l
		WHERE pd.project_id = $1
		GROUP BY l.key
		ORDER BY 2 DESC, 1`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Label])
}
