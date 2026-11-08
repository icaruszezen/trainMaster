package dataversion

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Comparing versions: the originals of two versions of a project matched
// by (dataset, path in the dataset).

// previewCompare compares what a preview would take with the newest ready
// version of the project (nil when there is none). The index has no
// boxes: AnnotationChanged stays 0.
func (s *Service) previewCompare(ctx context.Context, projectID int64, items []splitItem, as []splitAssignment) (*Compare, error) {
	var c Compare
	err := s.db.QueryRow(ctx, `SELECT id, version FROM data_versions
		WHERE project_id = $1 AND status = 'ready' AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`, projectID).
		Scan(&c.ID, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	datasetIDs := make([]int64, len(items))
	paths := make([]string, len(items))
	splits := make([]string, len(items))
	for i, it := range items {
		datasetIDs[i], paths[i], splits[i] = it.DatasetID, it.RelPath, as[i].Split
	}
	err = s.db.QueryRow(ctx, `WITH here AS (
			SELECT * FROM unnest($2::bigint[], $3::text[], $4::text[]) AS h (dataset_id, source_path, split)
		), there AS (
			SELECT dataset_id, source_path, split FROM data_version_images
			WHERE version_id = $1 AND augmented_from IS NULL
		)
		SELECT count(*) FILTER (WHERE t.source_path IS NULL), count(*) FILTER (WHERE h.source_path IS NULL),
			count(*) FILTER (WHERE h.split <> t.split)
		FROM here h FULL JOIN there t ON t.dataset_id = h.dataset_id AND t.source_path = h.source_path`,
		c.ID, datasetIDs, paths, splits).Scan(&c.Added, &c.Removed, &c.SplitChanged)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// compareVersions compares the originals of version id (number version of
// the project) with those of the newest other version of the project with
// a smaller number that is ready and not in the recycle bin (nil when
// there is none).
func compareVersions(ctx context.Context, q querier, id, projectID int64, version int) (*Compare, error) {
	var c Compare
	err := q.QueryRow(ctx, `SELECT id, version FROM data_versions
		WHERE project_id = $1 AND version < $2 AND id <> $3 AND status = 'ready' AND deleted_at IS NULL
		ORDER BY version DESC LIMIT 1`, projectID, version, id).Scan(&c.ID, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = q.QueryRow(ctx, `WITH here AS (
			SELECT dataset_id, source_path, split, objects FROM data_version_images
			WHERE version_id = $1 AND augmented_from IS NULL
		), there AS (
			SELECT dataset_id, source_path, split, objects FROM data_version_images
			WHERE version_id = $2 AND augmented_from IS NULL
		)
		SELECT count(*) FILTER (WHERE t.source_path IS NULL), count(*) FILTER (WHERE h.source_path IS NULL),
			count(*) FILTER (WHERE h.split <> t.split), count(*) FILTER (WHERE h.objects <> t.objects)
		FROM here h FULL JOIN there t ON t.dataset_id = h.dataset_id AND t.source_path = h.source_path`,
		id, c.ID).Scan(&c.Added, &c.Removed, &c.SplitChanged, &c.AnnotationChanged)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
