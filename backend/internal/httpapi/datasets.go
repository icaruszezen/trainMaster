package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Dataset API: /api/v1/datasets. The JSON shapes are defined by the types in
// internal/dataset; this file maps HTTP onto dataset.Service.

func (s *server) datasetRoutes(r chi.Router) {
	r.Get("/", s.listDatasets)
	r.Post("/", s.createDataset)
	r.Get("/directories", s.listDatasetDirectories)
	r.Route("/{datasetID}", func(r chi.Router) {
		r.Get("/", s.getDataset)
		r.Patch("/", s.updateDataset)
		r.Delete("/", s.deleteDataset)
		r.Get("/images", s.listDatasetImages)
		r.Get("/images/{imageID}", s.getDatasetImage)
		r.Get("/images/{imageID}/file", s.serveDatasetImage)
		r.Get("/images/{imageID}/thumbnail", s.serveDatasetThumbnail)
		r.Get("/labels", s.listDatasetLabels)
		r.Post("/scan", s.scanDataset)
		r.Get("/imports", s.listDatasetImports)
		r.Post("/imports", s.createDatasetImport)
		r.Get("/imports/{importID}", s.getDatasetImport)
		r.Put("/imports/{importID}/archive", s.uploadImportArchive)
		r.Post("/imports/{importID}/files", s.uploadImportFiles)
		r.Post("/imports/{importID}/commit", s.commitDatasetImport)
		r.Post("/imports/{importID}/cancel", s.cancelDatasetImport)
		if s.projects != nil {
			r.Put("/projects", s.setDatasetProjects)
		}
	})
}

// datasetError writes the response for an error returned by
// dataset.Service.
func (s *server) datasetError(w http.ResponseWriter, r *http.Request, err error) {
	var de *dataset.Error
	if errors.As(err, &de) {
		status, code := datasetErrorStatus(de.Kind)
		writeError(w, status, code, de.Message)
		return
	}
	s.unexpectedError(w, r, err)
}

// unexpectedError logs an error that is not the client's fault and
// answers 500.
func (s *server) unexpectedError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil && errors.Is(err, context.Canceled) {
		return // the client went away; nobody reads the response
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path,
		"request_id", RequestID(r.Context()), "err", err)
	writeError(w, http.StatusInternalServerError, codeInternal, msgInternalError)
}

func datasetErrorStatus(kind error) (int, string) {
	switch kind {
	case dataset.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case dataset.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case dataset.ErrBadRequest:
		return http.StatusBadRequest, codeBadRequest
	case dataset.ErrConflict:
		return http.StatusConflict, codeConflict
	case dataset.ErrOffsetMismatch:
		return http.StatusConflict, codeUploadOffsetMismatch
	case dataset.ErrTooLarge:
		return http.StatusRequestEntityTooLarge, codePayloadTooLarge
	case dataset.ErrInsufficientStorage:
		return http.StatusInsufficientStorage, codeInsufficientStorage
	case dataset.ErrUnsupportedImage:
		return http.StatusUnprocessableEntity, codeUnsupportedImage
	}
	return http.StatusInternalServerError, codeInternal
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, codeBadRequest, message)
}

// maxJSONBody bounds JSON request bodies.
const maxJSONBody = 1 << 20

// decodeJSON reads a JSON request body into v. Unknown fields are
// ignored. It writes the error response and returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("trailing data after the JSON value")
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "请求内容过大")
		} else {
			badRequest(w, msgMalformedRequest)
		}
		return false
	}
	return true
}

// pathID parses a positive decimal ID from the URL. Anything else is a
// resource that does not exist.
func pathID(r *http.Request, name string) (int64, bool) {
	v := chi.URLParam(r, name)
	if v == "" || len(v) > 18 || strings.Trim(v, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil && id > 0
}

func notFoundMessage(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotFound, codeNotFound, message)
}

// datasetIDs parses {datasetID} and, unless child is "", the child ID
// parameter ("imageID" or "importID"). It answers 404 for invalid IDs.
func datasetIDs(w http.ResponseWriter, r *http.Request, child string) (datasetID, childID int64, ok bool) {
	if datasetID, ok = pathID(r, "datasetID"); !ok {
		notFoundMessage(w, "数据集不存在")
		return 0, 0, false
	}
	switch child {
	case "imageID":
		if childID, ok = pathID(r, child); !ok {
			notFoundMessage(w, "图片不存在")
		}
	case "importID":
		if childID, ok = pathID(r, child); !ok {
			notFoundMessage(w, "导入任务不存在")
		}
	}
	return datasetID, childID, ok
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

// ---- datasets ----

func (s *server) listDatasets(w http.ResponseWriter, r *http.Request) {
	q := dataset.DatasetQuery{Search: r.URL.Query().Get("q")}
	// project_id: a project's datasets, or "none" for those in no project.
	switch v := r.URL.Query().Get("project_id"); v {
	case "":
	case "none":
		q.NoProject = true
	default:
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "参数 project_id 无效，应为项目 ID 或 none")
			return
		}
		q.ProjectID = id
	}
	items, err := s.datasets.ListDatasets(r.Context(), q)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

type createDatasetRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Directory   *string `json:"directory"`
}

func (s *server) createDataset(w http.ResponseWriter, r *http.Request) {
	var req createDatasetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	d, err := s.datasets.CreateDataset(r.Context(), dataset.CreateDatasetInput{
		Name: req.Name, Description: req.Description, Directory: req.Directory,
	})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *server) listDatasetDirectories(w http.ResponseWriter, r *http.Request) {
	items, err := s.datasets.ListDirectories(r.Context())
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) getDataset(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	d, err := s.datasets.GetDataset(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type updateDatasetRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *server) updateDataset(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	var req updateDatasetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	d, err := s.datasets.UpdateDataset(r.Context(), id, dataset.UpdateDatasetInput{Name: req.Name, Description: req.Description})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) deleteDataset(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	deleteFiles := false
	if v := r.URL.Query().Get("delete_files"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "参数 delete_files 无效，应为 true 或 false")
			return
		}
		deleteFiles = b
	}
	res, err := s.datasets.DeleteDataset(r.Context(), id, deleteFiles)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	if res != nil { // deleted, but part of the folder is left on disk
		writeJSON(w, http.StatusOK, res)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- images and labels ----

func (s *server) listDatasetImages(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	q := dataset.ImageQuery{Label: r.URL.Query().Get("label"), Search: strings.TrimSpace(r.URL.Query().Get("q"))}
	if q.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<31-1); !ok {
		return
	}
	if q.Limit, ok = intParam(w, r, "limit", 60, 1, 200); !ok {
		return
	}
	if q.Status, ok = enumParam(w, r, "status", dataset.FilterAll, dataset.FilterAnnotated, dataset.FilterEmpty,
		dataset.FilterInvalid, dataset.FilterUnannotated); !ok {
		return
	}
	if q.Sort, ok = enumParam(w, r, "sort", dataset.SortPath, dataset.SortModTime, dataset.SortObjects); !ok {
		return
	}
	items, total, err := s.datasets.ListImages(r.Context(), id, q)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, total))
}

func (s *server) getDatasetImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	img, err := s.datasets.GetImage(r.Context(), id, imageID)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (s *server) serveDatasetImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	f, err := s.datasets.OpenImage(r.Context(), id, imageID)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", inlineDisposition(f.Name))
	serveImageFile(w, r, f)
}

func (s *server) serveDatasetThumbnail(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	size, ok := enumParam(w, r, "size", dataset.ThumbSmall, dataset.ThumbLarge)
	if !ok {
		return
	}
	f, err := s.datasets.OpenThumbnail(r.Context(), id, imageID, size)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	serveImageFile(w, r, f)
}

