// Package dataset implements dataset management: datasets are folders of
// images (with Pascal VOC annotations next to them) under the storage
// directory datasets/, indexed in PostgreSQL. It covers creating and
// registering datasets, uploading archives and folders (with VOC,
// X-AnyLabeling or YOLO annotations, stored as VOC), background import and
// scan jobs, image browsing, thumbnails and downloads in those formats.
package dataset

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Options are the dependencies of a Service.
type Options struct {
	DB      *pgxpool.Pool
	Storage *storage.Storage
	Logger  *slog.Logger
	// Workers is the number of jobs processed in parallel (default 2).
	Workers int
}

// Service implements the dataset operations used by the HTTP handlers.
// Start must be called before jobs are processed.
type Service struct {
	db      *pgxpool.Pool
	storage *storage.Storage
	log     *slog.Logger
	runner  *runner
	thumbs  *thumbnailer
	uploads keyedMutex // serializes writes to one import's staging data
	exports exportTickets

	// annotationLocks serializes the annotation edits of an image (by ID).
	annotationLocks keyedMutex
	// recycleLocks serializes moving a dataset to the recycle bin and back
	// (by ID), with the folder that moves after the restore's commit.
	recycleLocks keyedMutex

	recycleHook RecycleHook // SetRecycleHook

	// base is the context jobs index with after a user cancel; only
	// stopping the service cancels it.
	base    context.Context
	stop    context.CancelFunc
	janitor sync.WaitGroup

	maxExtract atomic.Int64            // tests: replaces the extraction budget when > 0
	removeAll  func(path string) error // os.RemoveAll; tests make it fail
}

// NewService creates a Service. It does not touch the database yet.
func NewService(o Options) *Service {
	s := &Service{
		db:        o.DB,
		storage:   o.Storage,
		log:       o.Logger,
		thumbs:    newThumbnailer(),
		base:      context.Background(),
		removeAll: os.RemoveAll,
	}
	workers := o.Workers
	if workers <= 0 {
		workers = 2
	}
	s.runner = newRunner(s, workers)
	return s
}

// Start recovers jobs interrupted by a previous shutdown and brings the
// image sort keys up to date, then starts the job workers and the janitor.
// They run until ctx ends or Stop is called.
func (s *Service) Start(ctx context.Context) error {
	if err := s.recoverJobs(ctx); err != nil {
		return fmt.Errorf("recover dataset jobs: %w", err)
	}
	if err := s.reconcileRecycleFolders(ctx); err != nil {
		s.log.Error("check the folders of the recycle bin", "err", err)
	}
	if err := s.refreshSortKeys(ctx); err != nil {
		return fmt.Errorf("update image sort keys: %w", err)
	}
	s.base, s.stop = context.WithCancel(ctx)
	s.runner.start(s.base)
	s.janitor.Add(1)
	go func() {
		defer s.janitor.Done()
		s.janitorLoop(s.base)
	}()
	return nil
}

// Stop cancels running jobs (they end as failed and can be re-imported or
// rescanned) and waits for the workers to exit.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.runner.shutdown()
	s.janitor.Wait()
}

// ---- paths ----

func (s *Service) datasetsDir() string { return s.storage.Dir(storage.Datasets) }

// datasetDir returns the folder of a dataset. Dir names are validated
// before they are stored (validDirName), so the result stays inside
// datasets/.
func (s *Service) datasetDir(dirName string) string { return filepath.Join(s.datasetsDir(), dirName) }

func (s *Service) stagingRoot() string { return filepath.Join(s.storage.Dir(storage.Tmp), "imports") }

// stagingDir holds the uploaded data of one import job: upload.bin for
// archives, files/ for staged entries, parts/ for folder uploads in flight.
func (s *Service) stagingDir(importID int64) string {
	return filepath.Join(s.stagingRoot(), strconv.FormatInt(importID, 10))
}

func (s *Service) removeStaging(importID int64) {
	if err := os.RemoveAll(s.stagingDir(importID)); err != nil {
		s.log.Warn("remove import staging folder", "import_id", importID, "err", err)
	}
}

