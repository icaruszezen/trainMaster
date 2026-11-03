// Package model implements the model repository: Ultralytics weights
// files (.pt) bound to a project and numbered by version within it, stored
// as plain files under the storage directory models/ with their metadata
// in PostgreSQL. Users upload them in chunks (training jobs will register
// theirs with RegisterTrainedModel); the platform reads the metadata from
// the file itself (package checkpoint, no Python needed). Conversions to
// ONNX are queued in the database and executed by training nodes, which
// claim them over the agent API, download the weights, convert them and
// upload the result; finished conversions are cached in the model folder.
//
// The claim notifications, the list of nodes working on conversions and
// the per-upload locks live in this process, so the platform supports a
// single instance only (like the dataset import runner).
package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Options are the dependencies of a Service.
type Options struct {
	DB      *pgxpool.Pool
	Storage *storage.Storage
	Logger  *slog.Logger
	// Now returns the current time; nil means time.Now. It only drives the
	// in-memory bookkeeping (which nodes work on conversions); leases use
	// the database clock.
	Now func() time.Time
	// Inspect reads the metadata of a weights file; nil means
	// checkpoint.Inspect. Tests inject fixed results.
	Inspect func(r io.ReaderAt, size int64) (*checkpoint.Info, error)
	// DiskUsage reports the free space of the storage directory; nil means
	// Storage.Usage. Tests simulate a full disk.
	DiskUsage func() (storage.Usage, error)
}

// Service implements the model repository operations used by the HTTP
// handlers. Start runs the background work (lease expiry, cleanup).
type Service struct {
	db      *pgxpool.Pool
	storage *storage.Storage
	log     *slog.Logger
	clock   func() time.Time
	inspect func(r io.ReaderAt, size int64) (*checkpoint.Info, error)
	usage   func() (storage.Usage, error)
	// inspectSlots bounds the weights files inspected at once (inspectFile).
	inspectSlots chan struct{}

	uploads keyedMutex // serializes the writes to one upload's staged file (chunks, commit)
	outputs keyedMutex // serializes the result uploads of one export, its completion and cleanup
	waiters notifier   // wakes long-polling claims when a conversion is queued
	workers workerSet  // nodes that recently claimed or worked on conversions
	busy    pathSet    // temporary paths in use that the janitor must not remove

	// base ends with the service: long-polling claims return, and work that
	// outlives its request (a commit, a completion) is canceled.
	base    context.Context
	stop    context.CancelFunc
	loops   sync.WaitGroup
	closing chan struct{} // closed by StopLongPolls
	closeMu sync.Once

	removeAll func(path string) error // os.RemoveAll; tests make it fail
}

// NewService creates a Service. It does not touch the database yet.
func NewService(o Options) *Service {
	s := &Service{
		db:           o.DB,
		storage:      o.Storage,
		log:          o.Logger,
		clock:        o.Now,
		inspect:      o.Inspect,
		usage:        o.DiskUsage,
		inspectSlots: make(chan struct{}, maxInspections),
		base:         context.Background(),
		closing:      make(chan struct{}),
		removeAll:    os.RemoveAll,
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.inspect == nil {
		s.inspect = checkpoint.Inspect
	}
	if s.usage == nil {
		s.usage = o.Storage.Usage
	}
	return s
}

// maxInspections bounds the weights files inspected at once: reading one
// takes up to about a second of CPU and a few hundred MB of memory, and
// commits run until they finish even when their client goes away.
const maxInspections = 2

// Intervals of the background work.
const (
	leaseCheckInterval = 15 * time.Second
	cleanupInterval    = time.Hour
	// uploadIdleTimeout: uploads nobody touched for this long are canceled.
	uploadIdleTimeout = 24 * time.Hour
	msgUploadExpired  = "上传超时未完成，已自动取消"
)

// Start runs the background work until ctx ends or Stop is called: every
// 15 seconds, conversions whose node stopped reporting are queued again
// (or fail); at start and every hour, stale uploads are canceled and
// orphaned temporary files removed. Long-polling claims return when ctx
// ends.
func (s *Service) Start(ctx context.Context) {
	s.base, s.stop = context.WithCancel(ctx)
	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		s.backgroundLoop(s.base)
	}()
}

// Stop ends the background work and the long-polling claims, and waits
// for the background loop to exit.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.StopLongPolls()
	s.loops.Wait()
}

