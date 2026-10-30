package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func (f fakeDB) ServerVersion(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "17.6", nil
}

func (f fakeDB) SchemaVersion(context.Context) (int64, error) { return 1, f.err }

func newTestHandler(t *testing.T, db Database, web http.Handler) http.Handler {
	t.Helper()
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Options{
		Logger:    slog.New(slog.DiscardHandler),
		DB:        db,
		Storage:   st,
		StartedAt: time.Now().Add(-90 * time.Second),
		Web:       web,
	})
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealthz(t *testing.T) {
	h := newTestHandler(t, fakeDB{err: errors.New("down")}, nil) // must not depend on the database
	rec := serve(h, http.MethodGet, "/api/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := decode[map[string]string](t, rec); body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("API responses should not be cached")
	}
	if rec.Header().Get(requestIDHeader) == "" {
		t.Errorf("missing %s header", requestIDHeader)
	}
	if rec := serve(h, http.MethodHead, "/api/healthz"); rec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	h := newTestHandler(t, fakeDB{}, nil)
	for in, keep := range map[string]bool{
		"req-123_abc":            true,
		"has space":              false,
		strings.Repeat("x", 129): false,
		"":                       false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
		req.Header.Set(requestIDHeader, in)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get(requestIDHeader)
		if keep && got != in {
			t.Errorf("X-Request-ID %q was not passed through, got %q", in, got)
		}
		if !keep && (got == in || got == "") {
			t.Errorf("X-Request-ID %q should be replaced, got %q", in, got)
		}
	}
}

func TestErrorEnvelopes(t *testing.T) {
	h := newTestHandler(t, fakeDB{}, nil)
	tests := []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/api/nope", 404, "not_found"},
		{"GET", "/api", 404, "not_found"},
		{"GET", "/api/v1", 404, "not_found"},
		{"GET", "/api/v1/annotation", 404, "not_found"},
		{"GET", "/api/v1/annotation/tasksx", 404, "not_found"},
		// Without a dataset service the dataset routes are not mounted.
		{"GET", "/api/v1/datasets", 404, "not_found"},
		{"POST", "/api/healthz", 405, "method_not_allowed"},
		{"DELETE", "/api/v1/system/info", 405, "method_not_allowed"},
		{"PROPFIND", "/api/healthz", 405, "method_not_allowed"}, // unknown to chi
		{"GET", "/api/v1/annotation/tasks", 501, "not_implemented"},
		{"POST", "/api/v1/annotation/tasks/42/items", 501, "not_implemented"},
		{"GET", "/api/v1/training/jobs/", 501, "not_implemented"},
		{"PUT", "/api/v1/annotation/tasks/7", 501, "not_implemented"},
		{"GET", "/api/v1/annotation/labels", 501, "not_implemented"},
		{"GET", "/api/v1/training/jobs", 501, "not_implemented"},
		{"DELETE", "/api/v1/evaluations/3/report", 501, "not_implemented"},
		{"PATCH", "/api/v1/models", 501, "not_implemented"},
		// Without a web directory, non-API paths are JSON 404s too.
		{"GET", "/", 404, "not_found"},
		{"GET", "/datasets/versions", 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := serve(h, tt.method, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != tt.code || body.Error.Message == "" {
				t.Errorf("error = %+v, want code %q with a message", body.Error, tt.code)
			}
		})
	}
}

func TestMethodNotAllowedListsAllowedMethods(t *testing.T) {
	for _, web := range []http.Handler{nil, http.NotFoundHandler()} {
		h := newTestHandler(t, fakeDB{}, web)
		for _, tt := range []struct{ method, path string }{
			{"POST", "/api/healthz"},
			{"DELETE", "/api/v1/system/info"},
			{"PROPFIND", "/api/healthz"},
		} {
			rec := serve(h, tt.method, tt.path)
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s (web=%v): Allow = %q, want \"GET, HEAD\"", tt.method, tt.path, web != nil, got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s %s (web=%v): Cache-Control = %q", tt.method, tt.path, web != nil, got)
			}
		}
	}
}

func TestUnknownMethodOnWebPathReachesWebHandler(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	if rec := serve(newTestHandler(t, fakeDB{}, spa), "PROPFIND", "/datasets"); rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the web handler's answer", rec.Code)
	}
}

