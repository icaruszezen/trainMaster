package dataset

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Dataset versions (数据集版本). The dataset folder with its VOC files is the
// base version (基础版本): what imports, scans and the annotation workspace
// change. A merge of a model review (internal/review) does not write into
// it any more; it adds a review version (审图版本) to each dataset it covers:
// a snapshot of the merged review annotations, one row per image keyed by
// the image's path in the dataset folder, with the image's review status.
// Versions are named 审图<major>.<minor>, per dataset 审图1.0, 1.1, ... in
// the order they are completed; a number is never given twice
// (datasets.review_version_seq).
//
// A merge builds its versions while it runs (status building, no number)
// and finishes them in the transaction that records its end
// (FinishReviewVersions): each gets its number and becomes ready. A merge
// that fails or is interrupted discards them, so a ready version is always
// complete and never changes, except for its note.
//
// Downloads combine versions (see VersionSelection): newer review versions
// override older ones and the base, image by image.

// Version kinds and states.
const (
	VersionKindReview = "review"

	VersionBuilding = "building"
	VersionReady    = "ready"
)

// reviewVersionMajor is the major number of new review versions.
const reviewVersionMajor = 1

// maxVersionNoteRunes bounds a version note, like a description.
const maxVersionNoteRunes = maxDescriptionRunes

// maxSelectedVersions bounds the versions one download combines.
const maxSelectedVersions = 100

// VersionName is the name of a review version: 审图<major>.<minor>.
func VersionName(major, minor int) string { return fmt.Sprintf("审图%d.%d", major, minor) }

// NextReviewVersionName is the name the next review version of a dataset
// gets, given its datasets.review_version_seq.
func NextReviewVersionName(seq int) string { return VersionName(reviewVersionMajor, seq) }

// NormalizeVersionNote trims the note of a version and validates it: it is
// required, at most 500 characters; line breaks and tabs are allowed, other
// control characters are not.
func NormalizeVersionNote(raw string) (string, error) {
	note := strings.TrimSpace(raw)
	invalid := strings.ContainsFunc(note, func(r rune) bool {
		return r != '\n' && r != '\r' && r != '\t' && (r < 0x20 || r == 0x7f)
	})
	switch {
	case note == "":
		return "", &Error{Kind: ErrInvalid, Message: "请填写版本备注"}
	case invalid || !utf8.ValidString(note):
		return "", &Error{Kind: ErrInvalid, Message: "版本备注包含无效字符"}
	case utf8.RuneCountInString(note) > maxVersionNoteRunes:
		return "", &Error{Kind: ErrInvalid, Message: "版本备注不能超过 500 个字符"}
	}
	return note, nil
}

// VersionModel describes the model of the review a version came from, as
// it was when the version was made.
type VersionModel struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name"`
	Version     int    `json:"version"`
	YOLOVersion string `json:"yolo_version"`
	Task        string `json:"task"`
	FileName    string `json:"file_name"`
}

// VersionSource is where a review version came from (dataset_versions.
// source): a snapshot kept when the review or its merge is deleted.
type VersionSource struct {
	ReviewID   int64        `json:"review_id"`
	ReviewName string       `json:"review_name"`
	MergeID    int64        `json:"merge_id"`
	Statuses   []string     `json:"statuses"` // the review statuses the merge took
	Model      VersionModel `json:"model"`
}

