package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/evaluation"
)

// Evaluation API: /api/v1/evaluations.
// Exposes preview/alignment checks, single or batch multi-model creation,
// real-time threshold analysis, interactive error-sample inspection, multi-model
// comparison + same-source per-image diff, and offline HTML/JSON/CSV exports.

func (s *server) evaluationRoutes(r chi.Router) {
	r.Get("/", s.listEvaluations)
	r.Post("/", s.createEvaluations)
	r.Post("/preview", s.previewEvaluation)
	r.Route("/compare", func(r chi.Router) {
		r.Get("/", s.compareEvaluations)
		r.Get("/diff", s.compareEvaluationsDiff)
		r.Get("/export", s.exportCompareReport)
	})
	r.Route("/{evalID}", func(r chi.Router) {
		r.Get("/", s.getEvaluation)
		r.Patch("/", s.updateEvaluation)
		r.Delete("/", s.deleteEvaluation)
		r.Post("/cancel", s.cancelEvaluation)
		r.Post("/retry", s.retryEvaluation)
		r.Get("/analysis", s.getEvaluationAnalysis)
		r.Get("/export", s.exportEvaluationReport)
		r.Route("/images", func(r chi.Router) {
			r.Get("/", s.listEvaluationSamples)
			r.Route("/{sampleID}", func(r chi.Router) {
				r.Get("/", s.getEvaluationSample)
				r.Get("/file", s.serveEvaluationSampleImage)
				r.Get("/thumbnail", s.serveEvaluationSampleThumbnail)
			})
		})
	})
}

func (s *server) evaluationError(w http.ResponseWriter, r *http.Request, err error) {
	var ee *evaluation.Error
	if errors.As(err, &ee) {
		status, code := evaluationErrorStatus(ee.Kind)
		writeError(w, status, code, ee.Message)
		return
	}
	s.unexpectedError(w, r, err)
}

func evaluationErrorStatus(kind error) (int, string) {
	switch kind {
	case evaluation.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case evaluation.ErrBadRequest:
		return http.StatusBadRequest, codeBadRequest
	case evaluation.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case evaluation.ErrConflict:
		return http.StatusConflict, codeConflict
	}
	return http.StatusInternalServerError, codeInternal
}

func evaluationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "evalID", "评估任务不存在")
}

func evaluationSampleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "sampleID", "评估样本不存在")
}

func optFloatParam(w http.ResponseWriter, r *http.Request, name string) (*float64, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		badRequest(w, "参数 "+name+" 无效，应为数值")
		return nil, false
	}
	return &f, true
}

func parseIDListParam(w http.ResponseWriter, r *http.Request, name string) ([]int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		badRequest(w, "缺少参数 "+name)
		return nil, false
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "参数 "+name+" 包含无效的评估 ID："+p)
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

func (s *server) listEvaluations(w http.ResponseWriter, r *http.Request) {
	projectID, ok := idQueryParam(w, r, "project_id", "项目")
	if !ok {
		return
	}
	modelID, ok := idQueryParam(w, r, "model_id", "模型")
	if !ok {
		return
	}
	dvID, ok := idQueryParam(w, r, "data_version_id", "数据版本")
	if !ok {
		return
	}
	sourceType, ok := textParam(w, r, "source_type")
	if !ok {
		return
	}
	status, ok := textParam(w, r, "status")
	if !ok {
		return
	}
	search, ok := searchParam(w, r)
	if !ok {
		return
	}
	items, err := s.evaluations.List(r.Context(), evaluation.ListFilter{
		ProjectID:     projectID,
		ModelID:       modelID,
		DataVersionID: dvID,
		SourceType:    sourceType,
		Status:        status,
		Search:        search,
	})
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) previewEvaluation(w http.ResponseWriter, r *http.Request) {
	var req evaluation.PreviewInput
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.evaluations.Preview(r.Context(), req)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) createEvaluations(w http.ResponseWriter, r *http.Request) {
	var req evaluation.CreateInput
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.evaluations.Create(r.Context(), req)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, NewList(items, len(items)))
}

func (s *server) getEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	ev, err := s.evaluations.Get(r.Context(), id)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) updateEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	var req evaluation.UpdateInput
	if !decodeJSON(w, r, &req) {
		return
	}
	ev, err := s.evaluations.Update(r.Context(), id, req)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) deleteEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	if err := s.evaluations.Delete(r.Context(), id); err != nil {
		s.evaluationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) cancelEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	ev, err := s.evaluations.Cancel(r.Context(), id)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) retryEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	ev, err := s.evaluations.Retry(r.Context(), id)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) getEvaluationAnalysis(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	conf, ok := optFloatParam(w, r, "conf")
	if !ok {
		return
	}
	iou, ok := optFloatParam(w, r, "iou")
	if !ok {
		return
	}
	res, err := s.evaluations.Analyze(r.Context(), id, conf, iou)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) parseSampleFilter(w http.ResponseWriter, r *http.Request) (evaluation.SampleFilter, bool) {
	var f evaluation.SampleFilter
	var ok bool
	if f.Conf, ok = optFloatParam(w, r, "conf"); !ok {
		return f, false
	}
	if f.IoU, ok = optFloatParam(w, r, "iou"); !ok {
		return f, false
	}
	if f.ErrorType, ok = textParam(w, r, "error_type"); !ok {
		return f, false
	}
	if f.Class, ok = textParam(w, r, "class"); !ok {
		return f, false
	}
	if f.GTClass, ok = textParam(w, r, "gt_class"); !ok {
		return f, false
	}
	if f.PredClass, ok = textParam(w, r, "pred_class"); !ok {
		return f, false
	}
	if f.Search, ok = searchParam(w, r); !ok {
		return f, false
	}
	if f.Sort, ok = textParam(w, r, "sort"); !ok {
		return f, false
	}
	if f.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<31-1); !ok {
		return f, false
	}
	if f.Limit, ok = intParam(w, r, "limit", evaluation.DefaultSampleLimit, 1, evaluation.MaxSampleLimit); !ok {
		return f, false
	}
	return f, true
}