// StopLongPolls makes waiting claims return at once (without a task) and
// later ones return without waiting. The HTTP server calls it when it
// starts shutting down (http.Server.RegisterOnShutdown), so that the
// shutdown does not wait for the long polls.
func (s *Service) StopLongPolls() {
	s.closeMu.Do(func() { close(s.closing) })
}

func (s *Service) backgroundLoop(ctx context.Context) {
	leases := time.NewTicker(leaseCheckInterval)
	defer leases.Stop()
	cleanup := time.NewTicker(cleanupInterval)
	defer cleanup.Stop()
	s.cleanup(ctx)
	for {
		if err := s.expireLeases(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("requeue model exports with expired leases", "err", err)
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

// cleanup cancels stale uploads, clears the model folders of additions
// interrupted before their commit and removes temporary files that belong
// to nothing in progress.
func (s *Service) cleanup(ctx context.Context) {
	// Before uploads expire: a recovered file goes back to its upload.
	s.recoverPendingFolders(ctx)
	if err := s.expireUploads(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("expire stale model uploads", "err", err)
	}
	if err := s.cleanUploadTemp(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("clean model upload temporary files", "err", err)
	}
	if err := s.cleanExportTemp(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("clean model export temporary files", "err", err)
	}
	s.cleanUnused(s.registrationsRoot(), "")
	s.cleanUnused(s.modelsDir(), deletingPrefix)
}

// detach returns a context for work that must not stop when the client
// goes away (it would leave files half moved), only when the service
// stops.
func (s *Service) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(s.base, cancel)
	return ctx, func() { stop(); cancel() }
}

// ---- paths ----

// File names inside the folders below.
const (
	modelFile      = "model.pt"   // the weights as uploaded, in the model folder
	sidecarFile    = "model.json" // describes the model, in the model folder
	exportsSubdir  = "onnx"       // the cached conversions, in the model folder
	uploadFileName = "upload.pt"  // the file being uploaded, in its upload folder
	outputFileName = "output.onnx.part"
	// deletingPrefix marks folders in models/ of deleted models that are
	// being removed. Being hidden, they are no model's folder.
	deletingPrefix = ".deleting-"
	// pendingFile marks a model folder whose addModel has not committed
	// yet (it names the upload; see recoverPending).
	pendingFile = ".pending"
)

func (s *Service) modelsDir() string { return s.storage.Dir(storage.Models) }

// modelDir returns the folder of a model (dir_name is relative to models/
// and generated by the service, e.g. project-12/v3).
func (s *Service) modelDir(dirName string) (string, error) {
	return s.storage.Resolve(storage.Models, dirName)
}

func modelDirName(projectID int64, version int) string {
	return fmt.Sprintf("project-%d/v%d", projectID, version)
}

func exportRelPath(exportID int64) string {
	return exportsSubdir + "/" + strconv.FormatInt(exportID, 10) + ".onnx"
}

func (s *Service) tmpDir(name string) string { return filepath.Join(s.storage.Dir(storage.Tmp), name) }

// uploadsRoot holds one folder per upload: tmp/model-uploads/<id>/upload.pt.
func (s *Service) uploadsRoot() string { return s.tmpDir("model-uploads") }

func (s *Service) uploadDir(id int64) string {
	return filepath.Join(s.uploadsRoot(), strconv.FormatInt(id, 10))
}

func (s *Service) uploadPath(id int64) string { return filepath.Join(s.uploadDir(id), uploadFileName) }

// exportsRoot holds the results nodes are uploading:
// tmp/model-exports/<id>/output.onnx.part.
func (s *Service) exportsRoot() string { return s.tmpDir("model-exports") }

func (s *Service) exportTempDir(id int64) string {
	return filepath.Join(s.exportsRoot(), strconv.FormatInt(id, 10))
}

func (s *Service) outputPath(id int64) string {
	return filepath.Join(s.exportTempDir(id), outputFileName)
}

// registrationsRoot holds the copies RegisterTrainedModel makes before
// they become models.
func (s *Service) registrationsRoot() string { return s.tmpDir("model-registrations") }

func (s *Service) removeUploadDir(id int64) {
	if err := os.RemoveAll(s.uploadDir(id)); err != nil {
		s.log.Warn("remove model upload folder", "upload_id", id, "err", err)
	}
}

func (s *Service) removeExportTemp(id int64) {
	if err := os.RemoveAll(s.exportTempDir(id)); err != nil {
		s.log.Warn("remove model export temporary folder", "export_id", id, "err", err)
	}
}

// ---- cleanup ----

func (s *Service) expireUploads(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE model_uploads SET status = 'canceled', error = $1, updated_at = now()
		WHERE status = 'uploading' AND updated_at < now() - make_interval(secs => $2)
		RETURNING id`, msgUploadExpired, uploadIdleTimeout.Seconds())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.removeUploadDir(id)
	}
	if len(ids) > 0 {
		s.log.Info("canceled stale model uploads", "count", len(ids))
	}
	return nil
}

// cleanUploadTemp removes upload folders that belong to no upload in
// progress (canceled meanwhile, or deleted with their project).
func (s *Service) cleanUploadTemp(ctx context.Context) error {
	// List the folders before reading the uploads: a folder is created only
	// after its row is committed, so every folder seen here belongs to a row
	// the query below can see.
	entries, err := os.ReadDir(s.uploadsRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := s.idSet(ctx, "SELECT id FROM model_uploads WHERE status = 'uploading'")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && active[id] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.uploadsRoot(), e.Name())); err != nil {
			s.log.Warn("remove stale model upload folder", "name", e.Name(), "err", err)
		}
	}
	return nil
}

// cleanExportTemp removes partial results of conversions that are no
// longer running.
func (s *Service) cleanExportTemp(ctx context.Context) error {
	entries, err := os.ReadDir(s.exportsRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	running, err := s.idSet(ctx, "SELECT id FROM model_exports WHERE status = 'running'")
	if err != nil {
		return err
	}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			_ = os.RemoveAll(filepath.Join(s.exportsRoot(), e.Name()))
			continue
		}
		if !running[id] {
			s.removeIdleExportTemp(ctx, id)
		}
	}
	return nil
}

// removeIdleExportTemp removes the temporary folder of an export unless it
// is running again by now. Holding the export's lock keeps a result upload
// that just started from losing its file.
func (s *Service) removeIdleExportTemp(ctx context.Context, id int64) {
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	unlock, err := s.outputs.lock(lockCtx, id)
	if err != nil {
		return // an upload is in progress; try again next time
	}
	defer unlock()
	var running bool
	err = s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM model_exports WHERE id = $1 AND status = 'running')", id).Scan(&running)
	if err == nil && !running {
		s.removeExportTemp(id)
	}
}

// cleanUnused removes the entries of dir (with the given name prefix) that
// no operation of this process is using: leftovers of a crash or of a
// removal that failed earlier.
func (s *Service) cleanUnused(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !strings.HasPrefix(e.Name(), prefix) || s.busy.has(p) {
			continue
		}
		if err := s.removeAll(p); err != nil {
			s.log.Warn("remove leftover model files", "path", p, "err", err)
		}
	}
}

func (s *Service) idSet(ctx context.Context, sql string) (map[int64]bool, error) {
	rows, err := s.db.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
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

// notifier wakes everyone waiting for the next event.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

// wait returns a channel that is closed by the next notify. Take it before
// checking for work, so a notify in between is not missed.
func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

func (n *notifier) notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch != nil {
		close(n.ch)
		n.ch = nil
	}
}

// workerWindow is how long after its last claim or report a node counts
// as working on conversions (workers_online). Nodes claim again at once
// after an empty long poll (at most 30 seconds) and report progress every
// 15 seconds while converting.
const workerWindow = 60 * time.Second

// workerSet remembers when nodes last claimed or worked on conversions.
type workerSet struct {
	mu   sync.Mutex
	seen map[int64]time.Time
}

func (w *workerSet) touch(nodeID int64, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[int64]time.Time{}
	}
	w.seen[nodeID] = now
}

// count returns the nodes seen within workerWindow before now and forgets
// the others.
func (w *workerSet) count(now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for id, t := range w.seen {
		if now.Sub(t) <= workerWindow {
			n++
		} else {
			delete(w.seen, id)
		}
	}
	return n
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

// tryAdd adds path unless it is in use already, and reports whether it
// did.
func (p *pathSet) tryAdd(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paths[path] > 0 {
		return false
	}
	if p.paths == nil {
		p.paths = map[string]int{}
	}
	p.paths[path]++
	return true
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
