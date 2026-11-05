package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ListQuery filters the review list; zero values do not filter.
type ListQuery struct {
	Search    string // case-insensitive substring of the name, the description or the project name
	ProjectID int64
	DatasetID int64 // reviews that include the dataset
	ModelID   int64
	Status    string // a review state
}

// ListReviews returns the reviews matching q, newest first.
func (s *Service) ListReviews(ctx context.Context, q ListQuery) ([]Review, error) {
	var args sqlArgs
	where := "WHERE true"
	if q.Search != "" {
		p := args.add(q.Search)
		where += " AND (strpos(lower(r.name), lower(" + p + ")) > 0 OR strpos(lower(r.description), lower(" + p +
			")) > 0 OR strpos(lower(coalesce(p.name, r.model_ref->>'project_name')), lower(" + p + ")) > 0)"
	}
	if q.ProjectID > 0 {
		where += " AND r.project_id = " + args.add(q.ProjectID)
	}
	if q.DatasetID > 0 {
		where += " AND EXISTS (SELECT 1 FROM review_datasets rd WHERE rd.review_id = r.id AND rd.dataset_id = " +
			args.add(q.DatasetID) + ")"
	}
	if q.ModelID > 0 {
		where += " AND r.model_id = " + args.add(q.ModelID)
	}
	switch q.Status {
	case "":
	case StatusQueued, StatusRunning, StatusReady, StatusFailed:
		where += " AND r.status = " + args.add(q.Status)
	default:
		return nil, &Error{Kind: ErrBadRequest, Message: "status 参数只能是 queued、running、ready 或 failed"}
	}
	return s.queryReviews(ctx, s.db, where+" ORDER BY r.id DESC", args...)
}

// GetReview returns one review.
func (s *Service) GetReview(ctx context.Context, id int64) (Review, error) {
	return s.queryReview(ctx, s.db, id)
}

// UpdateInput holds the fields to change; nil leaves a field alone.
type UpdateInput struct {
	Name        *string
	Description *string
}

// UpdateReview changes the name or description of a review.
func (s *Service) UpdateReview(ctx context.Context, id int64, in UpdateInput) (Review, error) {
	var sets []string
	var args sqlArgs
	if in.Name != nil {
		name, err := normalizeName(*in.Name)
		if err != nil {
			return Review{}, err
		}
		sets = append(sets, "name = "+args.add(name))
	}
	if in.Description != nil {
		desc, err := normalizeDescription(*in.Description)
		if err != nil {
			return Review{}, err
		}
		sets = append(sets, "description = "+args.add(desc))
	}
	if len(sets) > 0 {
		tag, err := s.db.Exec(ctx, "UPDATE reviews SET "+strings.Join(sets, ", ")+", updated_at = now() WHERE id = "+
			args.add(id), args...)
		if err != nil {
			return Review{}, err
		}
		if tag.RowsAffected() == 0 {
			return Review{}, errReviewNotFound
		}
	}
	return s.GetReview(ctx, id)
}

// RetryReview queues a failed review again: the inference continues with
// the images not inferred yet.
func (s *Service) RetryReview(ctx context.Context, id int64) (Review, error) {
	var modelID *int64
	var status string
	err := s.db.QueryRow(ctx, "SELECT status, model_id FROM reviews WHERE id = $1", id).Scan(&status, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, errReviewNotFound
	}
	if err != nil {
		return Review{}, err
	}
	if status != StatusFailed {
		return Review{}, &Error{Kind: ErrConflict, Message: "只有识别失败的审图任务可以重试"}
	}
	if modelID == nil {
		return Review{}, &Error{Kind: ErrConflict, Message: "模型已被删除，无法重新识别"}
	}
	if _, err := s.reviewModel(ctx, *modelID, true); err != nil {
		return Review{}, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE reviews SET status = 'queued', stage = '', node_id = NULL, attempts = 0,
			lease_expires_at = NULL, error = '', error_detail = '', result = NULL, started_at = NULL, finished_at = NULL,
			updated_at = now()
		WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		return Review{}, err
	}
	if tag.RowsAffected() == 0 {
		return Review{}, &Error{Kind: ErrConflict, Message: "审图任务的状态已改变，请刷新后重试"}
	}
	s.models.NotifyTasks()
	s.log.Info("review queued again", "review_id", id)
	return s.GetReview(ctx, id)
}

// DeleteReview deletes a review with its folder (the review annotations).
// A node still inferring it gets "not found" from then on and stops. The
// datasets are not touched. The result is nil unless the review was
// deleted but its folder could not be removed completely.
func (s *Service) DeleteReview(ctx context.Context, id int64) (*DeleteResult, error) {
	ctx, cancel := s.detach(ctx)
	defer cancel()
	var trash string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := lockReview(ctx, tx, id, "UPDATE"); err != nil {
			return err
		}
		var merging bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM review_merges WHERE review_id = $1 AND status = 'running')",
			id).Scan(&merging); err != nil {
			return err
		}
		if merging {
			return errMergeRunning
		}
		if _, err := tx.Exec(ctx, "DELETE FROM reviews WHERE id = $1", id); err != nil {
			return err
		}
		// Move the folder out of the way first: this is atomic and quick, so
		// the transaction can still roll back, and the slow removal below
		// cannot leave a half-deleted review folder behind.
		var err error
		trash, err = s.moveToTrash(id)
		return err
	})
	if err != nil {
		if trash != "" {
			s.restoreFromTrash(trash, id)
		}
		return nil, err
	}
	s.log.Info("review deleted", "review_id", id)
	if trash == "" {
		return nil, nil
	}
	defer s.busy.remove(trash)
	if err := s.removeAll(trash); err != nil {
		return s.restoreLeftover(id, trash, err), nil
	}
	return nil, nil
}

