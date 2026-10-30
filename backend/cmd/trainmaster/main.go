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
	"github.com/icaruszezen/trainmaster/backend/internal/config"
	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/httpapi"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
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

	opts := httpapi.Options{
		Logger:         log,
		DB:             db,
		Storage:        store,
		StartedAt:      startedAt,
		TrustedProxies: cfg.TrustedProxies,
		Datasets:       datasets,
		Projects:       project.NewService(project.Options{DB: db.Pool, Logger: log}),
	}
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
		// No Read/WriteTimeout: dataset uploads and future model downloads
		// can legitimately take long.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// Shutdown waits for in-flight requests but does not interrupt them.
	// Long-lived handlers (e.g. SSE training logs) should stop by
	// themselves when it begins: hook them up via srv.RegisterOnShutdown.
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

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
