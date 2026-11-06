package database

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// newPrivateTestDB creates an empty database next to the one in
// TM_TEST_DATABASE_URL and drops it when the test ends, for tests that need
// the schema at an older version: dbtest.New (which this package cannot
// import) migrates to the latest one, and the configured database itself
// is migrated by TestIntegrationMigrate.
func newPrivateTestDB(t *testing.T) *DB {
	t.Helper()
	admin := openTestDB(t)
	ctx := context.Background()
	// rand.Text is base32 (A-Z, 2-7): a valid identifier once lowercased.
	name := "tm_test_" + strings.ToLower(rand.Text()[:16])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database (the TM_TEST_DATABASE_URL user needs CREATEDB): %v", err)
	}
	db := &DB{}
	// Registered after openTestDB's cleanup, so it runs before admin closes.
	t.Cleanup(func() {
		if db.Pool != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
	})
	cfg := admin.Config() // a copy
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	db.Pool = pool
	return db
}

// 0009_project_classes moves the class lists from the datasets to their
// projects.
func TestIntegrationMigrateProjectClasses(t *testing.T) {
	db := newPrivateTestDB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Migration) bool { return m.Version == 9 })
	if i < 0 {
		t.Fatal("no migration 0009")
	}
	if err := db.migrate(ctx, ms[:i], log); err != nil {
		t.Fatalf("migrate to 0008: %v", err)
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	insertID := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(ctx, sql+" RETURNING id", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	dataset := func(name, classes string) int64 {
		t.Helper()
		return insertID("INSERT INTO datasets (name, dir_name, origin, classes) VALUES ($1, $1, 'upload', $2::jsonb)", name, classes)
	}
	project := func(name string) int64 {
		t.Helper()
		return insertID("INSERT INTO projects (name) VALUES ($1)", name)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	link := func(projectID, datasetID int64, added time.Time) {
		t.Helper()
		exec("INSERT INTO project_datasets (project_id, dataset_id, created_at) VALUES ($1, $2, $3)", projectID, datasetID, added)
	}

	a := dataset("a", `[{"name": "scratch", "color": "#111111"}, {"name": "dent", "color": ""}]`)
	b := dataset("b", `[{"name": "dent", "color": "#222222"}, {"name": "crack", "color": ""}, {"name": "scratch", "color": "#333333"}]`)
	dataset("lone", `[{"name": "lone", "color": ""}]`) // in no project: its list is dropped
	plain := dataset("plain", `[]`)
	// Values the application never stored must not fail the migration.
	scalar := dataset("scalar", `"oops"`)
	odd := dataset("odd", `[{"color": "#444444"}, "str", {"name": ""}, {"name": "ok"}]`)
	// k<from> ... k<to>
	series := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			if b.Len() > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, `{"name": "k%d", "color": ""}`, i)
		}
		return "[" + b.String() + "]"
	}
	big1 := dataset("big1", series(1, 600))
	big2 := dataset("big2", series(501, 1100))

	// In the order the datasets were added: b, then a.
	byAdded := project("by-added")
	link(byAdded, a, t0.Add(time.Second))
	link(byAdded, b, t0)
	// Added together: by dataset ID.
	together := project("together")
	link(together, b, t0)
	link(together, a, t0)
	project("empty")
	withPlain := project("with-plain")
	link(withPlain, plain, t0)
	odds := project("odds")
	link(odds, scalar, t0)
	link(odds, odd, t0)
	big := project("big")
	link(big, big1, t0)
	link(big, big2, t0)

	if err := db.migrate(ctx, ms, log); err != nil {
		t.Fatalf("migrate 0009: %v", err)
	}

	rows, err := db.Query(ctx, "SELECT name, classes FROM projects")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]map[string]string{}
	var name string
	var classes []map[string]string
	if _, err := pgx.ForEachRow(rows, []any{&name, &classes}, func() error {
		got[name] = classes
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	class := func(name, color string) map[string]string { return map[string]string{"name": name, "color": color} }
	for project, want := range map[string][]map[string]string{
		// Each name once, with the color of its first occurrence.
		"by-added":   {class("dent", "#222222"), class("crack", ""), class("scratch", "#333333")},
		"together":   {class("scratch", "#111111"), class("dent", ""), class("crack", "")},
		"empty":      {},
		"with-plain": {},
		"odds":       {class("ok", "")},
	} {
		if !reflect.DeepEqual(got[project], want) {
			t.Errorf("classes of %s = %v, want %v", project, got[project], want)
		}
	}
	// At most 1000 names: k1 ... k600 of big1, then the names of big2 that
	// are new, up to k1000 (the limit counts names, not entries).
	if list := got["big"]; len(list) != 1000 || list[0]["name"] != "k1" || list[600]["name"] != "k601" ||
		list[999]["name"] != "k1000" {
		t.Errorf("classes of big: %d, %v ... %v", len(list), list[:min(len(list), 2)], list[max(len(list)-2, 0):])
	}

	var hasColumn bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'datasets' AND column_name = 'classes')`).Scan(&hasColumn); err != nil {
		t.Fatal(err)
	}
	if hasColumn {
		t.Error("datasets.classes still exists")
	}
	// New projects start with an empty list.
	var fresh string
	if err := db.QueryRow(ctx, "INSERT INTO projects (name) VALUES ('fresh') RETURNING classes::text").Scan(&fresh); err != nil || fresh != "[]" {
		t.Errorf("classes of a new project = %q, %v", fresh, err)
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

// The recycle bin tables of 0012_recycle_bin.sql receive rows moved with
// SELECT * and give them back the same way, so their columns must stay
// those of the tables they come from: same names, types, order and
// nullability.
func TestIntegrationRecycledTablesMatch(t *testing.T) {
	db := newPrivateTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	columns := func(table string) []string {
		t.Helper()
		rows, err := db.Query(ctx, `SELECT column_name || ' ' || data_type || ' ' || is_nullable
			FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1
			ORDER BY ordinal_position`, table)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return cols
	}
	for _, table := range []string{"project_datasets", "review_datasets", "review_images"} {
		want, got := columns(table), columns("recycled_"+table)
		if len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("columns of recycled_%s = %q, want those of %s: %q", table, got, table, want)
		}
	}
}
