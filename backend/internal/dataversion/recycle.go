package dataversion

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// The recycle bin (回收站) of data versions. Deleting a ready version moves
// it to the bin, a soft delete (deleted_at): every query but those of this
// file treats it as gone (versionLive). Its folder and its image rows stay
// as they are, so restoring gives everything back. It is purged for good,
// folder and thumbnails included, once recycle.Retention is over (the
// janitor). Versions in other states are never in the bin: deleting one
// removes it at once (create.go).
//
// Deleting a project with its data versions moves the ready ones to the
// bin; the project stays with them, out of use, until it is restored with
// one of them or the last of its models and data versions is purged
// (project.PurgeEmptyProjects).

var errVersionNotInBin = &Error{Kind: ErrNotFound, Message: "回收站中没有这个数据版本（可能已经恢复，或已被永久删除）"}

// moveToBin moves a ready version to the recycle bin (Delete calls it). It
// is one guarded statement on its own connection: do not call it while
// holding the version's row lock in another transaction.
func (s *Service) moveToBin(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE data_versions SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'ready' AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errVersionNotFound
	}
	s.log.Info("data version moved to the recycle bin", "data_version_id", id)
	return nil
}

// maxRestoreRetries bounds how often Restore starts again because the
// project was moved to the recycle bin meanwhile.
const maxRestoreRetries = 3

var errProjectDeletedMeanwhile = errors.New("project deleted meanwhile")

// Restore takes a version out of the recycle bin (with its project when the
// project went to the bin with it). Errors about the project are
// *project.Error values; one of kind project.ErrNameTaken means another
// project took its name (in.ProjectName restores it under another one). A
// version whose datasets were purged meanwhile is restored all the same:
// its folder holds everything.
func (s *Service) Restore(ctx context.Context, id int64, in RestoreInput) (DataVersion, error) {
	for range maxRestoreRetries {
		projectRestored, err := s.restore(ctx, id, in)
		if errors.Is(err, errProjectDeletedMeanwhile) {
			continue
		}
		if err != nil {
			return DataVersion{}, err
		}
		s.log.Info("data version restored from the recycle bin", "data_version_id", id, "project_restored", projectRestored)
		return s.Get(ctx, id)
	}
	return DataVersion{}, fmt.Errorf("restore data version: its project was deleted %d times while restoring", maxRestoreRetries)
}

func (s *Service) restore(ctx context.Context, id int64, in RestoreInput) (projectRestored bool, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		projectRestored = false
		var projectID int64
		var projectDeleted bool
		err := tx.QueryRow(ctx, `SELECT v.project_id, p.deleted_at IS NOT NULL
			FROM data_versions v JOIN projects p ON p.id = v.project_id
			WHERE v.id = $1 AND v.deleted_at IS NOT NULL`, id).Scan(&projectID, &projectDeleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return errVersionNotInBin
		}
		if err != nil {
			return err
		}
		// The project first, then the version (the lock order of deleting a
		// project: it locks its datasets, itself, then counts its versions).
		if projectDeleted {
			if _, err := project.RestoreInTx(ctx, tx, projectID, in.ProjectName); err != nil {
				return err
			}
			projectRestored = true
		} else {
			// Keeps the project from going to the bin until the version is
			// back (deleting it locks it FOR UPDATE, then counts its versions).
			var live bool
			if err := tx.QueryRow(ctx, "SELECT deleted_at IS NULL FROM projects WHERE id = $1 FOR KEY SHARE",
				projectID).Scan(&live); err != nil {
				return err
			}
			if !live {
				return errProjectDeletedMeanwhile
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE data_versions SET deleted_at = NULL, updated_at = now()
			WHERE id = $1 AND deleted_at IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errVersionNotInBin // restored or purged meanwhile
		}
		return nil
	})
	return projectRestored, err
}

// ListTrashed returns the versions in the recycle bin, newest deletion
// first.
func (s *Service) ListTrashed(ctx context.Context) ([]TrashedDataVersion, error) {
	rows, err := s.db.Query(ctx, "SELECT "+versionColumns+", v.deleted_at, p.deleted_at IS NOT NULL"+versionFrom+
		" WHERE v.deleted_at IS NOT NULL ORDER BY v.deleted_at DESC, v.id DESC")
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TrashedDataVersion, error) {
		var t TrashedDataVersion
		v, err := scanVersionWith(row, &t.DeletedAt, &t.ProjectDeleted)
		if err != nil {
			return t, err
		}
		t.DataVersion = v
		t.DeletedAt = t.DeletedAt.UTC()
		t.PurgeAt = recycle.PurgeAt(t.DeletedAt)
		return t, nil
	})
	if err != nil {
		return nil, err
	}
	versions := make([]DataVersion, len(out))
	for i := range out {
		versions[i] = out[i].DataVersion
	}
	if err := s.decorate(ctx, s.db, versions); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].DataVersion = versions[i]
	}
	if out == nil {
		out = []TrashedDataVersion{}
	}
	return out, nil
}

