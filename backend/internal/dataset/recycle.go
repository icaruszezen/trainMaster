package dataset

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

	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
)

// The recycle bin (回收站) of datasets and their review versions. Deleting
// either moves it to the bin, a soft delete (deleted_at, migration
// 0012_recycle_bin.sql); every other query treats it as gone. It is
// restored on request, or purged for good once recycle.Retention is over
// (purgeExpired, run by the janitor).
//
// A dataset in the bin keeps its rows (images, imports, review versions).
// Deleted with its files, its folder is renamed to datasets/.recycle-<id>
// (hidden, so the name and the folder name can be used again) and removed
// when it is purged; deleted without, the folder stays where it is and
// still belongs to the dataset (it cannot be added again meanwhile, only
// restored). The links other modules read move aside meanwhile: its
// projects to recycled_project_datasets (here), its review tasks through
// the RecycleHook (package review).

// recyclePrefix marks the folder of a dataset deleted with its files while
// it is in the recycle bin. Being hidden, it never shows up as a directory.
const recyclePrefix = ".recycle-"

func (s *Service) recycleDir(datasetID int64) string {
	return filepath.Join(s.datasetsDir(), recyclePrefix+strconv.FormatInt(datasetID, 10))
}

// RecycleHook moves the rows of another module that refer to a dataset out
// of the way while the dataset is in the recycle bin, and back when it is
// restored. Both run in the transaction that moves the dataset, which
// holds its row lock (FOR UPDATE).
type RecycleHook interface {
	DetachDataset(ctx context.Context, tx pgx.Tx, datasetID int64) error
	AttachDataset(ctx context.Context, tx pgx.Tx, datasetID int64) error
}

// SetRecycleHook installs the hook (package review). Call it before the
// service is used.
func (s *Service) SetRecycleHook(h RecycleHook) { s.recycleHook = h }

var (
	errBusyDelete = &Error{Kind: ErrConflict, Message: "数据集正在导入或扫描，请等待完成或取消后再删除"}
	errBuilding   = &Error{Kind: ErrConflict,
		Message: "审图任务正在合并到这个数据集（正在生成审图版本），请等合并完成后再删除"}
	errNotInBin = &Error{Kind: ErrNotFound, Message: "回收站中没有这个数据集（可能已经恢复，或已被永久删除）"}
)

// DeleteDataset moves a dataset to the recycle bin. With deleteFiles its
// folder is removed when the dataset is purged (and set aside until then);
// otherwise the folder stays and can be added again once the dataset is
// purged. Datasets with a queued or running job, or with a review version
// being built, cannot be deleted; unfinished uploads are canceled.
func (s *Service) DeleteDataset(ctx context.Context, id int64, deleteFiles bool) error {
	unlock, err := s.recycleLocks.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := s.detach(ctx)
	defer cancel()
	var canceled []int64
	var dirName, moved string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		d, err := queryDataset(ctx, tx, id, true)
		if err != nil {
			return err
		}
		dirName = d.DirName
		busy, err := hasProcessingJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if busy {
			return errBusyDelete
		}
		var building bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dataset_versions
			WHERE dataset_id = $1 AND status = 'building')`, id).Scan(&building); err != nil {
			return err
		}
		if building {
			return errBuilding
		}
		rows, err := tx.Query(ctx, `UPDATE dataset_imports SET status = 'canceled', finished_at = now(), updated_at = now()
			WHERE dataset_id = $1 AND status = 'uploading' RETURNING id`, id)
		if err != nil {
			return err
		}
		if canceled, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		if err := detachProjects(ctx, tx, id); err != nil {
			return err
		}
		if s.recycleHook != nil {
			if err := s.recycleHook.DetachDataset(ctx, tx, id); err != nil {
				return fmt.Errorf("detach dataset from reviews: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE datasets SET deleted_at = now(), delete_files = $2 WHERE id = $1`,
			id, deleteFiles); err != nil {
			return err
		}
		if deleteFiles {
			// Renaming is atomic and quick, so the transaction can still roll
			// back; the files are removed when the dataset is purged.
			if moved, err = s.moveFolder(d.DirName, s.recycleDir(id)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if moved != "" {
			if rerr := os.Rename(moved, s.datasetDir(dirName)); rerr != nil {
				s.log.Error("restore dataset folder after failed delete", "path", moved, "err", rerr)
			}
		}
		return err
	}
	for _, jobID := range canceled {
		s.removeStaging(jobID)
	}
	s.log.Info("dataset moved to the recycle bin", "dataset_id", id, "delete_files", deleteFiles)
	return nil
}

