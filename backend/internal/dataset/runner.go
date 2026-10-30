package dataset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// progressInterval throttles progress writes to the database.
	progressInterval = 500 * time.Millisecond
	// pollInterval is a safety net: workers also look for queued jobs this
	// often, in case a wake-up was missed.
	pollInterval = 10 * time.Second
	// finishTimeout bounds the final database writes of a job, which run
	// on a fresh context because the job's own may be canceled.
	finishTimeout = 10 * time.Second
)

// Messages stored as the error of interrupted jobs.
const (
	msgInterrupted = "服务重启，导入被中断；已移入数据集的文件可通过“重新扫描”纳入"
	msgInternal    = "处理失败：服务器内部错误，请查看服务日志"
)

// runner executes queued jobs with a fixed number of workers. The queue
// lives in the database (status 'queued'), so jobs survive restarts; at
// most one job per dataset runs at a time.
type runner struct {
	svc     *Service
	log     *slog.Logger
	workers int
	wake    chan struct{}

	claimMu sync.Mutex // serializes claims so two workers never pick jobs of one dataset

	mu      sync.Mutex
	cancels map[int64]context.CancelCauseFunc
	pending map[int64]bool // cancel requests that arrived before the worker registered

	stop context.CancelFunc
	wg   sync.WaitGroup
}

func newRunner(svc *Service, workers int) *runner {
	return &runner{
		svc:     svc,
		log:     svc.log,
		workers: workers,
		wake:    make(chan struct{}, 1),
		cancels: map[int64]context.CancelCauseFunc{},
		pending: map[int64]bool{},
	}
}

func (r *runner) start(ctx context.Context) {
	ctx, r.stop = context.WithCancel(ctx)
	for range r.workers {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.loop(ctx)
		}()
	}
}

// shutdown cancels running jobs and waits for the workers to return.
func (r *runner) shutdown() {
	if r.stop != nil {
		r.stop()
	}
	r.wg.Wait()
}

// notify wakes an idle worker to look for queued jobs.
func (r *runner) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// cancel stops a running job. It returns immediately; the job ends as
// canceled once its worker notices.
func (r *runner) cancel(importID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cancels[importID]; ok {
		c(errCanceled)
		return
	}
	r.pending[importID] = true
}

// forget drops a cancel request for a job that is no longer running, so
// a request that arrived after the job ended does not stay in pending.
func (r *runner) forget(importID int64) {
	r.mu.Lock()
	delete(r.pending, importID)
	r.mu.Unlock()
}

func (r *runner) loop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer.C:
		}
		for ctx.Err() == nil {
			job, err := r.claim(ctx)
			if err != nil {
				if ctx.Err() == nil {
					r.log.Error("claim dataset job", "err", err)
				}
				break
			}
			if job == nil {
				break
			}
			r.notify() // there may be more work for the other workers
			r.run(ctx, *job)
		}
		timer.Reset(pollInterval)
	}
}

