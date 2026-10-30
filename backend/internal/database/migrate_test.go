package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseMigrationName(t *testing.T) {
	good := []struct {
		file    string
		version int64
		name    string
	}{
		{"0001_init.sql", 1, "init"},
		{"0042_add_dataset_tables.sql", 42, "add_dataset_tables"},
		{"20261030_Big.sql", 20261030, "Big"},
	}
	for _, tt := range good {
		v, name, err := parseMigrationName(tt.file)
		if err != nil || v != tt.version || name != tt.name {
			t.Errorf("parseMigrationName(%q) = %d, %q, %v; want %d, %q", tt.file, v, name, err, tt.version, tt.name)
		}
	}

	bad := []string{"init.sql", "0001.sql", "0001_.sql", "0001-init.sql", "0001_init.SQL", "0001_init.sql.bak", "0000_zero.sql", "x0001_init.sql", "0001_bad name.sql"}
	for _, file := range bad {
		if _, _, err := parseMigrationName(file); err == nil {
			t.Errorf("parseMigrationName(%q) should fail", file)
		}
	}
}

func TestLoadMigrationsSortsByVersion(t *testing.T) {
	ms, err := loadMigrations(fstest.MapFS{
		"0010_ten.sql": {Data: []byte("SELECT 10;")},
		"0002_two.sql": {Data: []byte("SELECT 2;")},
		"README.md":    {Data: []byte("ignored")},
	})
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) != 2 || ms[0].Version != 2 || ms[1].Version != 10 || ms[1].SQL != "SELECT 10;" {
		t.Errorf("unexpected migrations: %+v", ms)
	}
}

func TestLoadMigrationsRejectsDuplicates(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{
		"0001_a.sql": {Data: []byte("SELECT 1;")},
		"001_b.sql":  {Data: []byte("SELECT 1;")},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate migration version 1") {
		t.Fatalf("expected a duplicate version error, got %v", err)
	}
}

func TestLoadMigrationsRejectsInvalidFiles(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"bad name": {"init.sql": {Data: []byte("SELECT 1;")}},
		"empty":    {"0001_empty.sql": {Data: []byte(" \n\t\n")}},
	} {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEmbeddedMigrations(t *testing.T) {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		t.Fatalf("embedded migrations are invalid: %v", err)
	}
	if len(ms) == 0 || ms[0].Version != 1 || ms[0].Name != "init" {
		t.Fatalf("expected 0001_init.sql first, got %+v", ms)
	}
	for i, m := range ms {
		if m.Version != int64(i+1) {
			t.Errorf("migration versions should be contiguous: position %d has version %d", i, m.Version)
		}
	}
}