// janitorLoop purges expired versions and removes leftover folders, at
// start and every janitorInterval, until ctx ends.
func (s *Service) janitorLoop(ctx context.Context) {
	tick := time.NewTicker(janitorInterval)
	defer tick.Stop()
	for {
		if err := s.janitorPass(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("data version cleanup", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// janitorPass runs the cleanup once: it purges the versions whose time in
// the recycle bin is over, then the projects left empty in the bin, and
// removes what interrupted operations left on disk. It goes on after an
// error and returns the errors joined.
func (s *Service) janitorPass(ctx context.Context) error {
	var errs []error
	if err := s.purgeExpired(ctx); err != nil {
		errs = append(errs, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.reconcile(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// purgeExpired purges the versions whose time in the recycle bin is over,
// then the projects in the bin left without models and data versions.
func (s *Service) purgeExpired(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id FROM data_versions
		WHERE deleted_at <= now() - $1 * interval '1 second' ORDER BY deleted_at, id`, recycle.RetentionSeconds)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil { // purgeVersion itself does not stop
			return ctx.Err()
		}
		if err := s.purgeVersion(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Error("purge data version from the recycle bin", "data_version_id", id, "err", err)
		}
	}
	purged, err := project.PurgeEmptyProjects(ctx, s.db)
	for _, id := range purged {
		s.RemoveProjectFolder(id)
		s.log.Info("project deleted with its last data version purged from the recycle bin", "project_id", id)
	}
	return err
}

// purgeVersion deletes a version whose time in the recycle bin is over
// for good, with its image rows, its folder and its thumbnails. A version
// restored meanwhile is left alone.
func (s *Service) purgeVersion(ctx context.Context, id int64) error {
	ctx = context.WithoutCancel(ctx) // a short transaction; the folder is moved after it
	var dirName string
	purged := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT dir_name FROM data_versions
			WHERE id = $1 AND deleted_at <= now() - $2 * interval '1 second' FOR UPDATE`,
			id, recycle.RetentionSeconds).Scan(&dirName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // restored meanwhile
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM data_versions WHERE id = $1", id); err != nil {
			return err
		}
		purged = true
		return nil
	})
	if err != nil || !purged {
		return err
	}
	s.log.Info("data version purged from the recycle bin", "data_version_id", id, "dir", dirName)
	s.discardThumbnails(id)
	if err := s.discard(id, s.versionDir(dirName)); err != nil {
		// Its row is gone and it has no .pending: nothing removes it later.
		return fmt.Errorf("move the folder of purged data version %d aside: %w", id, err)
	}
	return nil
}

// projectDirPrefix names the folder of a project's versions under rootDir.
const projectDirPrefix = "project-"

// RemoveProjectFolder removes the folder of a project from
// exports/data-versions if it is empty (a deleted project's; the folder of
// a project in the recycle bin still holds its versions and stays).
func (s *Service) RemoveProjectFolder(projectID int64) {
	_ = os.Remove(filepath.Join(s.rootDir(), projectDirPrefix+strconv.FormatInt(projectID, 10)))
}

// deletingGrace is how old a folder being removed (.deleting-*) must be
// before the janitor removes it too: younger ones are being removed by
// the goroutine discard started.
const deletingGrace = 10 * time.Minute

// reconcile removes what interrupted operations left on disk: folders
// being removed (.deleting-*), version folders still marked .pending whose
// version is gone, thumbnail caches of versions that are gone and the
// empty folders of projects that are gone. A version folder without
// .pending and without a version is left alone: it may come from a backup
// (and Create skips its number).
func (s *Service) reconcile(ctx context.Context) error {
	var errs []error
	entries, err := os.ReadDir(s.rootDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	var projectIDs []int64
	pending := map[string]string{} // dir_name -> path of version folders with .pending
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := e.Name()
		path := filepath.Join(s.rootDir(), name)
		if isDeletingName(name) {
			if deletingAge(name) < deletingGrace {
				continue
			}
			if err := removeTreeCtx(ctx, path); err != nil && ctx.Err() == nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
			}
			continue
		}
		id, ok := parseFolderID(name, projectDirPrefix)
		if !ok || !e.IsDir() {
			continue
		}
		projectIDs = append(projectIDs, id)
		versions, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, v := range versions {
			if _, ok := parseFolderID(v.Name(), "v"); !ok || !v.IsDir() {
				continue
			}
			if _, err := os.Lstat(filepath.Join(path, v.Name(), pendingMarker)); err == nil {
				pending["data-versions/"+name+"/"+v.Name()] = filepath.Join(path, v.Name())
			}
		}
	}
	if err := s.discardPendingOrphans(ctx, pending); err != nil {
		errs = append(errs, err)
	}
	if err := s.removeGoneProjects(ctx, projectIDs); err != nil {
		errs = append(errs, err)
	}
	if err := s.removeOrphanThumbnails(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// discardPendingOrphans clears the version folders still marked .pending
// (by dir_name) that have no version: a build interrupted by a crash
// after its version was deleted. The folder exists before the query, and
// a version's folder is made only after its row, so a missing row is gone
// for good.
func (s *Service) discardPendingOrphans(ctx context.Context, pending map[string]string) error {
	if len(pending) == 0 {
		return nil
	}
	names := make([]string, 0, len(pending))
	for name := range pending {
		names = append(names, name)
	}
	rows, err := s.db.Query(ctx, "SELECT dir_name FROM data_versions WHERE dir_name = ANY($1)", names)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range existing {
		delete(pending, name)
	}
	var errs []error
	for name, path := range pending {
		s.log.Info("data version: clearing the folder of an interrupted build", "dir", name)
		if err := s.discard(0, path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeGoneProjects removes the empty folders of projects that no longer
// exist.
func (s *Service) removeGoneProjects(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	gone, err := missingRowIDs(ctx, s, "projects", ids)
	if err != nil {
		return err
	}
	for _, id := range gone {
		s.RemoveProjectFolder(id)
	}
	return nil
}

// removeOrphanThumbnails removes the thumbnail caches of versions that no
// longer exist (a version purged while a thumbnail was being made).
func (s *Service) removeOrphanThumbnails(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(s.storage.Dir(storage.Cache), "data-versions"))
	if err != nil {
		return nil // no cache yet
	}
	var ids []int64
	for _, e := range entries {
		if id, ok := parseFolderID(e.Name(), ""); ok && e.IsDir() {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	gone, err := missingRowIDs(ctx, s, "data_versions", ids)
	if err != nil {
		return err
	}
	for _, id := range gone {
		s.discardThumbnails(id)
	}
	return nil
}

// missingRowIDs returns the ids that have no row in table.
func missingRowIDs(ctx context.Context, s *Service, table string, ids []int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, "SELECT id FROM "+table+" WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	found := make(map[int64]bool, len(existing))
	for _, id := range existing {
		found[id] = true
	}
	var gone []int64
	for _, id := range ids {
		if !found[id] {
			gone = append(gone, id)
		}
	}
	return gone, nil
}

// parseFolderID parses <prefix><positive decimal id> exactly.
func parseFolderID(name, prefix string) (int64, bool) {
	n, ok := strings.CutPrefix(name, prefix)
	if !ok || n == "" || len(n) > 18 || strings.Trim(n, "0123456789") != "" || n[0] == '0' {
		return 0, false
	}
	id, err := strconv.ParseInt(n, 10, 64)
	return id, err == nil && id > 0
}

// deletingAge is how long ago discard moved the folder name
// (.deleting-<id>-<unix nanos>) aside; a name without a time is old.
func deletingAge(name string) time.Duration {
	i := strings.LastIndexByte(name, '-')
	nanos, err := strconv.ParseInt(name[i+1:], 10, 64)
	if i < len(deletingPrefix) || err != nil {
		return time.Duration(1<<63 - 1)
	}
	return time.Since(time.Unix(0, nanos))
}

// removeTreeCtx removes path like os.RemoveAll, but stops when ctx ends (the
// rest is removed by a later pass), so that removing a large leftover
// cannot hold up a shutdown. Symbolic links are removed, not followed.
func removeTreeCtx(ctx context.Context, path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := removeTreeCtx(ctx, filepath.Join(path, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
