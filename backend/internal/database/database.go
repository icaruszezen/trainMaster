// Package database owns the PostgreSQL connection pool and the schema
// migrations.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps the connection pool. Handlers that need SQL use the embedded
// *pgxpool.Pool directly.
type DB struct {
	*pgxpool.Pool
}

const (
	// connectTimeout bounds how long Open keeps retrying, e.g. while the
	// database container is still starting.
	connectTimeout = 60 * time.Second
	pingTimeout    = 5 * time.Second
	maxRetryDelay  = 5 * time.Second
)

// Open creates a pool for dsn (URL or key=value form) and waits until the
// server answers, retrying with exponential backoff for up to one minute or
// until ctx is cancelled.
func Open(ctx context.Context, dsn string, log *slog.Logger) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cc := cfg.ConnConfig
	target := fmt.Sprintf("%s@%s:%d/%s", cc.User, cc.Host, cc.Port, cc.Database)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	delay := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pingCtx, cancelPing := context.WithTimeout(ctx, pingTimeout)
		err = pool.Ping(pingCtx)
		cancelPing()
		if err == nil {
			log.Info("database connected", "target", target)
			return &DB{Pool: pool}, nil
		}

		log.Warn("database not ready, retrying", "target", target, "attempt", attempt, "retry_in", delay.String(), "err", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("connect to database %s: %w", target, err)
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// ServerVersion returns the PostgreSQL server version, e.g. "17.6".
func (db *DB) ServerVersion(ctx context.Context) (string, error) {
	var v string
	err := db.QueryRow(ctx, "SHOW server_version").Scan(&v)
	return v, err
}

// SchemaVersion returns the highest applied migration version, or 0 if no
// migration has run yet.
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	err := db.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&v)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table: never migrated
		return 0, nil
	}
	return v, err
}
