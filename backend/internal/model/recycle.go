package model

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
)

// The recycle bin (回收站) of models. Deleting a model moves it to the bin,
// a soft delete (deleted_at, migration 0012_recycle_bin.sql): every query
// but those of this file treats it as gone (modelLive). Its folder stays
// where it is (the version in it is never reused) and its succeeded
// conversions are kept, so restoring gives everything back. It is purged
// for good, folder included, once recycle.Retention is over (cleanup).
//
// Deleting a project with its models moves them to the bin; the project
// stays with them, out of use, until it is restored with one of them or
// its last model is purged (package project).

var errModelNotInBin = &Error{Kind: ErrNotFound, Message: "回收站中没有这个模型（可能已经恢复，或已被永久删除）"}

// DeleteModel moves a model to the recycle bin. Its conversions in
// progress are deleted: the nodes working on them get "not found" from
// then on and stop.
func (s *Service) DeleteModel(ctx context.Context, id int64) error {
	var exportIDs []int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// FOR UPDATE: requesting a conversion (FOR SHARE) waits for this.
		var found bool
		err := tx.QueryRow(ctx, "SELECT true FROM models WHERE id = $1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return errModelNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `DELETE FROM model_exports WHERE model_id = $1 AND status IN ('queued', 'running')
			RETURNING id`, id)
		if err != nil {
			return err
		}
		if exportIDs, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE models SET deleted_at = now() WHERE id = $1", id)
		return err
	})
	if err != nil {
		return err
	}
	for _, eid := range exportIDs {
		s.removeExportTemp(eid)
	}
	s.log.Info("model moved to the recycle bin", "model_id", id, "exports_canceled", len(exportIDs))
	return nil
}

// DeleteProjectModels moves all models of a project to the recycle bin
// (as DeleteModel does), before the project itself is deleted.
func (s *Service) DeleteProjectModels(ctx context.Context, projectID int64) error {
	rows, err := s.db.Query(ctx, "SELECT id FROM models WHERE project_id = $1 AND deleted_at IS NULL ORDER BY id", projectID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.DeleteModel(ctx, id); err != nil && !errors.Is(err, ErrNotFound) { // not found: deleted meanwhile
			return err
		}
	}
	if len(ids) > 0 {
		s.log.Info("project models moved to the recycle bin", "project_id", projectID, "models", len(ids))
	}
	return nil
}

// RemoveProjectFolder removes the folder of a deleted project from models/
// if it is empty (a crash leftover is moved aside first). The folder of a
// project in the recycle bin still holds its models and stays.
func (s *Service) RemoveProjectFolder(ctx context.Context, projectID int64) {
	projectDir := fmt.Sprintf("project-%d", projectID)
	s.recoverPendingIn(ctx, projectDir)
	if dir, err := s.modelDir(projectDir); err == nil {
		_ = os.Remove(dir)
	}
}

// RestoreInput are the options of restoring a model.
type RestoreInput struct {
	// ProjectName, when set, is the name its project gets back: the
	// project was deleted with the model, and another project took its
	// name meanwhile.
	ProjectName *string
}

// RestoreModel takes a model out of the recycle bin, and its project too
// if the project was deleted with it. Errors about the project are
// *project.Error values; one of kind project.ErrNameTaken means another
// project took its name (in.ProjectName restores it under another one).
func (s *Service) RestoreModel(ctx context.Context, id int64, in RestoreInput) (Model, error) {
	for range maxRestoreRetries {
		projectRestored, err := s.restoreModel(ctx, id, in)
		if errors.Is(err, errProjectDeletedMeanwhile) {
			continue
		}
		if err != nil {
			return Model{}, err
		}
		s.log.Info("model restored from the recycle bin", "model_id", id, "project_restored", projectRestored)
		return s.GetModel(ctx, id)
	}
	return Model{}, fmt.Errorf("restore model: its project was deleted %d times while restoring", maxRestoreRetries)
}

// maxRestoreRetries bounds how often RestoreModel starts again because the
// project was moved to the recycle bin meanwhile.
const maxRestoreRetries = 3

var errProjectDeletedMeanwhile = errors.New("project deleted meanwhile")

func (s *Service) restoreModel(ctx context.Context, id int64, in RestoreInput) (projectRestored bool, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		projectRestored = false
		var projectID int64
		var projectDeleted bool
		err := tx.QueryRow(ctx, `SELECT m.project_id, p.deleted_at IS NOT NULL FROM models m JOIN projects p ON p.id = m.project_id
			WHERE m.id = $1 AND m.deleted_at IS NOT NULL`, id).Scan(&projectID, &projectDeleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return errModelNotInBin
		}
		if err != nil {
			return err
		}
		// The project first, then the model (the lock order of deleting a
		// project: it locks its datasets, itself, then reads its models).
		if projectDeleted {
			if _, err := project.RestoreInTx(ctx, tx, projectID, in.ProjectName); err != nil {
				return err
			}
			projectRestored = true
		} else {
			// Keeps the project from going to the bin until the model is
			// back (deleting it locks it FOR UPDATE, then counts its models).
			var live bool
			if err := tx.QueryRow(ctx, "SELECT deleted_at IS NULL FROM projects WHERE id = $1 FOR KEY SHARE",
				projectID).Scan(&live); err != nil {
				return err
			}
			if !live {
				return errProjectDeletedMeanwhile
			}
		}
		tag, err := tx.Exec(ctx, "UPDATE models SET deleted_at = NULL WHERE id = $1 AND deleted_at IS NOT NULL", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errModelNotInBin // restored or purged meanwhile
		}
		return nil
	})
	return projectRestored, err
}

// TrashedModel is a model in the recycle bin.
type TrashedModel struct {
	Model
	DeletedAt time.Time `json:"deleted_at"`
	PurgeAt   time.Time `json:"purge_at"` // when it is deleted for good
	// ProjectDeleted: the project was deleted with the model; restoring
	// the model restores it too.
	ProjectDeleted bool `json:"project_deleted"`
}

// ListTrashedModels returns the models in the recycle bin, the most
// recently deleted first.
func (s *Service) ListTrashedModels(ctx context.Context) ([]TrashedModel, error) {
	rows, err := s.db.Query(ctx, "SELECT "+modelColumns+", m.deleted_at, p.deleted_at IS NOT NULL"+modelFrom+
		" WHERE m.deleted_at IS NOT NULL ORDER BY m.deleted_at DESC, m.id DESC")
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TrashedModel, error) {
		var t TrashedModel
		r, err := scanModelWith(row, &t.DeletedAt, &t.ProjectDeleted)
		if err != nil {
			return t, err
		}
		t.Model = s.toModel(r)
		t.DeletedAt = t.DeletedAt.UTC()
		t.PurgeAt = recycle.PurgeAt(t.DeletedAt)
		t.DownloadURL = "" // not downloadable from the bin
		return t, nil
	})
	if out == nil && err == nil {
		out = []TrashedModel{}
	}
	return out, err
}

// ---- purging ----

// purgeExpired purges the models whose time in the recycle bin is over,
// then the projects in the bin left without models.
func (s *Service) purgeExpired(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id FROM models
		WHERE deleted_at <= now() - $1 * interval '1 second' ORDER BY deleted_at, id`, recycle.RetentionSeconds)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.purgeModel(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Error("purge model from the recycle bin", "model_id", id, "err", err)
		}
	}
	return s.purgeEmptyProjects(ctx)
}

