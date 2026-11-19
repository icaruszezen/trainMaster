package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/evaluation"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

// Compute-node endpoints for model_evaluation tasks, mounted under
// /api/v1/compute/agent/evaluations/{evalID} behind requireNodeToken.
// Every request requires X-Node-Key; 404 means the evaluation no longer
// exists, 409 means the node no longer holds the lease (canceled or expired).

const (
	maxEvalResultsCompressed   = 128 << 20 // 128 MiB
	maxEvalResultsDecompressed = 256 << 20 // 256 MiB
)

func (s *server) agentEvaluationRoutes(r chi.Router) {
	r.Route("/evaluations/{evalID}", func(r chi.Router) {
		r.Get("/model", s.agentEvaluationModel)
		r.Get("/images", s.agentEvaluationImages)
		r.Get("/images/{sampleID}/file", s.agentEvaluationImageFile)
		r.Post("/progress", s.agentEvaluationProgress)
		r.Post("/results", s.agentEvaluationResults)
		r.Post("/complete", s.agentEvaluationComplete)
		r.Post("/fail", s.agentEvaluationFail)
	})
}

func (s *server) agentEvaluation(w http.ResponseWriter, r *http.Request) (id, nodeID int64, ok bool) {
	if id, ok = evaluationID(w, r); !ok {
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
	return id, nodeID, true
}

func (s *server) agentEvaluationError(w http.ResponseWriter, r *http.Request, err error) {
	var me *model.Error
	if errors.As(err, &me) {
		s.modelError(w, r, err)
		return
	}
	s.evaluationError(w, r, err)
}

func (s *server) agentEvaluationModel(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	f, err := s.evaluations.OpenTaskModel(r.Context(), id, nodeID)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

func (s *server) agentEvaluationImages(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "参数 after 无效，应为样本 ID")
			return
		}
		after = n
	}
	limit, ok := intParam(w, r, "limit", evaluation.DefaultTaskImgLimit, 1, evaluation.MaxTaskImgLimit)
	if !ok {
		return
	}
	page, err := s.evaluations.ListTaskImages(r.Context(), id, nodeID, after, limit)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) agentEvaluationImageFile(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	sampleID, ok := evaluationSampleID(w, r)
	if !ok {
		return
	}
	f, err := s.evaluations.OpenTaskImage(r.Context(), id, nodeID, sampleID)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", inlineDisposition(f.Name))
	serveEvaluationImageFile(w, r, f)
}

func (s *server) agentEvaluationProgress(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	var req evaluation.ProgressInput
	if !decodeJSON(w, r, &req) {
		return
	}
	lease, err := s.evaluations.ReportProgress(r.Context(), id, nodeID, req)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, leaseResponse{LeaseSeconds: lease})
}

func (s *server) agentEvaluationResults(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}

	var reader io.Reader = http.MaxBytesReader(w, r.Body, maxEvalResultsCompressed)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			badRequest(w, "gzip 压缩内容无效")
			return
		}
		defer gz.Close()
		reader = io.LimitReader(gz, maxEvalResultsDecompressed+1)
	}

	var payload evaluation.UploadResultsPayload
	dec := json.NewDecoder(reader)
	if err := dec.Decode(&payload); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "评估结果上传体积超过限制")
			return
		}
		badRequest(w, msgMalformedRequest)
		return
	}

	if err := s.evaluations.UploadResults(r.Context(), id, nodeID, payload); err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type evalCompleteRequest struct {
	Result json.RawMessage `json:"result"`
}

func (s *server) agentEvaluationComplete(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	var req evalCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ev, err := s.evaluations.CompleteTask(r.Context(), id, nodeID, req.Result)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) agentEvaluationFail(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentEvaluation(w, r)
	if !ok {
		return
	}
	var req evaluation.FailInput
	if !decodeJSON(w, r, &req) {
		return
	}
	ev, err := s.evaluations.FailTask(r.Context(), id, nodeID, req)
	if err != nil {
		s.agentEvaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}
