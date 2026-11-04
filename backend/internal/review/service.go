// Package review implements model reviews (模型审图): a model of the model
// repository runs over the images of one or more datasets, and people go
// through the result image by image, correcting the boxes and marking each
// image as approved (已审核: the model got it right) or needs_sample (需加样:
// it should join later training).
//
// A review is created queued; a training node claims it through the claim
// long-poll of package model (task kind review_inference, see
// inference.go), runs the model over every image and uploads the boxes.
// The platform writes one VOC file per image into the review's own folder
// (reviews/review-<id>/, laid out by package reviewfile); those files are
// the review annotations people edit, on the web or in the local reviewer
// (packages and results), and they reach the datasets only when somebody
// merges them. The images themselves stay in the datasets.
//
// The per-image locks, the busy paths and the claim notifications live in
// this process, so the platform supports a single instance only (like the
// model repository).
package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Options are the dependencies of a Service.
type Options struct {
	DB       *pgxpool.Pool
	Storage  *storage.Storage
	Logger   *slog.Logger
	Datasets *dataset.Service
	Models   *model.Service
	Projects *project.Service
	// Now returns the current time; nil means time.Now. It only drives
	// in-memory bookkeeping; the database stamps rows with its own clock.
	Now func() time.Time
	// DiskUsage reports the free space of the storage directory; nil means
	// Storage.Usage. Tests simulate a full disk.
	DiskUsage func() (storage.Usage, error)
}

// Service implements the review operations used by the HTTP handlers.
// NewService registers it with the model service as the source of
// review_inference tasks; Start runs the background work.
type Service struct {
	db       *pgxpool.Pool
	storage  *storage.Storage
	log      *slog.Logger
	datasets *dataset.Service
	models   *model.Service
	projects *project.Service
	clock    func() time.Time
	usage    func() (storage.Usage, error)

	// imageLocks serializes the changes of one review image (by review
	// image ID): annotation saves, status changes, applying uploaded
	// results and merge reads.
	imageLocks keyedMutex
	// busy holds paths in use that the janitor must not remove: review
	// folders being created, folders being deleted, staged uploads.
	busy pathSet
	// packages holds the prepared package downloads.
	packages tickets[packageRequest]

	// base ends with the service: work that outlives its request (a
	// delete, applying results, a merge) is canceled only then.
	base  context.Context
	stop  context.CancelFunc
	loops sync.WaitGroup // the background loop
	jobs  sync.WaitGroup // background jobs (merges); Stop waits for them

	removeAll func(path string) error // os.RemoveAll; tests make it fail
	// beforeMergeSave, if set, runs before a merge writes an image's
	// annotation into its dataset; tests hold a merge running or change
	// the dataset file meanwhile with it.
	beforeMergeSave func(mergeID, reviewImageID int64)
}

// NewService creates a Service and registers it with o.Models as the
// source of review_inference tasks (before the model service starts). It
// does not touch the database yet.
func NewService(o Options) *Service {
	s := &Service{
		db:        o.DB,
		storage:   o.Storage,
		log:       o.Logger,
		datasets:  o.Datasets,
		models:    o.Models,
		projects:  o.Projects,
		clock:     o.Now,
		usage:     o.DiskUsage,
		base:      context.Background(),
		removeAll: os.RemoveAll,
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.usage == nil {
		s.usage = o.Storage.Usage
	}
	if s.models != nil {
		s.models.AddTaskSource(s)
	}
	return s
}

// Intervals of the background work.
const (
	leaseCheckInterval = 15 * time.Second
	cleanupInterval    = time.Hour
	// staleUploadAge: staged result uploads older than this that nothing
	// uses are removed.
	staleUploadAge = time.Hour
)

const msgMergeInterrupted = "服务重启，合并被中断（已合并的图片保留，可以再次合并）"

// Start marks the merges a previous run left running as failed, then runs
// the background work until ctx ends or Stop is called: every 15 seconds,
// reviews whose node stopped reporting are queued again (or fail); at
// start and every hour, leftovers of deletes and stale temporary files are
// removed.
func (s *Service) Start(ctx context.Context) error {
	tag, err := s.db.Exec(ctx, `UPDATE review_merges SET status = 'failed', error = $1, finished_at = now()
		WHERE status = 'running'`, msgMergeInterrupted)
	if err != nil {
		return fmt.Errorf("fail interrupted review merges: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		s.log.Warn("review merges interrupted by a restart marked failed", "count", n)
	}
	s.base, s.stop = context.WithCancel(ctx)
	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		s.backgroundLoop(s.base)
	}()
	return nil
}

// Stop ends the background work and background jobs and waits for them.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.loops.Wait()
	s.jobs.Wait()
}

// goJob runs fn in the background with the service's context; Stop waits
// for it.
func (s *Service) goJob(fn func(ctx context.Context)) {
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		fn(s.base)
	}()
}

func (s *Service) backgroundLoop(ctx context.Context) {
	leases := time.NewTicker(leaseCheckInterval)
	defer leases.Stop()
	cleanup := time.NewTicker(cleanupInterval)
	defer cleanup.Stop()
	s.cleanup(ctx)
	for {
		if err := s.expireLeases(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("requeue reviews with expired leases", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-leases.C:
		case <-cleanup.C:
			s.cleanup(ctx)
		}
	}
}

// detach returns a context for work that must not stop when the client
// goes away (it would leave files half written), only when the service
// stops.
func (s *Service) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(s.base, cancel)
	return ctx, func() { stop(); cancel() }
}

