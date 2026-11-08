package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

// Data version API (数据版本): /api/v1/data-versions. The JSON shapes are
// defined by the types in internal/dataversion; this file maps HTTP onto
// dataversion.Service. Restoring from the recycle bin is in recycle.go.

func (s *server) dataVersionRoutes(r chi.Router) {
	r.Get("/", s.listDataVersions)
	r.Post("/", s.createDataVersion)
	r.Post("/preview", s.previewDataVersion) // a static segment: before {dataVersionID}
	r.Route("/{dataVersionID}", func(r chi.Router) {
		r.Get("/", s.getDataVersion)
		r.Patch("/", s.updateDataVersion)
		r.Delete("/", s.deleteDataVersion)
		r.Post("/retry", s.retryDataVersion)
		r.Get("/images", s.listDataVersionImages)
		r.Get("/images/{imageID}", s.getDataVersionImage)
		r.Get("/images/{imageID}/file", s.serveDataVersionImage)
		r.Get("/images/{imageID}/thumbnail", s.serveDataVersionThumbnail)
		r.Post("/exports", s.createDataVersionExport)
		r.Get("/exports/{token}", s.downloadDataVersionExport) // HEAD too (middleware.GetHead)
	})
}

// dataVersionError writes the response for an error returned by
// dataversion.Service, which also passes on errors of package project
// (restoring a version restores its project) and package dataset.
func (s *server) dataVersionError(w http.ResponseWriter, r *http.Request, err error) {
	var de *dataversion.Error
	if errors.As(err, &de) {
		status, code := dataVersionErrorStatus(de.Kind)
		writeError(w, status, code, de.Message)
		return
	}
	var pe *project.Error
	if errors.As(err, &pe) {
		s.projectError(w, r, err)
		return
	}
	s.datasetError(w, r, err)
}

func dataVersionErrorStatus(kind error) (int, string) {
	switch kind {
	case dataversion.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case dataversion.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case dataversion.ErrBadRequest:
		return http.StatusBadRequest, codeBadRequest
	case dataversion.ErrConflict:
		return http.StatusConflict, codeConflict
	case dataversion.ErrNameTaken:
		return http.StatusConflict, codeNameTaken
	case dataversion.ErrInsufficientStorage:
		return http.StatusInsufficientStorage, codeInsufficientStorage
	case dataversion.ErrUnsupportedImage:
		return http.StatusUnprocessableEntity, codeUnsupportedImage
	}
	return http.StatusInternalServerError, codeInternal
}

const msgDataVersionNotFound = "数据版本不存在（可能已被删除）"

// dataVersionID parses {dataVersionID}. It answers 404 for invalid IDs.
func dataVersionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "dataVersionID", msgDataVersionNotFound)
}

// dataVersionImageIDs parses {dataVersionID} and {imageID}.
func dataVersionImageIDs(w http.ResponseWriter, r *http.Request) (id, imageID int64, ok bool) {
	if id, ok = dataVersionID(w, r); !ok {
		return 0, 0, false
	}
	imageID, ok = pathIDOr404(w, r, "imageID", "图片不存在")
	return id, imageID, ok
}

// listDataVersions lists the versions in use, newest first, optionally of
// one project (?project_id=). Not paginated.
func (s *server) listDataVersions(w http.ResponseWriter, r *http.Request) {
	var f dataversion.ListFilter
	if v := r.URL.Query().Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "参数 project_id 无效，应为项目 ID")
			return
		}
		f.ProjectID = id
	}
	items, err := s.dataVersions.List(r.Context(), f)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) previewDataVersion(w http.ResponseWriter, r *http.Request) {
	var in dataversion.CreateInput
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := s.dataVersions.Preview(r.Context(), in)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) createDataVersion(w http.ResponseWriter, r *http.Request) {
	var in dataversion.CreateInput
	if !decodeJSON(w, r, &in) {
		return
	}
	v, err := s.dataVersions.Create(r.Context(), in)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getDataVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	v, err := s.dataVersions.Get(r.Context(), id)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type updateDataVersionRequest struct {
	Note *string `json:"note"`
}

func (s *server) updateDataVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	var req updateDataVersionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.dataVersions.Update(r.Context(), id, dataversion.UpdateInput{Note: req.Note})
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteDataVersion moves a ready version to the recycle bin and removes
// one in another state (a build is stopped).
func (s *server) deleteDataVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	if err := s.dataVersions.Delete(r.Context(), id); err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) retryDataVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	v, err := s.dataVersions.Retry(r.Context(), id)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// textParam returns an optional text query parameter, which PostgreSQL
// must be able to store (valid UTF-8 without NUL bytes, like searchParam).
func textParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v := r.URL.Query().Get(name)
	if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		badRequest(w, "参数 "+name+" 无效：包含无效字符")
		return "", false
	}
	return v, true
}

