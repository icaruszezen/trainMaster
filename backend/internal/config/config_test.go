package config

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		HTTPAddr:    ":8080",
		DataDir:     "./data",
		WebDir:      "",
		DatabaseURL: "postgres://trainmaster:trainmaster@localhost:5432/trainmaster?sslmode=disable",
		AutoMigrate: true,
		LogLevel:    slog.LevelInfo,
		LogFormat:   "text",
	}
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("Load defaults\n got %+v\nwant %+v", *cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TM_HTTP_ADDR":       "127.0.0.1:9000",
		"TM_DATA_DIR":        "/data",
		"TM_WEB_DIR":         "/app/web",
		"TM_DB_HOST":         "postgres",
		"TM_DB_PORT":         "6543",
		"TM_DB_SSLMODE":      "require",
		"TM_DB_AUTO_MIGRATE": "false",
		"TM_LOG_LEVEL":       "DEBUG",
		"TM_LOG_FORMAT":      "json",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DataDir != "/data" || cfg.WebDir != "/app/web" {
		t.Errorf("unexpected paths/addr: %+v", cfg)
	}
	if cfg.AutoMigrate || cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "json" {
		t.Errorf("unexpected flags: %+v", cfg)
	}
	if want := "postgres://trainmaster:trainmaster@postgres:6543/trainmaster?sslmode=require"; cfg.DatabaseURL != want {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, want)
	}
}

func TestLoadDatabaseURLTakesPrecedence(t *testing.T) {
	const dsn = "host=db user=u password=p dbname=x"
	cfg, err := Load(env(map[string]string{
		"TM_DATABASE_URL": dsn,
		"TM_DB_HOST":      "ignored",
		"TM_DB_PORT":      "not-validated-either",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != dsn {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, dsn)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		key, value string
	}{
		{"TM_HTTP_ADDR", "8080"},
		{"TM_HTTP_ADDR", ":http-alt"},
		{"TM_DB_PORT", "abc"},
		{"TM_DB_PORT", "70000"},
		{"TM_DB_SSLMODE", "on"},
		{"TM_DB_AUTO_MIGRATE", "maybe"},
		{"TM_LOG_LEVEL", "verbose"},
		{"TM_LOG_FORMAT", "xml"},
		{"TM_TRUSTED_PROXIES", "nginx"},
		{"TM_TRUSTED_PROXIES", "10.0.0.0/33"},
		{"TM_TRUSTED_PROXIES", "fe80::1%eth0"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := Load(env(map[string]string{tt.key: tt.value}))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.key) || !strings.Contains(err.Error(), tt.value) {
				t.Errorf("error should name %s and the bad value %q: %v", tt.key, tt.value, err)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"TM_DB_PORT": "x", "TM_LOG_LEVEL": "y"}))
	if err == nil || !strings.Contains(err.Error(), "TM_DB_PORT") || !strings.Contains(err.Error(), "TM_LOG_LEVEL") {
		t.Fatalf("expected both problems to be reported, got %v", err)
	}
}

func TestDBParamsURLEscaping(t *testing.T) {
	p := DBParams{
		Host:     "::1",
		Port:     5432,
		User:     "train@master",
		Password: "p@ss:w/rd#%?&= 中文",
		Name:     "my db",
		SSLMode:  "disable",
	}
	// The driver must read back exactly what we put in.
	pc, err := pgconn.ParseConfig(p.URL())
	if err != nil {
		t.Fatalf("pgconn.ParseConfig(%q): %v", p.URL(), err)
	}
	if pc.Host != p.Host || pc.Port != uint16(p.Port) || pc.User != p.User ||
		pc.Password != p.Password || pc.Database != p.Name {
		t.Errorf("round trip mismatch: host=%q port=%d user=%q password=%q db=%q",
			pc.Host, pc.Port, pc.User, pc.Password, pc.Database)
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	cfg, err := Load(env(map[string]string{"TM_TRUSTED_PROXIES": " 172.18.0.1, 10.1.2.3/8 ,,::1"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"172.18.0.1/32", "10.0.0.0/8", "::1/128"}; !reflect.DeepEqual(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %q, want %q", cfg.TrustedProxies, want)
	}
}

func TestLoadKeepsCredentialsVerbatim(t *testing.T) {
	// docker compose hands the same untrimmed value to the postgres
	// container, so the app must not trim it either.
	cfg, err := Load(env(map[string]string{
		"TM_DB_USER":     " admin",
		"TM_DB_PASSWORD": "s3cret ",
		"TM_DB_HOST":     " db ",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pc, err := pgconn.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("pgconn.ParseConfig(%q): %v", cfg.DatabaseURL, err)
	}
	if pc.User != " admin" || pc.Password != "s3cret " || pc.Host != "db" {
		t.Errorf("user=%q password=%q host=%q", pc.User, pc.Password, pc.Host)
	}
}