// claim marks the oldest queued job whose dataset has no running job as
// running and returns it, or nil when there is none.
func (r *runner) claim(ctx context.Context) (*Import, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	job, err := scanImport(r.svc.db.QueryRow(ctx, `UPDATE dataset_imports SET
			status = 'running', started_at = now(), updated_at = now(),
			stage = '', progress_done = 0, progress_total = 0, progress_unit = ''
		WHERE id = (
			SELECT q.id FROM dataset_imports q
			WHERE q.status = 'queued' AND NOT EXISTS (
				SELECT 1 FROM dataset_imports r WHERE r.dataset_id = q.dataset_id AND r.status = 'running')
			ORDER BY q.id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING `+importColumns))
	if errors.Is(err, errImportNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// run executes one claimed job and records its outcome.
func (r *runner) run(parent context.Context, job Import) {
	ctx, cancel := context.WithCancelCause(parent)
	r.mu.Lock()
	r.cancels[job.ID] = cancel
	if r.pending[job.ID] {
		delete(r.pending, job.ID)
		cancel(errCanceled)
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.cancels, job.ID)
		r.mu.Unlock()
		cancel(nil)
	}()

	log := r.log.With("import_id", job.ID, "dataset_id", job.DatasetID, "source", job.Source)
	log.Info("dataset job started")
	start := time.Now()
	p := &progressTracker{db: r.svc.db, log: log, id: job.ID}

	summary, err := r.execute(ctx, job, p, log)

	// A job that completed despite a late cancel request still succeeded.
	status, message := StatusSucceeded, ""
	switch {
	case err == nil:
	case errors.Is(context.Cause(ctx), errCanceled):
		status = StatusCanceled
	case parent.Err() != nil:
		status, message = StatusFailed, msgInterrupted
	default:
		status, message = StatusFailed, userMessage(err)
	}
	if status == StatusFailed {
		log.Warn("dataset job failed", "err", err, "duration_ms", time.Since(start).Milliseconds())
	} else {
		log.Info("dataset job finished", "status", status, "summary", summary, "duration_ms", time.Since(start).Milliseconds())
	}

	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer fcancel()
	if summary == nil {
		summary = map[string]int64{}
	}
	// The last progress update may have been throttled; store the final one.
	if _, err := r.svc.db.Exec(fctx, `UPDATE dataset_imports SET status = $2, error = $3, summary = $4,
			stage = $5, progress_unit = $6, progress_done = $7, progress_total = $8,
			finished_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'running'`, job.ID, status, message, summary,
		p.stage, p.unit, p.done, p.total); err != nil {
		log.Error("record dataset job result", "err", err)
	}
	r.svc.removeStaging(job.ID)
	r.notify() // a job of the same dataset may be waiting
}

// execute runs a job. A panic, e.g. in an archive decoder fed a crafted
// file, fails that job instead of crashing the server.
func (r *runner) execute(ctx context.Context, job Import, p *progressTracker, log *slog.Logger) (summary map[string]int64, err error) {
	defer func() {
		if v := recover(); v != nil {
			log.Error("dataset job panicked", "panic", v, "stack", string(debug.Stack()))
			summary, err = nil, fmt.Errorf("dataset job panicked: %v", v)
		}
	}()
	return r.svc.execute(ctx, job, p)
}

// userMessage returns the Chinese message for a job error.
func userMessage(err error) string {
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Message
	}
	return msgInternal
}

// progressTracker persists the stage and progress of a running job, at
// most every progressInterval unless the stage changes.
type progressTracker struct {
	db  *pgxpool.Pool
	log *slog.Logger
	id  int64

	stage, unit string
	done, total int64
	last        time.Time
}

// setStage switches to a new stage and writes it immediately.
func (p *progressTracker) setStage(ctx context.Context, stage, unit string, total int64) {
	p.stage, p.unit, p.done, p.total = stage, unit, 0, total
	p.write(ctx)
}

// set records progress; the write is throttled.
func (p *progressTracker) set(ctx context.Context, done, total int64) {
	p.done, p.total = done, total
	if time.Since(p.last) >= progressInterval {
		p.write(ctx)
	}
}

func (p *progressTracker) write(ctx context.Context) {
	if p.db == nil {
		return // unit tests
	}
	p.last = time.Now()
	_, err := p.db.Exec(ctx, `UPDATE dataset_imports SET stage = $2, progress_unit = $3, progress_done = $4,
		progress_total = $5, updated_at = now() WHERE id = $1`, p.id, p.stage, p.unit, p.done, p.total)
	if err != nil && ctx.Err() == nil {
		p.log.Warn("record dataset job progress", "err", err)
	}
}

// recoverJobs runs once at startup, before the workers start: jobs that
// were running when the previous process stopped cannot be resumed (their
// staging data may be half consumed), so they fail; queued jobs simply get
// picked up again. The counters of their datasets are recomputed, since a
// crash can leave them behind the rows already indexed.
func (s *Service) recoverJobs(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE dataset_imports SET status = 'failed', error = $1,
			finished_at = now(), updated_at = now()
		WHERE status = 'running'
		RETURNING dataset_id`, msgInterrupted)
	if err != nil {
		return err
	}
	datasetIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	if len(datasetIDs) > 0 {
		s.log.Warn("marked interrupted dataset jobs as failed", "count", len(datasetIDs))
	}
	slices.Sort(datasetIDs)
	for _, id := range slices.Compact(datasetIDs) {
		if err := recomputeAggregates(ctx, s.db, id, false); err != nil {
			return fmt.Errorf("update dataset counters: %w", err)
		}
	}
	return s.cleanStaging(ctx)
}

// Upload jobs nobody touched for this long are canceled.
const (
	uploadIdleTimeout = 24 * time.Hour
	janitorInterval   = 30 * time.Minute
	msgUploadExpired  = "上传超时未完成，已自动取消"
)

func (s *Service) expireUploads(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE dataset_imports SET status = 'canceled', error = $1,
			finished_at = now(), updated_at = now()
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
		s.removeStaging(id)
	}
	if len(ids) > 0 {
		s.log.Info("canceled stale dataset uploads", "count", len(ids))
	}
	return nil
}
