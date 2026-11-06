package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Projects and the recycle bin (回收站). Projects are not items of the bin
// themselves, but the models deleted with a project are: models.project_id
// is ON DELETE RESTRICT, so a project whose models are in the bin stays
// too, out of use (deleted_at; it releases its name). Its dataset links
// move to recycled_project_datasets meanwhile. Restoring one of its models
// restores the project (RestoreInTx); package model deletes it for good
// once its last model is purged.

// ErrNameTaken: restoring a project whose name another project took
// meanwhile (a conflict; restore it under another name).
var ErrNameTaken = errors.New("name taken")

// DeleteProject deletes a project. Its datasets are kept; they only leave
// the project. A project with models in use cannot be deleted
// (ErrHasModels): package model moves them to the recycle bin first. One
// whose models are all in the bin goes there with them (see above);
// otherwise it is deleted for good. Uploads of models into the project
// that are still in progress go with it.
func (s *Service) DeleteProject(ctx context.Context, id int64) error {
	for range maxLinkRetries {
		soft, err := s.deleteProject(ctx, id)
		if errors.Is(err, errLinksChanged) {
			continue
		}
		if err != nil {
			return err
		}
		if soft {
			s.log.Info("project deleted; kept until its models are purged from the recycle bin", "project_id", id)
		} else {
			s.log.Info("project deleted", "project_id", id)
		}
		return nil
	}
	return fmt.Errorf("delete project: its datasets changed %d times while deleting", maxLinkRetries)
}

// maxLinkRetries bounds how often DeleteProject starts again because a
// dataset was added to the project meanwhile.
const maxLinkRetries = 3

// errLinksChanged: a dataset was added to the project after its datasets
// were locked; deleteProject must start again to lock it first.
var errLinksChanged = errors.New("project datasets changed")

func (s *Service) deleteProject(ctx context.Context, id int64) (soft bool, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		soft = false
		// Datasets first (the lock order): their links move with the
		// project into the bin, and the rows the moved links refer to must
		// not be waiting for this project (deleting a dataset locks it,
		// then its projects).
		rows, err := tx.Query(ctx, `SELECT id FROM datasets
			WHERE id IN (SELECT dataset_id FROM project_datasets WHERE project_id = $1)
			ORDER BY id FOR NO KEY UPDATE`, id)
		if err != nil {
			return err
		}
		locked, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if locked == nil {
			locked = []int64{} // not NULL for ANY below
		}
		// The lock waits for models being added to the project (they lock
		// its row to allocate their version) and keeps new ones out, as well
		// as new datasets.
		var found bool
		if err := tx.QueryRow(ctx, "SELECT true FROM projects WHERE id = $1 AND deleted_at IS NULL FOR UPDATE",
			id).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
			return errProjectNotFound
		} else if err != nil {
			return err
		}
		live, trashed, err := countModels(ctx, tx, id)
		if err != nil {
			return err
		}
		if live > 0 {
			return &Error{Kind: ErrHasModels, Message: fmt.Sprintf("项目中有 %d 个模型，请先删除模型，或选择同时删除模型", live)}
		}
		if trashed == 0 {
			_, err = tx.Exec(ctx, "DELETE FROM projects WHERE id = $1", id)
			return err
		}
		var added bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_datasets
			WHERE project_id = $1 AND NOT dataset_id = ANY($2))`, id, locked).Scan(&added); err != nil {
			return err
		}
		if added {
			return errLinksChanged
		}
		soft = true
		if _, err := tx.Exec(ctx, `WITH moved AS (DELETE FROM project_datasets WHERE project_id = $1 RETURNING *)
			INSERT INTO recycled_project_datasets SELECT * FROM moved ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
		// What deleting the project would cascade to (the upload folders
		// are cleaned up by package model).
		if _, err := tx.Exec(ctx, "DELETE FROM model_uploads WHERE project_id = $1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE projects SET deleted_at = now(), updated_at = now() WHERE id = $1", id)
		return err
	})
	return soft, err
}

// RestoreInTx takes a project out of the recycle bin in tx (restoring one
// of its models, package model): it is back in use with the datasets it
// had that are in use themselves. It returns the project's name; a project
// in use is left as it is. It fails with an *Error of kind ErrNameTaken
// when another project took the name: name restores it under another one.
// The caller may hold model rows, but no dataset or project rows (the
// lock order).
func RestoreInTx(ctx context.Context, tx pgx.Tx, id int64, name *string) (string, error) {
	// Datasets first, also those in the bin: one restored meanwhile is
	// read again after the wait, so its link comes back too.
	rows, err := tx.Query(ctx, `SELECT id FROM datasets
		WHERE id IN (SELECT dataset_id FROM recycled_project_datasets WHERE project_id = $1)
		ORDER BY id FOR NO KEY UPDATE`, id)
	if err != nil {
		return "", err
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	var current string
	var trashed bool
	err = tx.QueryRow(ctx, "SELECT name, deleted_at IS NOT NULL FROM projects WHERE id = $1 FOR UPDATE",
		id).Scan(&current, &trashed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errProjectNotFound
	}
	if err != nil || !trashed {
		return current, err
	}
	newName := current
	if name != nil {
		if newName, err = NormalizeName(*name); err != nil {
			return "", err
		}
	}
	if err := checkNameFree(ctx, tx, newName, id); errors.Is(err, errNameTaken) {
		return "", restoreNameTaken(newName)
	} else if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "UPDATE projects SET deleted_at = NULL, name = $2, updated_at = now() WHERE id = $1",
		id, newName)
	if err := uniqueViolation(err); errors.Is(err, errNameTaken) {
		return "", restoreNameTaken(newName)
	} else if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `WITH moved AS (DELETE FROM recycled_project_datasets r
			WHERE r.project_id = $1 AND EXISTS (SELECT 1 FROM datasets d WHERE d.id = r.dataset_id AND d.deleted_at IS NULL)
			RETURNING r.*)
		INSERT INTO project_datasets SELECT * FROM moved ON CONFLICT DO NOTHING`, id)
	return newName, err
}

func restoreNameTaken(name string) *Error {
	return newError(ErrNameTaken, "所属项目已被删除，恢复时会一并恢复，但已存在同名项目“%s”，请为恢复的项目换一个名称", name)
}
