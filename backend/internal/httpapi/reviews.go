package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
)

// Model reviews (模型审图), below /api/v1/reviews: a model runs over the
// images of datasets, and people check the result image by image (see
// package review). The inference itself is a task of the training nodes
// (review_agent.go); packages, results, merges and the local reviewer are
// in review_exchange.go.

func (s *server) reviewRoutes(r chi.Router) {
	r.Get("/", s.listReviews)
	r.Post("/", s.createReview)
	r.Get("/tool", s.getReviewTool)
	r.Get("/tool/download", s.downloadReviewTool)
	r.Route("/{reviewID}", func(r chi.Router) {
		r.Get("/", s.getReview)
		r.Patch("/", s.updateReview)
		r.Delete("/", s.deleteReview)
		r.Post("/retry", s.retryReview)
		r.Get("/images", s.listReviewImages)
		r.Get("/images/{imageID}", s.getReviewImage)
		r.Get("/images/{imageID}/neighbors", s.getReviewImageNeighbors)
		r.Put("/images/{imageID}/annotation", s.saveReviewAnnotation)
		r.Put("/images/{imageID}/status", s.setReviewImageStatus)
		r.Get("/images/{imageID}/file", s.serveReviewImage)
		r.Get("/images/{imageID}/thumbnail", s.serveReviewThumbnail)
		r.Get("/labels", s.listReviewLabels)
		r.Get("/classes", s.listReviewClasses)
		r.Post("/packages", s.createReviewPackage)
		r.Get("/packages/{token}", s.downloadReviewPackage)
		r.Post("/results", s.uploadReviewResults)
		r.Get("/uploads", s.listReviewUploads)
		r.Post("/merges", s.startReviewMerge)
		r.Get("/merges", s.listReviewMerges)
		r.Get("/merges/{mergeID}", s.getReviewMerge)
	})
}

// reviewError writes the response for an error returned by
// review.Service; problems with dataset images are dataset errors.
func (s *server) reviewError(w http.ResponseWriter, r *http.Request, err error) {
	var re *review.Error
	if errors.As(err, &re) {
		status, code := reviewErrorStatus(re.Kind)
		writeError(w, status, code, re.Message)
		return
	}
	var de *dataset.Error
	if errors.As(err, &de) {
		s.datasetError(w, r, err)
		return
	}
	s.unexpectedError(w, r, err)
}

func reviewErrorStatus(kind error) (int, string) {
	switch kind {
	case review.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case review.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case review.ErrBadRequest:
		return http.StatusBadRequest, codeBadRequest
	case review.ErrConflict:
		return http.StatusConflict, codeConflict
	case review.ErrAnnotationChanged:
		return http.StatusConflict, codeAnnotationConflict
	case review.ErrTooLarge:
		return http.StatusRequestEntityTooLarge, codePayloadTooLarge
	case review.ErrInsufficientStorage:
		return http.StatusInsufficientStorage, codeInsufficientStorage
	}
	return http.StatusInternalServerError, codeInternal
}

// reviewID parses {reviewID}; reviewImageIDs also {imageID}. They answer
// 404 for invalid IDs.
func reviewID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "reviewID", "审图任务不存在")
}

func reviewImageIDs(w http.ResponseWriter, r *http.Request) (id, imageID int64, ok bool) {
	if id, ok = reviewID(w, r); !ok {
		return 0, 0, false
	}
	if imageID, ok = pathIDOr404(w, r, "imageID", "图片不存在"); !ok {
		return 0, 0, false
	}
	return id, imageID, true
}

// idQueryParam parses an optional positive ID query parameter (0 when
// absent).
func idQueryParam(w http.ResponseWriter, r *http.Request, name, what string) (int64, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		badRequest(w, "参数 "+name+" 无效，应为"+what+" ID")
		return 0, false
	}
	return id, true
}

func (s *server) listReviews(w http.ResponseWriter, r *http.Request) {
	search, ok := searchParam(w, r)
	if !ok {
		return
	}
	q := review.ListQuery{Search: search}
	if q.ProjectID, ok = idQueryParam(w, r, "project_id", "项目"); !ok {
		return
	}
	if q.DatasetID, ok = idQueryParam(w, r, "dataset_id", "数据集"); !ok {
		return
	}
	if q.ModelID, ok = idQueryParam(w, r, "model_id", "模型"); !ok {
		return
	}
	if r.URL.Query().Get("status") != "" {
		if q.Status, ok = enumParam(w, r, "status", review.StatusQueued, review.StatusRunning, review.StatusReady,
			review.StatusFailed); !ok {
			return
		}
	}
	items, err := s.reviews.ListReviews(r.Context(), q)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

type reviewOptionsRequest struct {
	Conf    *float64        `json:"conf"`
	IoU     *float64        `json:"iou"`
	ImgSize json.RawMessage `json:"imgsz"` // an integer, [height, width] or null
	MaxDet  *int            `json:"max_det"`
}

type createReviewRequest struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	ModelID     int64                 `json:"model_id"`
	DatasetIDs  []int64               `json:"dataset_ids"`
	Options     *reviewOptionsRequest `json:"options"`
}