// cleanStaging removes staging folders that belong to no active job.
func (s *Service) cleanStaging(ctx context.Context) error {
	// List the folders before reading the jobs: a folder is created only
	// after its job row is committed, so every folder seen here belongs
	// to a job the query below can see.
	entries, err := os.ReadDir(s.stagingRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM dataset_imports WHERE status IN ('uploading', 'queued', 'running')")
	if err != nil {
		return err
	}
	active, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err == nil && slices.Contains(active, id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.stagingRoot(), e.Name())); err != nil {
			s.log.Warn("remove stale import staging folder", "name", e.Name(), "err", err)
		}
	}
	return nil
}

// dirExists reports whether a dataset folder exists as a real directory.
func (s *Service) dirExists(dirName string) bool {
	fi, err := os.Lstat(s.datasetDir(dirName))
	return err == nil && fi.IsDir()
}

// ---- datasets ----

// ListDatasets returns the datasets selected by q, most recently updated
// first.
func (s *Service) ListDatasets(ctx context.Context, q DatasetQuery) ([]Dataset, error) {
	q.Search = strings.TrimSpace(q.Search)
	ds, err := queryDatasets(ctx, s.db, q)
	if err != nil {
		return nil, err
	}
	return ds, s.decorate(ctx, ds)
}

// GetDataset returns one dataset.
func (s *Service) GetDataset(ctx context.Context, id int64) (Dataset, error) {
	d, err := queryDataset(ctx, s.db, id, false)
	if err != nil {
		return d, err
	}
	ds := []Dataset{d}
	err = s.decorate(ctx, ds)
	return ds[0], err
}

// decorate fills the fields that do not come from the datasets table.
func (s *Service) decorate(ctx context.Context, ds []Dataset) error {
	if len(ds) == 0 {
		return nil
	}
	ids := make([]int64, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	covers, err := queryCovers(ctx, s.db, ids)
	if err != nil {
		return fmt.Errorf("load dataset covers: %w", err)
	}
	active, err := queryActiveImports(ctx, s.db, ids)
	if err != nil {
		return fmt.Errorf("load active imports: %w", err)
	}
	projects, err := queryDatasetProjects(ctx, s.db, ids)
	if err != nil {
		return fmt.Errorf("load dataset projects: %w", err)
	}
	versions, err := queryVersionCounts(ctx, s.db, ids)
	if err != nil {
		return fmt.Errorf("load dataset versions: %w", err)
	}
	for i := range ds {
		d := &ds[i]
		d.Path = s.datasetDir(d.DirName)
		d.DirectoryMissing = !s.dirExists(d.DirName)
		if c := covers[d.ID]; c != nil {
			d.Covers = c
		}
		if p := projects[d.ID]; p != nil {
			d.Projects = p
		}
		d.ActiveImport = active[d.ID]
		d.VersionCount = versions[d.ID]
	}
	return nil
}

// CreateDatasetInput are the fields of a new dataset.
type CreateDatasetInput struct {
	Name        string
	Description string
	// Directory, when set, registers this existing direct child folder of
	// datasets/ instead of creating a new folder named after the dataset.
	Directory *string
}

// CreateDataset creates a dataset. Without Directory it creates the folder
// datasets/<name>; with Directory it registers that existing folder and
// queues a scan job that indexes it.
func (s *Service) CreateDataset(ctx context.Context, in CreateDatasetInput) (Dataset, error) {
	name, err := NormalizeName(in.Name)
	if err != nil {
		return Dataset{}, err
	}
	desc, err := NormalizeDescription(in.Description)
	if err != nil {
		return Dataset{}, err
	}
	var id int64
	if in.Directory != nil {
		id, err = s.registerDirectory(ctx, name, desc, *in.Directory)
	} else {
		id, err = s.createFolderDataset(ctx, name, desc)
	}
	if err != nil {
		return Dataset{}, err
	}
	return s.GetDataset(ctx, id)
}

func (s *Service) createFolderDataset(ctx context.Context, name, desc string) (int64, error) {
	var id int64
	var created string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := checkNameFree(ctx, tx, name, 0); err != nil {
			return err
		}
		if owner, err := datasetUsingDir(ctx, tx, name, 0); err != nil {
			return err
		} else if owner != nil {
			return newError(ErrConflict, "文件夹“%s”已被数据集%s使用", name, owner)
		}
		// Compare ignoring case so the data directory stays usable on
		// case-insensitive file systems (Windows, macOS).
		if existing, err := s.findDirEntry(name); err != nil {
			return err
		} else if existing != "" {
			return folderExists(existing)
		}
		d, err := insertDataset(ctx, tx, name, name, desc, OriginUpload)
		if err != nil {
			return err
		}
		dir := s.datasetDir(name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return folderExists(name)
			}
			return fmt.Errorf("create dataset folder: %w", err)
		}
		id, created = d.ID, dir
		return nil
	})
	if err != nil && created != "" {
		_ = os.Remove(created) // the commit failed; the folder is still empty
	}
	return id, err
}

