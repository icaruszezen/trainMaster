package review

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// The inference queue. A new review is queued; training nodes that offer
// review_inference in their claims (model.Service.ClaimTask) get the
// oldest one (SELECT ... FOR UPDATE SKIP LOCKED, so two nodes never get
// the same one). The node then holds a lease: every call about the review
// renews it, and a review whose lease expires (the node crashed or lost
// the network) is queued again, or fails after maxAttempts claims. The
// node downloads the weights, pages through the images not inferred yet,
// downloads and infers them and posts the boxes in batches; each batch is
// written (VOC files and rows) in one transaction, so a new claim after a
// failure continues with the images still missing.

const (
	// leaseRenewEvery: model downloads that take long renew the lease this
	// often.
	leaseRenewEvery = 15 * time.Second
	// maxAttempts bounds the claims of a review since a user queued it.
	maxAttempts = 3
	// maxClaimSkips bounds the queued reviews a claim fails (model gone)
	// before it gives up for this round.
	maxClaimSkips = 20

	// MaxResults bounds the image results of one report.
	MaxResults = 500
	// DefaultTaskImageLimit and MaxTaskImageLimit bound a page of images
	// to infer.
	DefaultTaskImageLimit = 200
	MaxTaskImageLimit     = 500
	// maxBoxes bounds the boxes kept per image (by confidence).
	maxBoxes = 1000
)

var msgLeaseLost = fmt.Sprintf("训练节点在识别过程中失去联系（已尝试 %d 次）", maxAttempts)

func leaseSeconds() int { return int(model.LeaseDuration / time.Second) }

// Kind is the task kind of the reviews' inference (model.TaskSource).
func (s *Service) Kind() string { return TaskKind }

// Claim hands the oldest queued review to the node (model.TaskSource). A
// queued review whose model was deleted, or whose weights are missing,
// fails on the way.
func (s *Service) Claim(ctx context.Context, nodeID int64) (any, error) {
	for range maxClaimSkips {
		task, skipped, err := s.claimOne(ctx, nodeID)
		switch {
		case err != nil:
			return nil, err
		case task != nil:
			return task, nil
		case !skipped:
			return nil, nil
		}
	}
	return nil, nil
}

// claimOne claims the oldest queued review, or fails it when its model
// cannot be used (skipped).
func (s *Service) claimOne(ctx context.Context, nodeID int64) (task *Task, skipped bool, err error) {
	var failed int64
	var failMsg string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id int64
		var modelID *int64
		// Reviews whose model is in the recycle bin wait for it.
		err := tx.QueryRow(ctx, `SELECT id, model_id FROM reviews r WHERE status = 'queued'
				AND NOT EXISTS (SELECT 1 FROM models m WHERE m.id = r.model_id AND m.deleted_at IS NOT NULL)
			ORDER BY id FOR UPDATE OF r SKIP LOCKED LIMIT 1`).Scan(&id, &modelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var m model.Model
		switch {
		case modelID == nil:
			failMsg = msgModelDeleted
		default:
			m, err = s.models.GetModel(ctx, *modelID)
			switch {
			case errors.Is(err, model.ErrNotFound):
				failMsg = msgModelDeleted
			case err != nil:
				return err
			case m.FileMissing:
				failMsg = msgModelFileMissing
			}
		}
		if failMsg != "" {
			failed, skipped = id, true
			_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'failed', stage = '', lease_expires_at = NULL,
					error = $2, error_detail = '', finished_at = now(), updated_at = now()
				WHERE id = $1`, id, failMsg)
			return err
		}

		t := Task{Kind: TaskKind, ID: id, LeaseSeconds: leaseSeconds()}
		if err := tx.QueryRow(ctx, `UPDATE reviews SET status = 'running', stage = 'downloading', node_id = $2,
				attempts = attempts + 1, lease_expires_at = now() + make_interval(secs => $3),
				started_at = coalesce(started_at, now()), error = '', error_detail = '', result = NULL,
				finished_at = NULL, updated_at = now()
			WHERE id = $1 RETURNING options`, id, nodeID, model.LeaseDuration.Seconds()).Scan(&t.Options); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE NOT inferred)
			FROM review_images WHERE review_id = $1`, id).Scan(&t.ImageCount, &t.Remaining); err != nil {
			return err
		}
		names := m.Info.ClassNames
		if names == nil {
			names = []string{}
		}
		t.Model = TaskModel{ID: m.ID, ProjectID: m.Project.ID, Version: m.Version, YOLOVersion: m.YOLOVersion,
			Task: m.Task, FileName: m.FileName, SizeBytes: m.SizeBytes, SHA256: m.SHA256, ClassNames: names}
		base := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d/", id)
		t.ModelURL, t.ImagesURL, t.ProgressURL = base+"model", base+"images", base+"progress"
		t.ResultsURL, t.CompleteURL, t.FailURL = base+"results", base+"complete", base+"fail"
		task = &t
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("claim review: %w", err)
	}
	if skipped {
		s.log.Warn("queued review failed: its model cannot be used", "review_id", failed, "error", failMsg)
	}
	if task != nil {
		s.models.TouchWorker(nodeID, TaskKind)
		s.log.Info("review inference claimed", "review_id", task.ID, "model_id", task.Model.ID, "node_id", nodeID,
			"images", task.ImageCount, "remaining", task.Remaining)
	}
	return task, skipped, nil
}

