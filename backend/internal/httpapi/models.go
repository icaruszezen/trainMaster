package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

// Model repository API: /api/v1/models and GET /api/v1/projects/{id}/models
// for the browser, and the conversion task calls of the training nodes
// under /api/v1/compute/agent. The JSON shapes are defined by the types in
// internal/model; this file maps HTTP onto model.Service.

func (s *server) modelRoutes(r chi.Router) {
	r.Get("/", s.listModels)
	r.Route("/uploads", func(r chi.Router) {
		r.Post("/", s.createModelUpload)
		r.Route("/{uploadID}", func(r chi.Router) {
			r.Get("/", s.getModelUpload)
			r.Put("/", s.uploadModelChunk)
			r.Post("/commit", s.commitModelUpload)
			r.Post("/cancel", s.cancelModelUpload)
		})
	})
	r.Route("/{modelID}", func(r chi.Router) {
		r.Get("/", s.getModel)
		r.Patch("/", s.updateModel)
		r.Delete("/", s.deleteModel)
		r.Get("/file", s.downloadModel)
		r.Get("/exports", s.listModelExports)
		r.Post("/exports", s.requestModelExport)
		r.Get("/exports/{exportID}", s.getModelExport)
		r.Delete("/exports/{exportID}", s.deleteModelExport)
		r.Get("/exports/{exportID}/file", s.downloadModelExport)
	})
}

// agentTaskRoutes are the task calls of the training nodes, mounted under
// /api/v1/compute/agent (behind requireNodeToken). A node claims a task
// with its node_key and then sends the X-Node-Key header with every call
// about it.
func (s *server) agentTaskRoutes(r chi.Router) {
	r.Post("/tasks/claim", s.claimAgentTask)
	r.Route("/model-exports/{exportID}", func(r chi.Router) {
		r.Get("/input", s.agentExportInput)
		r.Post("/progress", s.agentExportProgress)
		r.Put("/output", s.agentExportOutput)
		r.Post("/complete", s.agentExportComplete)
		r.Post("/fail", s.agentExportFail)
	})
}

// modelError writes the response for an error returned by model.Service.
func (s *server) modelError(w http.ResponseWriter, r *http.Request, err error) {
	var me *model.Error
	if errors.As(err, &me) {
		status, code := modelErrorStatus(me.Kind)
		writeError(w, status, code, me.Message)
		return
	}
	s.unexpectedError(w, r, err)
}

func modelErrorStatus(kind error) (int, string) {
	switch kind {
	case model.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case model.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case model.ErrBadRequest:
		return http.StatusBadRequest, codeBadRequest
	case model.ErrConflict:
		return http.StatusConflict, codeConflict
	case model.ErrOffsetMismatch:
		return http.StatusConflict, codeUploadOffsetMismatch
	case model.ErrTooLarge:
		return http.StatusRequestEntityTooLarge, codePayloadTooLarge
	case model.ErrInsufficientStorage:
		return http.StatusInsufficientStorage, codeInsufficientStorage
	case model.ErrInvalidModel:
		return http.StatusUnprocessableEntity, codeInvalidModel
	case model.ErrVersionMismatch:
		return http.StatusUnprocessableEntity, codeYOLOVersionMismatch
	}
	return http.StatusInternalServerError, codeInternal
}

// modelID parses {modelID}; uploadID, exportID likewise. They answer 404
// for invalid IDs.
func modelID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "modelID", "模型不存在")
}

func uploadID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "uploadID", "上传任务不存在")
}

func exportID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "exportID", "转换任务不存在")
}

func pathIDOr404(w http.ResponseWriter, r *http.Request, name, message string) (int64, bool) {
	id, ok := pathID(r, name)
	if !ok {
		notFoundMessage(w, message)
	}
	return id, ok
}

// ---- models ----

