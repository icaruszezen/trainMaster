// Command trainmaster-storage runs the standalone trainMaster storage node
// HTTP daemon for local or remote Docker storage containers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/storageagent"
)

const shutdownTimeout = 15 * time.Second

func main() {
	if err := run(os.Getenv); err != nil {
		slog.Error("trainmaster-storage stopped with an error", "err", err)
		os.Exit(1)
	}
}

func run(getenv func(string) string) error {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}

	addr := get("TM_STORAGE_ADDR", ":8090")
	dataDir := get("TM_STORAGE_DATA_DIR", "/data")
	token := getenv("TM_STORAGE_TOKEN")
	ttlStr := get("TM_STORAGE_TEMP_TTL", "2h")
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil || ttl <= 0 {
		return fmt.Errorf("TM_STORAGE_TEMP_TTL: invalid positive duration %q", ttlStr)
	}

	log := buildLogger(get("TM_LOG_LEVEL", "info"), get("TM_LOG_FORMAT", "text"))
	slog.SetDefault(log)
	log.Info("starting trainmaster-storage",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"build_time", buildinfo.BuildTime,
		"data_dir", dataDir,
		"token_required", token != "",
		"temp_ttl", ttl.String(),
	)

	agent, err := storageagent.NewServer(storageagent.Options{
		DataDir: dataDir,
		Token:   token,
		TempTTL: ttl,
		Logger:  log,
	})
	if err != nil {
		return err
	}
	defer agent.Close()

	srv := &http.Server{
		Handler:           agent.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No Read/WriteTimeout: large archive pack/unpack streams legitimately run for minutes.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("storage http server listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	stop()

	log.Info("shutting down trainmaster-storage", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown timed out, closing connections", "err", err)
		_ = srv.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("trainmaster-storage shutdown complete")
	return nil
}

func buildLogger(levelStr, formatStr string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(strings.TrimSpace(formatStr), "json") {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
