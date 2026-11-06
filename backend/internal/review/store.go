package review

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// ---- reviews ----

// reviewSelect reads reviews with their node, their project and their
// queue position; append WHERE and ORDER BY. The counts, datasets, uploads
// and merges are loaded by fillReviews. A model or project in the recycle
// bin counts as deleted. A queued review whose model is in the bin waits
// outside the queue (claimOne skips it): it has no position and does not
// count in the others'.
const reviewSelect = `SELECT r.id, r.name, r.description,
		EXISTS (SELECT 1 FROM models m WHERE m.id = r.model_id AND m.deleted_at IS NULL), r.model_ref, r.options, r.status,
		r.stage, r.attempts, r.node_id, n.name, r.error, r.error_detail, r.result, r.project_id, p.name,
		CASE WHEN r.status = 'queued' AND NOT EXISTS (SELECT 1 FROM models m WHERE m.id = r.model_id AND m.deleted_at IS NOT NULL)
			THEN (SELECT count(*) FROM reviews q WHERE q.status = 'queued' AND q.id <= r.id
				AND NOT EXISTS (SELECT 1 FROM models m WHERE m.id = q.model_id AND m.deleted_at IS NOT NULL)) END,
		r.created_at, r.updated_at, r.started_at, r.finished_at
	FROM reviews r
	LEFT JOIN compute_nodes n ON n.id = r.node_id
	LEFT JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL`

func scanReview(row pgx.Row) (Review, error) {
	var (
		r           Review
		nodeID      *int64
		nodeName    *string
		projectID   *int64
		projectName *string
		position    *int64
		result      []byte
	)
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.ModelExists, &r.Model, &r.Options, &r.Status,
		&r.Stage, &r.Attempts, &nodeID, &nodeName, &r.Error, &r.ErrorDetail, &result, &projectID, &projectName,
		&position, &r.CreatedAt, &r.UpdatedAt, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errReviewNotFound
	}
	if err != nil {
		return r, err
	}
	if nodeID != nil {
		r.Node = &NodeRef{ID: *nodeID}
		if nodeName != nil {
			r.Node.Name = *nodeName
		}
	}
	if projectID != nil && projectName != nil {
		r.Project = &ProjectRef{ID: *projectID, Name: *projectName}
	}
	if position != nil {
		p := int(*position)
		r.QueuePosition = &p
	}
	if len(result) > 0 && string(result) != "null" {
		r.Result = result
	}
	if r.Model.ClassNames == nil {
		r.Model.ClassNames = []string{}
	}
	r.Datasets = []Dataset{}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	r.StartedAt, r.FinishedAt = utcPtr(r.StartedAt), utcPtr(r.FinishedAt)
	return r, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// queryReviews reads the reviews a WHERE clause selects (with ORDER BY)
// and fills them in.
func (s *Service) queryReviews(ctx context.Context, q querier, where string, args ...any) ([]Review, error) {
	rows, err := q.Query(ctx, reviewSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	reviews, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Review, error) { return scanReview(row) })
	if err != nil {
		return nil, err
	}
	if err := s.fillReviews(ctx, q, reviews); err != nil {
		return nil, err
	}
	return reviews, nil
}

// queryReview reads one review, filled in.
func (s *Service) queryReview(ctx context.Context, q querier, id int64) (Review, error) {
	reviews, err := s.queryReviews(ctx, q, "WHERE r.id = $1", id)
	if err != nil {
		return Review{}, err
	}
	if len(reviews) == 0 {
		return Review{}, errReviewNotFound
	}
	return reviews[0], nil
}