func (s *server) listModels(w http.ResponseWriter, r *http.Request) {
	search, ok := searchParam(w, r)
	if !ok {
		return
	}
	q := model.ModelQuery{Search: search}
	if v := r.URL.Query().Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "参数 project_id 无效，应为项目 ID")
			return
		}
		q.ProjectID = id
	}
	if v := r.URL.Query().Get("yolo_version"); v != "" {
		if _, ok := model.LookupYOLOVersion(v); !ok {
			values := make([]string, len(model.YOLOVersions))
			for i, y := range model.YOLOVersions {
				values[i] = y.Value
			}
			badRequest(w, "参数 yolo_version 无效，可选值："+strings.Join(values, "、"))
			return
		}
		q.YOLOVersion = v
	}
	if r.URL.Query().Get("source") != "" {
		if q.Source, ok = enumParam(w, r, "source", model.SourceUpload, model.SourceTraining); !ok {
			return
		}
	}
	items, err := s.models.ListModels(r.Context(), q)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

// listProjectModels is GET /api/v1/projects/{id}/models: the latest
// version first, 404 for a project that does not exist.
func (s *server) listProjectModels(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	items, err := s.models.ListProjectModels(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) getModel(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	m, err := s.models.GetModel(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type updateModelRequest struct {
	Description *string `json:"description"`
}

func (s *server) updateModel(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	var req updateModelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m, err := s.models.UpdateModel(r.Context(), id, model.UpdateModelInput{Description: req.Description})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	res, err := s.models.DeleteModel(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	if res != nil { // deleted, but part of the folder is left on disk
		writeJSON(w, http.StatusOK, res)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) downloadModel(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	f, err := s.models.OpenModelFile(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

// serveModelFile sends a weights or conversion file as a download, with
// Range and conditional request support.
func serveModelFile(w http.ResponseWriter, r *http.Request, f *model.File) {
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachmentDisposition(f.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, f.Name, f.ModTime, f.Content)
}

// ---- uploads ----

type createModelUploadRequest struct {
	ProjectID   int64  `json:"project_id"`
	YOLOVersion string `json:"yolo_version"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	Description string `json:"description"`
}

func (s *server) createModelUpload(w http.ResponseWriter, r *http.Request) {
	var req createModelUploadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	up, err := s.models.CreateUpload(r.Context(), model.CreateUploadInput{
		ProjectID: req.ProjectID, YOLOVersion: req.YOLOVersion, FileName: req.FileName, FileSize: req.FileSize,
		Description: req.Description,
	})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, up)
}

func (s *server) getModelUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	up, err := s.models.GetUpload(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// limitModelBody is limitBody for model.Service.
func limitModelBody(w http.ResponseWriter, r *http.Request, limit int64, message string) io.ReadCloser {
	return tooLargeReader{ReadCloser: http.MaxBytesReader(w, r.Body, limit),
		tooLarge: &model.Error{Kind: model.ErrTooLarge, Message: message}}
}

const (
	msgModelChunkTooLarge = "单次上传的数据不能超过 64 MiB"
	msgOutputTooLarge     = "转换结果不能超过 4 GiB"
)

// maxRefusedDrain bounds how much of a refused upload drainRefused reads.
const maxRefusedDrain = 64 << 20

// drainRefused reads and discards (at most maxRefusedDrain bytes of) the
// rest of an upload body that model.Service refused because of the state
// of the upload or conversion (404, 409) or the disk (507), usually before
// reading any of it. The server itself discards only 256 KiB of an unread
// body and then closes the connection, so a client still sending a larger
// one sees the connection being reset instead of the response. Chunks of
// browser uploads fit; nodes ask before sending a larger result (the
// uploading progress report with size_bytes). It runs before the response
// is written: after that, the body may no longer be readable.
func drainRefused(body io.Reader, err error) {
	if errors.Is(err, model.ErrNotFound) || errors.Is(err, model.ErrConflict) ||
		errors.Is(err, model.ErrOffsetMismatch) || errors.Is(err, model.ErrInsufficientStorage) {
		_, _ = io.CopyN(io.Discard, body, maxRefusedDrain)
	}
}

func (s *server) uploadModelChunk(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		badRequest(w, "参数 offset 无效，应为已上传的字节数")
		return
	}
	if r.ContentLength > model.MaxChunkBytes {
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgModelChunkTooLarge)
		return
	}
	body := limitModelBody(w, r, model.MaxChunkBytes, msgModelChunkTooLarge)
	up, err := s.models.WriteChunk(r.Context(), id, offset, body)
	if err != nil {
		drainRefused(body, err)
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

type commitModelUploadRequest struct {
	YOLOVersion *string `json:"yolo_version"`
	Description *string `json:"description"`
}

// commitModelUpload answers 201 with the new model, or 200 with the model
// of an upload that was committed before.
func (s *server) commitModelUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	var req commitModelUploadRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	m, created, err := s.models.CommitUpload(r.Context(), id, model.CommitInput{
		YOLOVersion: req.YOLOVersion, Description: req.Description,
	})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, m)
}

// decodeOptionalJSON is decodeJSON for a body that may also be empty
// (nothing to decode).
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "请求内容过大")
		} else {
			badRequest(w, msgMalformedRequest)
		}
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(w, r, v)
}

func (s *server) cancelModelUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	up, err := s.models.CancelUpload(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// ---- conversions ----

func (s *server) listModelExports(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	items, err := s.models.ListExports(r.Context(), id)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

type modelExportRequest struct {
	Format      string          `json:"format"`
	ImgSize     json.RawMessage `json:"imgsz"` // an integer, [height, width] or null
	Dynamic     *bool           `json:"dynamic"`
	Postprocess string          `json:"postprocess"`
	Opset       *int            `json:"opset"`
}

// requestModelExport answers 200 with a conversion whose result is ready
// to download, else 202 with the queued (or running) conversion.
func (s *server) requestModelExport(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	var req modelExportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	imgsz, ok := parseImgSize(req.ImgSize)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "输入尺寸 imgsz 应为整数或 [高, 宽]")
		return
	}
	e, ready, err := s.models.RequestExport(r.Context(), id, model.ExportRequest{
		Format: req.Format, ImgSize: imgsz, Dynamic: req.Dynamic, Postprocess: req.Postprocess, Opset: req.Opset,
	})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if ready {
		status = http.StatusOK
	}
	writeJSON(w, status, e)
}

// parseImgSize reads imgsz: absent or null (nil), an integer, or an array
// of integers (the service checks how many and their range).
func parseImgSize(raw json.RawMessage) ([]int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var values []float64
	var one float64
	if json.Unmarshal(raw, &one) == nil {
		values = []float64{one}
	} else if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return nil, false
	}
	out := make([]int, len(values))
	for i, v := range values {
		if v != math.Trunc(v) || math.Abs(v) > 1<<30 {
			return nil, false
		}
		out[i] = int(v)
	}
	return out, true
}

func (s *server) getModelExport(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	eid, ok := exportID(w, r)
	if !ok {
		return
	}
	e, err := s.models.GetExport(r.Context(), id, eid)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) deleteModelExport(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	eid, ok := exportID(w, r)
	if !ok {
		return
	}
	if err := s.models.DeleteExport(r.Context(), id, eid); err != nil {
		s.modelError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) downloadModelExport(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	eid, ok := exportID(w, r)
	if !ok {
		return
	}
	f, err := s.models.OpenExportFile(r.Context(), id, eid)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

// ---- training node task calls ----

// nodeKeyHeader identifies the node in the task calls after a claim.
const nodeKeyHeader = "X-Node-Key"

// agentNode resolves the node of a task call from its node_key. It writes
// the error response and returns false on failure (404 unknown_node for a
// node that is not registered).
func (s *server) agentNode(w http.ResponseWriter, r *http.Request, nodeKey string) (int64, bool) {
	id, err := s.compute.NodeID(r.Context(), nodeKey)
	if err != nil {
		s.computeError(w, r, err)
		return 0, false
	}
	return id, true
}

// agentExport parses {exportID} and resolves the calling node.
func (s *server) agentExport(w http.ResponseWriter, r *http.Request) (exportID, nodeID int64, ok bool) {
	if exportID, ok = pathIDOr404(w, r, "exportID", "转换任务不存在"); !ok {
		return 0, 0, false
	}
	key := r.Header.Get(nodeKeyHeader)
	if key == "" {
		badRequest(w, "缺少请求头 "+nodeKeyHeader)
		return 0, 0, false
	}
	if nodeID, ok = s.agentNode(w, r, key); !ok {
		return 0, 0, false
	}
	return exportID, nodeID, true
}

type claimTaskRequest struct {
	NodeKey     string   `json:"node_key"`
	Kinds       []string `json:"kinds"`
	WaitSeconds float64  `json:"wait_seconds"` // 0–30
}

type claimTaskResponse struct {
	Task *model.Task `json:"task"` // null: nothing to do within the wait
}

// claimAgentTask long-polls for a task. It returns {"task": null} when
// none arrives within wait_seconds, and at once when the server shuts
// down.
func (s *server) claimAgentTask(w http.ResponseWriter, r *http.Request) {
	var req claimTaskRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.NodeKey == "" {
		req.NodeKey = r.Header.Get(nodeKeyHeader)
	}
	nodeID, ok := s.agentNode(w, r, req.NodeKey)
	if !ok {
		return
	}
	if req.WaitSeconds < 0 || req.WaitSeconds > model.MaxClaimWait.Seconds() {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed,
			"wait_seconds 必须在 0–"+strconv.Itoa(int(model.MaxClaimWait.Seconds()))+" 之间")
		return
	}
	wait := time.Duration(req.WaitSeconds * float64(time.Second))
	task, err := s.models.ClaimTask(r.Context(), nodeID, req.Kinds, wait)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, claimTaskResponse{Task: task})
}

func (s *server) agentExportInput(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentExport(w, r)
	if !ok {
		return
	}
	f, err := s.models.OpenTaskInput(r.Context(), id, nodeID)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

type exportProgressRequest struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	// SizeBytes, with stage uploading: the size of the result about to be
	// uploaded. The report then fails like the upload would (404, 409, 507)
	// before the node sends the file.
	SizeBytes int64 `json:"size_bytes"`
}

type leaseResponse struct {
	LeaseSeconds int `json:"lease_seconds"`
}

func (s *server) agentExportProgress(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentExport(w, r)
	if !ok {
		return
	}
	var req exportProgressRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	lease, err := s.models.ReportProgress(r.Context(), id, nodeID, model.ProgressInput{
		Stage: req.Stage, Message: req.Message, SizeBytes: req.SizeBytes,
	})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, leaseResponse{LeaseSeconds: lease})
}

type outputResponse struct {
	ReceivedBytes int64 `json:"received_bytes"`
}

// agentExportOutput receives the converted file: the raw bytes with
// Content-Length and X-Content-Sha256 (hex).
func (s *server) agentExportOutput(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentExport(w, r)
	if !ok {
		return
	}
	switch {
	case r.ContentLength < 0:
		badRequest(w, "缺少请求头 Content-Length")
		return
	case r.ContentLength > model.MaxOutputBytes:
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgOutputTooLarge)
		return
	}
	body := limitModelBody(w, r, model.MaxOutputBytes, msgOutputTooLarge)
	n, err := s.models.PutTaskOutput(r.Context(), id, nodeID, r.ContentLength, r.Header.Get("X-Content-Sha256"), body)
	if err != nil {
		drainRefused(body, err)
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, outputResponse{ReceivedBytes: n})
}

type exportCompleteRequest struct {
	Result json.RawMessage `json:"result"`
}

func (s *server) agentExportComplete(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentExport(w, r)
	if !ok {
		return
	}
	var req exportCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := s.models.CompleteTask(r.Context(), id, nodeID, req.Result)
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

type exportFailRequest struct {
	Error     string `json:"error"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}

func (s *server) agentExportFail(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentExport(w, r)
	if !ok {
		return
	}
	var req exportFailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := s.models.FailTask(r.Context(), id, nodeID, model.FailInput{
		Error: req.Error, Detail: req.Detail, Retryable: req.Retryable,
	})
	if err != nil {
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