func TestNotImplementedMessage(t *testing.T) {
	rec := serve(newTestHandler(t, fakeDB{}, nil), http.MethodGet, "/api/v1/models")
	if got, want := rec.Body.String(), `{"error":{"code":"not_implemented","message":"该功能尚未实现"}}`+"\n"; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestReadyz(t *testing.T) {
	rec := serve(newTestHandler(t, fakeDB{}, nil), http.MethodGet, "/api/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	ok := decode[readiness](t, rec)
	if ok.Status != "ok" || ok.Checks["database"].Status != "ok" || ok.Checks["storage"].Status != "ok" {
		t.Errorf("readiness = %+v", ok)
	}

	rec = serve(newTestHandler(t, fakeDB{err: errors.New("connection refused")}, nil), http.MethodGet, "/api/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	bad := decode[readiness](t, rec)
	if bad.Status != "unavailable" || bad.Checks["database"].Status != "unavailable" ||
		bad.Checks["database"].Error != "connection refused" || bad.Checks["storage"].Status != "ok" {
		t.Errorf("readiness = %+v", bad)
	}
}

func TestSystemInfo(t *testing.T) {
	rec := serve(newTestHandler(t, fakeDB{}, nil), http.MethodGet, "/api/v1/system/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	info := decode[map[string]any](t, rec)
	for _, k := range []string{"name", "version", "commit", "build_time", "go_version", "started_at", "uptime_seconds", "database", "storage"} {
		if _, ok := info[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	if info["name"] != "trainMaster" || info["uptime_seconds"].(float64) < 89 {
		t.Errorf("info = %v", info)
	}
	if _, err := time.Parse(time.RFC3339, info["started_at"].(string)); err != nil {
		t.Errorf("started_at: %v", err)
	}
	db := info["database"].(map[string]any)
	if db["status"] != "ok" || db["server_version"] != "17.6" || db["schema_version"] != float64(1) {
		t.Errorf("database = %v", db)
	}
	st := info["storage"].(map[string]any)
	dirs := st["directories"].([]any)
	if len(dirs) != len(storage.Keys) {
		t.Fatalf("directories = %v", dirs)
	}
	first := dirs[0].(map[string]any)
	if first["key"] != "datasets" || first["writable"] != true || !strings.HasSuffix(first["path"].(string), "datasets") {
		t.Errorf("directories[0] = %v", first)
	}
	for _, k := range []string{"root", "total_bytes", "free_bytes", "used_bytes"} {
		if _, ok := st[k]; !ok {
			t.Errorf("storage is missing %q", k)
		}
	}
}

func TestSystemInfoDatabaseDown(t *testing.T) {
	rec := serve(newTestHandler(t, fakeDB{err: errors.New("boom")}, nil), http.MethodGet, "/api/v1/system/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the database is down", rec.Code)
	}
	db := decode[map[string]any](t, rec)["database"].(map[string]any)
	if db["status"] != "unavailable" || db["error"] != "boom" {
		t.Errorf("database = %v", db)
	}
	if _, ok := db["schema_version"]; ok {
		t.Errorf("schema_version should be omitted when unknown: %v", db)
	}
}

func TestWebFallback(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "spa:"+r.URL.Path) })
	h := newTestHandler(t, fakeDB{}, spa)
	if rec := serve(h, http.MethodGet, "/datasets/versions"); rec.Body.String() != "spa:/datasets/versions" {
		t.Errorf("non-API path not delegated to the web handler: %q", rec.Body)
	}
	if rec := serve(h, http.MethodGet, "/api/nope"); rec.Code != http.StatusNotFound || strings.HasPrefix(rec.Body.String(), "spa:") {
		t.Errorf("unknown API path must not reach the web handler: %d %q", rec.Code, rec.Body)
	}
}

func TestClientIP(t *testing.T) {
	proxy := []string{"172.18.0.1/32"} // e.g. nginx reaching the app via the Docker gateway
	tests := []struct {
		trusted           []string
		remote, xff, want string
	}{
		// Nothing configured: X-Forwarded-For is ignored, even from private ranges.
		{nil, "192.168.1.20:5000", "", "192.168.1.20"},
		{nil, "192.168.1.20:5000", "10.0.0.1, 8.8.8.8", "192.168.1.20"},
		{nil, "127.0.0.1:5000", "1.2.3.4", "127.0.0.1"},
		// A configured proxy is believed; other peers still cannot spoof.
		{proxy, "172.18.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{proxy, "172.18.0.1:5000", "198.51.100.1, 172.18.0.1", "198.51.100.1"},
		{proxy, "10.1.2.3:5000", "10.9.9.9", "10.1.2.3"},
	}
	for _, tt := range tests {
		var got string
		h := NewHandler(Options{
			Logger:         slog.New(slog.DiscardHandler),
			TrustedProxies: tt.trusted,
			Web: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = clientIP(r)
			}),
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.remote
		if tt.xff != "" {
			req.Header.Set("X-Forwarded-For", tt.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != tt.want {
			t.Errorf("clientIP(trusted=%v, remote=%s, xff=%q) = %q, want %q", tt.trusted, tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestRecoverer(t *testing.T) {
	h := recoverer(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	rec := serve(h, http.MethodGet, "/api/anything")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := decode[errorBody](t, rec); body.Error.Code != "internal_error" {
		t.Errorf("error = %+v", body.Error)
	}
}

func TestNewListEncodesEmptyItems(t *testing.T) {
	b, _ := json.Marshal(NewList[string](nil, 0))
	if string(b) != `{"items":[],"total":0}` {
		t.Errorf("got %s", b)
	}
}