// renewLease extends the lease of a review the node holds, optionally
// setting its stage; errTaskGone or errTaskNotYours if it does not hold
// it (any more). In a transaction it also locks the review's row.
func (s *Service) renewLease(ctx context.Context, q querier, reviewID, nodeID int64, stage string) error {
	tag, err := q.Exec(ctx, `UPDATE reviews
		SET lease_expires_at = now() + make_interval(secs => $3), stage = coalesce(nullif($4, ''), stage), updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`, reviewID, nodeID, model.LeaseDuration.Seconds(), stage)
	if err != nil {
		return err
	}
	s.models.TouchWorker(nodeID, TaskKind)
	if tag.RowsAffected() == 0 {
		return s.taskError(ctx, q, reviewID)
	}
	return nil
}

// taskError explains why a node does not hold a review.
func (s *Service) taskError(ctx context.Context, q querier, reviewID int64) error {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM reviews WHERE id = $1)", reviewID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errTaskGone
	}
	return errTaskNotYours
}

// lockHeld locks a review the node holds (FOR UPDATE) and returns its
// attempts.
func lockHeld(ctx context.Context, tx pgx.Tx, reviewID, nodeID int64) (attempts int, err error) {
	var status string
	var holder *int64
	err = tx.QueryRow(ctx, "SELECT status, node_id, attempts FROM reviews WHERE id = $1 FOR UPDATE", reviewID).
		Scan(&status, &holder, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errTaskGone
	}
	if err != nil {
		return 0, err
	}
	if status != StatusRunning || holder == nil || *holder != nodeID {
		return 0, errTaskNotYours
	}
	return attempts, nil
}

// failHeld fails a review the node holds because it cannot be inferred.
func (s *Service) failHeld(ctx context.Context, reviewID, nodeID int64, msg string) error {
	tag, err := s.db.Exec(ctx, `UPDATE reviews SET status = 'failed', stage = '', lease_expires_at = NULL,
			error = $3, finished_at = now(), updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`, reviewID, nodeID, msg)
	if err == nil && tag.RowsAffected() > 0 {
		s.log.Warn("review failed", "review_id", reviewID, "node_id", nodeID, "error", msg)
	}
	return err
}

// OpenTaskModel opens the weights of a review the node holds. Reading the
// file renews the lease, so a slow download does not lose it. When the
// model or its weights are gone the review fails (it cannot succeed).
func (s *Service) OpenTaskModel(ctx context.Context, reviewID, nodeID int64) (*model.File, error) {
	if err := s.renewLease(ctx, s.db, reviewID, nodeID, ""); err != nil {
		return nil, err
	}
	var modelID *int64
	if err := s.db.QueryRow(ctx, "SELECT model_id FROM reviews WHERE id = $1", reviewID).Scan(&modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errTaskGone
		}
		return nil, err
	}
	gone := func(msg string) (*model.File, error) {
		if err := s.failHeld(ctx, reviewID, nodeID, msg); err != nil {
			return nil, err
		}
		return nil, &Error{Kind: ErrNotFound, Message: msg}
	}
	if modelID == nil {
		return gone(msgModelDeleted)
	}
	f, err := s.models.OpenModelFile(ctx, *modelID)
	if errors.Is(err, model.ErrNotFound) {
		if _, gerr := s.models.GetModel(ctx, *modelID); errors.Is(gerr, model.ErrNotFound) {
			if in, err := modelInBin(ctx, s.db, *modelID); err != nil {
				return nil, err
			} else if in {
				return gone(msgModelInBin)
			}
			return gone(msgModelDeleted)
		}
		return gone(msgModelFileMissing)
	}
	if err != nil {
		return nil, err
	}
	f.Content = &leaseReader{ReadSeeker: f.Content, renew: func() error {
		return s.renewLease(ctx, s.db, reviewID, nodeID, "")
	}, last: time.Now()}
	return f, nil
}