// purgeModel deletes a model whose time in the recycle bin is over for
// good, with its conversions and its folder. A model restored meanwhile is
// left alone.
func (s *Service) purgeModel(ctx context.Context, id int64) error {
	ctx, cancel := s.detach(ctx)
	defer cancel()
	var dirName, trash string
	var purged bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT dir_name FROM models
			WHERE id = $1 AND deleted_at <= now() - $2 * interval '1 second' FOR UPDATE`,
			id, recycle.RetentionSeconds).Scan(&dirName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // restored meanwhile
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM models WHERE id = $1", id); err != nil {
			return err
		}
		purged = true
		// Move the folder out of the way first: this is atomic and quick, so
		// the transaction can still roll back, and the slow removal below
		// cannot leave a half-deleted model folder behind.
		trash, err = s.moveToTrash(dirName, id)
		return err
	})
	if err != nil {
		if trash != "" {
			s.restoreFromTrash(trash, dirName)
		}
		return err
	}
	if !purged {
		return nil
	}
	s.log.Info("model purged from the recycle bin", "model_id", id, "dir", dirName)
	if trash == "" {
		return nil
	}
	defer s.busy.remove(trash)
	if err := s.removeAll(trash); err != nil {
		// cleanup removes what is left of .deleting-* folders every time.
		s.log.Error("model folder only partly removed; will retry", "model_id", id, "path", trash, "err", err)
	}
	return nil
}

// purgeEmptyProjects deletes the projects in the recycle bin whose last
// model and data version are purged (project.PurgeEmptyProjects), with
// their folders in models/. A project purged by package dataversion leaves
// its (empty) folder here: the folders of projects that are gone are
// removed too.
func (s *Service) purgeEmptyProjects(ctx context.Context) error {
	ids, err := project.PurgeEmptyProjects(ctx, s.db)
	for _, id := range ids {
		s.RemoveProjectFolder(ctx, id)
		s.log.Info("project deleted with its last model purged from the recycle bin", "project_id", id)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Error("delete projects left without models in the recycle bin", "err", err)
	}
	return s.removeGoneProjectFolders(ctx)
}

// removeGoneProjectFolders removes the empty folders in models/ of
// projects that no longer exist.
func (s *Service) removeGoneProjectFolders(ctx context.Context) error {
	entries, err := os.ReadDir(s.modelsDir())
	if err != nil {
		return nil
	}
	var ids []int64
	for _, e := range entries {
		if n, ok := strings.CutPrefix(e.Name(), "project-"); ok && e.IsDir() {
			if id, err := strconv.ParseInt(n, 10, 64); err == nil && id > 0 && strconv.FormatInt(id, 10) == n {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM projects WHERE id = ANY($1)", ids)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !slices.Contains(existing, id) {
			s.RemoveProjectFolder(ctx, id) // only an empty folder goes
		}
	}
	return nil
}

// moveToTrash renames the folder of a model to a hidden name in models/
// and marks it as in use (the caller removes the mark). A missing folder
// is not an error (nothing to delete). Symbolic links are moved, not
// followed.
func (s *Service) moveToTrash(dirName string, modelID int64) (string, error) {
	dir, err := s.modelDir(dirName)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat model folder: %w", err)
	}
	trash := filepath.Join(s.modelsDir(), fmt.Sprintf("%s%d", deletingPrefix, modelID))
	s.busy.add(trash)
	if err := os.Rename(dir, trash); err != nil {
		s.busy.remove(trash)
		return "", fmt.Errorf("move model folder aside: %w", err)
	}
	return trash, nil
}

// restoreFromTrash undoes moveToTrash after the purge failed.
func (s *Service) restoreFromTrash(trash, dirName string) {
	defer s.busy.remove(trash)
	dir, err := s.modelDir(dirName)
	if err == nil {
		err = os.Rename(trash, dir)
	}
	if err != nil {
		s.log.Error("restore model folder after failed purge", "path", trash, "err", err)
	}
}
