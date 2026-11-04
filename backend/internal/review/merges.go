package review

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Merges (合并到数据集): a background job copies the review annotations of
// the chosen images into the VOC files of their datasets through
// dataset.Service.SaveAnnotation, as an edit in the annotation workspace
// would (the dataset index and counters follow; the version check guards
// against a concurrent edit). The review keeps its own annotations. At
// most one merge runs per review (a unique index), and a review cannot be
// deleted while one runs. A restart ends a running merge as failed (see
// Start); what it merged so far stays merged.

// What a merge does with images that have a dataset annotation already.
const (
	MergeSkip      = "skip"
	MergeOverwrite = "overwrite"
)

// mergeProgressEvery: the progress of a merge is saved at least every so
// many images, and every mergeProgressInterval.
const (
	mergeProgressEvery    = 50
	mergeProgressInterval = time.Second
)

// MergeInput selects what a merge copies.
type MergeInput struct {
	DatasetIDs []int64  // datasets of the review; none: all
	Statuses   []string // the image statuses to merge, at least one
	OnExisting string   // MergeSkip or MergeOverwrite
}

var (
	errMergeNotFound = &Error{Kind: ErrNotFound, Message: "合并记录不存在"}
	errMergeBusy     = &Error{Kind: ErrConflict, Message: "这个审图任务正在合并到数据集，请等这次合并完成后再开始新的合并"}
)

// StartMerge starts merging the review annotations of the inferred images
// selected by in into their datasets, in the background. It returns the
// merge, running; GetMerge reports its progress.
func (s *Service) StartMerge(ctx context.Context, reviewID int64, in MergeInput) (Merge, error) {
	statuses, err := normalizeStatuses(in.Statuses, "statuses")
	if err != nil {
		return Merge{}, err
	}
	if len(statuses) == 0 {
		return Merge{}, &Error{Kind: ErrInvalid, Message: "请至少选择一种要合并的审核状态"}
	}
	if in.OnExisting != MergeSkip && in.OnExisting != MergeOverwrite {
		return Merge{}, &Error{Kind: ErrInvalid, Message: "on_existing 必须是 skip（跳过已有标注的图片）或 overwrite（覆盖已有标注）"}
	}
	var m Merge
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// The review cannot be deleted before the merge row commits; a
		// delete then sees it running.
		if _, err := lockReview(ctx, tx, reviewID, "KEY SHARE"); err != nil {
			return err
		}
		all, err := reviewDatasetIDs(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		datasetIDs, err := selectDatasets(in.DatasetIDs, all)
		if err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM review_merges WHERE review_id = $1 AND status = 'running')",
			reviewID).Scan(&running); err != nil {
			return err
		}
		if running {
			return errMergeBusy
		}
		var inferred, total int
		var args sqlArgs
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE ri.inferred),
				count(*) FILTER (WHERE `+selectionFilter(reviewID, datasetIDs, statuses, &args)+`)
			FROM review_images ri WHERE ri.review_id = $1`, args...).Scan(&inferred, &total); err != nil {
			return err
		}
		switch {
		case inferred == 0:
			return &Error{Kind: ErrConflict, Message: "审图任务还没有识别完成的图片，无法合并"}
		case total == 0:
			return &Error{Kind: ErrConflict, Message: "没有符合条件的图片可以合并（所选数据集中没有这些审核状态的已识别图片）"}
		}
		m, err = scanMerge(tx.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, on_existing, status, total)
			VALUES ($1, $2, $3, $4, 'running', $5) RETURNING `+mergeColumns, reviewID, datasetIDs, statuses, in.OnExisting, total))
		return err
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" { // started by another request meanwhile
		return Merge{}, errMergeBusy
	}
	if err != nil {
		return Merge{}, err
	}
	s.log.Info("review merge started", "review_id", reviewID, "merge_id", m.ID, "datasets", m.DatasetIDs,
		"statuses", m.Statuses, "on_existing", m.OnExisting, "images", m.Total)
	s.goJob(func(ctx context.Context) { s.runMerge(ctx, m) })
	return m, nil
}