// moveToTrash renames the folder of a review to a hidden name in reviews/
// and marks it as in use (the caller removes the mark). A missing folder
// is not an error (nothing to delete).
func (s *Service) moveToTrash(id int64) (string, error) {
	dir := s.reviewDir(id)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat review folder: %w", err)
	}
	trash := filepath.Join(s.reviewsDir(), fmt.Sprintf("%s%d", deletingPrefix, id))
	s.busy.add(trash)
	if _, err := os.Lstat(trash); err == nil {
		// A leftover of an earlier attempt; make room.
		if err := s.removeAll(trash); err != nil {
			s.busy.remove(trash)
			return "", fmt.Errorf("remove old review trash: %w", err)
		}
	}
	if err := os.Rename(dir, trash); err != nil {
		s.busy.remove(trash)
		return "", fmt.Errorf("move review folder aside: %w", err)
	}
	return trash, nil
}

// restoreFromTrash undoes moveToTrash after the delete failed.
func (s *Service) restoreFromTrash(trash string, id int64) {
	defer s.busy.remove(trash)
	if err := os.Rename(trash, s.reviewDir(id)); err != nil {
		s.log.Error("restore review folder after failed delete", "path", trash, "err", err)
	}
}

// restoreLeftover handles a review folder that could not be removed
// completely: what is left gets its name back, so it can be cleaned up by
// hand (review IDs are never reused).
func (s *Service) restoreLeftover(id int64, trash string, removeErr error) *DeleteResult {
	reason := "部分文件无法删除"
	if errors.Is(removeErr, fs.ErrPermission) {
		reason = "权限不足"
	}
	dir := s.reviewDir(id)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) && os.Rename(trash, dir) == nil {
		s.log.Error("review folder only partly deleted; kept the rest under its name", "review_id", id, "path", dir,
			"err", removeErr)
		return &DeleteResult{Message: fmt.Sprintf("审图任务已删除，但文件夹未能完全删除（%s）：%s。请在服务器上手动删除剩余的文件。",
			reason, dir)}
	}
	s.log.Error("review folder only partly deleted", "review_id", id, "path", trash, "err", removeErr)
	return &DeleteResult{Message: fmt.Sprintf("审图任务已删除，但文件夹未能完全删除（%s）：剩余的文件在 %s，请在服务器上手动删除。",
		reason, trash)}
}

// DatasetReview is a review that includes a dataset (the review records
// of a dataset): the review, the dataset's part of it and the merges that
// included the dataset.
type DatasetReview struct {
	Review
	Dataset       Dataset `json:"dataset"`        // with this dataset's counts
	DatasetMerges []Merge `json:"dataset_merges"` // newest first, at most maxDatasetMerges
}

// maxDatasetMerges bounds the merges listed per DatasetReview.
const maxDatasetMerges = 5

var errDatasetNotFound = &Error{Kind: ErrNotFound, Message: "数据集不存在"}

// ListDatasetReviews returns the reviews that include a dataset, newest
// first.
func (s *Service) ListDatasetReviews(ctx context.Context, datasetID int64) ([]DatasetReview, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1)", datasetID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, errDatasetNotFound
	}
	reviews, err := s.ListReviews(ctx, ListQuery{DatasetID: datasetID})
	if err != nil {
		return nil, err
	}
	out := make([]DatasetReview, len(reviews))
	index := make(map[int64]*DatasetReview, len(reviews))
	ids := make([]int64, len(reviews))
	for i, r := range reviews {
		out[i] = DatasetReview{Review: r, DatasetMerges: []Merge{}}
		for _, d := range r.Datasets {
			if d.ID == datasetID {
				out[i].Dataset = d
			}
		}
		index[r.ID] = &out[i]
		ids[i] = r.ID
	}
	if len(ids) == 0 {
		return out, nil
	}
	merges, err := collectMerges(s.db.Query(ctx, `SELECT `+mergeColumns+` FROM (
			SELECT *, row_number() OVER (PARTITION BY review_id ORDER BY id DESC) AS n
			FROM review_merges WHERE review_id = ANY($1) AND $2 = ANY(dataset_ids)) m
		WHERE n <= $3 ORDER BY review_id, id DESC`, ids, datasetID, maxDatasetMerges))
	if err != nil {
		return nil, err
	}
	if err := s.attachVersions(ctx, merges); err != nil {
		return nil, err
	}
	for _, m := range merges {
		r := index[m.ReviewID]
		r.DatasetMerges = append(r.DatasetMerges, m)
	}
	return out, nil
}
