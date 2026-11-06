package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

// Reviews and the recycle bin (回收站). A dataset in the bin leaves the
// reviews it is part of: its review_datasets and review_images rows move
// to recycled_review_datasets and recycled_review_images (migration
// 0012_recycle_bin.sql) and come back unchanged when it is restored (same
// IDs, positions, paths, annotations and states), so a review never shows
// it meanwhile and every query here stays as it is. The service is the
// dataset.RecycleHook (NewService installs it).
//
// A model in the bin keeps its reviews; the ones queued for it wait until
// it is restored (claimOne skips them) and fail once it is purged.

// msgModelInBin: the review's model is in the recycle bin.
const msgModelInBin = "模型已在回收站中，无法识别；在回收站中恢复模型后可以重试"

// DetachDataset moves the review rows of a dataset that goes to the
// recycle bin aside. It runs in the dataset's transaction, which holds the
// dataset row FOR UPDATE.
func (s *Service) DetachDataset(ctx context.Context, tx pgx.Tx, datasetID int64) error {
	// The reviews first, as everything else that changes their images does
	// (SubmitResults, changing an image).
	ids, err := lockDatasetReviews(ctx, tx, "review_datasets", datasetID)
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recycled_review_datasets
		SELECT * FROM review_datasets WHERE review_id = ANY($1) AND dataset_id = $2`, ids, datasetID); err != nil {
		return fmt.Errorf("set review datasets aside: %w", err)
	}
	// DELETE waits for images being changed and returns them as changed.
	if _, err := tx.Exec(ctx, `WITH moved AS (DELETE FROM review_images
			WHERE review_id = ANY($1) AND dataset_id = $2 RETURNING *)
		INSERT INTO recycled_review_images SELECT * FROM moved`, ids, datasetID); err != nil {
		return fmt.Errorf("set review images aside: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM review_datasets WHERE review_id = ANY($1) AND dataset_id = $2",
		ids, datasetID); err != nil {
		return err
	}
	s.log.Info("dataset left its reviews for the recycle bin", "dataset_id", datasetID, "reviews", len(ids))
	return nil
}

// AttachDataset puts the review rows of a dataset restored from the
// recycle bin back. A ready review that gets back images not inferred yet
// is queued again (requeueRestored). It runs in the dataset's transaction,
// which holds the dataset row FOR UPDATE.
func (s *Service) AttachDataset(ctx context.Context, tx pgx.Tx, datasetID int64) error {
	ids, err := lockDatasetReviews(ctx, tx, "recycled_review_datasets", datasetID)
	if err != nil || len(ids) == 0 {
		return err
	}
	// review_images refer to review_datasets: those first, and the
	// recycled ones last (their images cascade).
	if _, err := tx.Exec(ctx, `INSERT INTO review_datasets
		SELECT * FROM recycled_review_datasets WHERE review_id = ANY($1) AND dataset_id = $2`, ids, datasetID); err != nil {
		return fmt.Errorf("restore review datasets: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH moved AS (DELETE FROM recycled_review_images
			WHERE review_id = ANY($1) AND dataset_id = $2 RETURNING *)
		INSERT INTO review_images OVERRIDING SYSTEM VALUE SELECT * FROM moved`, ids, datasetID); err != nil {
		return fmt.Errorf("restore review images: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM recycled_review_datasets WHERE review_id = ANY($1) AND dataset_id = $2",
		ids, datasetID); err != nil {
		return err
	}
	queued, err := s.requeueRestored(ctx, tx, ids, datasetID)
	if err != nil {
		return err
	}
	if queued > 0 {
		// Before the commit; a claim that misses it finds the review on its
		// next poll.
		s.models.NotifyTasks()
	}
	s.log.Info("dataset back in its reviews from the recycle bin", "dataset_id", datasetID, "reviews", len(ids),
		"queued_again", queued)
	return nil
}

// lockDatasetReviews locks the reviews that have rows of a dataset in
// table (review_datasets or recycled_review_datasets), in ID order, and
// returns their IDs. A review deleted meanwhile is left out (its rows go
// with it).
func lockDatasetReviews(ctx context.Context, tx pgx.Tx, table string, datasetID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM reviews
		WHERE id IN (SELECT review_id FROM `+table+` WHERE dataset_id = $1)
		ORDER BY id FOR NO KEY UPDATE`, datasetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// requeueRestored queues the ready reviews among ids again (as
// RetryReview does) whose restored images of the dataset are not all
// inferred, if their model can still be used (otherwise they would only
// fail), and returns how many. A model in the recycle bin counts: the
// review waits for it in the queue (claimOne skips it) and fails once the
// model is purged, like any review queued for it.
//
// A running review is left alone: the node holding it lists the images not
// inferred yet from the start again after each pass and only completes on
// an empty one, so it infers the restored images too.
func (s *Service) requeueRestored(ctx context.Context, tx pgx.Tx, ids []int64, datasetID int64) (int, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.model_id FROM reviews r
		WHERE r.id = ANY($1) AND r.status = 'ready' AND r.model_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM review_images ri WHERE ri.review_id = r.id AND ri.dataset_id = $2 AND NOT ri.inferred)`,
		ids, datasetID)
	if err != nil {
		return 0, err
	}
	candidates := map[int64]int64{} // model by review
	var id, modelID int64
	if _, err := pgx.ForEachRow(rows, []any{&id, &modelID}, func() error {
		candidates[id] = modelID
		return nil
	}); err != nil {
		return 0, err
	}
	var queue []int64
	for id, modelID := range candidates {
		usable, err := s.canInferWith(ctx, tx, modelID)
		if err != nil {
			return 0, err
		}
		if usable {
			queue = append(queue, id)
		}
	}
	if len(queue) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE reviews SET status = 'queued', stage = '', node_id = NULL, attempts = 0,
			lease_expires_at = NULL, error = '', error_detail = '', result = NULL, started_at = NULL, finished_at = NULL,
			updated_at = now()
		WHERE id = ANY($1) AND status = 'ready'`, queue)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// canInferWith reports whether a review queued for the model can be
// inferred: the model is in use (or in the recycle bin, where the review
// waits for it), its file is there and its task has boxes.
func (s *Service) canInferWith(ctx context.Context, q querier, modelID int64) (bool, error) {
	m, err := s.models.GetModel(ctx, modelID)
	if errors.Is(err, model.ErrNotFound) {
		// In the recycle bin (the review waits for it), or gone.
		return modelInBin(ctx, q, modelID)
	}
	if err != nil {
		return false, err
	}
	return !m.FileMissing && model.HasBoxes(m.Task), nil
}

// modelInBin reports whether a model is in the recycle bin.
func modelInBin(ctx context.Context, q querier, id int64) (bool, error) {
	var in bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM models WHERE id = $1 AND deleted_at IS NOT NULL)", id).Scan(&in)
	return in, err
}
