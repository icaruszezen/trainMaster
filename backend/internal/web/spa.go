// Package web serves the built single-page frontend (frontend/dist).
package web

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	indexFile = "index.html"
	// assetsDir holds Vite's content-hashed bundles, which never change
	// under the same name and can be cached forever.
	assetsDir = "assets/"

	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// Handler serves files from a directory and falls back to index.html for
// unknown paths so client-side routes like /datasets/versions work on
// reload. Directory listings are never produced.
type Handler struct {
	fsys fs.FS
}

// New returns a Handler for dir, which must contain index.html.
func New(dir string) (*Handler, error) {
	fi, err := os.Stat(filepath.Join(dir, indexFile))
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("web directory %q has no %s (build the frontend first: cd frontend && npm run build)", dir, indexFile)
	}
	return &Handler{fsys: os.DirFS(dir)}, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	// path.Clean on a rooted path removes every "..", so the name can only
	// point inside the directory; fs.FS additionally rejects invalid names.
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexFile
	}

	if h.serveFile(w, r, name) {
		return
	}
	if strings.HasPrefix(name+"/", assetsDir) {
		// A missing bundle must not be answered with index.html: the
		// browser would cache HTML in place of JavaScript or CSS.
		http.NotFound(w, r)
		return
	}
	if !h.serveFile(w, r, indexFile) {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
	}
}

// serveFile serves a regular file and reports whether it existed.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}

	if strings.HasPrefix(name, assetsDir) {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheRevalidate)
	}
	http.ServeContent(w, r, name, fi.ModTime(), rs)
	return true
}
