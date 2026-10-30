// Package dbtest gives integration tests a private, migrated PostgreSQL
// database. Tests that use it are skipped unless TM_TEST_DATABASE_URL is
// set (see docs/development.md).
package dbtest

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
)

// New creates a fresh database next to the one in TM_TEST_DATABASE_URL,
// applies all migrations and drops it when the test ends. Every test gets
// its own database, so tests can run background workers and run in
// parallel with the migration tests of package database (which change the
// schema of the configured database itself).
func New(t testing.TB) *database.DB {
	t.Helper()
	dsn := os.Getenv("TM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TM_TEST_DATABASE_URL not set; skipping database integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TM_TEST_DATABASE_URL: %v", err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// rand.Text is base32 (A-Z, 2-7): a valid identifier once lowercased.
	name := "tm_test_" + strings.ToLower(rand.Text()[:16])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database (the TM_TEST_DATABASE_URL user needs CREATEDB): %v", err)
	}

	testCfg := cfg.Copy()
	testCfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, testCfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	db := &database.DB{Pool: pool}
	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
		admin.Close()
	})
	if err := db.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