// VersionReview names the review of a version that still exists, by its
// current name.
type VersionReview struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Version is a version of a dataset other than its base: a review
// version.
type Version struct {
	ID        int64  `json:"id"`
	DatasetID int64  `json:"dataset_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"` // 审图1.3; "" while building
	Major     *int   `json:"major"`
	Minor     *int   `json:"minor"`
	Note      string `json:"note"`
	Status    string `json:"status"`
	// Counts of its images (0 while building).
	ImageCount       int            `json:"image_count"`
	NeedsSampleCount int            `json:"needs_sample_count"`
	ApprovedCount    int            `json:"approved_count"`
	ObjectCount      int            `json:"object_count"`
	Review           *VersionReview `json:"review"`   // nil once the review is deleted
	MergeID          *int64         `json:"merge_id"` // nil once the merge (its review) is deleted
	Source           VersionSource  `json:"source"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
}

// VersionRef is a version as a merge of a review lists it.
type VersionRef struct {
	ID         int64  `json:"id"`
	DatasetID  int64  `json:"dataset_id"`
	Name       string `json:"name"` // "" while building
	Status     string `json:"status"`
	ImageCount int    `json:"image_count"`
}

var (
	errVersionNotFound = &Error{Kind: ErrNotFound, Message: "版本不存在（可能已被删除）"}
	errVersionBuilding = &Error{Kind: ErrConflict, Message: "这个版本正在生成，请等合并完成后再操作"}
	// errVersionDatasetGone: the dataset of a version being built was
	// deleted meanwhile.
	errVersionDatasetGone = &Error{Kind: ErrNotFound, Message: "数据集已被删除"}
)

const versionColumns = `v.id, v.dataset_id, v.kind, v.major, v.minor, v.note, v.status, v.image_count,
	v.needs_sample_count, v.approved_count, v.object_count, v.review_id, r.name, v.merge_id, v.source,
	v.created_at, v.updated_at, v.finished_at`

// versionFrom is the FROM clause of versionColumns for the versions in use:
// neither the version nor its dataset is in the recycle bin.
const versionFrom = ` FROM dataset_versions v
	JOIN datasets vd ON vd.id = v.dataset_id AND vd.deleted_at IS NULL
	LEFT JOIN reviews r ON r.id = v.review_id`

// versionLive is the condition (on dataset_versions v) of a version that is
// not in the recycle bin.
const versionLive = ` AND v.deleted_at IS NULL`

func scanVersion(row pgx.Row) (Version, error) { return scanVersionWith(row) }

// scanVersionWith scans versionColumns followed by the extra columns.
func scanVersionWith(row pgx.Row, extra ...any) (Version, error) {
	var v Version
	var reviewID *int64
	var reviewName *string
	err := row.Scan(append([]any{&v.ID, &v.DatasetID, &v.Kind, &v.Major, &v.Minor, &v.Note, &v.Status,
		&v.ImageCount, &v.NeedsSampleCount, &v.ApprovedCount, &v.ObjectCount, &reviewID, &reviewName, &v.MergeID,
		&v.Source, &v.CreatedAt, &v.UpdatedAt, &v.FinishedAt}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errVersionNotFound
	}
	if err != nil {
		return v, err
	}
	if v.Major != nil && v.Minor != nil {
		v.Name = VersionName(*v.Major, *v.Minor)
	}
	if reviewID != nil && reviewName != nil {
		v.Review = &VersionReview{ID: *reviewID, Name: *reviewName}
	}
	if v.Source.Statuses == nil {
		v.Source.Statuses = []string{}
	}
	v.CreatedAt, v.UpdatedAt, v.FinishedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC(), utcPtr(v.FinishedAt)
	return v, nil
}

func collectVersions(rows pgx.Rows, err error) ([]Version, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) { return scanVersion(row) })
}

// newestFirst orders ready versions before building ones, then by number
// (newest first); building versions by ID (newest first).
const newestFirst = ` ORDER BY v.major DESC NULLS FIRST, v.minor DESC NULLS FIRST, v.id DESC`

// ListVersions returns the review versions of a dataset, the ones being
// built first, then the ready ones newest first.
func (s *Service) ListVersions(ctx context.Context, datasetID int64) ([]Version, error) {
	if err := datasetExists(ctx, s.db, datasetID); err != nil {
		return nil, err
	}
	versions, err := collectVersions(s.db.Query(ctx, "SELECT "+versionColumns+versionFrom+
		" WHERE v.dataset_id = $1"+versionLive+newestFirst, datasetID))
	if versions == nil && err == nil {
		versions = []Version{}
	}
	return versions, err
}

// GetVersion returns one version of a dataset.
func (s *Service) GetVersion(ctx context.Context, datasetID, versionID int64) (Version, error) {
	v, err := scanVersion(s.db.QueryRow(ctx, "SELECT "+versionColumns+versionFrom+
		" WHERE v.id = $1 AND v.dataset_id = $2"+versionLive, versionID, datasetID))
	if errors.Is(err, ErrNotFound) {
		if derr := datasetExists(ctx, s.db, datasetID); derr != nil {
			return v, derr
		}
	}
	return v, err
}

// UpdateVersionInput holds the fields to change; nil leaves a field alone.
type UpdateVersionInput struct {
	Note *string
}

// UpdateVersion changes the note of a ready version.
func (s *Service) UpdateVersion(ctx context.Context, datasetID, versionID int64, in UpdateVersionInput) (Version, error) {
	if in.Note != nil {
		note, err := NormalizeVersionNote(*in.Note)
		if err != nil {
			return Version{}, err
		}
		var status string
		err = s.db.QueryRow(ctx, `UPDATE dataset_versions SET note = CASE WHEN status = 'ready' THEN $3 ELSE note END,
				updated_at = CASE WHEN status = 'ready' THEN now() ELSE updated_at END
			WHERE id = $1 AND dataset_id = $2 AND deleted_at IS NULL
				AND EXISTS (SELECT 1 FROM datasets WHERE id = $2 AND deleted_at IS NULL)
			RETURNING status`, versionID, datasetID, note).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.GetVersion(ctx, datasetID, versionID) // the right not-found error
		}
		if err != nil {
			return Version{}, err
		}
		if status != VersionReady {
			return Version{}, errVersionBuilding
		}
	}
	return s.GetVersion(ctx, datasetID, versionID)
}

// DeleteVersion moves a ready version to the recycle bin (recycle.go):
// it is left out of the dataset's versions and downloads until it is
// restored, or purged with its images after recycle.Retention. The dataset
// and the other versions do not change; the number is not given again.
func (s *Service) DeleteVersion(ctx context.Context, datasetID, versionID int64) error {
	var status string
	err := s.db.QueryRow(ctx, `UPDATE dataset_versions v SET deleted_at = now()
		WHERE v.id = $1 AND v.dataset_id = $2 AND v.status = 'ready' AND v.deleted_at IS NULL
			AND EXISTS (SELECT 1 FROM datasets WHERE id = $2 AND deleted_at IS NULL)
		RETURNING v.status`, versionID, datasetID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		v, gerr := s.GetVersion(ctx, datasetID, versionID)
		if gerr != nil {
			return gerr
		}
		if v.Status == VersionBuilding {
			return errVersionBuilding
		}
		return errVersionNotFound // deleted meanwhile
	}
	if err != nil {
		return err
	}
	s.log.Info("dataset version moved to the recycle bin", "dataset_id", datasetID, "version_id", versionID)
	return nil
}

// datasetExists returns errDatasetNotFound unless the dataset exists and
// is not in the recycle bin.
func datasetExists(ctx context.Context, q querier, id int64) error {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1 AND deleted_at IS NULL)", id).
		Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errDatasetNotFound
	}
	return nil
}

// queryVersionCounts counts the ready versions of datasets (not those in
// the recycle bin).
func queryVersionCounts(ctx context.Context, q querier, ids []int64) (map[int64]int, error) {
	rows, err := q.Query(ctx, `SELECT dataset_id, count(*) FROM dataset_versions
		WHERE dataset_id = ANY($1) AND status = 'ready' AND deleted_at IS NULL GROUP BY dataset_id`, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	var id int64
	var n int
	_, err = pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		out[id] = n
		return nil
	})
	return out, err
}

// ---- building review versions (used by merges of model reviews) ----

// ReviewVersionDraft describes the review version a merge builds for one
// dataset.
type ReviewVersionDraft struct {
	DatasetID int64
	ReviewID  int64
	MergeID   int64
	Note      string // normalized (NormalizeVersionNote)
	Source    VersionSource
}

// VersionImage is an image a merge adds to a version.
type VersionImage struct {
	Path          string // in the dataset folder
	ReviewStatus  string
	Width, Height *int               // the image size when known
	Objects       []AnnotationObject // [] for a negative sample
}

// CreateReviewVersion starts building a review version of a dataset. It
// fails with an *Error of kind ErrNotFound when the dataset no longer
// exists or is in the recycle bin. The dataset row is locked FOR KEY SHARE
// while the version is added: moving the dataset to the recycle bin locks
// it FOR UPDATE and refuses while it has a version being built, so a
// version is never built for a dataset in the bin.
func (s *Service) CreateReviewVersion(ctx context.Context, d ReviewVersionDraft) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `WITH d AS (SELECT id FROM datasets WHERE id = $1 AND deleted_at IS NULL FOR KEY SHARE)
		INSERT INTO dataset_versions (dataset_id, kind, note, status, review_id, merge_id, source)
		SELECT d.id, 'review', $2, 'building', $3, $4, $5 FROM d RETURNING id`,
		d.DatasetID, d.Note, d.ReviewID, d.MergeID, d.Source).Scan(&id)
	if foreignKeyViolation(err) || errors.Is(err, pgx.ErrNoRows) {
		return 0, errVersionDatasetGone
	}
	return id, err
}

// errVersionImageGone: the image a merge adds to a version is no longer
// in the dataset.
var errVersionImageGone = &Error{Kind: ErrNotFound, Message: "图片已不在数据集中（可能已被删除）"}

// AddVersionImage adds an image of the dataset to a version being built,
// with the size and modification time of its file now: the version applies
// to the image only while it is that file. It fails with an *Error of kind
// ErrNotFound when the image or the version (with its dataset) is gone.
func (s *Service) AddVersionImage(ctx context.Context, versionID int64, img VersionImage) error {
	objects := img.Objects
	if objects == nil {
		objects = []AnnotationObject{}
	}
	labels := map[string]int{}
	for _, o := range objects {
		labels[o.Name]++
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO dataset_version_images (version_id, rel_path, image_size, image_mod_time,
			review_status, width, height, objects, object_count, labels)
		SELECT v.id, di.rel_path, di.size_bytes, di.mod_time, $3, $4, $5, $6, $7, $8
		FROM dataset_versions v JOIN dataset_images di ON di.dataset_id = v.dataset_id AND di.rel_path = $2
		WHERE v.id = $1`,
		versionID, img.Path, img.ReviewStatus, img.Width, img.Height, objects, len(objects), labels)
	switch {
	case foreignKeyViolation(err): // the version was deleted meanwhile
		return errVersionImageGone
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return errVersionImageGone
	}
	return nil
}

func foreignKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// FinishReviewVersions completes the versions a merge built, in the
// caller's transaction (the one that records the end of the merge): each
// gets the next number of its dataset and becomes ready, with its counts.
// Versions of datasets deleted meanwhile are gone already.
//
// Lock order: the datasets first, in ID order and FOR NO KEY UPDATE (the
// order of package project; the rows stay referenceable, so creating a
// review or another merge's versions does not wait), then their versions.
// Deleting a dataset also locks it before its versions.
func (s *Service) FinishReviewVersions(ctx context.Context, tx pgx.Tx, mergeID int64) ([]VersionRef, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM datasets
		WHERE id IN (SELECT dataset_id FROM dataset_versions WHERE merge_id = $1 AND status = 'building')
		ORDER BY id FOR NO KEY UPDATE`, mergeID)
	if err != nil {
		return nil, err
	}
	datasetIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(datasetIDs) == 0 {
		return []VersionRef{}, err
	}
	rows, err = tx.Query(ctx, `SELECT id, dataset_id FROM dataset_versions
		WHERE merge_id = $1 AND status = 'building' AND dataset_id = ANY($2) ORDER BY dataset_id, id`, mergeID, datasetIDs)
	if err != nil {
		return nil, err
	}
	type building struct{ ID, DatasetID int64 }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[building])
	if err != nil {
		return nil, err
	}
	refs := make([]VersionRef, 0, len(list))
	for _, b := range list {
		var minor int
		if err := tx.QueryRow(ctx, `UPDATE datasets SET review_version_seq = review_version_seq + 1, updated_at = now()
			WHERE id = $1 RETURNING review_version_seq - 1`, b.DatasetID).Scan(&minor); err != nil {
			return nil, err
		}
		ref := VersionRef{ID: b.ID, DatasetID: b.DatasetID, Name: VersionName(reviewVersionMajor, minor), Status: VersionReady}
		if err := tx.QueryRow(ctx, `UPDATE dataset_versions v SET status = 'ready', major = $2, minor = $3,
				image_count = c.images, needs_sample_count = c.needs_sample, approved_count = c.approved,
				object_count = c.objects, finished_at = now(), updated_at = now()
			FROM (SELECT count(*) AS images,
					count(*) FILTER (WHERE review_status = 'needs_sample') AS needs_sample,
					count(*) FILTER (WHERE review_status = 'approved') AS approved,
					coalesce(sum(object_count), 0) AS objects
				FROM dataset_version_images WHERE version_id = $1) c
			WHERE v.id = $1 RETURNING v.image_count`, b.ID, reviewVersionMajor, minor).Scan(&ref.ImageCount); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// DiscardReviewVersions removes the versions a merge was building, in the
// caller's transaction (a merge that did not complete leaves none). It
// locks only the versions, which deleting their dataset locks after the
// dataset: there is no cycle.
func (s *Service) DiscardReviewVersions(ctx context.Context, tx pgx.Tx, mergeID int64) error {
	_, err := tx.Exec(ctx, "DELETE FROM dataset_versions WHERE merge_id = $1 AND status = 'building'", mergeID)
	return err
}

// DiscardBuildingVersions removes every version being built, in the
// caller's transaction: at startup, after the merges a previous run left
// running were failed, none of them can be completed.
func (s *Service) DiscardBuildingVersions(ctx context.Context, tx pgx.Tx) (int64, error) {
	tag, err := tx.Exec(ctx, "DELETE FROM dataset_versions WHERE status = 'building'")
	return tag.RowsAffected(), err
}

// MergeVersions returns the versions made by merges (building or ready),
// by merge, in dataset ID order.
func (s *Service) MergeVersions(ctx context.Context, mergeIDs []int64) (map[int64][]VersionRef, error) {
	out := map[int64][]VersionRef{}
	if len(mergeIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT merge_id, id, dataset_id, major, minor, status, image_count
		FROM dataset_versions WHERE merge_id = ANY($1) AND deleted_at IS NULL
		ORDER BY merge_id, dataset_id, id`, mergeIDs)
	if err != nil {
		return nil, err
	}
	var mergeID int64
	var ref VersionRef
	var major, minor *int
	_, err = pgx.ForEachRow(rows, []any{&mergeID, &ref.ID, &ref.DatasetID, &major, &minor, &ref.Status, &ref.ImageCount},
		func() error {
			r := ref
			if major != nil && minor != nil {
				r.Name = VersionName(*major, *minor)
			}
			out[mergeID] = append(out[mergeID], r)
			return nil
		})
	return out, err
}

