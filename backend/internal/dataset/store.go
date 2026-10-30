package dataset

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// upsertBatchSize bounds the rows written per transaction while indexing.
const upsertBatchSize = 500

// ---- datasets ----

const datasetColumns = `id, name, description, dir_name, origin, image_count, annotated_count, empty_count,
	invalid_count, object_count, label_count, total_bytes, scanned_at, created_at, updated_at`

func scanDataset(row pgx.Row) (Dataset, error) {
	var d Dataset
	err := row.Scan(&d.ID, &d.Name, &d.Description, &d.DirName, &d.Origin, &d.ImageCount, &d.AnnotatedCount,
		&d.EmptyCount, &d.InvalidCount, &d.ObjectCount, &d.LabelCount, &d.TotalBytes, &d.ScannedAt, &d.CreatedAt,
		&d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errDatasetNotFound
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	d.ScannedAt = utcPtr(d.ScannedAt)
	d.Covers = []ImageRef{}
	return d, err
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func queryDataset(ctx context.Context, q querier, id int64, forUpdate bool) (Dataset, error) {
	sql := "SELECT " + datasetColumns + " FROM datasets WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return scanDataset(q.QueryRow(ctx, sql, id))
}

func queryDatasets(ctx context.Context, q querier, search string) ([]Dataset, error) {
	rows, err := q.Query(ctx, `SELECT `+datasetColumns+` FROM datasets
		WHERE $1 = '' OR strpos(lower(name), lower($1)) > 0 OR strpos(lower(description), lower($1)) > 0
		ORDER BY updated_at DESC, id DESC`, search)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Dataset, error) { return scanDataset(row) })
}

// Names of the unique indexes on datasets, used to explain violations.
const (
	datasetsNameKey    = "datasets_name_key"
	datasetsDirNameKey = "datasets_dir_name_key"
)

var errNameTaken = &Error{Kind: ErrConflict, Message: "已存在同名数据集"}

// checkNameFree fails with errNameTaken if another dataset (not exceptID)
// already uses name, ignoring case.
func checkNameFree(ctx context.Context, q querier, name string, exceptID int64) error {
	var exists bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM datasets WHERE lower(name) = lower($1) AND id <> $2)",
		name, exceptID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return errNameTaken
	}
	return nil
}

// datasetUsingDir returns the name of the dataset registered for dirName
// (ignoring case), or "".
func datasetUsingDir(ctx context.Context, q querier, dirName string) (string, error) {
	var name string
	err := q.QueryRow(ctx, "SELECT name FROM datasets WHERE lower(dir_name) = lower($1)", dirName).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func insertDataset(ctx context.Context, q querier, name, dirName, description, origin string) (Dataset, error) {
	d, err := scanDataset(q.QueryRow(ctx, `INSERT INTO datasets (name, dir_name, description, origin)
		VALUES ($1, $2, $3, $4) RETURNING `+datasetColumns, name, dirName, description, origin))
	return d, uniqueViolation(err, dirName)
}

// uniqueViolation turns a unique index violation on datasets into the
// matching conflict error (the checks before the write can race).
func uniqueViolation(err error, dirName string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return err
	}
	if pgErr.ConstraintName == datasetsDirNameKey {
		return newError(ErrConflict, "文件夹“%s”已被其他数据集使用", dirName)
	}
	return errNameTaken
}

