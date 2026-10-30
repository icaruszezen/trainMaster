package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestSite lays out a fake frontend build next to a file that must
// never be reachable.
func newTestSite(t *testing.T) *Handler {
	t.Helper()
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	files := map[string]string{
		"secret.txt":                "top secret",
		"dist/index.html":           "<!doctype html><title>trainMaster</title>",
		"dist/favicon.svg":          "<svg/>",
		"dist/assets/index-abc.js":  "console.log(1)",
		"dist/assets/index-abc.css": "body{}",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(dist)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestServesFilesWithCacheHeaders(t *testing.T) {
	h := newTestSite(t)
	tests := []struct {
		path, body, contentType, cache string
	}{
		{"/", "<!doctype html>", "text/html", "no-cache"},
		{"/index.html", "<!doctype html>", "text/html", "no-cache"},
		{"/favicon.svg", "<svg/>", "image/svg+xml", "no-cache"},
		{"/assets/index-abc.js", "console.log(1)", "text/javascript", "public, max-age=31536000, immutable"},
		{"/assets/index-abc.css", "body{}", "text/css", "public, max-age=31536000, immutable"},
	}
	for _, tt := range tests {
		rec := get(h, http.MethodGet, tt.path)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), tt.body) {
			t.Errorf("GET %s = %d %q", tt.path, rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.contentType) {
			t.Errorf("GET %s Content-Type = %q, want %s", tt.path, ct, tt.contentType)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != tt.cache {
			t.Errorf("GET %s Cache-Control = %q, want %q", tt.path, cc, tt.cache)
		}
	}
}

func TestFallsBackToIndex(t *testing.T) {
	h := newTestSite(t)
	for _, p := range []string{"/datasets/versions", "/annotation/workspace/", "/does-not-exist.html"} {
		rec := get(h, http.MethodGet, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>trainMaster") {
			t.Errorf("GET %s = %d %q, want index.html", p, rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
	}
}

func TestMissingAssetIs404(t *testing.T) {
	h := newTestSite(t)
	for _, p := range []string{"/assets/index-old.js", "/assets/", "/assets"} {
		rec := get(h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("GET %s: a 404 must not be cached as immutable", p)
		}
		if strings.Contains(rec.Body.String(), "index-abc.js") {
			t.Errorf("GET %s leaked a directory listing", p)
		}
	}
}

func TestRejectsTraversal(t *testing.T) {
	h := newTestSite(t)
	// net/http decodes %2e and %2f into r.URL.Path, which the handler uses.
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/assets/../../secret.txt", "/..%2fsecret.txt", "/assets/..%2f..%2fsecret.txt"} {
		rec := get(h, http.MethodGet, p)
		if strings.Contains(rec.Body.String(), "top secret") {
			t.Errorf("GET %s escaped the web directory", p)
		}
	}
}

func TestMethods(t *testing.T) {
	h := newTestSite(t)
	if rec := get(h, http.MethodHead, "/"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	rec := get(h, http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST / = %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestNewRequiresIndex(t *testing.T) {
	if _, err := New(t.TempDir()); err == nil {
		t.Error("New should fail without index.html")
	}
}
