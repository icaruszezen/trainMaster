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

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/hostinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
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
	// Datasets serves /api/v1/datasets. If nil, those routes are not
	// mounted (tests of other areas need no database).
	Datasets *dataset.Service
	// Projects serves /api/v1/projects and the projects of a dataset. It
	// requires Datasets; if either is nil, those routes are not mounted.
	Projects *project.Service
	// Compute serves /api/v1/compute (training nodes). If nil, those
	// routes are not mounted.
	Compute *compute.Service
	// Models serves /api/v1/models (the model repository) and the models
	// of a project. The conversion tasks of the training nodes
	// (/api/v1/compute/agent/tasks, /model-exports) also need Compute,
	// which identifies the nodes. If nil, those routes are not mounted.
	Models *model.Service
	// Reviews serves /api/v1/reviews (model reviews) and the review
	// records of a dataset (/api/v1/datasets/{id}/reviews, with Datasets).
	// The inference calls of the training nodes
	// (/api/v1/compute/agent/review-inferences) also need Compute. If nil,
	// those routes are not mounted.
	Reviews *review.Service
	// ReviewerTool is the path of the bundled local reviewer for Windows
	// (TM_REVIEWER_TOOL), served by GET /api/v1/reviews/tool/download.
	// Empty, or a file that is not there: none is bundled.
	ReviewerTool string
	// Host describes the server machine for GET /api/v1/compute/server,
	// which is mounted only when Host and Compute are both set.
	Host *hostinfo.Host
	// NodeToken is the bearer token training nodes must send to
	// /api/v1/compute/agent/* (TM_NODE_TOKEN). Empty: none is required.
	NodeToken string
	// LocalTrainerMode is the mode of the training node on the server
	// machine (TM_TRAINER_MODE: auto, gpu, cpu or off), which
	// GET /api/v1/compute/server reports so the page's hints for this
	// machine fit it. Empty: not known (null).
	LocalTrainerMode string
}

// plannedModules are API areas reserved for upcoming features. Each answers
// 501 for any method and sub-path until a real router replaces its entry.
var plannedModules = []string{
	"/annotation/tasks",
	"/annotation/labels",
	"/training/jobs",
	"/evaluations",
}

type server struct {
	log       *slog.Logger
	db        Database
	storage   *storage.Storage
	datasets  *dataset.Service
	projects  *project.Service // nil unless datasets is set
	compute   *compute.Service
	models    *model.Service
	reviews   *review.Service
	host      *hostinfo.Host
	nodeToken string
	startedAt time.Time
	// localTrainerMode is Options.LocalTrainerMode.
	localTrainerMode string
	// reviewerTool is Options.ReviewerTool.
	reviewerTool string
}

// NewHandler returns the root HTTP handler.
func NewHandler(o Options) http.Handler {
	s := &server{log: o.Logger, db: o.DB, storage: o.Storage, datasets: o.Datasets, compute: o.Compute,
		models: o.Models, reviews: o.Reviews, reviewerTool: o.ReviewerTool, host: o.Host, nodeToken: o.NodeToken,
		localTrainerMode: o.LocalTrainerMode, startedAt: o.StartedAt}
	if o.Datasets != nil {
		s.projects = o.Projects
	}

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
			if s.datasets != nil {
				r.Route("/datasets", s.datasetRoutes)
			}
			if s.projects != nil {
				r.Route("/projects", s.projectRoutes)
			}
			if s.compute != nil {
				r.Route("/compute", s.computeRoutes)
			}
			if s.models != nil {
				r.Route("/models", s.modelRoutes)
			}
			if s.reviews != nil {
				r.Route("/reviews", s.reviewRoutes)
			}
			if s.datasets != nil {
				r.Route("/recycle-bin", s.recycleBinRoutes)
			}

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
