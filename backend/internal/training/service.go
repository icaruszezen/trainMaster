package training

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

const (
	maxAttempts     = 3
	maxClaimSkips   = 20
	janitorInterval = 15 * time.Second

	// MaxLogReadBytes bounds one GET /logs response (256 KiB).
	MaxLogReadBytes     = 256 * 1024
	DefaultLogReadBytes = 128 * 1024
	// MaxLogFileBytes bounds runs/job-<id>/train.log on disk (32 MiB) so an
	// unbounded loop cannot exhaust disk space.
	MaxLogFileBytes = 32 * 1024 * 1024
	// MaxPlotArtifactBytes bounds non-weight artifact uploads (50 MiB).
	MaxPlotArtifactBytes = 50 * 1024 * 1024
)

var msgLeaseLost = fmt.Sprintf("训练节点在训练过程中失去联系（已尝试 %d 次）", maxAttempts)

// querier abstracts *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configures the training Service.
type Options struct {
	DB           *pgxpool.Pool
	Storage      *storage.Storage
	Logger       *slog.Logger
	Projects     *project.Service
	DataVersions *dataversion.Service
	Models       *model.Service
}

// Service manages YOLO training jobs, run artifacts, logs, and compute-node
// execution leases.
type Service struct {
	db           *pgxpool.Pool
	storage      *storage.Storage
	log          *slog.Logger
	projects     *project.Service
	dataVersions *dataversion.Service
	models       *model.Service

	logMu sync.Mutex // serializes appends to train.log files
	stop  context.CancelFunc
	wg    sync.WaitGroup
}

// NewService constructs a training Service and registers it with Models as the
// source of training_job tasks.
func NewService(opts Options) *Service {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	svc := &Service{
		db:           opts.DB,
		storage:      opts.Storage,
		log:          log.With("component", "training"),
		projects:     opts.Projects,
		dataVersions: opts.DataVersions,
		models:       opts.Models,
	}
	if opts.Models != nil {
		opts.Models.AddTaskSource(svc)
	}
	return svc
}

// WorkersOnline returns how many compute nodes recently offered or reported on
// training_job tasks.
func (s *Service) WorkersOnline() int {
	if s.models == nil {
		return 0
	}
	return s.models.WorkersOnline(TaskKind)
}

func (s *Service) notifyTasks() {
	if s.models != nil {
		s.models.NotifyTasks()
	}
}

// Start launches background lease-expiration checks until ctx is canceled or
// Stop is called.
func (s *Service) Start(ctx context.Context) error {
	if err := os.MkdirAll(s.runsDir(), 0o755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}
	if err := s.expireLeases(ctx); err != nil {
		return fmt.Errorf("expire stale training leases: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.stop = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.janitorLoop(runCtx)
	}()
	return nil
}

// Stop stops background goroutines and waits for them to exit.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
}

// WaitBackground waits for background goroutines started by Start to exit.
func (s *Service) WaitBackground() {
	s.wg.Wait()
}

func (s *Service) runsDir() string {
	return s.storage.Dir(storage.Runs)
}

func jobDirRel(jobID int64) string {
	return fmt.Sprintf("job-%d", jobID)
}

func (s *Service) jobDir(jobID int64) string {
	return filepath.Join(s.runsDir(), jobDirRel(jobID))
}

func (s *Service) jobLogPath(jobID int64) string {
	return filepath.Join(s.jobDir(jobID), "train.log")
}

func (s *Service) janitorLoop(ctx context.Context) {
	tick := time.NewTicker(janitorInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.expireLeases(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("expire training leases", "err", err)
			}
		}
	}
}

// expireLeases requeues or fails running jobs whose compute-node lease has
// expired.
func (s *Service) expireLeases(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE training_jobs
		SET status = CASE WHEN attempts < $1 THEN 'queued' ELSE 'failed' END,
			stage = '',
			lease_expires_at = NULL,
			queued_at = CASE WHEN attempts < $1 THEN now() ELSE queued_at END,
			error = CASE WHEN attempts < $1 THEN '' ELSE $2 END,
			finished_at = CASE WHEN attempts < $1 THEN NULL ELSE now() END,
			updated_at = now()
		WHERE status = 'running' AND lease_expires_at <= now()
		RETURNING id, status, attempts`, maxAttempts, msgLeaseLost)
	if err != nil {
		return err
	}
	var id int64
	var status string
	var attempts int
	requeued := false
	_, err = pgx.ForEachRow(rows, []any{&id, &status, &attempts}, func() error {
		if status == StatusQueued {
			requeued = true
		}
		s.log.Warn("training job lease expired", "job_id", id, "new_status", status, "attempts", attempts)
		return nil
	})
	if requeued {
		s.notifyTasks()
	}
	return err
}
