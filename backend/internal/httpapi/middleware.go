package httpapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID returns the ID assigned to the request by the middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID reuses a sane incoming X-Request-ID (e.g. set by a reverse
// proxy) or generates one, and echoes it in the response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = rand.Text()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' { // printable ASCII without spaces
			return false
		}
	}
	return true
}

// accessLog logs one line per request. Health probes, and requests that
// machines repeat every few seconds when they succeed, are logged at debug
// level to keep container logs readable.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case status >= 500 && status != http.StatusNotImplemented: // 501 marks planned modules
				level = slog.LevelError
			case r.URL.Path == "/api/healthz" || r.URL.Path == "/api/readyz":
				level = slog.LevelDebug
			case status < 400 && isPolling(r):
				level = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Float64("duration_ms", milliseconds(time.Since(start))),
				slog.String("client_ip", clientIP(r)),
				slog.String("request_id", RequestID(r.Context())),
			)
		})
	}
}

// isPolling reports whether r is one of the requests that are repeated
// every few seconds: training node heartbeats (each node, all the time)
// and the refreshes of the compute page (while it is open). Logged at info
// level they would push everything else out of the rotated container logs
// within days; node registrations and deletions are logged by package
// compute, and rejected node tokens by requireNodeToken.
func isPolling(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v1/compute/agent/heartbeat":
		return r.Method == http.MethodPost
	case "/api/v1/compute/nodes", "/api/v1/compute/server":
		return r.Method == http.MethodGet
	}
	return false
}

// clientIP returns the address resolved by the ClientIPFromXFF middleware
// (installed only when TM_TRUSTED_PROXIES is set), falling back to the TCP
// peer. It is only as trustworthy as that list: before using it for audit
// logs or access control, make sure only real proxies are listed.
func clientIP(r *http.Request) string {
	if ip := middleware.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// recoverer turns a panic into a logged error and a 500 JSON response.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { // deliberate abort; let net/http handle it
					panic(rec)
				}
				log.Error("panic while handling request",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", RequestID(r.Context()),
					"stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, codeInternal, msgInternalError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// noStore keeps browsers and proxies from caching API responses.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// milliseconds converts d to fractional milliseconds rounded to 0.01 ms.
func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()/10) / 100
}
