package reviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// The local API. Below /api/review it answers like the platform's
// /api/v1/reviews/{id} (the same paths, parameters, JSON, errors and
// codes; see internal/httpapi/reviews.go), for the review editor. The rest
// is the local reviewer's own (session, browsing, opening, export, quit).

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在")
			return
		}
		s.serveWeb(w, r)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持该请求方法")
	})
	r.Route("/api", func(r chi.Router) {
		r.Get("/session", s.getSession)
		r.Get("/browse", s.browse)
		r.Post("/open", s.open)
		r.Post("/open/cancel", s.cancelOpen)
		r.Post("/close", s.close)
		r.Post("/export", s.export)
		r.Post("/reveal", s.reveal)
		r.Post("/recent/remove", s.removeRecent)
		r.Post("/closing", s.closing)
		r.Post("/quit", s.quit)
		r.Route("/review", func(r chi.Router) {
			r.Get("/", s.getSummary)
			r.Get("/images", s.listImages)
			r.Get("/images/{imageID}", s.getImage)
			r.Get("/images/{imageID}/neighbors", s.getNeighbors)
			r.Put("/images/{imageID}/annotation", s.saveAnnotation)
			r.Put("/images/{imageID}/status", s.setStatus)
			r.Get("/images/{imageID}/file", s.serveImage)
			r.Get("/images/{imageID}/thumbnail", s.serveThumbnail)
			r.Get("/labels", s.listLabels)
			r.Get("/classes", s.listClasses)
		})
	})
	return r
}

// ---- responses ----

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// list is the response of collections, as on the platform.
type list[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func newList[T any](items []T, total int) list[T] { return list[T]{Items: nonNil(items), Total: total} }

// fail writes the response for an error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var re *Error
	if errors.As(err, &re) {
		status, code := errorStatus(re.Kind)
		if status >= 500 {
			s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		}
		writeError(w, status, code, re.Message)
		return
	}
	var de *dataset.Error
	if errors.As(err, &de) {
		switch {
		case errors.Is(err, dataset.ErrUnsupportedImage):
			writeError(w, http.StatusUnprocessableEntity, "unsupported_image", de.Message)
		case errors.Is(err, dataset.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", de.Message)
		default:
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", de.Message)
		}
		return
	}
	if r.Context().Err() != nil {
		return // the client went away
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "本地审图程序出错："+err.Error())
}

func errorStatus(kind error) (int, string) {
	switch kind {
	case ErrNotFound:
		return http.StatusNotFound, "not_found"
	case ErrInvalid:
		return http.StatusUnprocessableEntity, "validation_failed"
	case ErrBadRequest:
		return http.StatusBadRequest, "bad_request"
	case ErrConflict:
		return http.StatusConflict, "conflict"
	case ErrAnnotationChanged:
		return http.StatusConflict, "annotation_conflict"
	case ErrForbidden:
		return http.StatusForbidden, "forbidden"
	case ErrNoSpace:
		return http.StatusInsufficientStorage, "insufficient_storage"
	case ErrUnsupported:
		return http.StatusNotImplemented, "not_implemented"
	}
	return http.StatusInternalServerError, "internal_error"
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "bad_request", message)
}

const (
	maxJSONBody       = 1 << 20
	maxAnnotationBody = 8 << 20 // as on the platform
)

// decodeJSON reads a JSON body of at most limit bytes into v; it writes
// the error response and returns false on failure. An empty body is {}.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("trailing data after the JSON value")
	}
	if err != nil && err.Error() != "EOF" {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "请求内容过大")
		} else {
			badRequest(w, "请求格式不正确")
		}
		return false
	}
	return true
}

// imageID parses {imageID}; anything but a positive decimal ID is an image
// that does not exist.
func imageID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := chi.URLParam(r, "imageID")
	id, err := strconv.ParseInt(v, 10, 64)
	if v == "" || len(v) > 18 || strings.Trim(v, "0123456789") != "" || err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "图片不存在")
		return 0, false
	}
	return id, true
}

// ---- the review (the platform's API) ----

func (s *Server) getSummary(w http.ResponseWriter, r *http.Request) {
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.Summary())
}

// enumParam parses an optional query parameter that must be one of values
// (the first is the default).
func enumParam(w http.ResponseWriter, r *http.Request, name string, values ...string) (string, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return values[0], true
	}
	for _, allowed := range values {
		if v == allowed {
			return v, true
		}
	}
	badRequest(w, "参数 "+name+" 无效，可选值："+strings.Join(values, "、"))
	return "", false
}

// intParam parses an optional integer query parameter within [lo, hi].
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		badRequest(w, "参数 "+name+" 无效，取值范围为 "+strconv.Itoa(lo)+"–"+strconv.Itoa(hi))
		return 0, false
	}
	return n, true
}

