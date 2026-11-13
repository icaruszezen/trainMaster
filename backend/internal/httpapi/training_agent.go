package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/training"
)

// Training task routes for compute nodes, mounted under /api/v1/compute/agent
// behind requireNodeToken. Every call requires X-Node-Key and renews the lease;
// 404 means the job was deleted, 409 means the node no longer holds it (e.g.
// canceled by user or lease expired).

func (s *server) agentTrainingRoutes(r chi.Router) {
	r.Route("/training-jobs/{jobID}", func(r chi.Router) {
		r.Get("/dataset", s.agentTrainingDataset)
		r.Get("/base-model", s.agentTrainingBaseModel)
		r.Post("/progress", s.agentTrainingProgress)
		r.Put("/artifacts", s.agentTrainingUploadArtifact)
		r.Post("/complete", s.agentTrainingComplete)
		r.Post("/fail", s.agentTrainingFail)
	})
}

func (s *server) agentTrainingJob(w http.ResponseWriter, r *http.Request) (id, nodeID int64, ok bool) {
	if id, ok = trainingJobID(w, r); !ok {
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

func (s *server) agentTrainingError(w http.ResponseWriter, r *http.Request, err error) {
	var de *dataversion.Error
	if errors.As(err, &de) {
		s.dataVersionError(w, r, err)
		return
	}
	var me *model.Error
	if errors.As(err, &me) {
		s.modelError(w, r, err)
		return
	}
	s.trainingError(w, r, err)
}

func (s *server) agentTrainingDataset(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	exp, err := s.training.OpenTaskDataset(r.Context(), id, nodeID)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	defer exp.Close()

	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", attachmentDisposition(exp.FileName))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if err := exp.Write(r.Context(), w); err != nil {
		if r.Context().Err() == nil {
			s.log.Error("agent dataset download failed", "job_id", id, "node_id", nodeID, "err", err)
		}
		panic(http.ErrAbortHandler)
	}
}

func (s *server) agentTrainingBaseModel(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	f, err := s.training.OpenTaskBaseModel(r.Context(), id, nodeID)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

func (s *server) agentTrainingProgress(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	var req training.ProgressInput
	if !decodeJSON(w, r, &req) {
		return
	}
	lease, err := s.training.ReportProgress(r.Context(), id, nodeID, req)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, leaseResponse{LeaseSeconds: lease})
}

func (s *server) agentTrainingUploadArtifact(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	relPath, ok := textParam(w, r, "path")
	if !ok {
		return
	}
	if relPath == "" {
		badRequest(w, "缺少参数 path")
		return
	}
	art, err := s.training.UploadArtifact(r.Context(), id, nodeID, relPath, r.Body)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, art)
}

type trainingCompleteRequest struct {
	Result json.RawMessage `json:"result"`
}

func (s *server) agentTrainingComplete(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	var req trainingCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.training.CompleteTask(r.Context(), id, nodeID, req.Result)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) agentTrainingFail(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentTrainingJob(w, r)
	if !ok {
		return
	}
	var req training.FailInput
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.training.FailTask(r.Context(), id, nodeID, req)
	if err != nil {
		s.agentTrainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