// listDataVersionImages is a page of the images of a ready version, in
// their order: ?split=&label=&dataset_id=&augmented=&q=&offset=&limit=.
func (s *server) listDataVersionImages(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var f dataversion.ImageFilter
	if f.Split = q.Get("split"); f.Split != "" && !slices.Contains(dataversion.Splits, f.Split) {
		badRequest(w, "参数 split 无效，可选值："+strings.Join(dataversion.Splits, "、"))
		return
	}
	if f.Label, ok = textParam(w, r, "label"); !ok {
		return
	}
	if v := q.Get("dataset_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "参数 dataset_id 无效，应为数据集 ID")
			return
		}
		f.DatasetID = n
	}
	if v := q.Get("augmented"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "参数 augmented 无效，应为 true 或 false")
			return
		}
		f.Augmented = &b
	}
	if f.Search, ok = searchParam(w, r); !ok {
		return
	}
	if f.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<31-1); !ok {
		return
	}
	if f.Limit, ok = intParam(w, r, "limit", 60, 1, 200); !ok {
		return
	}
	items, total, err := s.dataVersions.ListImages(r.Context(), id, f)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, total))
}

func (s *server) getDataVersionImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := dataVersionImageIDs(w, r)
	if !ok {
		return
	}
	img, err := s.dataVersions.GetImage(r.Context(), id, imageID)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (s *server) serveDataVersionImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := dataVersionImageIDs(w, r)
	if !ok {
		return
	}
	f, err := s.dataVersions.OpenImage(r.Context(), id, imageID)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", inlineDisposition(f.Name))
	serveDataVersionFile(w, r, f)
}

func (s *server) serveDataVersionThumbnail(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := dataVersionImageIDs(w, r)
	if !ok {
		return
	}
	size, ok := enumParam(w, r, "size", dataset.ThumbSmall, dataset.ThumbLarge)
	if !ok {
		return
	}
	f, err := s.dataVersions.OpenThumbnail(r.Context(), id, imageID, size)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	serveDataVersionFile(w, r, f)
}

// serveDataVersionFile sends an image or thumbnail of a version with Range
// and conditional request support. A version never changes once ready, so
// browsers may cache its files for good (this overrides the no-store of
// the API).
func serveDataVersionFile(w http.ResponseWriter, r *http.Request, f *dataversion.ImageFile) {
	defer f.File.Close()
	fi, err := f.File.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, msgInternalError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, f.Name, fi.ModTime(), f.File)
}

type createDataVersionExportRequest struct {
	Format           string `json:"format"` // yolo, voc or coco
	IncludeAugmented bool   `json:"include_augmented"`
}

func (s *server) createDataVersionExport(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	var req createDataVersionExportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.dataVersions.CreateExport(r.Context(), id, dataversion.ExportInput{Format: req.Format,
		IncludeAugmented: req.IncludeAugmented})
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, exportResponse{
		URL:      fmt.Sprintf("/api/v1/data-versions/%d/exports/%s", id, t.Token),
		FileName: t.FileName, ExpiresAt: t.ExpiresAt,
	})
}

// downloadDataVersionExport streams a prepared download, like
// downloadDatasetExport: no Content-Length; a failure midway aborts the
// connection so that the browser does not keep a truncated zip.
func (s *server) downloadDataVersionExport(w http.ResponseWriter, r *http.Request) {
	id, ok := dataVersionID(w, r)
	if !ok {
		return
	}
	token := chi.URLParam(r, "token")
	if token == "" || len(token) > 64 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" {
		notFoundMessage(w, "下载链接已失效，请重新下载")
		return
	}
	e, err := s.dataVersions.OpenExport(r.Context(), id, token)
	if err != nil {
		s.dataVersionError(w, r, err)
		return
	}
	defer e.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", attachmentDisposition(e.FileName))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if err := e.Write(r.Context(), w); err != nil {
		if r.Context().Err() == nil {
			s.log.Error("data version download failed", "data_version_id", id, "request_id", RequestID(r.Context()),
				"err", err)
		}
		panic(http.ErrAbortHandler)
	}
}