// detach returns a context for a transaction that moves a folder with it:
// a client going away must not cancel it, least of all during the commit,
// which would leave it unknown whether the move was recorded. Stopping the
// service still does.
func (s *Service) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if s.base == nil { // not started (tests)
		return ctx, cancel
	}
	stop := context.AfterFunc(s.base, cancel)
	return ctx, func() { stop(); cancel() }
}

// moveFolder renames the dataset folder dirName to the hidden path to and
// returns to, or "" when there is no folder (nothing to set aside). It
// refuses symbolic links, so the files removed later are never those a
// link points to.
func (s *Service) moveFolder(dirName, to string) (string, error) {
	dir := s.datasetDir(dirName)
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("stat dataset folder: %w", err)
	case !fi.IsDir(): // includes symbolic links
		return "", &Error{Kind: ErrConflict, Message: "数据集文件夹是符号链接或不是普通文件夹，无法删除其中的文件"}
	}
	if err := os.Rename(dir, to); err != nil {
		return "", fmt.Errorf("move dataset folder aside: %w", err)
	}
	return to, nil
}

// detachProjects moves the project links of a dataset that goes to the
// recycle bin to recycled_project_datasets, marking the projects as
// updated (they lose a dataset). The caller holds the dataset's row lock;
// the projects are locked after it in ID order, the lock order of package
// project.
func detachProjects(ctx context.Context, tx pgx.Tx, datasetID int64) error {
	if err := touchDatasetProjects(ctx, tx, datasetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `WITH moved AS (DELETE FROM project_datasets WHERE dataset_id = $1 RETURNING *)
		INSERT INTO recycled_project_datasets SELECT * FROM moved ON CONFLICT DO NOTHING`, datasetID)
	return err
}

// attachProjects puts back the project links of a dataset restored from
// the recycle bin, for the projects in use (one in the bin keeps the link
// aside until it is restored too).
func attachProjects(ctx context.Context, tx pgx.Tx, datasetID int64) error {
	rows, err := tx.Query(ctx, `SELECT p.id FROM projects p
		JOIN recycled_project_datasets r ON r.project_id = p.id AND r.dataset_id = $1
		WHERE p.deleted_at IS NULL ORDER BY p.id FOR NO KEY UPDATE OF p`, datasetID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `WITH moved AS (DELETE FROM recycled_project_datasets
			WHERE dataset_id = $1 AND project_id = ANY($2) RETURNING *)
		INSERT INTO project_datasets SELECT * FROM moved ON CONFLICT DO NOTHING`, datasetID, ids); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE projects SET updated_at = now() WHERE id = ANY($1)", ids)
	return err
}

// RestoreInput are the options of restoring a dataset.
type RestoreInput struct {
	// Name, when set, is the name the dataset gets back (its name is used
	// by another dataset). A dataset deleted with its files whose folder
	// name is taken meanwhile also gets its folder back under this name.
	Name *string
}

