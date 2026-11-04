package reviewer

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The server listens on 127.0.0.1 only, but any program or web page on
// the computer can reach that. So it answers only the page the program
// opened:
//
//   - The program opens /?token=<random token>; the server sets the token
//     as a cookie (HttpOnly, SameSite=Strict) and redirects to the URL
//     without it. Every other request must carry the cookie.
//   - The Host header must be the server's own address, which defeats DNS
//     rebinding (a site whose name resolves to 127.0.0.1).
//   - Requests a browser marks as coming from another origin (Origin,
//     Sec-Fetch-Site) are refused: other ports of 127.0.0.1 count as the
//     same site for cookies, so a page there would otherwise send the
//     cookie along.

// tokenParam is the query parameter of the token in the first URL.
const tokenParam = "token"

func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.opts.Hosts) > 0 && !slices.Contains(s.opts.Hosts, r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !s.sameOrigin(r) {
			s.refuse(w, r, http.StatusForbidden, "forbidden", "不接受来自其他网页的请求")
			return
		}
		q := r.URL.Query()
		if tok := q.Get(tokenParam); tok != "" && s.validToken(tok) {
			http.SetCookie(w, &http.Cookie{Name: s.opts.CookieName, Value: s.opts.Token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteStrictMode})
			if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
				q.Del(tokenParam)
				u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, u.String(), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(s.opts.CookieName); err != nil || !s.validToken(c.Value) {
			s.refuse(w, r, http.StatusUnauthorized, "unauthorized",
				"这个页面的链接已失效：请关闭它，从本地审图程序重新打开")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(tok string) bool {
	return s.opts.Token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.Token)) == 1
}

// sameOrigin reports whether a request may come from the page: browsers
// send Sec-Fetch-Site (same-origin, or none when the user opened the URL)
// and, for writes, Origin.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || u.Scheme != "http" {
			return false
		}
	}
	return true
}

// refuse answers a refused request: JSON for the API, a short page
// otherwise.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>trainMaster 本地审图</title>
<style>body{font-family:system-ui,sans-serif;margin:15vh auto;max-width:32rem;padding:0 1rem;color:#333}</style></head>
<body><h1>trainMaster 本地审图</h1><p>` + message + `</p></body></html>`))
}