// leaseReader renews a lease every leaseRenewEvery while it is read, and
// fails once the node no longer holds the review.
type leaseReader struct {
	io.ReadSeeker
	renew func() error
	last  time.Time
}

func (l *leaseReader) Read(p []byte) (int, error) {
	if time.Since(l.last) >= leaseRenewEvery {
		if err := l.renew(); err != nil {
			return 0, err
		}
		l.last = time.Now()
	}
	return l.ReadSeeker.Read(p)
}

// ListTaskImages returns the next images (id > after, by id) the node has
// still to infer, and renews the lease. limit 0 means the default.
func (s *Service) ListTaskImages(ctx context.Context, reviewID, nodeID, after int64, limit int) (TaskImages, error) {
	if limit == 0 {
		limit = DefaultTaskImageLimit
	}
	if limit < 1 || limit > MaxTaskImageLimit || after < 0 {
		return TaskImages{}, newError(ErrBadRequest, "limit 必须在 1–%d 之间，after 不能为负数", MaxTaskImageLimit)
	}
	if err := s.renewLease(ctx, s.db, reviewID, nodeID, ""); err != nil {
		return TaskImages{}, err
	}
	// The size as the dataset's index has it now: a rescan updates it when
	// a file is replaced by another one.
	rows, err := s.db.Query(ctx, `SELECT ri.id, ri.file_name, coalesce(di.width, ri.width), coalesce(di.height, ri.height),
			di.size_bytes
		FROM review_images ri JOIN dataset_images di ON di.id = ri.image_id
		WHERE ri.review_id = $1 AND NOT ri.inferred AND ri.id > $2
		ORDER BY ri.id LIMIT $3`, reviewID, after, limit)
	if err != nil {
		return TaskImages{}, err
	}
	base := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d/images/", reviewID)
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TaskImage, error) {
		var t TaskImage
		err := row.Scan(&t.ID, &t.FileName, &t.Width, &t.Height, &t.SizeBytes)
		t.URL = fmt.Sprintf("%s%d/file", base, t.ID)
		return t, err
	})
	if err != nil {
		return TaskImages{}, err
	}
	out := TaskImages{Items: items}
	if out.Items == nil {
		out.Items = []TaskImage{}
	}
	err = s.db.QueryRow(ctx, "SELECT count(*) FROM review_images WHERE review_id = $1 AND NOT inferred", reviewID).
		Scan(&out.Remaining)
	return out, err
}

// OpenTaskImage opens the file of an image of a review the node holds,
// and renews the lease. An image that cannot be read from its dataset (the
// file or the dataset folder is missing) is ErrNotFound with the dataset's
// message: the node reports that image as failed and goes on (a conflict
// would tell it that it no longer holds the review).
func (s *Service) OpenTaskImage(ctx context.Context, reviewID, nodeID, imageID int64) (*dataset.ImageFile, error) {
	if err := s.renewLease(ctx, s.db, reviewID, nodeID, ""); err != nil {
		return nil, err
	}
	datasetID, datasetImageID, err := s.imageSource(ctx, reviewID, imageID)
	if err != nil {
		return nil, err
	}
	f, err := s.datasets.OpenImage(ctx, datasetID, datasetImageID)
	var de *dataset.Error
	if errors.As(err, &de) && (errors.Is(err, dataset.ErrNotFound) || errors.Is(err, dataset.ErrConflict)) {
		return nil, &Error{Kind: ErrNotFound, Message: de.Message}
	}
	return f, err
}

// ProgressInput is a node's report on a review it holds.
type ProgressInput struct {
	Stage   string // downloading or inferring
	Message string // for the log
}

// ReportProgress records the stage of a review the node holds and renews
// its lease. It returns the lease in seconds.
func (s *Service) ReportProgress(ctx context.Context, reviewID, nodeID int64, in ProgressInput) (int, error) {
	if in.Stage != StageDownloading && in.Stage != StageInferring {
		return 0, &Error{Kind: ErrInvalid, Message: "stage 必须是 downloading 或 inferring"}
	}
	if err := s.renewLease(ctx, s.db, reviewID, nodeID, in.Stage); err != nil {
		return 0, err
	}
	if message := cleanLabel(in.Message, maxErrorRunes); message != "" {
		s.log.Debug("review inference progress", "review_id", reviewID, "node_id", nodeID, "stage", in.Stage,
			"message", message)
	}
	return leaseSeconds(), nil
}