// RestoreDataset takes a dataset out of the recycle bin: its folder goes
// back under its name (deleted with its files), it is back in the projects
// still in use and in its review tasks. It fails with an *Error of kind
// ErrNameTaken when another dataset has its name, or its folder name is
// taken meanwhile: in.Name restores it under another one.
//
// The folder moves back after the commit, so that whatever happens the
// dataset is never in the bin while its folder is back under its name
// (where nothing tells it from a folder added later): a crash before the
// move leaves a dataset in use with its folder under .recycle-<id>, which
// reconcileRecycleFolders moves back at the next start, and so does a move
// that fails.
func (s *Service) RestoreDataset(ctx context.Context, id int64, in RestoreInput) (Dataset, error) {
	unlock, err := s.recycleLocks.lock(ctx, id)
	if err != nil {
		return Dataset{}, err
	}
	defer unlock()
	ctx, cancel := s.detach(ctx)
	defer cancel()
	var moveBack bool
	var dirName string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		d, err := queryTrashedDataset(ctx, tx, id)
		if err != nil {
			return err
		}
		name := d.Name
		if in.Name != nil {
			if name, err = NormalizeName(*in.Name); err != nil {
				return err
			}
		}
		if err := checkNameFree(ctx, tx, name, id); errors.Is(err, errNameTaken) {
			return newError(ErrNameTaken, "已有名为“%s”的数据集，请输入新的名称后恢复（也可以先把那个数据集改名）", name)
		} else if err != nil {
			return err
		}
		dirName = d.DirName
		if d.DeleteFiles {
			// The folder name was free while the dataset was in the bin.
			if taken, err := s.dirTaken(ctx, tx, dirName, id); err != nil {
				return err
			} else if taken != "" {
				if in.Name == nil || !validDirName(name) {
					return newError(ErrNameTaken, "数据目录中的文件夹“%s”已%s，请输入新的名称后恢复（文件夹也会使用新的名称）",
						dirName, taken)
				}
				dirName = name
				if taken, err := s.dirTaken(ctx, tx, dirName, id); err != nil {
					return err
				} else if taken != "" {
					return newError(ErrNameTaken, "数据目录中的文件夹“%s”已%s，请换一个名称", dirName, taken)
				}
			}
		}
		if err := attachProjects(ctx, tx, id); err != nil {
			return err
		}
		if s.recycleHook != nil {
			if err := s.recycleHook.AttachDataset(ctx, tx, id); err != nil {
				return fmt.Errorf("attach dataset to reviews: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE datasets SET deleted_at = NULL, delete_files = false, name = $2, dir_name = $3,
				updated_at = now()
			WHERE id = $1`, id, name, dirName); err != nil {
			return uniqueViolation(err, dirName)
		}
		moveBack = d.DeleteFiles
		return nil
	})
	if err != nil {
		return Dataset{}, err
	}
	if moveBack {
		from := s.recycleDir(id)
		if err := os.Rename(from, s.datasetDir(dirName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Error("move the folder of a restored dataset back; trying again at the next start",
				"dataset_id", id, "path", from, "err", err)
		}
	}
	s.log.Info("dataset restored from the recycle bin", "dataset_id", id)
	return s.GetDataset(ctx, id)
}

// dirTaken says why the folder name dirName cannot be used by the dataset
// exceptID ("" if it can): another dataset uses it, or the data directory
// has an entry of that name (ignoring case, like creating a dataset).
func (s *Service) dirTaken(ctx context.Context, q querier, dirName string, exceptID int64) (string, error) {
	if owner, err := datasetUsingDir(ctx, q, dirName, exceptID); err != nil {
		return "", err
	} else if owner != nil {
		return "被数据集" + owner.String() + "使用", nil
	}
	if existing, err := s.findDirEntry(dirName); err != nil {
		return "", err
	} else if existing != "" {
		return "存在", nil
	}
	return "", nil
}

// queryTrashedDataset reads and locks a dataset in the recycle bin.
func queryTrashedDataset(ctx context.Context, tx pgx.Tx, id int64) (trashedDataset, error) {
	var t trashedDataset
	d, err := scanDatasetWith(tx.QueryRow(ctx, "SELECT "+datasetColumns+
		", deleted_at, delete_files FROM datasets WHERE id = $1 AND deleted_at IS NOT NULL FOR UPDATE", id),
		&t.DeletedAt, &t.DeleteFiles)
	if errors.Is(err, ErrNotFound) {
		return t, errNotInBin
	}
	t.Dataset = d
	return t, err
}

type trashedDataset struct {
	Dataset
	DeletedAt   time.Time
	DeleteFiles bool
}

// TrashedDataset is a dataset in the recycle bin.
type TrashedDataset struct {
	Dataset
	DeletedAt   time.Time `json:"deleted_at"`
	PurgeAt     time.Time `json:"purge_at"` // when it is deleted for good
	DeleteFiles bool      `json:"delete_files"`
	// The projects in use it goes back to and the number of review tasks
	// it is part of when restored.
	RestoreProjects []ProjectRef `json:"restore_projects"`
	ReviewCount     int          `json:"review_count"`
}

// ListTrashedDatasets returns the datasets in the recycle bin, the most
// recently deleted first.
func (s *Service) ListTrashedDatasets(ctx context.Context) ([]TrashedDataset, error) {
	rows, err := s.db.Query(ctx, "SELECT "+datasetColumns+
		`, deleted_at, delete_files FROM datasets WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TrashedDataset, error) {
		var t TrashedDataset
		d, err := scanDatasetWith(row, &t.DeletedAt, &t.DeleteFiles)
		t.Dataset, t.DeletedAt = d, t.DeletedAt.UTC()
		t.PurgeAt = recycle.PurgeAt(t.DeletedAt)
		t.RestoreProjects = []ProjectRef{}
		return t, err
	})
	if err != nil || len(out) == 0 {
		return []TrashedDataset{}, err
	}
	ids := make([]int64, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	versions, err := queryVersionCounts(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT r.dataset_id, p.id, p.name
		FROM recycled_project_datasets r JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
		WHERE r.dataset_id = ANY($1) ORDER BY r.dataset_id, lower(p.name), p.id`, ids)
	if err != nil {
		return nil, err
	}
	projects := map[int64][]ProjectRef{}
	var datasetID int64
	var p ProjectRef
	if _, err := pgx.ForEachRow(rows, []any{&datasetID, &p.ID, &p.Name}, func() error {
		projects[datasetID] = append(projects[datasetID], p)
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT dataset_id, count(*) FROM recycled_review_datasets
		WHERE dataset_id = ANY($1) GROUP BY dataset_id`, ids)
	if err != nil {
		return nil, err
	}
	reviews := map[int64]int{}
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&datasetID, &n}, func() error {
		reviews[datasetID] = n
		return nil
	}); err != nil {
		return nil, err
	}
	for i := range out {
		t := &out[i]
		t.VersionCount = versions[t.ID]
		t.ReviewCount = reviews[t.ID]
		if p := projects[t.ID]; p != nil {
			t.RestoreProjects = p
		}
		if t.DeleteFiles {
			t.Path = s.recycleDir(t.ID)
		} else {
			t.Path = s.datasetDir(t.DirName)
		}
		fi, err := os.Lstat(t.Path)
		t.DirectoryMissing = err != nil || !fi.IsDir()
	}
	return out, nil
}

