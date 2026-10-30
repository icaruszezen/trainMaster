package database

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migrations are plain SQL files named NNNN_description.sql. Each file runs
// in its own transaction, so it must not contain BEGIN/COMMIT or statements
// that cannot run inside a transaction (e.g. CREATE INDEX CONCURRENTLY).
// Applied files must never be edited or renamed; add a new file instead.
//
//go:embed migrations/*.sql
var embedded embed.FS

// Migration is one versioned SQL file.
type Migration struct {
	Version int64
	Name    string
	SQL     string
}

// migrationLockID is the pg_advisory_lock key that serializes migrations
// when several instances start at the same time. Arbitrary but fixed.
const migrationLockID int64 = 0x746d5f6d69677261 // "tm_migra"

var migrationFile = regexp.MustCompile(`^(\d+)_(\w+)\.sql$`)

// parseMigrationName extracts the version and name from "0001_init.sql".
func parseMigrationName(file string) (int64, string, error) {
	m := migrationFile.FindStringSubmatch(file)
	if m == nil {
		return 0, "", fmt.Errorf("migration %q: file name must look like 0001_description.sql", file)
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || v <= 0 {
		return 0, "", fmt.Errorf("migration %q: version must be a positive integer", file)
	}
	return v, m[2], nil
}

// loadMigrations reads all *.sql files in fsys, sorted by version.
func loadMigrations(fsys fs.FS) ([]Migration, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]string)
	out := make([]Migration, 0, len(files))
	for _, file := range files {
		v, name, err := parseMigrationName(file)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", v, prev, file)
		}
		seen[v] = file
		sql, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(sql)) == "" {
			return nil, fmt.Errorf("migration %q is empty", file)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(sql)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out, nil
}

// Migrate applies all pending embedded migrations.
func (db *DB) Migrate(ctx context.Context, log *slog.Logger) error {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return err
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		return err
	}
	return db.migrate(ctx, ms, log)
}

// appliedMigration is a row of schema_migrations.
type appliedMigration struct {
	Version int64
	Name    string
}

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    bigint PRIMARY KEY,
    name       text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

func (db *DB) migrate(ctx context.Context, ms []Migration, log *slog.Logger) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	// The advisory lock belongs to this session, so everything below must
	// use the same connection.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migrate: acquire lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			// Closing the session releases the lock on the server.
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	rows, _ := conn.Query(ctx, "SELECT version, name FROM schema_migrations")
	recorded, err := pgx.CollectRows(rows, pgx.RowToStructByPos[appliedMigration])
	if err != nil {
		return fmt.Errorf("migrate: read applied versions: %w", err)
	}

	// applied maps each recorded version to its file name.
	applied := make(map[int64]string, len(recorded))
	for _, a := range recorded {
		applied[a.Version] = a.Name
		if !slices.ContainsFunc(ms, func(m Migration) bool { return m.Version == a.Version }) {
			log.Warn("database has a migration this build does not know about (was a newer version deployed?)", "version", a.Version, "name", a.Name)
		}
	}
	// A version recorded under another name means two different files share
	// a number (e.g. branches that both added 0002_*.sql against one dev
	// database). Skipping it silently would leave the schema incomplete.
	for _, m := range ms {
		if name, ok := applied[m.Version]; ok && name != m.Name {
			return fmt.Errorf("migrate: version %d was applied as %04d_%s.sql, but this build has %04d_%s.sql; "+
				"migration versions must never be reused or renamed", m.Version, m.Version, name, m.Version, m.Name)
		}
	}

	count := 0
	for _, m := range ms {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		start := time.Now()
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %04d_%s failed: %w", m.Version, m.Name, err)
		}
		log.Info("migration applied", "version", m.Version, "name", m.Name, "duration_ms", time.Since(start).Milliseconds())
		count++
	}

	var latest int64
	if len(ms) > 0 {
		latest = ms[len(ms)-1].Version
	}
	log.Info("database schema up to date", "latest_migration", latest, "newly_applied", count)
	return nil
}