// ---- combining versions (downloads) ----

// VersionSelection is the versions a download combines: the base version
// (the dataset's own annotations) and review versions.
//
// An image's annotation comes from the newest selected review version that
// has the image (of any review status), else from the dataset. With the
// base, every image of the dataset is included. Without it, only the images
// whose annotation comes from a review version that marked them
// needs_sample (需加样): an image the newest version marked approved is left
// out, even if an older selected version marked it needs_sample, so that
// the same versions give an image the same annotation with or without the
// base. A version image applies only while the dataset has the same file at
// its path: images no longer in the dataset are left out, a file replaced
// since the merge keeps its base annotation.
type VersionSelection struct {
	IncludeBase bool
	VersionIDs  []int64
}

// selectedVersions is a validated VersionSelection.
type selectedVersions struct {
	includeBase bool
	versions    []Version // newest first
}

func (sv selectedVersions) ids() []int64 {
	ids := make([]int64, len(sv.versions))
	for i, v := range sv.versions {
		ids[i] = v.ID
	}
	return ids
}

// baseOnly reports whether the selection is the base version alone (a
// download as before versions existed).
func (sv selectedVersions) baseOnly() bool { return sv.includeBase && len(sv.versions) == 0 }

// label names the selection, the layers from the bottom up, e.g.
// "基础+审图1.1+审图1.3"; long selections are shortened to the first and
// last versions with the count.
func (sv selectedVersions) label() string {
	var parts []string
	if sv.includeBase {
		parts = append(parts, "基础")
	}
	for _, v := range slices.Backward(sv.versions) {
		parts = append(parts, v.Name)
	}
	if len(sv.versions) > 4 {
		oldest, newest := sv.versions[len(sv.versions)-1].Name, sv.versions[0].Name
		short := fmt.Sprintf("%s…%s（%d个版本）", oldest, newest, len(sv.versions))
		if sv.includeBase {
			short = "基础+" + short
		}
		return short
	}
	return strings.Join(parts, "+")
}

