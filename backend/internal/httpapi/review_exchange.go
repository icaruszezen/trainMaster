package httpapi

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
)

// The data exchange of model reviews: packages for the local reviewer,
// the results uploaded from it, merges into the datasets, the review
// records of a dataset and the bundled local reviewer itself.

// reviewerToolName is the file name of the bundled local reviewer.
const reviewerToolName = "trainmaster-reviewer.exe"

// ---- packages ----

type createPackageRequest struct {
	Statuses   []string `json:"statuses"`
	DatasetIDs []int64  `json:"dataset_ids"`
}

type packageResponse struct {
	URL        string    `json:"url"`
	FileName   string    `json:"file_name"`
	ExpiresAt  time.Time `json:"expires_at"`
	ImageCount int       `json:"image_count"`
}

func (s *server) createReviewPackage(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	var req createPackageRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	t, err := s.reviews.CreatePackage(r.Context(), id, review.PackageInput{Statuses: req.Statuses, DatasetIDs: req.DatasetIDs})
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, packageResponse{
		URL:      fmt.Sprintf("/api/v1/reviews/%d/packages/%s", id, t.Token),
		FileName: t.FileName, ExpiresAt: t.ExpiresAt, ImageCount: t.ImageCount,
	})
}

// downloadReviewPackage streams a prepared package. Like a dataset
// download it has no Content-Length, and a failure midway aborts the
// connection, so that the browser reports the download as failed instead
// of saving a truncated zip.
func (s *server) downloadReviewPackage(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	token := chi.URLParam(r, "token")
	if token == "" || len(token) > 64 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" {
		notFoundMessage(w, "下载链接已失效，请重新下载")
		return
	}
	p, err := s.reviews.OpenPackage(r.Context(), id, token)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	defer p.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", attachmentDisposition(p.FileName))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if err := p.Write(r.Context(), w); err != nil {
		if r.Context().Err() == nil {
			s.log.Error("review package download failed", "review_id", id, "request_id", RequestID(r.Context()), "err", err)
		}
		panic(http.ErrAbortHandler)
	}
}

// ---- results ----

const msgResultsTooLarge = "审图结果不能超过 4 GiB"

// limitReviewBody is limitBody for review.Service.
func limitReviewBody(w http.ResponseWriter, r *http.Request, limit int64, message string) io.ReadCloser {
	return tooLargeReader{ReadCloser: http.MaxBytesReader(w, r.Body, limit),
		tooLarge: &review.Error{Kind: review.ErrTooLarge, Message: message}}
}

// uploadReviewResults receives a results zip (or a package zipped again)
// as the raw request body with its Content-Length.
func (s *server) uploadReviewResults(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	overwrite := false
	if v := r.URL.Query().Get("overwrite"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "参数 overwrite 无效，应为 true 或 false")
			return
		}
		overwrite = b
	}
	fileName := r.URL.Query().Get("file_name")
	if !utf8.ValidString(fileName) || len(fileName) > 1024 {
		badRequest(w, "参数 file_name 无效")
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); strings.HasPrefix(mt, "multipart/") {
		badRequest(w, "请直接上传 zip 文件本身（Content-Type: application/zip），不要用表单上传")
		return
	}
	switch {
	case r.ContentLength < 0:
		badRequest(w, "缺少请求头 Content-Length")
		return
	case r.ContentLength > review.MaxResultsBytes:
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgResultsTooLarge)
		return
	}
	body := limitReviewBody(w, r, review.MaxResultsBytes, msgResultsTooLarge)
	up, err := s.reviews.UploadResults(r.Context(), id, review.ResultsInput{FileName: fileName, Overwrite: overwrite,
		Size: r.ContentLength, Body: body})
	if err != nil {
		drainRefusedReview(body, err)
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// drainRefusedReview is drainRefused for an upload that review.Service
// refused before reading it (the review is gone, the disk is full).
func drainRefusedReview(body io.Reader, err error) {
	if errors.Is(err, review.ErrNotFound) || errors.Is(err, review.ErrInsufficientStorage) {
		_, _ = io.CopyN(io.Discard, body, maxRefusedDrain)
	}
}

func (s *server) listReviewUploads(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	items, err := s.reviews.ListUploads(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

// ---- merges ----

type startMergeRequest struct {
	DatasetIDs []int64  `json:"dataset_ids"`
	Statuses   []string `json:"statuses"`
	OnExisting string   `json:"on_existing"`
}

func (s *server) startReviewMerge(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	var req startMergeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m, err := s.reviews.StartMerge(r.Context(), id, review.MergeInput{DatasetIDs: req.DatasetIDs, Statuses: req.Statuses,
		OnExisting: req.OnExisting})
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *server) listReviewMerges(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	items, err := s.reviews.ListMerges(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) getReviewMerge(w http.ResponseWriter, r *http.Request) {
	id, ok := reviewID(w, r)
	if !ok {
		return
	}
	mergeID, ok := pathIDOr404(w, r, "mergeID", "合并记录不存在")
	if !ok {
		return
	}
	m, err := s.reviews.GetMerge(r.Context(), id, mergeID)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// ---- the review records of a dataset ----

func (s *server) listDatasetReviews(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	items, err := s.reviews.ListDatasetReviews(r.Context(), id)
	if err != nil {
		s.reviewError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

// ---- the local reviewer ----

// reviewTool describes the bundled local reviewer.
type reviewTool struct {
	Available bool   `json:"available"`
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes"`
	Version   string `json:"version"` // the platform's: they are built together; "" when not available
}

// openReviewerTool opens the bundled local reviewer (TM_REVIEWER_TOOL);
// nil when none is configured or the file is not there. It is looked up
// on every request, so a file put in place later is found.
func (s *server) openReviewerTool() (*os.File, fs.FileInfo) {
	if s.reviewerTool == "" {
		return nil, nil
	}
	f, err := os.Open(s.reviewerTool)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("open the bundled local reviewer", "path", s.reviewerTool, "err", err)
		}
		return nil, nil
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil
	}
	return f, fi
}

func (s *server) getReviewTool(w http.ResponseWriter, _ *http.Request) {
	t := reviewTool{FileName: reviewerToolName}
	if f, fi := s.openReviewerTool(); f != nil {
		f.Close()
		t.Available, t.SizeBytes, t.Version = true, fi.Size(), buildinfo.Version
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) downloadReviewTool(w http.ResponseWriter, r *http.Request) {
	f, fi := s.openReviewerTool()
	if f == nil {
		notFoundMessage(w, "平台没有附带本地审图程序（未配置 TM_REVIEWER_TOOL，或配置的文件不存在），请联系管理员")
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachmentDisposition(reviewerToolName))
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, reviewerToolName, fi.ModTime(), f)
}