// recomputeAggregates refreshes the counters of a dataset from its images.
// markScanned also records the time of the finished import/scan.
func recomputeAggregates(ctx context.Context, q querier, datasetID int64, markScanned bool) error {
	_, err := q.Exec(ctx, `UPDATE datasets d SET
			image_count = s.image_count,
			annotated_count = s.annotated_count,
			empty_count = s.empty_count,
			invalid_count = s.invalid_count,
			object_count = s.object_count,
			total_bytes = s.total_bytes,
			label_count = l.label_count,
			scanned_at = CASE WHEN $2 THEN now() ELSE d.scanned_at END,
			updated_at = now()
		FROM (SELECT count(*) AS image_count,
				count(*) FILTER (WHERE annotation_state = 'annotated') AS annotated_count,
				count(*) FILTER (WHERE annotation_state = 'empty') AS empty_count,
				count(*) FILTER (WHERE annotation_state = 'invalid') AS invalid_count,
				coalesce(sum(object_count), 0) AS object_count,
				coalesce(sum(size_bytes), 0) AS total_bytes
			FROM dataset_images WHERE dataset_id = $1) s,
			(SELECT count(DISTINCT k) AS label_count
			FROM dataset_images, jsonb_object_keys(label_counts) AS k WHERE dataset_id = $1) l
		WHERE d.id = $1`, datasetID, markScanned)
	return err
}

// queryCovers returns up to 4 images per dataset (first by path).
func queryCovers(ctx context.Context, q querier, ids []int64) (map[int64][]ImageRef, error) {
	rows, err := q.Query(ctx, `SELECT c.dataset_id, c.id, c.size_bytes, c.mod_time
		FROM unnest($1::bigint[]) AS ds (id)
		CROSS JOIN LATERAL (SELECT dataset_id, id, size_bytes, mod_time, rel_path FROM dataset_images
			WHERE dataset_id = ds.id ORDER BY rel_path LIMIT 4) c
		ORDER BY c.dataset_id, c.rel_path`, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]ImageRef)
	var datasetID, id, size int64
	var mod time.Time
	_, err = pgx.ForEachRow(rows, []any{&datasetID, &id, &size, &mod}, func() error {
		out[datasetID] = append(out[datasetID], ImageRef{ID: id, Version: fileVersion(size, mod)})
		return nil
	})
	return out, err
}

// ---- imports ----

const importColumns = `id, dataset_id, source, status, on_conflict, file_name, file_size, file_count,
	received_bytes, received_files, stage, progress_done, progress_total, progress_unit, summary, error,
	created_at, updated_at, started_at, finished_at`

func scanImport(row pgx.Row) (Import, error) {
	var j Import
	err := row.Scan(&j.ID, &j.DatasetID, &j.Source, &j.Status, &j.OnConflict, &j.FileName, &j.FileSize,
		&j.FileCount, &j.ReceivedBytes, &j.ReceivedFiles, &j.Stage, &j.Progress.Done, &j.Progress.Total,
		&j.Progress.Unit, &j.Summary, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, errImportNotFound
	}
	if j.Summary == nil {
		j.Summary = map[string]int64{}
	}
	j.CreatedAt, j.UpdatedAt = j.CreatedAt.UTC(), j.UpdatedAt.UTC()
	j.StartedAt, j.FinishedAt = utcPtr(j.StartedAt), utcPtr(j.FinishedAt)
	return j, err
}

func collectImports(rows pgx.Rows, err error) ([]Import, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Import, error) { return scanImport(row) })
}

func queryImport(ctx context.Context, q querier, datasetID, importID int64, forUpdate bool) (Import, error) {
	sql := "SELECT " + importColumns + " FROM dataset_imports WHERE id = $1 AND dataset_id = $2"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return scanImport(q.QueryRow(ctx, sql, importID, datasetID))
}

// queryActiveImports returns the latest uploading/queued/running job of
// each of the given datasets.
func queryActiveImports(ctx context.Context, q querier, ids []int64) (map[int64]*Import, error) {
	jobs, err := collectImports(q.Query(ctx, `SELECT DISTINCT ON (dataset_id) `+importColumns+`
		FROM dataset_imports
		WHERE dataset_id = ANY($1) AND status IN ('uploading', 'queued', 'running')
		ORDER BY dataset_id, id DESC`, ids))
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Import, len(jobs))
	for i := range jobs {
		out[jobs[i].DatasetID] = &jobs[i]
	}
	return out, nil
}

