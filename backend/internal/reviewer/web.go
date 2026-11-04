package reviewer

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The page: the frontend build is embedded in the program
// (cmd/trainmaster-reviewer/web/dist, built by npm run build:reviewer):
// reviewer.html for every page path (the frontend routes itself), the
// hashed assets with long caching. A program built without the frontend
// serves the committed placeholder.html, which says how to build it.

const (
	webDist        = "dist"
	webIndex       = "reviewer.html"
	webPlaceholder = "placeholder.html"
)

// serveWeb serves the frontend.
func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持该请求方法")
		return
	}
	web := s.opts.Web
	if web == nil {
		web = emptyFS{}
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != webIndex {
		if data, err := fs.ReadFile(web, path.Join(webDist, name)); err == nil {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
			return
		}
		if path.Ext(name) != "" { // a missing file, not a page of the frontend
			http.NotFound(w, r)
			return
		}
	}
	page, err := fs.ReadFile(web, path.Join(webDist, webIndex))
	if err != nil {
		if page, err = fs.ReadFile(web, webPlaceholder); err != nil {
			page = []byte(`<!doctype html><meta charset="utf-8"><title>trainMaster 本地审图</title><p>本地审图程序的界面没有构建。</p>`)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, webIndex, time.Time{}, bytes.NewReader(page))
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