// shortLabel names the selection by the number of versions, e.g.
// "基础+3个审图版本".
func (sv selectedVersions) shortLabel() string {
	label := fmt.Sprintf("%d个审图版本", len(sv.versions))
	if sv.includeBase {
		label = "基础+" + label
	}
	return label
}

// resolveSelection validates a selection for a dataset: at least one
// version, review versions of this dataset that are ready, each once.
func resolveSelection(ctx context.Context, q querier, datasetID int64, sel VersionSelection) (selectedVersions, error) {
	out := selectedVersions{includeBase: sel.IncludeBase}
	var ids []int64
	for _, id := range sel.VersionIDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0 && !sel.IncludeBase:
		return out, &Error{Kind: ErrInvalid, Message: "请至少选择一个要导出的版本"}
	case len(ids) > maxSelectedVersions:
		return out, newError(ErrInvalid, "一次最多选择 %d 个版本", maxSelectedVersions)
	case len(ids) == 0:
		return out, nil
	}
	versions, err := collectVersions(q.Query(ctx, "SELECT "+versionColumns+versionFrom+
		" WHERE v.id = ANY($1) AND v.dataset_id = $2"+versionLive+newestFirst, ids, datasetID))
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		i := slices.IndexFunc(versions, func(v Version) bool { return v.ID == id })
		switch {
		case i < 0:
			return out, newError(ErrNotFound, "所选的版本不存在（可能已被删除），请刷新后重新选择")
		case versions[i].Status != VersionReady:
			return out, errVersionBuilding
		}
	}
	out.versions = versions
	return out, nil
}