// ListMerges returns the newest merges of a review (at most 50), newest
// first.
func (s *Service) ListMerges(ctx context.Context, reviewID int64) ([]Merge, error) {
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return nil, err
	}
	merges, err := collectMerges(s.db.Query(ctx, "SELECT "+mergeColumns+
		" FROM review_merges WHERE review_id = $1 ORDER BY id DESC LIMIT 50", reviewID))
	if merges == nil {
		merges = []Merge{}
	}
	return merges, err
}

// GetMerge returns one merge of a review.
func (s *Service) GetMerge(ctx context.Context, reviewID, mergeID int64) (Merge, error) {
	m, err := scanMerge(s.db.QueryRow(ctx, "SELECT "+mergeColumns+" FROM review_merges WHERE id = $1 AND review_id = $2",
		mergeID, reviewID))
	if errors.Is(err, pgx.ErrNoRows) {
		if rerr := s.reviewExists(ctx, s.db, reviewID); rerr != nil {
			return Merge{}, rerr
		}
		return Merge{}, errMergeNotFound
	}
	return m, err
}

// mergeJob is the state of a running merge.
type mergeJob struct {
	Merge
	saved time.Time // when the progress was last saved
}

func (j *mergeJob) problem(path, kind, msg string) {
	if len(j.Problems) < MaxProblems {
		j.Problems = append(j.Problems, Problem{Path: path, Kind: kind, Message: msg})
	}
}