// serveImageFile sends an image with Range and conditional request
// support. A URL whose v matches the file's current version never changes,
// so browsers may cache it for good; anything else must be revalidated.
func serveImageFile(w http.ResponseWriter, r *http.Request, f *dataset.ImageFile) {
	defer f.File.Close()
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	if v := r.URL.Query().Get("v"); v != "" && v == f.Version {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	http.ServeContent(w, r, f.Name, f.ModTime, f.File)
}

// inlineDisposition builds a Content-Disposition header that keeps
// non-ASCII file names (RFC 6266 / RFC 5987), with an ASCII fallback for
// old clients.
func inlineDisposition(name string) string {
	var fallback, encoded strings.Builder
	for _, r := range name {
		if r < 0x80 && r >= 0x20 && r != '"' && r != '\\' && r != 0x7f {
			fallback.WriteRune(r)
		} else {
			fallback.WriteByte('_')
		}
	}
	for _, b := range []byte(name) {
		if isAttrChar(b) {
			encoded.WriteByte(b)
		} else {
			fmt.Fprintf(&encoded, "%%%02X", b)
		}
	}
	return `inline; filename="` + fallback.String() + `"; filename*=UTF-8''` + encoded.String()
}

// isAttrChar reports whether b may appear unescaped in an RFC 5987 value.
func isAttrChar(b byte) bool {
	switch {
	case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

func (s *server) listDatasetLabels(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	items, err := s.datasets.ListLabels(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

// ---- jobs ----

func (s *server) scanDataset(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	job, err := s.datasets.StartScan(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *server) listDatasetImports(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	items, total, err := s.datasets.ListImports(r.Context(), id, limit)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, total))
}

type createImportRequest struct {
	Source     string `json:"source"`
	FileName   string `json:"file_name"`
	FileSize   int64  `json:"file_size"`
	FileCount  int    `json:"file_count"`
	OnConflict string `json:"on_conflict"`
}

func (s *server) createDatasetImport(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	var req createImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.datasets.CreateImport(r.Context(), id, dataset.CreateImportInput{
		Source: req.Source, FileName: req.FileName, FileSize: req.FileSize, FileCount: req.FileCount, OnConflict: req.OnConflict,
	})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *server) getDatasetImport(w http.ResponseWriter, r *http.Request) {
	id, importID, ok := datasetIDs(w, r, "importID")
	if !ok {
		return
	}
	job, err := s.datasets.GetImport(r.Context(), id, importID)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// limitBody caps a request body at limit bytes; reading past it fails with
// a dataset error of kind ErrTooLarge carrying message.
func limitBody(w http.ResponseWriter, r *http.Request, limit int64, message string) io.ReadCloser {
	return tooLargeReader{ReadCloser: http.MaxBytesReader(w, r.Body, limit), message: message}
}

type tooLargeReader struct {
	io.ReadCloser
	message string
}

func (t tooLargeReader) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		err = &dataset.Error{Kind: dataset.ErrTooLarge, Message: t.message}
	}
	return n, err
}

const (
	msgChunkTooLarge  = "单次上传的数据不能超过 64 MiB"
	msgFolderTooLarge = "单次上传的文件总大小不能超过 256 MiB"
)

func (s *server) uploadImportArchive(w http.ResponseWriter, r *http.Request) {
	id, importID, ok := datasetIDs(w, r, "importID")
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		badRequest(w, "参数 offset 无效，应为已上传的字节数")
		return
	}
	if r.ContentLength > dataset.MaxChunkBytes {
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgChunkTooLarge)
		return
	}
	body := limitBody(w, r, dataset.MaxChunkBytes, msgChunkTooLarge)
	job, err := s.datasets.WriteArchiveChunk(r.Context(), id, importID, offset, body)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// maxPathField bounds the "path" form field of folder uploads.
const maxPathField = 4096

func (s *server) uploadImportFiles(w http.ResponseWriter, r *http.Request) {
	id, importID, ok := datasetIDs(w, r, "importID")
	if !ok {
		return
	}
	if r.ContentLength > dataset.MaxFolderRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgFolderTooLarge)
		return
	}
	r.Body = limitBody(w, r, dataset.MaxFolderRequestBytes, msgFolderTooLarge)
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "请求格式不正确：需要 multipart/form-data")
		return
	}
	job, err := s.datasets.AddFolderFiles(r.Context(), id, importID, folderFiles(mr))
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// folderFiles reads the parts of a folder upload: each "file" part must be
// preceded by a "path" part with its relative path inside the folder
// (Part.FileName drops directories, so it is not used). Other fields are
// ignored.
func folderFiles(mr *multipart.Reader) dataset.FileSource {
	return func() (string, io.Reader, error) {
		path, havePath := "", false
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				if havePath {
					return "", nil, malformedMultipart("path 字段之后缺少 file 字段")
				}
				return "", nil, io.EOF
			}
			if err != nil {
				return "", nil, multipartError(err)
			}
			switch part.FormName() {
			case "path":
				b, err := io.ReadAll(io.LimitReader(part, maxPathField+1))
				if err != nil {
					return "", nil, multipartError(err)
				}
				if len(b) > maxPathField {
					return "", nil, malformedMultipart("path 字段过长")
				}
				path, havePath = string(b), true
			case "file":
				if !havePath {
					return "", nil, malformedMultipart("每个 file 字段之前必须有对应的 path 字段")
				}
				return path, part, nil
			}
		}
	}
}

func malformedMultipart(detail string) error {
	return &dataset.Error{Kind: dataset.ErrBadRequest, Message: msgMalformedRequest + "：" + detail}
}

// multipartError keeps size-limit errors and reports anything else as a
// malformed body.
func multipartError(err error) error {
	var de *dataset.Error
	if errors.As(err, &de) {
		return de
	}
	return malformedMultipart("multipart 数据无效")
}

func (s *server) commitDatasetImport(w http.ResponseWriter, r *http.Request) {
	id, importID, ok := datasetIDs(w, r, "importID")
	if !ok {
		return
	}
	job, err := s.datasets.CommitImport(r.Context(), id, importID)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *server) cancelDatasetImport(w http.ResponseWriter, r *http.Request) {
	id, importID, ok := datasetIDs(w, r, "importID")
	if !ok {
		return
	}
	job, err := s.datasets.CancelImport(r.Context(), id, importID)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
