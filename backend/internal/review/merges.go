package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Merges (合并到数据集): a background job adds the review annotations of
// the chosen images to a new review version (审图版本) of each of their
// datasets (dataset.Service.CreateReviewVersion, AddVersionImage). The
// datasets' own files, their base version, are not touched. The versions
// are numbered (审图1.0, 1.1, ...) and become ready in the transaction that
// records the end of the merge; a merge that fails or is interrupted
// discards them, and marks its images merged only when it succeeds. At
// most one merge runs per review (a unique index), and a review cannot be
// deleted while one runs. A restart ends a running merge as failed and
// discards its versions (see Start).
//
// Merges made before versions existed wrote into the datasets' VOC files;
// their records keep on_existing (MergeSkip or MergeOverwrite).

// What a merge made before versions existed did with images that had a
// dataset annotation already (Merge.OnExisting).
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
	Note       string   // the note of the versions made (required)
}

var (
	errMergeNotFound = &Error{Kind: ErrNotFound, Message: "合并记录不存在"}
	errMergeBusy     = &Error{Kind: ErrConflict, Message: "这个审图任务正在合并到数据集，请等这次合并完成后再开始新的合并"}
)

// Messages of merges that did not complete.
const (
	msgMergeFailed   = "合并失败：服务器内部错误（详见服务器日志），没有生成新的版本，可以重新合并"
	msgMergeStopped  = "服务停止，合并被中断，没有生成新的版本，可以重新合并"
	msgMergeRestart  = "服务重启，合并被中断，没有生成新的版本，可以重新合并"
	msgDatasetGone   = "数据集已被删除"
	msgStatusChanged = "审核状态在合并时被改为其它状态，没有合并"
)

