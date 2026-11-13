// Command trainmaster runs the trainMaster platform server: the JSON API
// under /api and, when TM_WEB_DIR is set, the built frontend.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/config"
	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/hostinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/httpapi"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/trainerpkg"
	"github.com/icaruszezen/trainmaster/backend/internal/training"
	"github.com/icaruszezen/trainmaster/backend/internal/web"
)

const shutdownTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("trainmaster stopped with an error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	startedAt := time.Now()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting trainmaster",
		"version", buildinfo.Version, "commit", buildinfo.Commit, "build_time", buildinfo.BuildTime)

	// Cancelled on SIGINT/SIGTERM, including while still connecting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.New(cfg.DataDir)
	if err != nil {
		return err
	}
	log.Info("storage ready", "root", store.Root())

	db, err := database.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return interrupted(ctx, log, err)
	}
	defer db.Close()

	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, log); err != nil {
			return interrupted(ctx, log, err)
		}
	} else {
		log.Info("automatic migrations disabled (TM_DB_AUTO_MIGRATE=false)")
	}

	// Background dataset import/scan jobs. A shutdown signal cancels ctx,
	// which interrupts running jobs (they end as failed; queued jobs run
	// after the next start). Stop waits for the workers; deferred after
	// db.Close, it runs before the pool closes.
	datasets := dataset.NewService(dataset.Options{DB: db.Pool, Storage: store, Logger: log})
	if err := datasets.Start(ctx); err != nil {
		return interrupted(ctx, log, err)
	}
	defer datasets.Stop()

	// The model repository. Its background work (conversions whose
	// training node stopped reporting are queued again, stale uploads and
	// temporary files are cleaned up) runs until shutdown; Stop runs before
	// the pool closes.
	models := model.NewService(model.Options{DB: db.Pool, Storage: store, Logger: log})
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	// Data versions (frozen training sets of a project). Start fails the
	// builds a previous run left (they can be retried), then builds queued
	// versions one at a time and purges the recycle bin until shutdown; Stop
	// runs before the services whose data it reads.
	dataVersions := dataversion.NewService(dataversion.Options{DB: db.Pool, Storage: store, Logger: log,
		Datasets: datasets, Projects: projects})
	// Model reviews and YOLO training jobs register themselves as task sources
	// with the model service before models.Start begins serving claims.
	reviews := review.NewService(review.Options{DB: db.Pool, Storage: store, Logger: log, Datasets: datasets,
		Models: models, Projects: projects})
	trainingJobs := training.NewService(training.Options{DB: db.Pool, Storage: store, Logger: log,
		Projects: projects, DataVersions: dataVersions, Models: models})
	models.Start(ctx)
	defer models.Stop()
	if err := reviews.Start(ctx); err != nil {
		return interrupted(ctx, log, err)
	}
	defer reviews.Stop()
	if err := dataVersions.Start(ctx); err != nil {
		return interrupted(ctx, log, err)
	}
	defer dataVersions.Stop()
	if err := trainingJobs.Start(ctx); err != nil {
		return interrupted(ctx, log, err)
	}
	defer trainingJobs.Stop()

	// CPU utilization of this machine for the compute page, sampled in the
	// background until shutdown.
	host := hostinfo.New(hostinfo.Options{FS: hostinfo.SystemFS()})
	host.Start(ctx)
	defer host.Stop()

	log.Info("training node registration",
		"token_required", cfg.NodeToken != "", "heartbeat_interval", cfg.NodeHeartbeatInterval.String())

	opts := httpapi.Options{
		Logger:         log,
		DB:             db,
		Storage:        store,
		StartedAt:      startedAt,
		TrustedProxies: cfg.TrustedProxies,
		Datasets:       datasets,
		Projects:       projects,
		Compute: compute.NewService(compute.Options{
			DB: db.Pool, Logger: log, HeartbeatInterval: cfg.NodeHeartbeatInterval,
		}),
		Models:            models,
		Reviews:           reviews,
		DataVersions:      dataVersions,
		Training:          trainingJobs,
		ReviewerTool:      cfg.ReviewerTool,
		TrainerPackageDir: cfg.TrainerPackageDir,
		Host:              host,
		NodeToken:         cfg.NodeToken,
		LocalTrainerMode:  cfg.TrainerMode,
	}
	logReviewerTool(log, cfg.ReviewerTool)
	logTrainerPackage(log, cfg.TrainerPackageDir)
	if cfg.WebDir != "" {
		h, err := web.New(cfg.WebDir)
		if err != nil {
			return err
		}
		opts.Web = h
		log.Info("serving frontend", "dir", cfg.WebDir)
	}

	srv := &http.Server{
		Handler:           httpapi.NewHandler(opts),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No Read/WriteTimeout: dataset and model uploads, model downloads
		// and the training nodes' long polls can legitimately take long.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// Shutdown waits for in-flight requests but does not interrupt them.
	// Long-lived handlers (e.g. SSE training logs) should stop by
	// themselves when it begins: hook them up via srv.RegisterOnShutdown.
	// The training nodes' task claims (long polls of up to 30 seconds)
	// return at once; the nodes claim again after the restart.
	srv.RegisterOnShutdown(models.StopLongPolls)
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("http server listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	stop() // a second signal now terminates immediately

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Close the remaining connections. This cancels their request
		// contexts, so the handlers return and release their database
		// connections before db.Close (which waits for them) runs.
		log.Warn("graceful shutdown timed out, closing remaining connections", "err", err)
		_ = srv.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// interrupted returns nil if a startup step failed only because a shutdown
// signal arrived (e.g. `docker compose stop` while still waiting for the
// database), so that is not reported as a failure; otherwise it returns err.
func interrupted(ctx context.Context, log *slog.Logger, err error) error {
	if ctx.Err() != nil {
		log.Info("startup interrupted by signal", "err", err)
		return nil
	}
	return err
}

// logReviewerTool reports whether the local reviewer for Windows can be
// downloaded (TM_REVIEWER_TOOL). A configured file that is missing is no
// error, the download is just unavailable; the API looks for it again on
// every request.
func logReviewerTool(log *slog.Logger, path string) {
	if path == "" {
		log.Info("no local reviewer bundled (TM_REVIEWER_TOOL is not set)")
		return
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		log.Warn("local reviewer not found; its download is unavailable", "path", path, "err", err)
		return
	}
	log.Info("local reviewer bundled", "path", path, "size_bytes", fi.Size())
}

// logTrainerPackage reports whether the training node for Windows
// computers can be installed from the platform: its install script and
// package (TM_TRAINER_PACKAGE_DIR). Like for the local reviewer, a
// directory that is missing or incomplete is no error, the downloads are
// just unavailable; the API looks at it again on every request.
func logTrainerPackage(log *slog.Logger, dir string) {
	if dir == "" {
		log.Info("no Windows training node installer bundled (TM_TRAINER_PACKAGE_DIR is not set)")
		return
	}
	if err := trainerpkg.New(dir).Check(); err != nil {
		log.Warn("Windows training node installer not found or incomplete; its downloads are unavailable", "dir", dir, "err", err)
		return
	}
	log.Info("Windows training node installer bundled", "dir", dir)
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
