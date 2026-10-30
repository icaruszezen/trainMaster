package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// API conventions:
//   - success: the resource object itself, or List for collections
//   - failure: {"error": {"code": "not_found", "message": "接口不存在"}}
//
// code is a stable machine-readable identifier; message is shown to users
// and therefore written in Chinese.

// Error codes shared by all endpoints.
const (
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeNotImplemented   = "not_implemented"
	codeInternal         = "internal_error"
)

// List is the response body for collection endpoints.
type List[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// NewList builds a List, encoding a nil slice as [] rather than null.
func NewList[T any](items []T, total int) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, Total: total}
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding error can only be
	// a broken connection or a programming error; nothing useful to do.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "接口不存在")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", strings.Join(allowedMethods(r), ", "))
	writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "不支持该请求方法")
}

// allowedMethods lists the methods the router accepts for r's path. A
// custom 405 handler replaces chi's own Allow header, so probe the root
// router (which includes all sub-routers) instead.
func allowedMethods(r *http.Request) []string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil || rctx.Routes == nil {
		return nil
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	matches := func(method string) bool {
		return rctx.Routes.Match(chi.NewRouteContext(), method, path)
	}
	var allowed []string
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		// middleware.GetHead answers HEAD for every GET route.
		if matches(m) || (m == http.MethodHead && matches(http.MethodGet)) {
			allowed = append(allowed, m)
		}
	}
	return allowed
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, codeNotImplemented, "该功能尚未实现")
}