// reconcileRecycleFolders repairs what a crash between renaming a folder
// and the commit leaves in the data directory: the folder of a dataset in
// use found under .recycle-<id> (its delete was rolled back, or it was
// restored and its folder not moved back yet) goes back under its name,
// and one of a dataset that is gone is left to the janitor (renamed to
// .deleting-<id>). Start runs it before anything else uses the folders.
func (s *Service) reconcileRecycleFolders(ctx context.Context) error {
	entries, err := os.ReadDir(s.datasetsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), recyclePrefix)
		id, err := strconv.ParseInt(rest, 10, 64)
		if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != rest {
			continue
		}
		path := filepath.Join(s.datasetsDir(), e.Name())
		var dirName string
		var trashed, deleteFiles bool
		err = s.db.QueryRow(ctx, "SELECT dir_name, deleted_at IS NOT NULL, delete_files FROM datasets WHERE id = $1",
			id).Scan(&dirName, &trashed, &deleteFiles)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			trash := filepath.Join(s.datasetsDir(), deletingPrefix+rest)
			if err := os.Rename(path, trash); err != nil {
				s.log.Error("set aside the recycled folder of a purged dataset", "path", path, "err", err)
			}
		case err != nil:
			return err
		case trashed && deleteFiles:
			// In the bin, as it should be.
		case !trashed:
			target := s.datasetDir(dirName)
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				s.log.Error("dataset in use has a recycled folder and a folder under its name; left both alone",
					"dataset_id", id, "recycled", path, "folder", target)
				continue
			}
			if err := os.Rename(path, target); err != nil {
				s.log.Error("move the folder of a dataset in use back from the recycle bin", "path", path, "err", err)
				continue
			}
			s.log.Warn("moved the folder of a dataset in use back from the recycle bin (an interrupted delete or restore)",
				"dataset_id", id, "path", target)
		}
	}
	return nil
}

// ---- purging ----

// purgeExpired purges the datasets and the review versions whose time in
// the recycle bin is over.
func (s *Service) purgeExpired(ctx context.Context) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM dataset_versions
		WHERE deleted_at <= now() - $1 * interval '1 second'`, recycle.RetentionSeconds)
	if err != nil {
		return fmt.Errorf("purge dataset versions: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		s.log.Info("dataset versions purged from the recycle bin", "versions", n)
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM datasets
		WHERE deleted_at <= now() - $1 * interval '1 second' ORDER BY deleted_at, id`, recycle.RetentionSeconds)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.purgeDataset(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Error("purge dataset from the recycle bin", "dataset_id", id, "err", err)
		}
	}
	return nil
}

