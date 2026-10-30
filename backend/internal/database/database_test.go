package database

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// openTestDB connects to TM_TEST_DATABASE_URL and skips the test when it is
// not set. Point it at a throwaway database, e.g.
//
//	docker run -d --rm --name tm-test-pg -e POSTGRES_PASSWORD=x -p 55432:5432 postgres:17-alpine
//	TM_TEST_DATABASE_URL='postgres://postgres:x@localhost:55432/postgres?sslmode=disable' go test ./...
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TM_TEST_DATABASE_URL not set; skipping database integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := Open(ctx, dsn, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestIntegrationMigrate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	// Concurrent runs must serialize on the advisory lock and apply each
	// migration exactly once.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Migrate(ctx, log)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	// Running again is a no-op.
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	got, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	var rows int64
	if err := db.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if got < 1 || rows != got {
		t.Errorf("schema version %d with %d recorded migrations; want equal and >= 1", got, rows)
	}

	// app_settings from 0001_init exists and stores JSON.
	const key = "integration-test"
	if _, err := db.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, map[string]any{"ok": true}); err != nil {
		t.Fatalf("insert app_settings: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), "DELETE FROM app_settings WHERE key = $1", key); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	var value map[string]any
	if err := db.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = $1", key).Scan(&value); err != nil || value["ok"] != true {
		t.Errorf("read app_settings: %v, %v", value, err)
	}

	if v, err := db.ServerVersion(ctx); err != nil || v == "" {
		t.Errorf("ServerVersion() = %q, %v", v, err)
	}
}

// testVersion is far above the real migrations so these tests never clash
// with them; cleanup removes whatever they record.
const testVersion = 900_001

func cleanupTestMigration(t *testing.T, db *DB, table string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := db.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Logf("cleanup: %v", err)
		}
		if _, err := db.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", testVersion); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
}

func TestIntegrationMigrateFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	cleanupTestMigration(t, db, "tm_test_rollback")

	bad := []Migration{{Version: testVersion, Name: "bad", SQL: "CREATE TABLE tm_test_rollback (); SELECT missing_function();"}}
	if err := db.migrate(ctx, bad, log); err == nil {
		t.Fatal("expected the failing migration to return an error")
	}

	var tableExists, recorded bool
	if err := db.QueryRow(ctx, "SELECT to_regclass('tm_test_rollback') IS NOT NULL").Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", testVersion).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if tableExists || recorded {
		t.Errorf("failed migration left traces: table exists=%v, recorded=%v", tableExists, recorded)
	}

	// The advisory lock must have been released. Lock and unlock on one
	// session: advisory locks belong to the connection that took them.
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if locked {
		_, err = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("migration lock is still held after a failed run")
	}

	// Once the file is fixed, the next run applies it.
	fixed := []Migration{{Version: testVersion, Name: "bad", SQL: "CREATE TABLE tm_test_rollback ()"}}
	if err := db.migrate(ctx, fixed, log); err != nil {
		t.Fatalf("migrate after fix: %v", err)
	}
}

func TestIntegrationMigrateRejectsReusedVersion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	cleanupTestMigration(t, db, "tm_test_datasets")

	first := []Migration{{Version: testVersion, Name: "create_datasets", SQL: "CREATE TABLE tm_test_datasets ()"}}
	if err := db.migrate(ctx, first, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Another branch used the same number for a different file.
	other := []Migration{{Version: testVersion, Name: "create_labels", SQL: "CREATE TABLE tm_test_labels ()"}}
	err := db.migrate(ctx, other, log)
	if err == nil || !strings.Contains(err.Error(), "create_datasets") || !strings.Contains(err.Error(), "create_labels") {
		t.Fatalf("expected an error naming both files, got %v", err)
	}
}

func TestOpenGivesUpWhenContextEnds(t *testing.T) {
	// Nothing listens on port 1, so every ping fails fast.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Open took %v after its context expired", d)
	}
}

func TestOpenRejectsInvalidDSN(t *testing.T) {
	if _, err := Open(context.Background(), "postgres://%zz", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("expected a parse error")
	}
}