// imageFilters parses the filters of the image list and neighbors.
func imageFilters(w http.ResponseWriter, r *http.Request) (ImageQuery, bool) {
	var q ImageQuery
	var ok bool
	if q.Status, ok = enumParam(w, r, "status", filterAll, reviewfile.StatusPending, reviewfile.StatusApproved,
		reviewfile.StatusNeedsSample, filterReviewed); !ok {
		return q, false
	}
	if v := r.URL.Query().Get("dataset_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "参数 dataset_id 无效，应为数据集 ID")
			return q, false
		}
		q.DatasetID = id
	}
	if q.Label = r.URL.Query().Get("label"); !utf8.ValidString(q.Label) || strings.ContainsRune(q.Label, 0) {
		badRequest(w, "参数 label 无效：包含无效字符")
		return q, false
	}
	if q.Search = r.URL.Query().Get("q"); !utf8.ValidString(q.Search) || strings.ContainsRune(q.Search, 0) {
		badRequest(w, "参数 q 无效：搜索内容包含无效字符")
		return q, false
	}
	if q.Edited, ok = enumParam(w, r, "edited", filterAll, filterEdited, filterUnedited); !ok {
		return q, false
	}
	if q.Boxes, ok = enumParam(w, r, "boxes", filterAll, filterWith, filterWithout); !ok {
		return q, false
	}
	if q.Inference, ok = enumParam(w, r, "inference", filterAll, filterFailed); !ok {
		return q, false
	}
	return q, true
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	q, ok := imageFilters(w, r)
	if !ok {
		return
	}
	if q.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<30); !ok {
		return
	}
	if q.Limit, ok = intParam(w, r, "limit", defaultImageLimit, 1, maxImageLimit); !ok {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, total := p.ListImages(q)
	writeJSON(w, http.StatusOK, newList(items, total))
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := p.GetImage(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) getNeighbors(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	q, ok := imageFilters(w, r)
	if !ok {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := p.ImageNeighbors(id, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type saveAnnotationRequest struct {
	Objects     *[]dataset.AnnotationObject `json:"objects"`
	BaseVersion *string                     `json:"base_version"`
	Force       bool                        `json:"force"`
}

func (s *Server) saveAnnotation(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var req saveAnnotationRequest
	if !decodeJSON(w, r, &req, maxAnnotationBody) {
		return
	}
	switch {
	case req.Objects == nil:
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "缺少 objects（目标列表，没有目标时为 []）")
		return
	case req.BaseVersion == nil && !req.Force:
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "缺少 base_version（开始编辑时的标注版本，没有标注时为空字符串）")
		return
	}
	in := SaveInput{Objects: *req.Objects, Force: req.Force}
	if req.BaseVersion != nil {
		in.BaseVersion = *req.BaseVersion
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := p.SaveAnnotation(id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req, maxJSONBody) {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := p.SetStatus(id, req.Status)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) serveImage(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := p.OpenImage(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", contentDisposition("inline", f.name))
	serveImageFile(w, r, f)
}

func (s *Server) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	size, ok := enumParam(w, r, "size", dataset.ThumbSmall, dataset.ThumbLarge)
	if !ok {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := s.thumbs.open(r.Context(), p, id, size)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serveImageFile(w, r, f)
}

// serveImageFile sends an image as the platform does: a URL whose v is the
// file's version never changes, so the browser may keep it.
func serveImageFile(w http.ResponseWriter, r *http.Request, f *imageFile) {
	defer f.file.Close()
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	if v := r.URL.Query().Get("v"); v != "" && v == f.version {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	http.ServeContent(w, r, f.name, f.modTime, f.file)
}

// contentDisposition keeps non-ASCII file names (RFC 6266 / RFC 5987).
func contentDisposition(kind, name string) string {
	var fallback, encoded strings.Builder
	for _, c := range name {
		if c < 0x80 && c >= 0x20 && c != '"' && c != '\\' && c != 0x7f {
			fallback.WriteRune(c)
		} else {
			fallback.WriteByte('_')
		}
	}
	for _, b := range []byte(name) {
		if ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9') || strings.IndexByte("!#$&+-.^_`|~", b) >= 0 {
			encoded.WriteByte(b)
		} else {
			fmt.Fprintf(&encoded, "%%%02X", b)
		}
	}
	return kind + `; filename="` + fallback.String() + `"; filename*=UTF-8''` + encoded.String()
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	labels := p.ListLabels()
	writeJSON(w, http.StatusOK, newList(labels, len(labels)))
}

func (s *Server) listClasses(w http.ResponseWriter, r *http.Request) {
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.Classes())
}

// ---- local ----

func (s *Server) getSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Session())
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if !utf8.ValidString(dir) || strings.ContainsRune(dir, 0) {
		badRequest(w, "参数 path 无效")
		return
	}
	b, err := browse(dir, s.opts.Places())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

type pathRequest struct {
	Path string `json:"path"`
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if !decodeJSON(w, r, &req, maxJSONBody) {
		return
	}
	o, err := s.Open(req.Path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]Opening{"opening": o})
}

func (s *Server) cancelOpen(w http.ResponseWriter, _ *http.Request) {
	s.CancelOpen()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) close(w http.ResponseWriter, _ *http.Request) {
	s.Close()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := p.Export()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("results exported", "path", res.Path, "images", res.Images, "status_changes", res.StatusChanges,
		"annotation_changes", res.AnnotationChanges, "skipped", res.Skipped)
	s.putRecent(p.Info(), false)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"` // results or folder
	}
	if !decodeJSON(w, r, &req, maxJSONBody) {
		return
	}
	p, err := s.current()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var target string
	switch req.Target {
	case "results":
		info := p.Info()
		if info.LastExport == nil {
			s.fail(w, r, &Error{Kind: ErrConflict, Message: "还没有导出审图结果"})
			return
		}
		target = info.LastExport.Path
	case "folder":
		target = p.dir
	default:
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "target 必须是 results 或 folder")
		return
	}
	if err := s.opts.Reveal(target); err != nil {
		s.fail(w, r, newError(ErrUnsupported, "无法打开文件管理器：%v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeRecent(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if !decodeJSON(w, r, &req, maxJSONBody) {
		return
	}
	if err := s.recent.Remove(req.Path); err != nil {
		s.fail(w, r, newError(ErrIO, "无法保存最近打开的审图包：%v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) closing(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Closing != nil {
		s.opts.Closing()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) quit(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if s.opts.Quit != nil {
		go s.opts.Quit()
	}
}