// fillReviews loads the counts, datasets, last upload and merges of
// reviews, and sets workers_online.
func (s *Service) fillReviews(ctx context.Context, q querier, reviews []Review) error {
	if len(reviews) == 0 {
		return nil
	}
	ids := make([]int64, len(reviews))
	index := make(map[int64]*Review, len(reviews))
	workers := s.workersOnline()
	for i := range reviews {
		ids[i] = reviews[i].ID
		index[reviews[i].ID] = &reviews[i]
		reviews[i].WorkersOnline = workers
	}

	// Datasets in selection order, then the counts per dataset.
	rows, err := q.Query(ctx, `SELECT rd.review_id, rd.dataset_id, coalesce(d.name, rd.dataset_name), rd.folder,
			d.review_version_seq
		FROM review_datasets rd LEFT JOIN datasets d ON d.id = rd.dataset_id
		WHERE rd.review_id = ANY($1) ORDER BY rd.review_id, rd.position`, ids)
	if err != nil {
		return err
	}
	var reviewID int64
	var d Dataset
	var seq *int
	if _, err := pgx.ForEachRow(rows, []any{&reviewID, &d.ID, &d.Name, &d.Folder, &seq}, func() error {
		r := index[reviewID]
		next := d
		if seq != nil {
			next.NextVersion = dataset.NextReviewVersionName(*seq)
		}
		r.Datasets = append(r.Datasets, next)
		return nil
	}); err != nil {
		return err
	}
	counts, err := queryCounts(ctx, q, ids)
	if err != nil {
		return err
	}
	for key, c := range counts {
		r := index[key.review]
		r.Counts.add(c)
		for i := range r.Datasets {
			if r.Datasets[i].ID == key.dataset {
				r.Datasets[i].Counts = c
			}
		}
	}

	// The newest upload, the running merge and the newest finished one.
	uploads, err := collectUploads(q.Query(ctx, `SELECT DISTINCT ON (review_id) `+uploadColumns+`
		FROM review_uploads WHERE review_id = ANY($1) ORDER BY review_id, id DESC`, ids))
	if err != nil {
		return err
	}
	for _, u := range uploads {
		index[u.ReviewID].LastUpload = &u
	}
	merges, err := collectMerges(q.Query(ctx, `(SELECT `+mergeColumns+` FROM review_merges
			WHERE review_id = ANY($1) AND status = 'running')
		UNION ALL
		(SELECT DISTINCT ON (review_id) `+mergeColumns+` FROM review_merges
			WHERE review_id = ANY($1) AND status <> 'running' ORDER BY review_id, id DESC)`, ids))
	if err != nil {
		return err
	}
	if err := s.attachVersions(ctx, merges); err != nil {
		return err
	}
	for _, m := range merges {
		if m.Status == MergeRunning {
			index[m.ReviewID].ActiveMerge = &m
		} else {
			index[m.ReviewID].LastMerge = &m
		}
	}
	return nil
}

// workersOnline returns the nodes that recently claimed review inference.
func (s *Service) workersOnline() int {
	if s.models == nil {
		return 0
	}
	return s.models.WorkersOnline(TaskKind)
}

type countKey struct{ review, dataset int64 }

func (c *Counts) add(o Counts) {
	c.Total += o.Total
	c.Inferred += o.Inferred
	c.InferenceFailed += o.InferenceFailed
	c.Pending += o.Pending
	c.Approved += o.Approved
	c.NeedsSample += o.NeedsSample
	c.Edited += o.Edited
	c.Merged += o.Merged
}

// queryCounts counts the images of reviews per dataset.
func queryCounts(ctx context.Context, q querier, reviewIDs []int64) (map[countKey]Counts, error) {
	rows, err := q.Query(ctx, `SELECT review_id, dataset_id, count(*),
			count(*) FILTER (WHERE inferred),
			count(*) FILTER (WHERE inferred AND inference_error <> ''),
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'approved'),
			count(*) FILTER (WHERE status = 'needs_sample'),
			count(*) FILTER (WHERE edited),
			count(*) FILTER (WHERE merged_at IS NOT NULL)
		FROM review_images WHERE review_id = ANY($1) GROUP BY review_id, dataset_id`, reviewIDs)
	if err != nil {
		return nil, err
	}
	out := map[countKey]Counts{}
	var k countKey
	var c Counts
	_, err = pgx.ForEachRow(rows, []any{&k.review, &k.dataset, &c.Total, &c.Inferred, &c.InferenceFailed,
		&c.Pending, &c.Approved, &c.NeedsSample, &c.Edited, &c.Merged}, func() error {
		out[k] = c
		return nil
	})
	return out, err
}

// lockReview locks the row of a review: FOR KEY SHARE keeps it from being
// deleted (changes of its images take it), FOR UPDATE also from being
// changed. It returns the review's status.
func lockReview(ctx context.Context, q querier, id int64, mode string) (string, error) {
	var status string
	err := q.QueryRow(ctx, "SELECT status FROM reviews WHERE id = $1 FOR "+mode, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errReviewNotFound
	}
	return status, err
}

// ---- uploads and merges ----

const uploadColumns = `id, review_id, file_name, overwrite, summary, problems, created_at`

func scanUpload(row pgx.Row) (Upload, error) {
	var u Upload
	err := row.Scan(&u.ID, &u.ReviewID, &u.FileName, &u.Overwrite, &u.Summary, &u.Problems, &u.CreatedAt)
	if u.Problems == nil {
		u.Problems = []Problem{}
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, err
}

func collectUploads(rows pgx.Rows, err error) ([]Upload, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Upload, error) { return scanUpload(row) })
}

const mergeColumns = `id, review_id, dataset_ids, statuses, note, on_existing, status, total, done, summary, problems,
	error, created_at, finished_at`