func folderExists(name string) error {
	return newError(ErrConflict, "数据目录中已存在同名文件夹“%s”，可通过“选择已有目录”直接添加为数据集", name)
}

// findDirEntry returns the name of the entry in datasets/ that equals name
// ignoring case, or "".
func (s *Service) findDirEntry(name string) (string, error) {
	entries, err := os.ReadDir(s.datasetsDir())
	if err != nil {
		return "", fmt.Errorf("read datasets folder: %w", err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			return e.Name(), nil
		}
	}
	return "", nil
}

func (s *Service) registerDirectory(ctx context.Context, name, desc, dirName string) (int64, error) {
	if dirName == "" {
		return 0, &Error{Kind: ErrInvalid, Message: "请选择要添加的目录"}
	}
	notFound := newError(ErrNotFound, "目录不存在：%s", dirName)
	if !validDirName(dirName) {
		return 0, notFound
	}
	fi, err := os.Lstat(s.datasetDir(dirName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, notFound
	case err != nil:
		return 0, fmt.Errorf("stat dataset folder: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return 0, newError(ErrNotFound, "“%s”是符号链接，不能作为数据集目录", dirName)
	case !fi.IsDir():
		return 0, newError(ErrNotFound, "“%s”不是文件夹", dirName)
	}

	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := checkNameFree(ctx, tx, name, 0); err != nil {
			return err
		}
		if owner, err := datasetUsingDir(ctx, tx, dirName, 0); err != nil {
			return err
		} else if owner != nil {
			return newError(ErrConflict, "目录“%s”已添加为数据集%s", dirName, owner)
		}
		d, err := insertDataset(ctx, tx, name, dirName, desc, OriginDirectory)
		if err != nil {
			return err
		}
		if _, err := insertImport(ctx, tx, newImport{DatasetID: d.ID, Source: SourceScan, Status: StatusQueued,
			OnConflict: ConflictSkip, FileName: dirName}); err != nil {
			return err
		}
		id = d.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.runner.notify()
	return id, nil
}

// UpdateDatasetInput holds the fields to change; nil leaves a field alone.
type UpdateDatasetInput struct {
	Name        *string
	Description *string
	Status      *string // a DatasetStatus*; DatasetStatusNone clears it
}

// UpdateDataset renames a dataset, changes its description or its status.
// The folder keeps its name. Changing only the status leaves updated_at
// alone (the dataset list is ordered by it) and sets status_updated_at
// when the status is a different one.
func (s *Service) UpdateDataset(ctx context.Context, id int64, in UpdateDatasetInput) (Dataset, error) {
	var status string
	if in.Status != nil {
		var err error
		if status, err = NormalizeDatasetStatus(*in.Status); err != nil {
			return Dataset{}, err
		}
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		d, err := queryDataset(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if in.Status != nil && status != d.Status {
			if _, err := tx.Exec(ctx, "UPDATE datasets SET status = $2, status_updated_at = now() WHERE id = $1",
				id, status); err != nil {
				return err
			}
		}
		if in.Status != nil && in.Name == nil && in.Description == nil {
			return nil
		}
		name, desc := d.Name, d.Description
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
		_, err = tx.Exec(ctx, "UPDATE datasets SET name = $2, description = $3, updated_at = now() WHERE id = $1",
			id, name, desc)
		return uniqueViolation(err, d.DirName)
	})
	if err != nil {
		return Dataset{}, err
	}
	return s.GetDataset(ctx, id)
}

// deletingPrefix marks folders of purged datasets that are being removed.
// Being hidden, they never show up as directories.
const deletingPrefix = ".deleting-"

// cleanTrash removes folders left behind by deletes that failed midway.
func (s *Service) cleanTrash() {
	entries, err := os.ReadDir(s.datasetsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), deletingPrefix) {
			if err := os.RemoveAll(filepath.Join(s.datasetsDir(), e.Name())); err != nil {
				s.log.Warn("remove deleted dataset folder", "name", e.Name(), "err", err)
			}
		}
	}
}

