package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/training"
)

// Training API: /api/v1/training. Exposes full Ultralytics YOLO parameter
// metadata/defaults (GET /params-schema) and training job lifecycle management
// (/jobs).

func (s *server) trainingRoutes(r chi.Router) {
	r.Get("/params-schema", s.getTrainingParamsSchema)
	r.Route("/jobs", func(r chi.Router) {
		r.Get("/", s.listTrainingJobs)
		r.Post("/", s.createTrainingJob)
		r.Route("/{jobID}", func(r chi.Router) {
			r.Get("/", s.getTrainingJob)
			r.Patch("/", s.updateTrainingJob)
			r.Delete("/", s.deleteTrainingJob)
			r.Post("/cancel", s.cancelTrainingJob)
			r.Post("/retry", s.retryTrainingJob)
			r.Post("/register-model", s.registerTrainedModel)
			r.Get("/logs", s.getTrainingJobLogs)
			r.Get("/artifacts/*", s.serveTrainingArtifact)
		})
	})
}

func (s *server) trainingError(w http.ResponseWriter, r *http.Request, err error) {
	var te *training.Error
	if errors.As(err, &te) {
		status, code := trainingErrorStatus(te.Kind)
		writeError(w, status, code, te.Message)
		return
	}
	s.unexpectedError(w, r, err)
}

func trainingErrorStatus(kind error) (int, string) {
	switch kind {
	case training.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case training.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case training.ErrConflict:
		return http.StatusConflict, codeConflict
	}
	return http.StatusInternalServerError, codeInternal
}

func trainingJobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "jobID", "训练任务不存在")
}

func (s *server) getTrainingParamsSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, training.ParamsSchema())
}

func (s *server) listTrainingJobs(w http.ResponseWriter, r *http.Request) {
	projectID, ok := idQueryParam(w, r, "project_id", "项目")
	if !ok {
		return
	}
	dataVersionID, ok := idQueryParam(w, r, "data_version_id", "数据版本")
	if !ok {
		return
	}
	status, ok := textParam(w, r, "status")
	if !ok {
		return
	}
	yoloVersion, ok := textParam(w, r, "yolo_version")
	if !ok {
		return
	}
	search, ok := searchParam(w, r)
	if !ok {
		return
	}
	items, err := s.training.List(r.Context(), training.ListFilter{
		ProjectID:     projectID,
		DataVersionID: dataVersionID,
		Status:        status,
		YOLOVersion:   yoloVersion,
		Search:        search,
	})
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) createTrainingJob(w http.ResponseWriter, r *http.Request) {
	var req training.CreateInput
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.training.Create(r.Context(), req)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *server) getTrainingJob(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	job, err := s.training.Get(r.Context(), id)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) updateTrainingJob(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	var req training.UpdateInput
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.training.Update(r.Context(), id, req)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) cancelTrainingJob(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	job, err := s.training.Cancel(r.Context(), id)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) retryTrainingJob(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	job, err := s.training.Retry(r.Context(), id)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) deleteTrainingJob(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	if err := s.training.Delete(r.Context(), id); err != nil {
		s.trainingError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) registerTrainedModel(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	var req training.RegisterModelInput
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	m, err := s.training.RegisterModel(r.Context(), id, req)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *server) getTrainingJobLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	var offset int64 = -1
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < -1 {
			badRequest(w, "参数 offset 无效")
			return
		}
		offset = n
	}
	limit, ok := intParam(w, r, "limit", training.DefaultLogReadBytes, 1024, training.MaxLogReadBytes)
	if !ok {
		return
	}
	slice, err := s.training.ReadLogs(r.Context(), id, offset, limit)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, slice)
}

func (s *server) serveTrainingArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := trainingJobID(w, r)
	if !ok {
		return
	}
	rel := chi.URLParam(r, "*")
	f, err := s.training.OpenArtifact(r.Context(), id, rel)
	if err != nil {
		s.trainingError(w, r, err)
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(strings.ToLower(f.Name), ".pt") || r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", attachmentDisposition(f.Name))
	}
	http.ServeContent(w, r, f.Name, f.ModTime, f.Content)
}
