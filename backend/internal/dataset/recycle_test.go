package dataset

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
)

// expire makes the rows of table (datasets or dataset_versions) look as if
// they had been in the recycle bin for longer than recycle.Retention.
func (e *testEnv) expire(t *testing.T, table string, ids ...int64) {
	t.Helper()
	tag, err := e.db.Exec(context.Background(), "UPDATE "+table+
		" SET deleted_at = now() - interval '8 days' WHERE id = ANY($1) AND deleted_at IS NOT NULL", ids)
	if err != nil || tag.RowsAffected() != int64(len(ids)) {
		t.Fatalf("expire %s %v: %d rows, %v", table, ids, tag.RowsAffected(), err)
	}
}

func (e *testEnv) trashed(t *testing.T) []TrashedDataset {
	t.Helper()
	items, err := e.svc.ListTrashedDatasets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (e *testEnv) newProject(t *testing.T, name string, datasetIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := e.db.QueryRow(ctx, "INSERT INTO projects (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, d := range datasetIDs {
		if _, err := e.db.Exec(ctx, "INSERT INTO project_datasets (project_id, dataset_id) VALUES ($1, $2)", id, d); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Deleting without the files: the dataset leaves every list, its folder
// stays and still belongs to it, and restoring brings it back as it was.
func TestIntegrationRecycleDatasetKeepingFiles(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()

	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "keep-files"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2)})
	upload, _ := e.svc.CreateImport(ctx, d.ID, CreateImportInput{Source: SourceFolder})
	projectID := e.newProject(t, "项目", d.ID)
	before := time.Now()
	if err := e.svc.DeleteDataset(ctx, d.ID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !exists(filepath.Join(d.Path, "a.png")) {
		t.Error("files must stay without delete_files")
	}
	if exists(e.svc.stagingDir(upload.ID)) {
		t.Error("staging of the canceled upload remains")
	}
	_, err := e.svc.GetDataset(ctx, d.ID)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	wantKind(t, e.svc.DeleteDataset(ctx, d.ID, false), ErrNotFound, "数据集不存在")
	if list, err := e.svc.ListDatasets(ctx, DatasetQuery{}); err != nil || len(list) != 0 {
		t.Errorf("ListDatasets = %+v, %v", list, err)
	}
	_, err = e.svc.GetImport(ctx, d.ID, upload.ID)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	var links, recycled int
	if err := e.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_datasets),
		(SELECT count(*) FROM recycled_project_datasets)`).Scan(&links, &recycled); err != nil || links != 0 || recycled != 1 {
		t.Errorf("links = %d, recycled = %d, %v", links, recycled, err)
	}

	items := e.trashed(t)
	if len(items) != 1 {
		t.Fatalf("recycle bin = %+v", items)
	}
	it := items[0]
	if it.ID != d.ID || it.DeleteFiles || it.DirectoryMissing || it.Path != d.Path || it.ImageCount != d.ImageCount ||
		it.DeletedAt.Before(before.Add(-time.Second)) || it.PurgeAt.Sub(it.DeletedAt) != recycle.Retention ||
		len(it.RestoreProjects) != 1 || it.RestoreProjects[0].ID != projectID {
		t.Errorf("trashed dataset = %+v", it)
	}

	// Its folder still belongs to it; its name is free.
	dirs, err := e.svc.ListDirectories(ctx)
	if err != nil || len(dirs) != 1 || !dirs[0].Registered || !dirs[0].InRecycleBin || *dirs[0].DatasetID != d.ID {
		t.Errorf("ListDirectories = %+v, %v", dirs, err)
	}
	dirName := "keep-files"
	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "again", Directory: &dirName})
	wantKind(t, err, ErrConflict, "回收站")
	other, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	name := "Keep-Files"
	if _, err := e.svc.UpdateDataset(ctx, other.ID, UpdateDatasetInput{Name: &name}); err != nil {
		t.Fatalf("the name of a dataset in the bin must be free: %v", err)
	}

	_, err = e.svc.RestoreDataset(ctx, d.ID, RestoreInput{})
	wantKind(t, err, ErrNameTaken, "已有名为“keep-files”的数据集")
	newName := "keep-files-2"
	got, err := e.svc.RestoreDataset(ctx, d.ID, RestoreInput{Name: &newName})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got.Name != newName || got.DirName != "keep-files" || got.Path != d.Path || len(got.Projects) != 1 ||
		got.Projects[0].ID != projectID {
		t.Errorf("restored = %+v", got)
	}
	if len(e.trashed(t)) != 0 {
		t.Error("restored dataset still in the bin")
	}
	_, err = e.svc.RestoreDataset(ctx, d.ID, RestoreInput{})
	wantKind(t, err, ErrNotFound, "回收站中没有这个数据集")

	// Purged after the retention: the folder stays and can be added again.
	if err := e.svc.DeleteDataset(ctx, d.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.purgeExpired(ctx); err != nil || len(e.trashed(t)) != 1 {
		t.Fatalf("purged before the retention ended: %v", err)
	}
	e.expire(t, "datasets", d.ID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.trashed(t)) != 0 || !exists(filepath.Join(d.Path, "a.png")) {
		t.Error("purge must remove the dataset but keep its folder")
	}
	if _, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "again", Directory: &dirName}); err != nil {
		t.Errorf("re-register after the purge: %v", err)
	}
}

// Deleting with the files: the folder is set aside (hidden, its name free)
// and removed when the dataset is purged.
func TestIntegrationRecycleDatasetWithFiles(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()

	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "pics"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2)})
	outside := t.TempDir()
	writeFiles(t, outside, map[string][]byte{"precious.png": pngFile(t, 2, 2)})
	if err := os.Symlink(outside, filepath.Join(d.Path, "link")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, e.svc.thumbDir(d.ID), map[string][]byte{"1-sm-x.jpg": []byte("thumb")})
	if err := e.svc.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatalf("delete with files: %v", err)
	}
	hidden := e.svc.recycleDir(d.ID)
	if exists(d.Path) || !exists(filepath.Join(hidden, "a.png")) || !exists(e.svc.thumbDir(d.ID)) {
		t.Errorf("folder not set aside: dataset folder %v, hidden %v", exists(d.Path), exists(hidden))
	}
	if dirs, err := e.svc.ListDirectories(ctx); err != nil || len(dirs) != 0 {
		t.Errorf("ListDirectories = %+v, %v", dirs, err)
	}
	if items := e.trashed(t); len(items) != 1 || !items[0].DeleteFiles || items[0].Path != hidden || items[0].DirectoryMissing {
		t.Errorf("recycle bin = %+v", items)
	}

	// Name and folder are free meanwhile: restoring needs a new name, which
	// the folder takes too.
	newer, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "pics"})
	if err != nil {
		t.Fatalf("the folder name of a dataset deleted with its files must be free: %v", err)
	}
	_, err = e.svc.RestoreDataset(ctx, d.ID, RestoreInput{})
	wantKind(t, err, ErrNameTaken, "已有名为“pics”的数据集")
	name := "pics-old"
	got, err := e.svc.RestoreDataset(ctx, d.ID, RestoreInput{Name: &name})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got.Name != name || got.DirName != name || !exists(filepath.Join(got.Path, "a.png")) || exists(hidden) {
		t.Errorf("restored = %+v", got)
	}
	if n, err := e.svc.GetDataset(ctx, newer.ID); err != nil || n.DirName != "pics" {
		t.Errorf("the newer dataset = %+v, %v", n, err)
	}

	// A folder of the old name appeared: restoring under the same name is
	// refused, with the reason.
	if err := e.svc.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(got.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RestoreDataset(ctx, d.ID, RestoreInput{})
	wantKind(t, err, ErrNameTaken, "文件夹“pics-old”已存在")
	if err := os.Remove(got.Path); err != nil {
		t.Fatal(err)
	}

	// The purge removes the folder and the thumbnails, never following a
	// symbolic link.
	e.expire(t, "datasets", d.ID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(hidden) || exists(e.svc.thumbDir(d.ID)) || len(e.trashed(t)) != 0 {
		t.Errorf("after purge: hidden %v, thumbnails %v", exists(hidden), exists(e.svc.thumbDir(d.ID)))
	}
	if !exists(filepath.Join(outside, "precious.png")) {
		t.Error("the purge followed a symbolic link")
	}
	if entries, _ := os.ReadDir(e.datasetsDir()); len(entries) != 1 || entries[0].Name() != "pics" {
		t.Errorf("datasets folder holds %v", entries)
	}

	// A dataset folder replaced by a symbolic link is not set aside.
	sym, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "sym"})
	if err := os.Remove(sym.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sym.Path); err != nil {
		t.Fatal(err)
	}
	wantKind(t, e.svc.DeleteDataset(ctx, sym.ID, true), ErrConflict, "符号链接")
	if _, err := e.svc.GetDataset(ctx, sym.ID); err != nil {
		t.Errorf("a refused delete must keep the dataset: %v", err)
	}
	if err := e.svc.DeleteDataset(ctx, sym.ID, false); err != nil {
		t.Errorf("delete without files: %v", err)
	}
}

// A purge that cannot remove all files leaves them to the janitor.
func TestIntegrationPurgeLeavesUndeletableFiles(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	e.svc.removeAll = func(path string) error {
		_ = os.Remove(filepath.Join(path, "a.png"))
		return &fs.PathError{Op: "unlinkat", Path: filepath.Join(path, "root-owned", "b.png"), Err: syscall.EACCES}
	}
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "leftover"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2), "root-owned/b.png": pngFile(t, 2, 2)})
	if err := e.svc.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	e.expire(t, "datasets", d.ID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.trashed(t)) != 0 {
		t.Error("dataset not purged")
	}
	trash := filepath.Join(e.datasetsDir(), deletingPrefix+strconv.FormatInt(d.ID, 10))
	if !exists(filepath.Join(trash, "root-owned", "b.png")) {
		t.Fatalf("the rest should wait in %s", trash)
	}
	e.svc.cleanTrash()
	if exists(trash) {
		t.Error("the janitor did not remove the rest")
	}
}

// What a crash between renaming a folder and the commit leaves behind is
// repaired at startup.
func TestIntegrationReconcileRecycleFolders(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	live, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "live"})
	writeFiles(t, live.Path, map[string][]byte{"a.png": nil})
	if err := os.Rename(live.Path, e.svc.recycleDir(live.ID)); err != nil { // a delete that never committed
		t.Fatal(err)
	}
	binned, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "binned"})
	if err := e.svc.DeleteDataset(ctx, binned.ID, true); err != nil {
		t.Fatal(err)
	}
	restored, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "restored"})
	writeFiles(t, restored.Path, map[string][]byte{"a.png": nil})
	if err := e.svc.DeleteDataset(ctx, restored.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE datasets SET deleted_at = NULL, delete_files = false WHERE id = $1",
		restored.ID); err != nil { // a restore that committed, its folder not moved back yet
		t.Fatal(err)
	}
	writeFiles(t, e.svc.recycleDir(999999), map[string][]byte{"a.png": nil}) // its dataset is gone
	writeFiles(t, filepath.Join(e.datasetsDir(), recyclePrefix+"x"), map[string][]byte{"a.png": nil})

	if err := e.svc.reconcileRecycleFolders(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(live.Path, "a.png")) || exists(e.svc.recycleDir(live.ID)) {
		t.Error("the folder of a dataset in use was not moved back")
	}
	if !exists(filepath.Join(restored.Path, "a.png")) || exists(e.svc.recycleDir(restored.ID)) {
		t.Error("the folder of a restored dataset was not moved back")
	}
	if !exists(e.svc.recycleDir(binned.ID)) {
		t.Error("the folder of a dataset in the bin must stay aside")
	}
	if exists(e.svc.recycleDir(999999)) || !exists(filepath.Join(e.datasetsDir(), deletingPrefix+"999999")) {
		t.Error("the folder of a gone dataset must be left to the janitor")
	}
	if !exists(filepath.Join(e.datasetsDir(), recyclePrefix+"x")) {
		t.Error("an unrelated folder was touched")
	}
}

func TestIntegrationRecycleVersion(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2)})
	if err := upsertImages(ctx, e.db.Pool, d.ID, []imageRecord{{RelPath: "a.png", Size: 10, ModTime: dbTime(time.Now()), State: annotationNone}}); err != nil {
		t.Fatal(err)
	}
	v1 := e.addVersion(t, d.ID, "第一轮", testVersionImage{"a.png", "approved", nil})
	v2 := e.addVersion(t, d.ID, "第二轮", testVersionImage{"a.png", "approved", nil})
	if err := e.svc.DeleteVersion(ctx, d.ID, v1.ID); err != nil {
		t.Fatal(err)
	}
	if vs, err := e.svc.ListVersions(ctx, d.ID); err != nil || len(vs) != 1 || vs[0].ID != v2.ID {
		t.Errorf("ListVersions = %+v, %v", vs, err)
	}
	_, err := e.svc.GetVersion(ctx, d.ID, v1.ID)
	wantKind(t, err, ErrNotFound, "版本不存在")
	items, err := e.svc.ListTrashedVersions(ctx)
	if err != nil || len(items) != 1 || items[0].ID != v1.ID || items[0].DatasetName != "ds" || items[0].DatasetDeleted ||
		items[0].PurgeAt.Sub(items[0].DeletedAt) != recycle.Retention {
		t.Fatalf("trashed versions = %+v, %v", items, err)
	}
	if got, err := e.svc.RestoreVersion(ctx, v1.ID); err != nil || got.ID != v1.ID || got.Name != v1.Name {
		t.Fatalf("restore = %+v, %v", got, err)
	}
	_, err = e.svc.RestoreVersion(ctx, v1.ID)
	wantKind(t, err, ErrNotFound, "回收站中没有这个版本")

	// A version whose dataset is in the bin too waits for the dataset, and
	// goes with it when that is purged sooner.
	if err := e.svc.DeleteVersion(ctx, d.ID, v1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteDataset(ctx, d.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE datasets SET deleted_at = now() - interval '2 days' WHERE id = $1", d.ID); err != nil {
		t.Fatal(err)
	}
	items, _ = e.svc.ListTrashedVersions(ctx)
	if len(items) != 1 || !items[0].DatasetDeleted || !items[0].PurgeAt.Before(items[0].DeletedAt.Add(recycle.Retention)) {
		t.Errorf("trashed version of a trashed dataset = %+v", items)
	}
	_, err = e.svc.RestoreVersion(ctx, v1.ID)
	wantKind(t, err, ErrConflict, "也在回收站中")
	if _, err := e.svc.RestoreDataset(ctx, d.ID, RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if vs, _ := e.svc.ListVersions(ctx, d.ID); len(vs) != 1 {
		t.Errorf("restoring the dataset must not restore the version deleted by itself: %+v", vs)
	}

	// Purged after the retention.
	e.expire(t, "dataset_versions", v1.ID)
	if err := e.svc.purgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if items, _ := e.svc.ListTrashedVersions(ctx); len(items) != 0 {
		t.Errorf("version not purged: %+v", items)
	}
	_, err = e.svc.RestoreVersion(ctx, v1.ID)
	wantKind(t, err, ErrNotFound, "回收站中没有这个版本")
	if vs, _ := e.svc.ListVersions(ctx, d.ID); len(vs) != 1 || vs[0].ID != v2.ID {
		t.Errorf("versions after the purge = %+v", vs)
	}
}

// A dataset with a review version being built cannot be deleted (the
// merge is still writing it).
func TestIntegrationRecycleRefusesBuildingVersion(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	mergeID, reviewID := e.addMerge(t, d.ID)
	if _, err := e.svc.CreateReviewVersion(ctx, ReviewVersionDraft{DatasetID: d.ID, ReviewID: reviewID, MergeID: mergeID,
		Note: "x"}); err != nil {
		t.Fatal(err)
	}
	wantKind(t, e.svc.DeleteDataset(ctx, d.ID, false), ErrConflict, "正在生成审图版本")
	if err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error {
		return e.svc.DiscardReviewVersions(ctx, tx, mergeID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteDataset(ctx, d.ID, false); err != nil {
		t.Errorf("delete after the merge: %v", err)
	}
}

// The recycle hook moves the rows of other modules aside and back in the
// dataset's transaction.
type recordingHook struct{ calls []string }

func (h *recordingHook) DetachDataset(_ context.Context, _ pgx.Tx, id int64) error {
	h.calls = append(h.calls, "detach "+strconv.FormatInt(id, 10))
	return nil
}

func (h *recordingHook) AttachDataset(_ context.Context, _ pgx.Tx, id int64) error {
	h.calls = append(h.calls, "attach "+strconv.FormatInt(id, 10))
	return nil
}

func TestIntegrationRecycleHook(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	h := &recordingHook{}
	e.svc.SetRecycleHook(h)
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	if err := e.svc.DeleteDataset(ctx, d.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RestoreDataset(ctx, d.ID, RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(d.ID, 10)
	if want := []string{"detach " + id, "attach " + id}; !slices.Equal(h.calls, want) {
		t.Errorf("hook calls = %q, want %q", h.calls, want)
	}
}