// ResultsReceipt answers a report of results.
type ResultsReceipt struct {
	LeaseSeconds int `json:"lease_seconds"`
	Accepted     int `json:"accepted"`  // results recorded now
	Remaining    int `json:"remaining"` // images not inferred yet
}

// SubmitResults records the node's results for images of a review it
// holds, in one transaction: the boxes of each image are cleaned (see
// sanitizeBoxes) and written as its VOC file, a failed image keeps its
// error and gets no file. The size of an image is the one the dataset's
// index has now (a rescan updates it when the file is replaced), else the
// one copied when the review was created, and only when neither is known
// the one the node reports; the row keeps it. Results for images already
// inferred or not in the review are ignored, so a report can be sent
// again. It renews the lease.
func (s *Service) SubmitResults(ctx context.Context, reviewID, nodeID int64, results []ImageResult) (ResultsReceipt, error) {
	if len(results) > MaxResults {
		return ResultsReceipt{}, newError(ErrInvalid, "一次最多提交 %d 张图片的结果", MaxResults)
	}
	ids := make([]int64, 0, len(results))
	for _, r := range results {
		if r.ID <= 0 {
			return ResultsReceipt{}, &Error{Kind: ErrInvalid, Message: "results 中有无效的图片 ID"}
		}
		ids = append(ids, r.ID)
	}
	ctx, cancel := s.detach(ctx) // files are written inside the transaction
	defer cancel()

	type target struct {
		relPath, annotationPath string
		width, height           *int // the review image's
		indexW, indexH          *int // the dataset image's now
	}
	type file struct{ tmp, rel string }
	var staged []file   // VOC files written to temporary files
	var placed []string // VOC files renamed into place
	receipt := ResultsReceipt{LeaseSeconds: leaseSeconds()}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.renewLease(ctx, tx, reviewID, nodeID, StageInferring); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT ri.id, ri.rel_path, ri.annotation_path, ri.width, ri.height, di.width, di.height
			FROM review_images ri JOIN dataset_images di ON di.id = ri.image_id
			WHERE ri.review_id = $1 AND ri.id = ANY($2) AND NOT ri.inferred ORDER BY ri.id FOR UPDATE OF ri`, reviewID, ids)
		if err != nil {
			return err
		}
		targets := map[int64]*target{}
		var id int64
		var t target
		if _, err := pgx.ForEachRow(rows, []any{&id, &t.relPath, &t.annotationPath, &t.width, &t.height,
			&t.indexW, &t.indexH}, func() error {
			c := t
			targets[id] = &c
			return nil
		}); err != nil {
			return err
		}
		if len(targets) > 0 {
			r, err := s.openReviewRoot(reviewID, true)
			if err != nil {
				return err
			}
			defer r.Close()
			batch := &pgx.Batch{}
			for _, res := range results {
				t := targets[res.ID]
				if t == nil {
					continue // inferred already, repeated, or not in the review
				}
				delete(targets, res.ID)
				if !res.OK {
					msg := cleanText(strings.TrimSpace(res.Error), maxErrorRunes)
					if msg == "" {
						msg = "识别失败（训练节点没有给出原因）"
					}
					batch.Queue(`UPDATE review_images SET inferred = true, inference_error = $2, predictions = '[]',
							object_count = 0, labels = '{}', edited = false, annotation_version = '', updated_at = now()
						WHERE id = $1`, res.ID, msg)
					receipt.Accepted++
					continue
				}
				width, height := t.width, t.height
				switch {
				case t.indexW != nil && t.indexH != nil:
					width, height = t.indexW, t.indexH
				case (width == nil || height == nil) && res.Width > 0 && res.Height > 0:
					width, height = &res.Width, &res.Height
				}
				w, h := 0, 0
				if width != nil && height != nil {
					w, h = *width, *height
				}
				predictions := sanitizeBoxes(res.Boxes, w, h)
				objects := reviewfile.PredictionObjects(predictions)
				data := reviewfile.EncodeAnnotation(t.relPath, width, height, objects)
				tmp, err := reviewfile.StageFile(r, t.annotationPath, data)
				if err != nil {
					return fmt.Errorf("write review annotation: %w", err)
				}
				staged = append(staged, file{tmp, t.annotationPath})
				batch.Queue(`UPDATE review_images SET inferred = true, inference_error = '', predictions = $2,
						object_count = $3, labels = $4, edited = false, annotation_version = $5,
						width = $6, height = $7, updated_at = now()
					WHERE id = $1`, res.ID, predictions, len(objects), reviewfile.LabelCounts(objects),
					reviewfile.AnnotationVersion(data), width, height)
				receipt.Accepted++
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
			for i, f := range staged {
				if err := reviewfile.Rename(r, f.tmp, f.rel); err != nil {
					return fmt.Errorf("write review annotation: %w", err)
				}
				placed = append(placed, f.rel)
				staged[i].tmp = ""
			}
		}
		return tx.QueryRow(ctx, "SELECT count(*) FROM review_images WHERE review_id = $1 AND NOT inferred", reviewID).
			Scan(&receipt.Remaining)
	})
	if err != nil {
		if len(staged) > 0 {
			// The images stay not inferred: drop what was written for them.
			if r, rerr := os.OpenRoot(s.reviewDir(reviewID)); rerr == nil {
				for _, f := range staged {
					if f.tmp != "" {
						_ = r.Remove(filepath.FromSlash(f.tmp))
					}
				}
				for _, rel := range placed {
					_ = r.Remove(filepath.FromSlash(rel))
				}
				r.Close()
			}
		}
		return ResultsReceipt{}, err
	}
	if receipt.Accepted > 0 {
		s.log.Debug("review inference results", "review_id", reviewID, "node_id", nodeID,
			"accepted", receipt.Accepted, "remaining", receipt.Remaining)
	}
	return receipt, nil
}

// sanitizeBoxes turns the boxes a node reported into predictions: names
// trimmed and cleaned (at most 255 characters; boxes without a name are
// dropped), coordinates clamped to the image (when its size is known; to
// [0, 2^31-1] otherwise) and rounded to whole pixels, boxes without area
// dropped, confidences clamped to [0, 1] and rounded to 4 decimals. At
// most maxBoxes are kept, the most confident first.
func sanitizeBoxes(boxes []ResultBox, width, height int) []Prediction {
	out := make([]Prediction, 0, len(boxes))
	for _, b := range boxes {
		name := cleanLabel(b.Name, maxLabelRunes)
		if name == "" || !finite(b.Xmin, b.Ymin, b.Xmax, b.Ymax, b.Confidence) {
			continue
		}
		p := Prediction{
			Name:       name,
			Confidence: roundTo(min(max(b.Confidence, 0), 1), 4),
			Xmin:       clampCoord(b.Xmin, width),
			Ymin:       clampCoord(b.Ymin, height),
			Xmax:       clampCoord(b.Xmax, width),
			Ymax:       clampCoord(b.Ymax, height),
		}
		if p.Xmin >= p.Xmax || p.Ymin >= p.Ymax {
			continue
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Prediction) int { return cmp.Compare(b.Confidence, a.Confidence) })
	if len(out) > maxBoxes {
		out = out[:maxBoxes]
	}
	return out
}

func finite(values ...float64) bool {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// clampCoord limits a coordinate to [0, limit] (limit <= 0: the size is
// unknown, [0, MaxInt32]) and rounds it to a whole pixel.
func clampCoord(v float64, limit int) int {
	hi := float64(math.MaxInt32)
	if limit > 0 {
		hi = float64(limit)
	}
	return int(math.Round(min(max(v, 0), hi)))
}

// CompleteTask finishes a review the node holds once every image is
// inferred: it becomes ready with what the node reported about the run.
func (s *Service) CompleteTask(ctx context.Context, reviewID, nodeID int64, result json.RawMessage) (Review, error) {
	resultJSON, err := json.Marshal(sanitizeResult(result))
	if err != nil {
		return Review{}, fmt.Errorf("encode review result: %w", err)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := lockHeld(ctx, tx, reviewID, nodeID); err != nil {
			return err
		}
		var remaining int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM review_images WHERE review_id = $1 AND NOT inferred", reviewID).
			Scan(&remaining); err != nil {
			return err
		}
		if remaining > 0 {
			return newError(ErrConflict, "还有 %d 张图片没有识别", remaining)
		}
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'ready', stage = '', lease_expires_at = NULL, result = $2,
				finished_at = now(), updated_at = now()
			WHERE id = $1`, reviewID, resultJSON)
		return err
	})
	if err != nil {
		return Review{}, err
	}
	s.models.TouchWorker(nodeID, TaskKind)
	r, err := s.GetReview(ctx, reviewID)
	if err == nil {
		s.log.Info("review inference finished", "review_id", reviewID, "node_id", nodeID, "images", r.Counts.Total,
			"failed_images", r.Counts.InferenceFailed, "attempts", r.Attempts)
	}
	return r, err
}

