// Package httpapi wires the HTTP routes, middleware and JSON handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Database is what the handlers need from the database layer;
// *database.DB implements it.
type Database interface {
	Ping(ctx context.Context) error
	ServerVersion(ctx context.Context) (string, error)
	SchemaVersion(ctx context.Context) (int64, error)
}

// Options are the dependencies of the HTTP handler.
type Options struct {
	Logger    *slog.Logger
	DB        Database
	Storage   *storage.Storage
	StartedAt time.Time
	// Web serves every non-/api path (the built frontend). If nil, those
	// paths answer 404 JSON, e.g. during development when Vite serves the UI.
	Web http.Handler
	// TrustedProxies are CIDR prefixes of reverse proxies whose
	// X-Forwarded-For header determines the client IP. Empty: the header
	// is ignored and the TCP peer is the client.
	TrustedProxies []string
}

// plannedModules are API areas reserved for upcoming features. Each answers
// 501 for any method and sub-path until a real router replaces its entry.
var plannedModules = []string{
	"/datasets",
	"/annotation/tasks",
	"/annotation/labels",
	"/training/jobs",
	"/evaluations",
	"/models",
}

type server struct {
	log       *slog.Logger
	db        Database
	storage   *storage.Storage
	startedAt time.Time
}

// NewHandler returns the root HTTP handler.
func NewHandler(o Options) http.Handler {
	s := &server{log: o.Logger, db: o.DB, storage: o.Storage, startedAt: o.StartedAt}

	r := chi.NewRouter()
	r.Use(requestID)
	if len(o.TrustedProxies) > 0 {
		r.Use(middleware.ClientIPFromXFF(o.TrustedProxies...))
	}
	r.Use(
		accessLog(o.Logger),
		recoverer(o.Logger),
		middleware.GetHead,
	)

	r.Route("/api", func(r chi.Router) {
		r.Use(noStore)
		// Set before the sub-routes so they inherit the JSON responders.
		r.NotFound(notFound)
		r.MethodNotAllowed(methodNotAllowed)

		r.Get("/healthz", s.healthz)
		r.Get("/readyz", s.readyz)

		r.Route("/v1", func(r chi.Router) {
			r.Get("/system/info", s.systemInfo)

			for _, p := range plannedModules {
				r.HandleFunc(p, notImplemented)
				r.HandleFunc(p+"/*", notImplemented)
			}
		})
	})

	if o.Web != nil {
		r.NotFound(o.Web.ServeHTTP)
	} else {
		r.NotFound(notFound)
	}
	// chi rejects methods it does not know (e.g. PROPFIND) at the root,
	// before routing reaches /api.
	apiMethodNotAllowed := noStore(http.HandlerFunc(methodNotAllowed))
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if o.Web != nil && !isAPIPath(req.URL.Path) {
			o.Web.ServeHTTP(w, req) // answers 405 itself
			return
		}
		apiMethodNotAllowed.ServeHTTP(w, req)
	})
	return r
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}