// runMerge merges the images that matched when the merge started, in
// review order, and records the outcome. Only a problem of the job itself
// (the database, the service stopping) makes it fail; images that cannot
// be merged are counted and listed.
func (s *Service) runMerge(ctx context.Context, m Merge) {
	start := time.Now()
	j := &mergeJob{Merge: m, saved: start}
	j.Problems = []Problem{}
	err := s.mergeImages(ctx, j)
	status, msg := MergeSucceeded, ""
	if err != nil {
		status, msg = MergeFailed, "合并失败：服务器内部错误（详见服务器日志），已合并的图片保留，可以再次合并"
		if ctx.Err() != nil {
			msg = "服务停止，合并被中断（已合并的图片保留，可以再次合并）"
		} else {
			s.log.Error("review merge failed", "review_id", m.ReviewID, "merge_id", m.ID, "err", err)
		}
	}
	// The outcome is recorded also when the service is stopping.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.db.Exec(fctx, `UPDATE review_merges SET status = $2, error = $3, done = $4, summary = $5, problems = $6,
			finished_at = now()
		WHERE id = $1 AND status = 'running'`, m.ID, status, msg, j.Done, j.Summary, j.Problems); err != nil {
		s.log.Error("record the end of a review merge", "review_id", m.ReviewID, "merge_id", m.ID, "err", err)
		return
	}
	sum := j.Summary
	s.log.Info("review merge finished", "review_id", m.ReviewID, "merge_id", m.ID, "status", status, "images", j.Done,
		"merged", sum.Merged, "skipped_existing", sum.SkippedExisting, "skipped_empty", sum.SkippedEmpty,
		"conflicts", sum.Conflicts, "failed", sum.Failed, "duration_ms", time.Since(start).Milliseconds())
}

// mergeImages works through the images of a merge.
func (s *Service) mergeImages(ctx context.Context, j *mergeJob) error {
	var args sqlArgs
	rows, err := s.db.Query(ctx, "SELECT ri.id, ri.package_path FROM review_images ri WHERE "+
		selectionFilter(j.ReviewID, j.DatasetIDs, j.Statuses, &args)+" ORDER BY ri.position", args...)
	if err != nil {
		return err
	}
	type target struct { // exported fields: RowToStructByPos skips the others
		ID   int64
		Path string
	}
	targets, err := pgx.CollectRows(rows, pgx.RowToStructByPos[target])
	if err != nil {
		return err
	}
	if len(targets) != j.Total { // images changed between the request and now
		j.Total = len(targets)
		if _, err := s.db.Exec(ctx, "UPDATE review_merges SET total = $2 WHERE id = $1", j.ID, j.Total); err != nil {
			return err
		}
	}
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.mergeImage(ctx, j, t.ID, t.Path); err != nil {
			return err
		}
		j.Done++
		if j.Done%mergeProgressEvery == 0 || time.Since(j.saved) >= mergeProgressInterval {
			if _, err := s.db.Exec(ctx, "UPDATE review_merges SET done = $2, summary = $3, problems = $4 WHERE id = $1",
				j.ID, j.Done, j.Summary, j.Problems); err != nil {
				return err
			}
			j.saved = time.Now()
		}
	}
	return nil
}

// mergeSource is a review image as a merge reads it.
type mergeSource struct {
	datasetID, imageID int64
	path               string // in the package, for problems
	revision           int
	annotation         annotationFile
}

// mergeImage merges one review image into its dataset. Only errors that
// stop the job are returned.
func (s *Service) mergeImage(ctx context.Context, j *mergeJob, id int64, path string) error {
	src, err := s.readMergeSource(ctx, j.ReviewID, id)
	switch {
	case errors.Is(err, errImageNotFound): // removed with its dataset image meanwhile
		j.Summary.Failed++
		j.problem(path, ProblemFailed, msgUploadUnknown)
		return nil
	case err != nil:
		return err
	case src.annotation.err != "":
		j.Summary.Failed++
		j.problem(src.path, ProblemFailed, "审图标注无法读取："+src.annotation.err)
		return nil
	case src.annotation.data == nil: // inference failed, never edited
		j.Summary.SkippedEmpty++
		return nil
	}
	failed := func(err error) error {
		var de *dataset.Error
		if errors.As(err, &de) {
			j.Summary.Failed++
			j.problem(src.path, ProblemFailed, de.Message)
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		// A problem with one image's files (a folder without write
		// permission, ...): logged, and the merge goes on.
		s.log.Error("merge review image into its dataset", "review_id", j.ReviewID, "merge_id", j.ID,
			"review_image_id", id, "err", err)
		j.Summary.Failed++
		j.problem(src.path, ProblemFailed, "写入数据集标注时出错（详见服务器日志）")
		return nil
	}
	current, err := s.datasets.GetImage(ctx, src.datasetID, src.imageID)
	if err != nil {
		return failed(err)
	}
	switch {
	case current.AnnotationBlocked != "":
		j.Summary.Failed++
		j.problem(src.path, ProblemFailed, current.AnnotationBlocked)
		return nil
	case current.AnnotationState != "none" && j.OnExisting == MergeSkip:
		j.Summary.SkippedExisting++
		return nil
	}
	objects := slices.Clone(src.annotation.file.Objects)
	if objects == nil {
		objects = []dataset.AnnotationObject{} // an empty annotation: a negative sample
	}
	if s.beforeMergeSave != nil {
		s.beforeMergeSave(j.ID, id)
	}
	_, err = s.datasets.SaveAnnotation(ctx, src.datasetID, src.imageID, dataset.SaveAnnotationInput{
		Objects: objects, BaseVersion: current.AnnotationVersion, Regenerate: j.OnExisting == MergeOverwrite,
	})
	if errors.Is(err, dataset.ErrAnnotationChanged) {
		j.Summary.Conflicts++
		j.problem(src.path, ProblemConflict, "数据集中这张图片的标注在合并时被其他人修改了，没有合并")
		return nil
	}
	if err != nil {
		return failed(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE review_images SET merged_at = now(), merged_revision = $2 WHERE id = $1",
		id, src.revision); err != nil {
		return err
	}
	j.Summary.Merged++
	return nil
}

// readMergeSource reads a review image and its VOC file under the image's
// lock, so that the two agree.
func (s *Service) readMergeSource(ctx context.Context, reviewID, id int64) (mergeSource, error) {
	unlock, err := s.lockImage(ctx, id)
	if err != nil {
		return mergeSource{}, err
	}
	defer unlock()
	var src mergeSource
	var annotationPath string
	err = s.db.QueryRow(ctx, `SELECT dataset_id, image_id, package_path, annotation_path, revision
		FROM review_images WHERE id = $1 AND review_id = $2`, id, reviewID).
		Scan(&src.datasetID, &src.imageID, &src.path, &annotationPath, &src.revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return src, errImageNotFound
	}
	if err != nil {
		return src, err
	}
	// The review cannot be deleted while its merge runs, so its folder
	// stays.
	src.annotation, err = s.readAnnotation(reviewID, annotationPath)
	return src, err
}