func (s *server) createReview(w http.ResponseWriter, r *http.Request) {
	var req createReviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := review.CreateInput{Name: req.Name, Description: req.Description, ModelID: req.ModelID, DatasetIDs: req.DatasetIDs}
	if o := req.Options; o != nil {
		imgsz, ok := parseImgSize(o.ImgSize)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "输入尺寸 imgsz 应为整数或 [高, 宽]")
			return
		}
		in.Options = review.OptionsInput{Conf: o.Conf, IoU: o.IoU, ImgSize: imgsz, MaxDet: o.MaxDet}
	}
	rv, err := s.reviews.CreateReview(r.Context(), in)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rv)
}

func (s *server) getReview(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	rv, err := s.reviews.GetReview(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}

type updateReviewRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *server) updateReview(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	var req updateReviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rv, err := s.reviews.UpdateReview(r.Context(), id, review.UpdateInput{Name: req.Name, Description: req.Description})
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}

func (s *server) deleteReview(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	res, err := s.reviews.DeleteReview(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	if res != nil { // deleted, but part of the folder is left on disk
		writeJSON(w, http.StatusOK, res)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) retryReview(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	rv, err := s.reviews.RetryReview(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, rv)
}

// reviewImageFilters parses the filters of the image list and neighbors.
func reviewImageFilters(w http.ResponseWriter, r *http.Request) (review.ImageQuery, bool) {
	var q review.ImageQuery
	var ok bool
	if q.Status, ok = enumParam(w, r, "status", review.FilterAll, "pending", "approved", "needs_sample",
		review.FilterReviewed); !ok {
		return q, false
	}
	if q.DatasetID, ok = idQueryParam(w, r, "dataset_id", "数据集"); !ok {
		return q, false
	}
	if q.Label = r.URL.Query().Get("label"); !utf8.ValidString(q.Label) || strings.ContainsRune(q.Label, 0) {
		badRequest(w, "参数 label 无效：包含无效字符")
		return q, false
	}
	if q.Search, ok = searchParam(w, r); !ok {
		return q, false
	}
	if q.Edited, ok = enumParam(w, r, "edited", review.FilterAll, review.FilterEdited, review.FilterUnedited); !ok {
		return q, false
	}
	if q.Boxes, ok = enumParam(w, r, "boxes", review.FilterAll, review.FilterWith, review.FilterWithout); !ok {
		return q, false
	}
	if q.Inference, ok = enumParam(w, r, "inference", review.FilterAll, review.FilterFailed); !ok {
		return q, false
	}
	return q, true
}

func (s *server) listReviewImages(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	q, ok := reviewImageFilters(w, r)
	if !ok {
		return
	}
	if q.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<30); !ok {
		return
	}
	if q.Limit, ok = intParam(w, r, "limit", review.DefaultImageLimit, 1, review.MaxImageLimit); !ok {
		return
	}
	items, total, err := s.reviews.ListImages(r.Context(), id, q)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, total))
}

func (s *server) getReviewImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	img, err := s.reviews.GetImage(r.Context(), id, imageID)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (s *server) getReviewImageNeighbors(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	q, ok := reviewImageFilters(w, r)
	if !ok {
		return
	}
	n, err := s.reviews.ImageNeighbors(r.Context(), id, imageID, q)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type saveReviewAnnotationRequest struct {
	Objects     *[]dataset.AnnotationObject `json:"objects"`
	BaseVersion *string                     `json:"base_version"`
	Force       bool                        `json:"force"` // skip the version check
}

func (s *server) saveReviewAnnotation(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	var req saveReviewAnnotationRequest
	if !decodeJSONLimit(w, r, &req, maxAnnotationBody) {
		return
	}
	switch {
	case req.Objects == nil:
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 objects（目标列表，没有目标时为 []）")
		return
	case req.BaseVersion == nil && !req.Force:
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 base_version（开始编辑时的标注版本，没有标注时为空字符串）")
		return
	}
	in := review.SaveInput{Objects: *req.Objects, Force: req.Force}
	if req.BaseVersion != nil {
		in.BaseVersion = *req.BaseVersion
	}
	img, err := s.reviews.SaveAnnotation(r.Context(), id, imageID, in)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

type setReviewStatusRequest struct {
	Status string `json:"status"`
}

func (s *server) setReviewImageStatus(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	var req setReviewStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	img, err := s.reviews.SetStatus(r.Context(), id, imageID, req.Status)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (s *server) serveReviewImage(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	f, err := s.reviews.OpenImage(r.Context(), id, imageID)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", inlineDisposition(f.Name))
	serveImageFile(w, r, f)
}

func (s *server) serveReviewThumbnail(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := reviewImageIDs(w, r)
	if !ok {
		return
	}
	size, ok := enumParam(w, r, "size", dataset.ThumbSmall, dataset.ThumbLarge)
	if !ok {
		return
	}
	f, err := s.reviews.OpenThumbnail(r.Context(), id, imageID, size)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	serveImageFile(w, r, f)
}

func (s *server) listReviewLabels(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	labels, err := s.reviews.ListLabels(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(labels, len(labels)))
}

func (s *server) listReviewClasses(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	list, err := s.reviews.ListClasses(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