// janitorLoop runs the periodic cleanup until ctx ends.
func (s *Service) janitorLoop(ctx context.Context) {
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		if err := s.expireUploads(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("expire stale dataset uploads", "err", err)
		}
		if err := s.cleanStaging(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("clean dataset import staging", "err", err)
		}
		if err := s.purgeExpired(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("purge datasets from the recycle bin", "err", err)
		}
		s.cleanTrash()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- directories ----

// Bounds of the file count shown for each folder in ListDirectories.
const (
	dirCountMaxEntries = 50_000
	dirCountTimeout    = 2 * time.Second
	dirListTimeout     = 5 * time.Second
	dirCountParallel   = 4
)

// ListDirectories lists the direct child folders of datasets/ (hidden
// folders and symbolic links excluded) with a bounded count of their
// images; unregistered folders first, then by name. Folders whose name is
// not valid UTF-8 are left out (and logged): JSON would turn the name into
// replacement characters, so they could not be registered anyway.
func (s *Service) ListDirectories(ctx context.Context) ([]Directory, error) {
	entries, err := os.ReadDir(s.datasetsDir())
	if err != nil {
		return nil, fmt.Errorf("read datasets folder: %w", err)
	}
	// The folders of the datasets in use and of those in the recycle bin
	// that kept their folder in place (deleted without their files).
	rows, err := s.db.Query(ctx, `SELECT dir_name, id, deleted_at IS NOT NULL FROM datasets
		WHERE deleted_at IS NULL OR NOT delete_files`)
	if err != nil {
		return nil, err
	}
	type owner struct {
		id      int64
		deleted bool
	}
	registered := map[string]owner{}
	var dirName string
	var o owner
	if _, err := pgx.ForEachRow(rows, []any{&dirName, &o.id, &o.deleted}, func() error {
		registered[dirName] = o
		return nil
	}); err != nil {
		return nil, err
	}

	var out []Directory
	for _, e := range entries {
		// ReadDir reports the type without following symbolic links.
		if !e.IsDir() || isJunkSegment(e.Name()) {
			continue
		}
		if !utf8.ValidString(e.Name()) {
			s.log.Warn("not listing dataset folder with a name that is not valid UTF-8", "name", strconv.Quote(e.Name()))
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}
		d := Directory{Name: e.Name(), Path: s.datasetDir(e.Name()), ModifiedAt: info.ModTime().UTC()}
		if o, ok := registered[e.Name()]; ok {
			d.Registered, d.DatasetID, d.InRecycleBin = true, &o.id, o.deleted
		}
		out = append(out, d)
	}

	listDeadline := time.Now().Add(dirListTimeout)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(dirCountParallel)
	for i := range out {
		g.Go(func() error {
			deadline := time.Now().Add(dirCountTimeout)
			if listDeadline.Before(deadline) {
				deadline = listDeadline
			}
			d := &out[i]
			d.ImageCount, d.AnnotationCount, d.TotalBytes, d.Approximate = countFiles(gctx, d.Path, deadline)
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Directory) int {
		if a.Registered != b.Registered {
			if a.Registered {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

var errStopWalk = errors.New("stop walk")

// countFiles counts images and XML files below dir, stopping early (and
// reporting approximate) after dirCountMaxEntries entries or at deadline.
// Like a scan, it skips names that are not valid UTF-8.
func countFiles(ctx context.Context, dir string, deadline time.Time) (images, xmls int, bytes int64, approximate bool) {
	entries := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil // unreadable parts are skipped (WalkDir then skips the folder)
		}
		entries++
		if entries > dirCountMaxEntries || ctx.Err() != nil || (entries%64 == 0 && time.Now().After(deadline)) {
			approximate = true
			return errStopWalk
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if isJunkSegment(d.Name()) || !utf8.ValidString(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !utf8.ValidString(d.Name()) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		switch classify(filepath.ToSlash(rel)) {
		case kindImage:
			images++
			if info, err := d.Info(); err == nil {
				bytes += info.Size()
			}
		case kindAnnotation:
			xmls++
		}
		return nil
	})
	return images, xmls, bytes, approximate
}

// ---- images and labels ----

// ListImages returns a page of a dataset's images and the number of images
// matching the filters.
func (s *Service) ListImages(ctx context.Context, datasetID int64, q ImageQuery) ([]Image, int, error) {
	if _, err := queryDataset(ctx, s.db, datasetID, false); err != nil {
		return nil, 0, err
	}
	return queryImages(ctx, s.db, datasetID, q)
}

// ImageNeighbors returns the images before and after an image among those
// matching the filters of q, in image order, and its position there. The
// annotation workspace steps through a dataset with it.
func (s *Service) ImageNeighbors(ctx context.Context, datasetID, imageID int64, q ImageQuery) (ImageNeighbors, error) {
	if _, err := queryDataset(ctx, s.db, datasetID, false); err != nil {
		return ImageNeighbors{}, err
	}
	return queryNeighbors(ctx, s.db, datasetID, imageID, q)
}

// GetImage returns an image with the objects parsed from its VOC file as it
// is on disk now. The VOC file is the one the index paired with the image,
// unless a file next to the image now takes over from a paired file
// elsewhere; without one, it is the file a scan would pair with it now, if
// there is one (see resolveAnnotationTarget). Other files added or removed
// since the last scan are noticed only by a scan.
func (s *Service) GetImage(ctx context.Context, datasetID, imageID int64) (ImageDetail, error) {
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return ImageDetail{}, err
	}
	img, err := queryImage(ctx, s.db, datasetID, imageID)
	if err != nil {
		return ImageDetail{}, err
	}
	return s.imageDetail(ctx, d, img)
}

// annotationErrorMessage explains why an existing VOC file cannot be used.
// Details from the XML parser stay in English after the Chinese summary.
func annotationErrorMessage(err error) string {
	var pe *fs.PathError
	var se *xml.SyntaxError
	switch {
	case errors.Is(err, errNotVOC):
		return "标注文件不是 Pascal VOC 格式（根元素不是 <annotation>）"
	case errors.As(err, &pe):
		return "标注文件无法读取：" + pe.Err.Error()
	case errors.As(err, &se):
		return "标注文件无法解析（XML 格式错误）：" + se.Error()
	}
	return "标注文件无法解析：" + strings.TrimPrefix(err.Error(), "parse annotation: ")
}

// ListLabels returns the labels of a dataset by object count.
func (s *Service) ListLabels(ctx context.Context, datasetID int64) ([]Label, error) {
	if _, err := queryDataset(ctx, s.db, datasetID, false); err != nil {
		return nil, err
	}
	return queryLabels(ctx, s.db, datasetID)
}

// ImageFile is an open image or thumbnail to send to a client. The caller
// closes File.
type ImageFile struct {
	File        *os.File
	Name        string // file name for Content-Disposition
	ContentType string
	ModTime     time.Time
	// Version is the current version of the source image; it equals the
	// version in the index unless the file changed since the last scan.
	Version string
}

// OpenImage opens the original file of an image. The file is opened
// through an os.Root of the dataset folder, so symbolic links cannot lead
// outside of it.
func (s *Service) OpenImage(ctx context.Context, datasetID, imageID int64) (*ImageFile, error) {
	img, root, err := s.imageRoot(ctx, datasetID, imageID)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(img.Path))
	if err != nil {
		return nil, imageFileError(err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, imageFileError(fs.ErrNotExist)
	}
	return &ImageFile{
		File:        f,
		Name:        img.FileName,
		ContentType: imageContentType(img.Path),
		ModTime:     fi.ModTime(),
		Version:     fileVersion(fi.Size(), dbTime(fi.ModTime())),
	}, nil
}

// imageRoot loads an image row and opens the folder of its dataset.
func (s *Service) imageRoot(ctx context.Context, datasetID, imageID int64) (Image, *os.Root, error) {
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return Image{}, nil, err
	}
	img, err := queryImage(ctx, s.db, datasetID, imageID)
	if err != nil {
		return img, nil, err
	}
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return img, nil, err
	}
	return img, root, nil
}

func imageFileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Kind: ErrNotFound, Message: "图片文件不存在，可能已被移动或删除（可重新扫描数据集）"}
	}
	return fmt.Errorf("open image: %w", err)
}

func imageContentType(rel string) string {
	if ct := imageContentTypes[strings.ToLower(filepath.Ext(rel))]; ct != "" {
		return ct
	}
	return "application/octet-stream"
}
