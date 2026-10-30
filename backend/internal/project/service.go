// Package project implements project management: a project groups the
// datasets of one inspection task (and, later, its training jobs and
// models). Projects are metadata in PostgreSQL only; a dataset can belong
// to any number of projects.
package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options are the dependencies of a Service.
type Options struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
}

// Service implements the project operations used by the HTTP handlers.
type Service struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

// NewService creates a Service.
func NewService(o Options) *Service {
	return &Service{db: o.DB, log: o.Logger}
}

// ListProjects returns the projects whose name or description contains
// search (ignoring case), most recently updated first.
func (s *Service) ListProjects(ctx context.Context, search string) ([]Project, error) {
	ps, err := queryProjects(ctx, s.db, strings.TrimSpace(search))
	if err != nil {
		return nil, err
	}
	return ps, s.decorate(ctx, ps)
}

// GetProject returns one project.
func (s *Service) GetProject(ctx context.Context, id int64) (Project, error) {
	p, err := queryProject(ctx, s.db, id)
	if err != nil {
		return p, err
	}
	ps := []Project{p}
	err = s.decorate(ctx, ps)
	return ps[0], err
}

// decorate fills the fields that do not come from the projects query.
func (s *Service) decorate(ctx context.Context, ps []Project) error {
	if len(ps) == 0 {
		return nil
	}
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	covers, err := queryCovers(ctx, s.db, ids)
	if err != nil {
		return fmt.Errorf("load project covers: %w", err)
	}
	for i := range ps {
		if c := covers[ps[i].ID]; c != nil {
			ps[i].Covers = c
		}
	}
	return nil
}

// CreateProjectInput are the fields of a new project.
type CreateProjectInput struct {
	Name        string
	Description string
	DatasetIDs  []int64 // datasets to include right away
}

// CreateProject creates a project, optionally with datasets.
func (s *Service) CreateProject(ctx context.Context, in CreateProjectInput) (Project, error) {
	name, err := NormalizeName(in.Name)
	if err != nil {
		return Project{}, err
	}
	desc, err := NormalizeDescription(in.Description)
	if err != nil {
		return Project{}, err
	}
	datasetIDs, err := normalizeIDs(in.DatasetIDs, "数据集")
	if err != nil {
		return Project{}, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Datasets first: the INSERT below already holds the new name in
		// the unique index, which others (a rename to the same name) can
		// wait for.
		if len(datasetIDs) > 0 {
			datasets, err := lockRows(ctx, tx, "datasets", datasetIDs)
			if err != nil {
				return err
			}
			if len(datasets) != len(datasetIDs) {
				return errDatasetsMissing
			}
		}
		if err := checkNameFree(ctx, tx, name, 0); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "INSERT INTO projects (name, description) VALUES ($1, $2) RETURNING id",
			name, desc).Scan(&id)
		if err != nil {
			return uniqueViolation(err)
		}
		if len(datasetIDs) == 0 {
			return nil
		}
		_, err = insertLinks(ctx, tx, id, datasetIDs)
		return err
	})
	if err != nil {
		return Project{}, err
	}
	s.log.Info("project created", "project_id", id, "datasets", len(datasetIDs))
	return s.GetProject(ctx, id)
}

// UpdateProjectInput holds the fields to change; nil leaves a field alone.
type UpdateProjectInput struct {
	Name        *string
	Description *string
}

// UpdateProject renames a project or changes its description.
func (s *Service) UpdateProject(ctx context.Context, id int64, in UpdateProjectInput) (Project, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name, desc string
		err := tx.QueryRow(ctx, "SELECT name, description FROM projects WHERE id = $1 FOR NO KEY UPDATE", id).Scan(&name, &desc)
		if errors.Is(err, pgx.ErrNoRows) {
			return errProjectNotFound
		}
		if err != nil {
			return err
		}
		if in.Name != nil {
			if name, err = NormalizeName(*in.Name); err != nil {
				return err
			}
			if err := checkNameFree(ctx, tx, name, id); err != nil {
				return err
			}
		}
		if in.Description != nil {
			if desc, err = NormalizeDescription(*in.Description); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE projects SET name = $2, description = $3, updated_at = now() WHERE id = $1",
			id, name, desc)
		return uniqueViolation(err)
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

// DeleteProject deletes a project. Its datasets are kept; they only leave
// the project.
func (s *Service) DeleteProject(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, "DELETE FROM projects WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errProjectNotFound
	}
	s.log.Info("project deleted", "project_id", id)
	return nil
}