// hasProcessingJob reports whether the dataset has a queued or running job.
func hasProcessingJob(ctx context.Context, q querier, datasetID int64) (bool, error) {
	var busy bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dataset_imports
		WHERE dataset_id = $1 AND status IN ('queued', 'running'))`, datasetID).Scan(&busy)
	return busy, err
}

type newImport struct {
	DatasetID  int64
	Source     string
	Status     string
	OnConflict string
	FileName   string
	FileSize   int64
	FileCount  int
}

func insertImport(ctx context.Context, q querier, n newImport) (Import, error) {
	return scanImport(q.QueryRow(ctx, `INSERT INTO dataset_imports
		(dataset_id, source, status, on_conflict, file_name, file_size, file_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+importColumns,
		n.DatasetID, n.Source, n.Status, n.OnConflict, n.FileName, n.FileSize, n.FileCount))
}

// ---- images ----

// Image list filters.
const (
	FilterAll         = "all"
	FilterAnnotated   = "annotated"
	FilterEmpty       = "empty"
	FilterInvalid     = "invalid"     // the VOC file cannot be parsed
	FilterUnannotated = "unannotated" // no VOC file
)

// Image list orders.
const (
	SortPath     = "path"
	SortModTime  = "-mod_time"
	SortObjects  = "-objects"
	imageColumns = `id, dataset_id, rel_path, size_bytes, mod_time, width, height,
	annotation_path, annotation_state, object_count, label_counts`
)

// ImageQuery selects a page of a dataset's images.
type ImageQuery struct {
	Offset int
	Limit  int
	Status string // Filter*
	Label  string // exact label name, "" for any
	Search string // case-insensitive substring of the path
	Sort   string // Sort*
}

func scanImage(row pgx.Row) (Image, error) {
	var (
		img     Image
		objects int
		labels  map[string]int
	)
	err := row.Scan(&img.ID, &img.DatasetID, &img.Path, &img.SizeBytes, &img.ModTime, &img.Width, &img.Height,
		&img.AnnotationPath, &img.AnnotationState, &objects, &labels)
	if errors.Is(err, pgx.ErrNoRows) {
		return img, errImageNotFound
	}
	if err != nil {
		return img, err
	}
	img.ModTime = img.ModTime.UTC()
	img.FileName = fileName(img.Path)
	img.Version = fileVersion(img.SizeBytes, img.ModTime)
	if img.AnnotationPath != nil && (img.AnnotationState == annotationAnnotated || img.AnnotationState == annotationEmpty) {
		img.Annotation = &ImageAnnotation{Path: *img.AnnotationPath, ObjectCount: objects, Labels: sortedLabels(labels)}
	}
	return img, nil
}

// sortedLabels orders label counts by count desc, then name.
func sortedLabels(counts map[string]int) []LabelCount {
	out := make([]LabelCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, LabelCount{Name: name, Count: n})
	}
	slices.SortFunc(out, func(a, b LabelCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func queryImages(ctx context.Context, q querier, datasetID int64, f ImageQuery) ([]Image, int, error) {
	where := "dataset_id = $1"
	args := []any{datasetID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	switch f.Status {
	case FilterAnnotated:
		where += " AND annotation_state = 'annotated'"
	case FilterEmpty:
		where += " AND annotation_state = 'empty'"
	case FilterInvalid:
		where += " AND annotation_state = 'invalid'"
	case FilterUnannotated:
		where += " AND annotation_state = 'none'"
	}
	if f.Label != "" {
		where += " AND label_counts ? " + arg(f.Label)
	}
	if f.Search != "" {
		where += " AND strpos(lower(rel_path), lower(" + arg(f.Search) + ")) > 0"
	}

	var total int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM dataset_images WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "rel_path, id"
	switch f.Sort {
	case SortModTime:
		order = "mod_time DESC, id DESC"
	case SortObjects:
		order = "object_count DESC, rel_path, id"
	}
	sql := "SELECT " + imageColumns + " FROM dataset_images WHERE " + where +
		" ORDER BY " + order + " LIMIT " + arg(f.Limit) + " OFFSET " + arg(f.Offset)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	images, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Image, error) { return scanImage(row) })
	return images, total, err
}

func queryImage(ctx context.Context, q querier, datasetID, imageID int64) (Image, error) {
	return scanImage(q.QueryRow(ctx, "SELECT "+imageColumns+" FROM dataset_images WHERE id = $1 AND dataset_id = $2",
		imageID, datasetID))
}

func queryLabels(ctx context.Context, q querier, datasetID int64) ([]Label, error) {
	rows, err := q.Query(ctx, `SELECT l.key, sum(l.value::bigint)::bigint, count(*)
		FROM dataset_images i, jsonb_each_text(i.label_counts) AS l
		WHERE i.dataset_id = $1
		GROUP BY l.key
		ORDER BY 2 DESC, 1`, datasetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Label])
}