// layerSQL is a WITH clause "layer" of the version images a selection
// takes, one per dataset image ($1): the newest selected version that has
// the image as it is now (the same file: a version image whose file was
// replaced since, or that is no longer in the dataset, does not apply). $2
// holds the selected versions, newest first.
const layerSQL = `layer AS (
		SELECT DISTINCT ON (vi.rel_path) vi.rel_path, vi.version_id, vi.review_status, vi.object_count, vi.labels
		FROM dataset_version_images vi
		JOIN dataset_images di ON di.dataset_id = $1 AND di.rel_path = vi.rel_path
			AND di.size_bytes = vi.image_size AND di.mod_time = vi.image_mod_time
		WHERE vi.version_id = ANY($2::bigint[])
		ORDER BY vi.rel_path, array_position($2::bigint[], vi.version_id)
	)`

// layerTaken is the condition (on layer l) of the layer images a selection
// exports: all with the base ($3), else the needs_sample ones.
const layerTaken = `($3::boolean OR l.review_status = 'needs_sample')`

// composedSQL is a WITH clause "composed" (after layerSQL) of the images of
// a selection: the dataset images ($1), with the layer image that
// replaces their annotation (version_id NULL for none).
const composedSQL = `composed AS (
		SELECT di.id, di.sort_key, di.rel_path, di.mod_time, di.width, di.height, di.annotation_path,
			di.annotation_state, di.object_count AS base_objects, di.label_counts AS base_labels,
			l.version_id, l.object_count AS layer_objects, l.labels AS layer_labels
		FROM dataset_images di LEFT JOIN layer l ON l.rel_path = di.rel_path
		WHERE di.dataset_id = $1 AND ($3::boolean OR (l.version_id IS NOT NULL AND ` + layerTaken + `))
	)`