// ---- paths ----

const (
	reviewDirPrefix = "review-" // reviews/review-<id>
	// deletingPrefix marks folders in reviews/ of deleted reviews that are
	// being removed (and leftovers moved aside). Being hidden, they are no
	// review's folder.
	deletingPrefix = ".deleting-"
)

func (s *Service) reviewsDir() string { return s.storage.Dir(storage.Reviews) }

// reviewDirName is the folder of a review in reviews/.
func reviewDirName(id int64) string { return reviewDirPrefix + strconv.FormatInt(id, 10) }

func (s *Service) reviewDir(id int64) string { return filepath.Join(s.reviewsDir(), reviewDirName(id)) }

// uploadsTempDir holds result uploads being staged
// (tmp/review-uploads/); the janitor removes files there that are older
// than an hour and not in s.busy.
func (s *Service) uploadsTempDir() string {
	return filepath.Join(s.storage.Dir(storage.Tmp), "review-uploads")
}

// openReviewRoot opens the folder of a review; with create it is created
// when missing. Callers hold a lock on the review's row (FOR KEY SHARE or
// stronger), so the review cannot be deleted meanwhile, which would move
// the folder away.
func (s *Service) openReviewRoot(id int64, create bool) (*os.Root, error) {
	dir := s.reviewDir(id)
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create review folder: %w", err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open review folder: %w", err)
	}
	return root, nil
}

// ---- cleanup ----

// cleanup removes what deletes and crashes left behind: folders of
// deleted reviews (.deleting-*), folders of reviews that do not exist
// (a creation interrupted by a crash) and stale staged uploads.
func (s *Service) cleanup(ctx context.Context) {
	if err := s.cleanReviewFolders(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("clean review folders", "err", err)
	}
	s.cleanUploadTemp()
}

func (s *Service) cleanReviewFolders(ctx context.Context) error {
	// List the folders before reading the reviews: a folder is created
	// before its row commits, but marked busy until then.
	entries, err := os.ReadDir(s.reviewsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM reviews")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	exists := make(map[int64]bool, len(ids))
	for _, id := range ids {
		exists[id] = true
	}
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(s.reviewsDir(), name)
		var stale bool
		switch {
		case strings.HasPrefix(name, deletingPrefix):
			stale = true
		case strings.HasPrefix(name, reviewDirPrefix):
			id, err := strconv.ParseInt(strings.TrimPrefix(name, reviewDirPrefix), 10, 64)
			stale = err == nil && strconv.FormatInt(id, 10) == strings.TrimPrefix(name, reviewDirPrefix) && !exists[id]
		}
		if !stale || s.busy.has(p) {
			continue
		}
		if err := s.removeAll(p); err != nil {
			s.log.Warn("remove leftover review folder", "path", p, "err", err)
		}
	}
	return nil
}

func (s *Service) cleanUploadTemp() {
	dir := s.uploadsTempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || s.busy.has(p) || s.clock().Sub(info.ModTime()) < staleUploadAge {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			s.log.Warn("remove stale review upload", "path", p, "err", err)
		}
	}
}

// ---- disk space ----

// reserveBytes is the free disk space uploads always leave.
const reserveBytes = 512 << 20

// checkFreeSpace fails with ErrInsufficientStorage unless need bytes plus
// the reserve are available. Platforms without disk usage support skip it.
func (s *Service) checkFreeSpace(need int64) error {
	u, err := s.usage()
	if err != nil {
		if !errors.Is(err, storage.ErrUsageUnsupported) {
			s.log.Warn("check free disk space", "err", err)
		}
		return nil
	}
	want := uint64(need) + reserveBytes
	if u.FreeBytes < want {
		return newError(ErrInsufficientStorage, "磁盘剩余空间不足：可用 %s，至少需要 %s",
			formatBytes(u.FreeBytes), formatBytes(want))
	}
	return nil
}

// formatBytes renders a byte count for messages, e.g. "1.5 GB".
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, i := float64(n)/unit, 0
	for value >= unit && i < 4 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"KB", "MB", "GB", "TB", "PB"}[i])
}

// ---- in-memory helpers ----

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// keyedMutex is a set of mutexes identified by ID whose Lock can be
// abandoned when the request ends.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[int64]*keyedLock
}

type keyedLock struct {
	token chan struct{} // holds a value while locked
	refs  int
}

func (k *keyedMutex) lock(ctx context.Context, key int64) (unlock func(), err error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[int64]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{token: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	select {
	case l.token <- struct{}{}:
		return func() { <-l.token; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// lockImage takes the lock of a review image (see Service.imageLocks).
func (s *Service) lockImage(ctx context.Context, reviewImageID int64) (unlock func(), err error) {
	return s.imageLocks.lock(ctx, reviewImageID)
}

// pathSet is a set of paths in use.
type pathSet struct {
	mu    sync.Mutex
	paths map[string]int
}

func (p *pathSet) add(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paths == nil {
		p.paths = map[string]int{}
	}
	p.paths[path]++
}

func (p *pathSet) remove(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paths[path]--; p.paths[path] <= 0 {
		delete(p.paths, path)
	}
}

func (p *pathSet) has(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paths[path] > 0
}