// sanitizeResult keeps the known fields of a node's report, cleaned.
func sanitizeResult(raw json.RawMessage) InferenceResult {
	var r InferenceResult
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return r
	}
	str := func(key string, n int) string {
		v, _ := m[key].(string)
		return cleanLabel(v, n)
	}
	num := func(key string, hi float64) *float64 {
		v, ok := m[key].(float64)
		if !ok || !finite(v) || v < 0 || v > hi {
			return nil
		}
		v = roundTo(v, 3)
		return &v
	}
	r.Device = str("device", 64)
	r.TorchVersion = str("torch_version", 64)
	r.UltralyticsVersion = str("ultralytics_version", 64)
	r.NodeName = str("node_name", 255)
	r.ElapsedSeconds = num("elapsed_seconds", 1e9)
	r.ImagesPerSecond = num("images_per_second", 1e6)
	return r
}

// FailInput is a node's report that a review's inference failed.
type FailInput struct {
	Error     string // in Chinese, shown to users
	Detail    string // the node's error output
	Retryable bool   // e.g. a network error: another attempt may succeed
}

// FailTask ends the inference of a review the node holds: a retryable
// failure queues it again unless it has been claimed maxAttempts times;
// anything else fails it with the node's message. Images inferred so far
// keep their results.
func (s *Service) FailTask(ctx context.Context, reviewID, nodeID int64, in FailInput) (Review, error) {
	msg := cleanText(strings.TrimSpace(in.Error), maxErrorRunes)
	if msg == "" {
		msg = "识别失败（训练节点没有给出原因）"
	}
	detail := truncateBytes(cleanText(in.Detail, maxDetailBytes), maxDetailBytes)
	var requeued bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		attempts, err := lockHeld(ctx, tx, reviewID, nodeID)
		if err != nil {
			return err
		}
		if requeued = in.Retryable && attempts < maxAttempts; requeued {
			_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'queued', stage = '', node_id = NULL,
					lease_expires_at = NULL, updated_at = now()
				WHERE id = $1`, reviewID)
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE reviews SET status = 'failed', stage = '', lease_expires_at = NULL,
				error = $2, error_detail = $3, finished_at = now(), updated_at = now()
			WHERE id = $1`, reviewID, msg, detail)
		return err
	})
	if err != nil {
		return Review{}, err
	}
	s.models.TouchWorker(nodeID, TaskKind)
	if requeued {
		s.models.NotifyTasks()
		s.log.Warn("review inference attempt failed; queued again", "review_id", reviewID, "node_id", nodeID, "error", msg)
	} else {
		s.log.Warn("review inference failed", "review_id", reviewID, "node_id", nodeID, "error", msg,
			"retryable", in.Retryable)
	}
	return s.GetReview(ctx, reviewID)
}

// expireLeases queues the running reviews whose node stopped reporting
// again, or fails them after maxAttempts claims.
func (s *Service) expireLeases(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE reviews SET
			status = CASE WHEN attempts < $1 THEN 'queued' ELSE 'failed' END,
			node_id = CASE WHEN attempts < $1 THEN NULL ELSE node_id END,
			finished_at = CASE WHEN attempts < $1 THEN NULL ELSE now() END,
			error = CASE WHEN attempts < $1 THEN '' ELSE $2 END,
			stage = '', lease_expires_at = NULL, updated_at = now()
		WHERE status = 'running' AND lease_expires_at < now()
		RETURNING id, status`, maxAttempts, msgLeaseLost)
	if err != nil {
		return err
	}
	var id int64
	var status string
	var requeued, failed int
	if _, err := pgx.ForEachRow(rows, []any{&id, &status}, func() error {
		if status == StatusQueued {
			requeued++
		} else {
			failed++
		}
		return nil
	}); err != nil {
		return err
	}
	if requeued > 0 {
		s.models.NotifyTasks()
	}
	if requeued+failed > 0 {
		s.log.Warn("reviews lost their node", "queued_again", requeued, "failed", failed)
	}
	return nil
}