// StartMerge starts merging the review annotations of the inferred images
// selected by in into new review versions of their datasets, in the
// background. It returns the merge, running; GetMerge reports its
// progress.
func (s *Service) StartMerge(ctx context.Context, reviewID int64, in MergeInput) (Merge, error) {
	statuses, err := normalizeStatuses(in.Statuses, "statuses")
	if err != nil {
		return Merge{}, err
	}
	if len(statuses) == 0 {
		return Merge{}, &Error{Kind: ErrInvalid, Message: "请至少选择一种要合并的审核状态"}
	}
	note, err := dataset.NormalizeVersionNote(in.Note)
	if err != nil {
		var de *dataset.Error
		if errors.As(err, &de) {
			return Merge{}, &Error{Kind: ErrInvalid, Message: de.Message}
		}
		return Merge{}, err
	}
	var m Merge
	var source dataset.VersionSource
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
		var model ModelRef
		if err := tx.QueryRow(ctx, "SELECT name, model_ref FROM reviews WHERE id = $1", reviewID).
			Scan(&source.ReviewName, &model); err != nil {
			return err
		}
		source.ReviewID, source.Statuses = reviewID, statuses
		source.Model = dataset.VersionModel{ID: model.ID, ProjectID: model.ProjectID, ProjectName: model.ProjectName,
			Version: model.Version, YOLOVersion: model.YOLOVersion, Task: model.Task, FileName: model.FileName}
		m, err = scanMerge(tx.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, note, status, total)
			VALUES ($1, $2, $3, $4, 'running', $5) RETURNING `+mergeColumns, reviewID, datasetIDs, statuses, note, total))
		return err
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" { // started by another request meanwhile
		return Merge{}, errMergeBusy
	}
	if err != nil {
		return Merge{}, err
	}
	source.MergeID = m.ID
	s.log.Info("review merge started", "review_id", reviewID, "merge_id", m.ID, "datasets", m.DatasetIDs,
		"statuses", m.Statuses, "images", m.Total)
	s.goJob(func(ctx context.Context) { s.runMerge(ctx, m, source) })
	return m, nil
}

// ListMerges returns the newest merges of a review (at most
// maxListedRecords), newest first, and how many the review has.
func (s *Service) ListMerges(ctx context.Context, reviewID int64) ([]Merge, int, error) {
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return nil, 0, err
	}
	merges, err := collectMerges(s.db.Query(ctx, "SELECT "+mergeColumns+
		" FROM review_merges WHERE review_id = $1 ORDER BY id DESC LIMIT $2", reviewID, maxListedRecords))
	if err != nil {
		return nil, 0, err
	}
	if merges == nil {
		merges = []Merge{}
	}
	if err := s.attachVersions(ctx, merges); err != nil {
		return nil, 0, err
	}
	total := len(merges)
	if total == maxListedRecords {
		if err := s.db.QueryRow(ctx, "SELECT count(*) FROM review_merges WHERE review_id = $1", reviewID).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return merges, total, nil
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
	if err != nil {
		return Merge{}, err
	}
	merges := []Merge{m}
	err = s.attachVersions(ctx, merges)
	return merges[0], err
}

// attachVersions fills in the versions the merges made (or are making).
func (s *Service) attachVersions(ctx context.Context, merges []Merge) error {
	ids := make([]int64, len(merges))
	for i, m := range merges {
		ids[i] = m.ID
	}
	versions, err := s.datasets.MergeVersions(ctx, ids)
	if err != nil {
		return fmt.Errorf("load the versions of merges: %w", err)
	}
	for i := range merges {
		if v := versions[merges[i].ID]; v != nil {
			merges[i].Versions = v
		}
	}
	return nil
}

// mergeJob is the state of a running merge.
type mergeJob struct {
	Merge
	source   dataset.VersionSource
	versions map[int64]int64 // the version being built, by dataset
	merged   []mergedImage   // the review images added to a version
	saved    time.Time       // when the progress was last saved
}

// mergedImage is a review image a merge added to a version, with the
// revision it took.
type mergedImage struct {
	id       int64
	revision int
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
func (s *Service) runMerge(ctx context.Context, m Merge, source dataset.VersionSource) {
	start := time.Now()
	j := &mergeJob{Merge: m, source: source, versions: map[int64]int64{}, saved: start}
	j.Problems = []Problem{}
	err := s.mergeImages(ctx, j)
	status, msg := MergeSucceeded, ""
	if err != nil {
		status, msg = MergeFailed, msgMergeFailed
		if ctx.Err() != nil {
			msg = msgMergeStopped
		} else {
			s.log.Error("review merge failed", "review_id", m.ReviewID, "merge_id", m.ID, "err", err)
		}
	}
	status, versions, err := s.recordMergeEnd(ctx, j, status, msg)
	if err != nil {
		return // the service is stopping; Start fails the merge on the next start
	}
	sum := j.Summary
	s.log.Info("review merge finished", "review_id", m.ReviewID, "merge_id", m.ID, "status", status, "images", j.Done,
		"merged", sum.Merged, "skipped_empty", sum.SkippedEmpty, "skipped_changed", sum.SkippedChanged,
		"failed", sum.Failed, "versions", versions, "duration_ms", time.Since(start).Milliseconds())
}

// mergeEndRetry are the first and the longest wait between attempts to
// record the end of a merge; tests shorten them.
var mergeEndRetry = [2]time.Duration{time.Second, 30 * time.Second}

// mergeEndFault, if set, is called before each attempt to record the end
// of a merge (from 1); an error fails that attempt. Tests simulate a
// database outage with it.
var mergeEndFault func(attempt int) error

// recordMergeEnd records the outcome of a merge, also when the service is
// stopping, in one transaction with its versions: a merge that succeeded
// numbers them and marks its images merged, one that failed discards them.
// The merge must not stay running, which would refuse new merges and
// deleting the review until a restart. A transient failure (the database
// restarting, a deadlock, ...) is retried, waiting longer each time, until
// it works or the service stops (Start then fails the merge on the next
// start). Any other failure to complete the versions fails the merge
// instead (retried the same way). It returns the final status and the
// versions made.
func (s *Service) recordMergeEnd(ctx context.Context, j *mergeJob, status, msg string) (string, []dataset.VersionRef, error) {
	ids := make([]int64, len(j.merged))
	revisions := make([]int32, len(j.merged))
	for i, img := range j.merged {
		ids[i], revisions[i] = img.id, int32(img.revision)
	}
	wait := mergeEndRetry[0]
	for attempt := 1; ; attempt++ {
		var err error
		if mergeEndFault != nil {
			err = mergeEndFault(attempt)
		}
		var versions []dataset.VersionRef
		if err == nil {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			err = pgx.BeginFunc(fctx, s.db, func(tx pgx.Tx) error {
				// The merge row first (nothing else locks it), then the
				// datasets and their versions (FinishReviewVersions), then
				// the review images.
				tag, err := tx.Exec(fctx, `UPDATE review_merges SET status = $2, error = $3, done = $4, summary = $5,
						problems = $6, finished_at = now()
					WHERE id = $1 AND status = 'running'`, j.ID, status, msg, j.Done, j.Summary, j.Problems)
				if err != nil {
					return err
				}
				if status != MergeSucceeded || tag.RowsAffected() == 0 { // ended elsewhere: keep nothing
					return s.datasets.DiscardReviewVersions(fctx, tx, j.ID)
				}
				if versions, err = s.datasets.FinishReviewVersions(fctx, tx, j.ID); err != nil {
					return err
				}
				_, err = tx.Exec(fctx, `UPDATE review_images ri SET merged_at = now(), merged_revision = m.revision
					FROM unnest($1::bigint[], $2::int[]) AS m (id, revision) WHERE ri.id = m.id`, ids, revisions)
				return err
			})
			cancel()
		}
		if err == nil {
			return status, versions, nil
		}
		s.log.Error("record the end of a review merge", "review_id", j.ReviewID, "merge_id", j.ID, "attempt", attempt,
			"status", status, "err", err)
		if status == MergeSucceeded && !transientError(err) {
			status, msg, versions = MergeFailed, msgMergeFailed, nil
			continue // fail it now; a retry would fail the same way
		}
		select {
		case <-ctx.Done():
			return status, nil, err
		case <-time.After(wait):
		}
		wait = min(2*wait, mergeEndRetry[1])
	}
}

// transientError reports whether a database operation that failed with
// err may work when tried again: errors that are not from the server
// (connection problems, timeouts), and server errors about the connection,
// the server's resources or state, serialization failures and deadlocks.
// Other server errors (a constraint violated, ...) would happen again.
func transientError(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return true
	}
	if len(pe.Code) < 2 {
		return false
	}
	switch pe.Code[:2] {
	case "08", "53", "57", "58": // connection, resources, operator intervention, system
		return true
	case "40": // transaction rollback: serialization failure, deadlock
		return true
	}
	return false
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
	datasetID     int64
	path          string // in the package, for problems
	relPath       string // in the dataset
	status        string
	revision      int
	width, height *int
	annotation    annotationFile
}

// mergeImage adds one review image to the version of its dataset. Only
// errors that stop the job are returned.
func (s *Service) mergeImage(ctx context.Context, j *mergeJob, id int64, path string) error {
	src, err := s.readMergeSource(ctx, j.ReviewID, id)
	switch {
	case errors.Is(err, errImageNotFound): // removed with its dataset image meanwhile
		j.Summary.Failed++
		j.problem(path, ProblemFailed, msgUploadUnknown)
		return nil
	case err != nil:
		return err
	case !slices.Contains(j.Statuses, src.status): // changed since the merge started
		j.Summary.SkippedChanged++
		j.problem(src.path, ProblemSkipped, msgStatusChanged)
		return nil
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
		return err
	}
	versionID, ok := j.versions[src.datasetID]
	if !ok {
		versionID, err = s.datasets.CreateReviewVersion(ctx, dataset.ReviewVersionDraft{DatasetID: src.datasetID,
			ReviewID: j.ReviewID, MergeID: j.ID, Note: j.Note, Source: j.source})
		if err != nil {
			return failed(err)
		}
		j.versions[src.datasetID] = versionID
	}
	if s.beforeMergeSave != nil {
		s.beforeMergeSave(j.ID, id)
	}
	// The size the review file records (the node's decoded size), else the
	// dataset's at creation.
	width, height := src.width, src.height
	if f := src.annotation.file; f.Width > 0 && f.Height > 0 {
		width, height = &f.Width, &f.Height
	}
	if err := s.datasets.AddVersionImage(ctx, versionID, dataset.VersionImage{Path: src.relPath,
		ReviewStatus: src.status, Width: width, Height: height, Objects: src.annotation.file.Objects}); err != nil {
		return failed(err)
	}
	j.merged = append(j.merged, mergedImage{id: id, revision: src.revision})
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
	err = s.db.QueryRow(ctx, `SELECT dataset_id, package_path, rel_path, annotation_path, status, revision, width, height
		FROM review_images WHERE id = $1 AND review_id = $2`, id, reviewID).
		Scan(&src.datasetID, &src.path, &src.relPath, &annotationPath, &src.status, &src.revision, &src.width, &src.height)
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
