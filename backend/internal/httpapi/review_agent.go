package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/review"
)

// The review inference calls of the training nodes, mounted under
// /api/v1/compute/agent (behind requireNodeToken) next to the conversion
// tasks. A node claims a review_inference task through POST /tasks/claim
// and then sends X-Node-Key with every call about it. They answer 404
// when the review is gone (the node abandons it) and 409 when the node
// does not hold it (any more).

func (s *server) agentReviewRoutes(r chi.Router) {
	r.Route("/review-inferences/{reviewID}", func(r chi.Router) {
		r.Get("/model", s.agentReviewModel)
		r.Get("/images", s.agentReviewImages)
		r.Get("/images/{imageID}/file", s.agentReviewImageFile)
		r.Post("/progress", s.agentReviewProgress)
		r.Post("/results", s.agentReviewResults)
		r.Post("/complete", s.agentReviewComplete)
		r.Post("/fail", s.agentReviewFail)
	})
}

// agentReview parses {reviewID} and resolves the calling node.
func (s *server) agentReview(w http.ResponseWriter, r *http.Request) (id, nodeID int64, ok bool) {
	if id, ok = reviewID(w, r); !ok {
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

func (s *server) agentReviewModel(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	f, err := s.reviews.OpenTaskModel(r.Context(), id, nodeID)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	serveModelFile(w, r, f)
}

func (s *server) agentReviewImages(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "参数 after 无效，应为图片 ID")
			return
		}
		after = n
	}
	limit, ok := intParam(w, r, "limit", review.DefaultTaskImageLimit, 1, review.MaxTaskImageLimit)
	if !ok {
		return
	}
	page, err := s.reviews.ListTaskImages(r.Context(), id, nodeID, after, limit)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) agentReviewImageFile(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	imageID, ok := pathIDOr404(w, r, "imageID", "图片不存在")
	if !ok {
		return
	}
	f, err := s.reviews.OpenTaskImage(r.Context(), id, nodeID, imageID)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	serveImageFile(w, r, f)
}

type reviewProgressRequest struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

func (s *server) agentReviewProgress(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	var req reviewProgressRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	lease, err := s.reviews.ReportProgress(r.Context(), id, nodeID, review.ProgressInput{Stage: req.Stage, Message: req.Message})
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, leaseResponse{LeaseSeconds: lease})
}

type reviewResultsRequest struct {
	Results []review.ImageResult `json:"results"`
}

// agentReviewResults records a batch of results (at most 500 images in at
// most 1 MiB).
func (s *server) agentReviewResults(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	var req reviewResultsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	receipt, err := s.reviews.SubmitResults(r.Context(), id, nodeID, req.Results)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

type reviewCompleteRequest struct {
	Result json.RawMessage `json:"result"`
}

func (s *server) agentReviewComplete(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	var req reviewCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rv, err := s.reviews.CompleteTask(r.Context(), id, nodeID, req.Result)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}

type reviewFailRequest struct {
	Error     string `json:"error"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}

func (s *server) agentReviewFail(w http.ResponseWriter, r *http.Request) {
	id, nodeID, ok := s.agentReview(w, r)
	if !ok {
		return
	}
	var req reviewFailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rv, err := s.reviews.FailTask(r.Context(), id, nodeID, review.FailInput{
		Error: req.Error, Detail: req.Detail, Retryable: req.Retryable,
	})
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}