// AddDatasets includes datasets in a project. Datasets already in it are
// left alone.
func (s *Service) AddDatasets(ctx context.Context, projectID int64, datasetIDs []int64) (Project, error) {
	ids, err := normalizeIDs(datasetIDs, "数据集")
	if err != nil {
		return Project{}, err
	}
	if len(ids) == 0 {
		return Project{}, &Error{Kind: ErrInvalid, Message: "请选择要添加的数据集"}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		datasets, err := lockRows(ctx, tx, "datasets", ids)
		if err != nil {
			return err
		}
		if found, err := lockOne(ctx, tx, "projects", projectID); err != nil {
			return err
		} else if !found {
			return errProjectNotFound
		}
		if len(datasets) != len(ids) {
			return errDatasetsMissing
		}
		added, err := insertLinks(ctx, tx, projectID, ids)
		if err != nil || added == 0 {
			return err
		}
		return touchProjects(ctx, tx, []int64{projectID})
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, projectID)
}

var errDatasetsMissing = &Error{Kind: ErrInvalid, Message: "所选数据集不存在或已被删除，请刷新后重试"}

// insertLinks links locked datasets to a locked (or new) project and
// returns how many were not linked before.
func insertLinks(ctx context.Context, tx pgx.Tx, projectID int64, datasetIDs []int64) (int64, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO project_datasets (project_id, dataset_id)
		SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, projectID, datasetIDs)
	return tag.RowsAffected(), err
}

// RemoveDataset takes a dataset out of a project. It succeeds if the
// dataset exists but is not in the project.
func (s *Service) RemoveDataset(ctx context.Context, projectID, datasetID int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		datasetFound, err := lockOne(ctx, tx, "datasets", datasetID)
		if err != nil {
			return err
		}
		if found, err := lockOne(ctx, tx, "projects", projectID); err != nil {
			return err
		} else if !found {
			return errProjectNotFound
		}
		if !datasetFound {
			return errDatasetNotFound
		}
		tag, err := tx.Exec(ctx, "DELETE FROM project_datasets WHERE project_id = $1 AND dataset_id = $2",
			projectID, datasetID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return touchProjects(ctx, tx, []int64{projectID})
	})
}

// SetDatasetProjects makes a dataset belong to exactly the given projects.
func (s *Service) SetDatasetProjects(ctx context.Context, datasetID int64, projectIDs []int64) error {
	ids, err := normalizeIDs(projectIDs, "项目")
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if found, err := lockOne(ctx, tx, "datasets", datasetID); err != nil {
			return err
		} else if !found {
			return errDatasetNotFound
		}
		// The dataset lock keeps its links as they are now (only deleting a
		// project can still remove one).
		rows, err := tx.Query(ctx, "SELECT project_id FROM project_datasets WHERE dataset_id = $1", datasetID)
		if err != nil {
			return err
		}
		current, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		involved := slices.Compact(slices.Sorted(slices.Values(slices.Concat(current, ids))))
		existing, err := lockRows(ctx, tx, "projects", involved)
		if err != nil {
			return err
		}
		var added, removed []int64
		for _, id := range ids {
			if !slices.Contains(existing, id) {
				return &Error{Kind: ErrInvalid, Message: "所选项目不存在或已被删除，请刷新后重试"}
			}
			if !slices.Contains(current, id) {
				added = append(added, id)
			}
		}
		for _, id := range current {
			if !slices.Contains(ids, id) && slices.Contains(existing, id) {
				removed = append(removed, id)
			}
		}
		if len(removed) > 0 {
			if _, err := tx.Exec(ctx, "DELETE FROM project_datasets WHERE dataset_id = $1 AND project_id = ANY($2)",
				datasetID, removed); err != nil {
				return err
			}
		}
		if len(added) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO project_datasets (project_id, dataset_id)
				SELECT unnest($2::bigint[]), $1`, datasetID, added); err != nil {
				return err
			}
		}
		return touchProjects(ctx, tx, append(added, removed...))
	})
}

// ListLabels returns the label distribution over all datasets of a
// project, by object count.
func (s *Service) ListLabels(ctx context.Context, projectID int64) ([]Label, error) {
	if err := s.Exists(ctx, projectID); err != nil {
		return nil, err
	}
	return queryLabels(ctx, s.db, projectID)
}

// Exists fails with a not-found error unless the project exists.
func (s *Service) Exists(ctx context.Context, projectID int64) error {
	var found bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)", projectID).Scan(&found); err != nil {
		return err
	}
	if !found {
		return errProjectNotFound
	}
	return nil
}