// purgeDataset deletes a dataset whose time in the recycle bin is over for
// good: its rows (with everything that refers to them) and, if it was
// deleted with its files, its folder. A dataset restored meanwhile is left
// alone.
func (s *Service) purgeDataset(ctx context.Context, id int64) error {
	var trash, aside string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var deleteFiles bool
		err := tx.QueryRow(ctx, `SELECT delete_files FROM datasets
			WHERE id = $1 AND deleted_at <= now() - $2 * interval '1 second' FOR UPDATE`,
			id, recycle.RetentionSeconds).Scan(&deleteFiles)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // restored meanwhile
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM datasets WHERE id = $1", id); err != nil {
			return err
		}
		if deleteFiles {
			aside = s.recycleDir(id)
			trash = filepath.Join(s.datasetsDir(), deletingPrefix+strconv.FormatInt(id, 10))
			if err := os.Rename(aside, trash); errors.Is(err, fs.ErrNotExist) {
				aside, trash = "", ""
			} else if err != nil {
				aside, trash = "", ""
				return fmt.Errorf("move recycled dataset folder: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		if trash != "" {
			if rerr := os.Rename(trash, aside); rerr != nil {
				s.log.Error("set recycled dataset folder back after a failed purge", "path", trash, "err", rerr)
			}
		}
		return err
	}
	s.removeAllThumbnails(id)
	s.log.Info("dataset purged from the recycle bin", "dataset_id", id, "delete_files", trash != "")
	if trash != "" {
		if err := s.removeAll(trash); err != nil {
			// The janitor tries again (cleanTrash).
			s.log.Error("dataset folder only partly removed; will retry", "dataset_id", id, "path", trash, "err", err)
		}
	}
	return nil
}

// ---- review versions ----

// errVersionNotInBin: the version is not in the recycle bin.
var errVersionNotInBin = &Error{Kind: ErrNotFound, Message: "回收站中没有这个版本（可能已经恢复，或已被永久删除）"}

// RestoreVersion takes a review version out of the recycle bin. Its
// dataset must be in use (restore it first).
func (s *Service) RestoreVersion(ctx context.Context, versionID int64) (Version, error) {
	var datasetID int64
	var datasetName string
	var datasetDeleted bool
	err := s.db.QueryRow(ctx, `SELECT v.dataset_id, d.name, d.deleted_at IS NOT NULL
		FROM dataset_versions v JOIN datasets d ON d.id = v.dataset_id
		WHERE v.id = $1 AND v.deleted_at IS NOT NULL`, versionID).Scan(&datasetID, &datasetName, &datasetDeleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, errVersionNotInBin
	}
	if err != nil {
		return Version{}, err
	}
	if datasetDeleted {
		return Version{}, newError(ErrConflict, "这个版本所属的数据集“%s”也在回收站中，请先恢复数据集", datasetName)
	}
	tag, err := s.db.Exec(ctx, `UPDATE dataset_versions v SET deleted_at = NULL
		WHERE v.id = $1 AND v.deleted_at IS NOT NULL
			AND EXISTS (SELECT 1 FROM datasets WHERE id = v.dataset_id AND deleted_at IS NULL)`, versionID)
	if err != nil {
		return Version{}, err
	}
	if tag.RowsAffected() == 0 {
		return Version{}, errVersionNotInBin // restored (or its dataset deleted) meanwhile
	}
	s.log.Info("dataset version restored from the recycle bin", "dataset_id", datasetID, "version_id", versionID)
	return s.GetVersion(ctx, datasetID, versionID)
}

// TrashedVersion is a review version in the recycle bin.
type TrashedVersion struct {
	Version
	DatasetName string    `json:"dataset_name"`
	DeletedAt   time.Time `json:"deleted_at"`
	// PurgeAt is when it is deleted for good: its own time, or its
	// dataset's if that is sooner.
	PurgeAt time.Time `json:"purge_at"`
	// DatasetDeleted: the dataset is in the recycle bin too; it must be
	// restored first (the version goes when the dataset is purged).
	DatasetDeleted bool `json:"dataset_deleted"`
}

// ListTrashedVersions returns the review versions in the recycle bin, the
// most recently deleted first.
func (s *Service) ListTrashedVersions(ctx context.Context) ([]TrashedVersion, error) {
	rows, err := s.db.Query(ctx, "SELECT "+versionColumns+`, vd.name, v.deleted_at, vd.deleted_at
		FROM dataset_versions v JOIN datasets vd ON vd.id = v.dataset_id LEFT JOIN reviews r ON r.id = v.review_id
		WHERE v.deleted_at IS NOT NULL ORDER BY v.deleted_at DESC, v.id DESC`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TrashedVersion, error) {
		var t TrashedVersion
		var datasetDeletedAt *time.Time
		v, err := scanVersionWith(row, &t.DatasetName, &t.DeletedAt, &datasetDeletedAt)
		t.Version, t.DeletedAt = v, t.DeletedAt.UTC()
		t.PurgeAt = recycle.PurgeAt(t.DeletedAt)
		if datasetDeletedAt != nil {
			t.DatasetDeleted = true
			if at := recycle.PurgeAt(datasetDeletedAt.UTC()); at.Before(t.PurgeAt) {
				t.PurgeAt = at
			}
		}
		return t, err
	})
	if out == nil && err == nil {
		out = []TrashedVersion{}
	}
	return out, err
}