// ExportPreview describes what a download of a selection contains.
type ExportPreview struct {
	ImageCount       int     `json:"image_count"`       // images in the download
	FromVersions     int     `json:"from_versions"`     // of them, with the annotation of a review version
	AnnotatedCount   int     `json:"annotated_count"`   // with >= 1 object
	EmptyCount       int     `json:"empty_count"`       // with an annotation of 0 objects
	InvalidCount     int     `json:"invalid_count"`     // base VOC file that cannot be parsed
	UnannotatedCount int     `json:"unannotated_count"` // without an annotation
	ObjectCount      int     `json:"object_count"`
	Labels           []Label `json:"labels"` // by object count desc, then name
	// The images of the selected versions the selection takes (all with
	// the base, else the needs_sample ones), one per path, and of them
	// those no longer in the dataset and those whose file was replaced
	// since the merge: they do not use the version's annotation.
	VersionImageCount int `json:"version_image_count"`
	MissingCount      int `json:"missing_count"`
	ChangedCount      int `json:"changed_count"`
}

// entriesSQL is a WITH clause "entries": per path, the newest selected
// version image, whether or not it applies (see layerSQL).
const entriesSQL = `entries AS (
		SELECT DISTINCT ON (vi.rel_path) vi.rel_path, vi.review_status
		FROM dataset_version_images vi
		WHERE vi.version_id = ANY($2::bigint[])
		ORDER BY vi.rel_path, array_position($2::bigint[], vi.version_id)
	)`

// PreviewExport counts what a download of the selection would contain,
// from the index (like the dataset's counters).
func (s *Service) PreviewExport(ctx context.Context, datasetID int64, sel VersionSelection) (ExportPreview, error) {
	if err := datasetExists(ctx, s.db, datasetID); err != nil {
		return ExportPreview{}, err
	}
	sv, err := resolveSelection(ctx, s.db, datasetID, sel)
	if err != nil {
		return ExportPreview{}, err
	}
	args := []any{datasetID, sv.ids(), sv.includeBase}
	var p ExportPreview
	if err := s.db.QueryRow(ctx, `WITH `+layerSQL+`, `+composedSQL+`, `+entriesSQL+`,
		taken AS (
			SELECT e.rel_path, EXISTS (SELECT 1 FROM dataset_images di WHERE di.dataset_id = $1 AND di.rel_path = e.rel_path) AS present,
				EXISTS (SELECT 1 FROM layer l WHERE l.rel_path = e.rel_path) AS applies
			FROM entries e WHERE $3::boolean OR e.review_status = 'needs_sample'
		)
		SELECT count(*),
			count(*) FILTER (WHERE version_id IS NOT NULL),
			count(*) FILTER (WHERE CASE WHEN version_id IS NOT NULL THEN layer_objects > 0
				ELSE annotation_state = 'annotated' END),
			count(*) FILTER (WHERE CASE WHEN version_id IS NOT NULL THEN layer_objects = 0
				ELSE annotation_state = 'empty' END),
			count(*) FILTER (WHERE version_id IS NULL AND annotation_state = 'invalid'),
			count(*) FILTER (WHERE version_id IS NULL AND annotation_state = 'none'),
			coalesce(sum(CASE WHEN version_id IS NOT NULL THEN layer_objects ELSE base_objects END), 0),
			(SELECT count(*) FROM taken),
			(SELECT count(*) FROM taken WHERE NOT present),
			(SELECT count(*) FROM taken WHERE present AND NOT applies)
		FROM composed`, args...).Scan(&p.ImageCount, &p.FromVersions, &p.AnnotatedCount, &p.EmptyCount,
		&p.InvalidCount, &p.UnannotatedCount, &p.ObjectCount, &p.VersionImageCount, &p.MissingCount,
		&p.ChangedCount); err != nil {
		return ExportPreview{}, err
	}
	rows, err := s.db.Query(ctx, `WITH `+layerSQL+`, `+composedSQL+`
		SELECT lc.key, sum(lc.value::int)::int, count(*)::int
		FROM composed c,
			jsonb_each_text(CASE WHEN c.version_id IS NOT NULL THEN c.layer_labels ELSE c.base_labels END) AS lc
		GROUP BY lc.key ORDER BY 2 DESC, lc.key`, args...)
	if err != nil {
		return ExportPreview{}, err
	}
	p.Labels, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Label])
	if err != nil {
		return ExportPreview{}, err
	}
	if p.Labels == nil {
		p.Labels = []Label{}
	}
	return p, nil
}