// Annotation states stored in dataset_images.annotation_state.
const (
	annotationNone      = "none"
	annotationInvalid   = "invalid"
	annotationEmpty     = "empty"
	annotationAnnotated = "annotated"
)

// indexedImage is the part of an image row a scan compares with the disk.
type indexedImage struct {
	ID         int64
	Size       int64
	ModTime    time.Time
	AnnPath    *string
	AnnModTime *time.Time
	AnnSize    *int64
	State      string
}

func queryIndex(ctx context.Context, q querier, datasetID int64) (map[string]indexedImage, error) {
	rows, err := q.Query(ctx, `SELECT rel_path, id, size_bytes, mod_time, annotation_path, annotation_mod_time,
		annotation_size, annotation_state FROM dataset_images WHERE dataset_id = $1`, datasetID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]indexedImage)
	var rel string
	var r indexedImage
	_, err = pgx.ForEachRow(rows, []any{&rel, &r.ID, &r.Size, &r.ModTime, &r.AnnPath, &r.AnnModTime, &r.AnnSize, &r.State},
		func() error {
			out[rel] = r
			return nil
		})
	return out, err
}

// imageRecord is a row to insert or update while indexing.
type imageRecord struct {
	RelPath     string
	Size        int64
	ModTime     time.Time
	Width       *int
	Height      *int
	AnnPath     *string
	AnnModTime  *time.Time
	AnnSize     *int64
	State       string
	ObjectCount int
	Labels      map[string]int
}

// upsertImages writes records in one transaction (callers pass at most
// upsertBatchSize rows).
func upsertImages(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}, datasetID int64, recs []imageRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	b := &pgx.Batch{}
	for _, r := range recs {
		labels := r.Labels
		if labels == nil {
			labels = map[string]int{}
		}
		b.Queue(`INSERT INTO dataset_images (dataset_id, rel_path, size_bytes, mod_time, width, height,
				annotation_path, annotation_mod_time, annotation_size, annotation_state, object_count, label_counts)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (dataset_id, rel_path) DO UPDATE SET
				size_bytes = EXCLUDED.size_bytes, mod_time = EXCLUDED.mod_time,
				width = EXCLUDED.width, height = EXCLUDED.height,
				annotation_path = EXCLUDED.annotation_path, annotation_mod_time = EXCLUDED.annotation_mod_time,
				annotation_size = EXCLUDED.annotation_size, annotation_state = EXCLUDED.annotation_state,
				object_count = EXCLUDED.object_count, label_counts = EXCLUDED.label_counts, updated_at = now()`,
			datasetID, r.RelPath, r.Size, r.ModTime, r.Width, r.Height,
			r.AnnPath, r.AnnModTime, r.AnnSize, r.State, r.ObjectCount, labels)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("upsert images: %w", err)
	}
	return tx.Commit(ctx)
}

func deleteImages(ctx context.Context, q querier, datasetID int64, ids []int64) error {
	for chunk := range slices.Chunk(ids, upsertBatchSize) {
		if _, err := q.Exec(ctx, "DELETE FROM dataset_images WHERE dataset_id = $1 AND id = ANY($2)", datasetID, chunk); err != nil {
			return fmt.Errorf("delete images: %w", err)
		}
	}
	return nil
}