// scanMerge reads a merge; its Versions are filled in by attachVersions.
func scanMerge(row pgx.Row) (Merge, error) {
	var m Merge
	err := row.Scan(&m.ID, &m.ReviewID, &m.DatasetIDs, &m.Statuses, &m.Note, &m.OnExisting, &m.Status, &m.Total,
		&m.Done, &m.Summary, &m.Problems, &m.Error, &m.CreatedAt, &m.FinishedAt)
	m.Versions = []dataset.VersionRef{}
	if m.DatasetIDs == nil {
		m.DatasetIDs = []int64{}
	}
	if m.Statuses == nil {
		m.Statuses = []string{}
	}
	if m.Problems == nil {
		m.Problems = []Problem{}
	}
	m.CreatedAt, m.FinishedAt = m.CreatedAt.UTC(), utcPtr(m.FinishedAt)
	return m, err
}

func collectMerges(rows pgx.Rows, err error) ([]Merge, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Merge, error) { return scanMerge(row) })
}

// ---- images ----

// imageColumns are the columns scanImage reads, from review_images ri.
const imageColumns = `ri.id, ri.review_id, ri.dataset_id, ri.image_id, ri.position, ri.rel_path, ri.file_name,
	ri.package_path, ri.annotation_path, ri.width, ri.height, ri.image_version, ri.inferred, ri.inference_error,
	ri.object_count, ri.labels, jsonb_array_length(ri.predictions), ri.edited, ri.status, ri.revision,
	ri.reviewed_at, ri.merged_at, ri.annotation_version`

func scanImage(row pgx.Row) (Image, error) {
	var img Image
	var labels map[string]int
	err := row.Scan(&img.ID, &img.ReviewID, &img.DatasetID, &img.ImageID, &img.Position, &img.Path, &img.FileName,
		&img.PackagePath, &img.AnnotationPath, &img.Width, &img.Height, &img.ImageVersion, &img.Inferred,
		&img.InferenceError, &img.ObjectCount, &labels, &img.PredictionCount, &img.Edited, &img.Status, &img.Revision,
		&img.ReviewedAt, &img.MergedAt, &img.AnnotationVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return img, errImageNotFound
	}
	if err != nil {
		return img, err
	}
	img.Labels = sortedLabels(labels)
	img.ReviewedAt, img.MergedAt = utcPtr(img.ReviewedAt), utcPtr(img.MergedAt)
	return img, nil
}

func collectImages(rows pgx.Rows, err error) ([]Image, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Image, error) { return scanImage(row) })
}

// sortedLabels orders label counts by count desc, then name.
func sortedLabels(counts map[string]int) []dataset.LabelCount {
	out := make([]dataset.LabelCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, dataset.LabelCount{Name: name, Count: n})
	}
	slices.SortFunc(out, func(a, b dataset.LabelCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// imageRow is a review image with what changing it needs.
type imageRow struct {
	Image
	predictions []Prediction
	// The package whose uploaded results last changed the image and the
	// revision they made (nil: none did).
	uploadPackageID *string
	uploadRevision  *int
}

// continues reports whether an upload of results from the package
// packageID continues from the image's state: an earlier upload of the
// same package made it, and nothing changed it since.
func (r *imageRow) continues(packageID string) bool {
	return packageID != "" && r.uploadPackageID != nil && *r.uploadPackageID == packageID &&
		r.uploadRevision != nil && *r.uploadRevision == r.Revision
}

// queryImageRow reads a review image with its predictions; forUpdate
// locks the row.
func queryImageRow(ctx context.Context, q querier, reviewID, imageID int64, forUpdate bool) (imageRow, error) {
	sql := "SELECT " + imageColumns + ", ri.predictions, ri.upload_package_id, ri.upload_revision" +
		" FROM review_images ri WHERE ri.id = $1 AND ri.review_id = $2"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var r imageRow
	var labels map[string]int
	img := &r.Image
	err := q.QueryRow(ctx, sql, imageID, reviewID).Scan(&img.ID, &img.ReviewID, &img.DatasetID, &img.ImageID,
		&img.Position, &img.Path, &img.FileName, &img.PackagePath, &img.AnnotationPath, &img.Width, &img.Height,
		&img.ImageVersion, &img.Inferred, &img.InferenceError, &img.ObjectCount, &labels, &img.PredictionCount,
		&img.Edited, &img.Status, &img.Revision, &img.ReviewedAt, &img.MergedAt, &img.AnnotationVersion,
		&r.predictions, &r.uploadPackageID, &r.uploadRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errImageNotFound
	}
	if err != nil {
		return r, fmt.Errorf("read review image %d: %w", imageID, err)
	}
	img.Labels = sortedLabels(labels)
	img.ReviewedAt, img.MergedAt = utcPtr(img.ReviewedAt), utcPtr(img.MergedAt)
	if r.predictions == nil {
		r.predictions = []Prediction{}
	}
	return r, nil
}

// sqlArgs collects the positional arguments of a query being built.
type sqlArgs []any

// add appends v and returns its placeholder.
func (a *sqlArgs) add(v any) string {
	*a = append(*a, v)
	return fmt.Sprintf("$%d", len(*a))
}