func (s *server) listEvaluationSamples(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	f, ok := s.parseSampleFilter(w, r)
	if !ok {
		return
	}
	items, total, err := s.evaluations.ListSamples(r.Context(), id, f)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, total))
}

func (s *server) getEvaluationSample(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	sampleID, ok := evaluationSampleID(w, r)
	if !ok {
		return
	}
	conf, ok := optFloatParam(w, r, "conf")
	if !ok {
		return
	}
	iou, ok := optFloatParam(w, r, "iou")
	if !ok {
		return
	}
	detail, err := s.evaluations.GetSample(r.Context(), id, sampleID, conf, iou)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) serveEvaluationSampleImage(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	sampleID, ok := evaluationSampleID(w, r)
	if !ok {
		return
	}
	f, err := s.evaluations.OpenSampleImage(r.Context(), id, sampleID)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", inlineDisposition(f.Name))
	serveEvaluationImageFile(w, r, f)
}

func (s *server) serveEvaluationSampleThumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	sampleID, ok := evaluationSampleID(w, r)
	if !ok {
		return
	}
	size, ok := enumParam(w, r, "size", dataset.ThumbSmall, dataset.ThumbLarge)
	if !ok {
		return
	}
	f, err := s.evaluations.OpenSampleThumbnail(r.Context(), id, sampleID, size)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	serveEvaluationImageFile(w, r, f)
}

func serveEvaluationImageFile(w http.ResponseWriter, r *http.Request, f *evaluation.ImageFile) {
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, f.Name, f.ModTime, f.File)
}

func (s *server) exportEvaluationReport(w http.ResponseWriter, r *http.Request) {
	id, ok := evaluationID(w, r)
	if !ok {
		return
	}
	format, ok := textParam(w, r, "format")
	if !ok {
		return
	}
	inline, ok := boolQuery(w, r, "inline")
	if !ok {
		return
	}
	f, ok := s.parseSampleFilter(w, r)
	if !ok {
		return
	}
	exp, err := s.evaluations.ExportEvaluationReport(r.Context(), id, format, f)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	serveExportFile(w, r, exp, inline)
}

func (s *server) compareEvaluations(w http.ResponseWriter, r *http.Request) {
	ids, ok := parseIDListParam(w, r, "ids")
	if !ok {
		return
	}
	mode, ok := textParam(w, r, "mode")
	if !ok {
		return
	}
	conf, ok := optFloatParam(w, r, "conf")
	if !ok {
		return
	}
	iou, ok := optFloatParam(w, r, "iou")
	if !ok {
		return
	}
	res, err := s.evaluations.Compare(r.Context(), ids, mode, conf, iou)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) compareEvaluationsDiff(w http.ResponseWriter, r *http.Request) {
	baseID, ok := idQueryParam(w, r, "base_id", "基准评估")
	if !ok {
		return
	}
	targetID, ok := idQueryParam(w, r, "target_id", "对比评估")
	if !ok {
		return
	}
	mode, ok := textParam(w, r, "mode")
	if !ok {
		return
	}
	conf, ok := optFloatParam(w, r, "conf")
	if !ok {
		return
	}
	iou, ok := optFloatParam(w, r, "iou")
	if !ok {
		return
	}
	diffType, ok := textParam(w, r, "diff_type")
	if !ok {
		return
	}
	search, ok := searchParam(w, r)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, 1<<31-1)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", 24, 1, 100)
	if !ok {
		return
	}
	res, err := s.evaluations.CompareDiff(r.Context(), baseID, targetID, mode, conf, iou, diffType, search, offset, limit)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) exportCompareReport(w http.ResponseWriter, r *http.Request) {
	ids, ok := parseIDListParam(w, r, "ids")
	if !ok {
		return
	}
	format, ok := textParam(w, r, "format")
	if !ok {
		return
	}
	mode, ok := textParam(w, r, "mode")
	if !ok {
		return
	}
	conf, ok := optFloatParam(w, r, "conf")
	if !ok {
		return
	}
	iou, ok := optFloatParam(w, r, "iou")
	if !ok {
		return
	}
	inline, ok := boolQuery(w, r, "inline")
	if !ok {
		return
	}
	exp, err := s.evaluations.ExportCompareReport(r.Context(), ids, format, mode, conf, iou)
	if err != nil {
		s.evaluationError(w, r, err)
		return
	}
	serveExportFile(w, r, exp, inline)
}

func serveExportFile(w http.ResponseWriter, r *http.Request, exp evaluation.ExportFile, inline bool) {
	h := w.Header()
	h.Set("Content-Type", exp.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	if inline && strings.HasPrefix(exp.ContentType, "text/html") {
		h.Set("Content-Disposition", inlineDisposition(exp.FileName))
	} else {
		h.Set("Content-Disposition", attachmentDisposition(exp.FileName))
	}
	h.Set("Content-Length", strconv.Itoa(len(exp.Data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(exp.Data)
}
