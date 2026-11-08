package dataset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// OWNER: backend-core agent (data versions). A snapshot is the composed
// image list of a VersionSelection (what a download of it would take, with
// the same layering rules as layerSQL / composedSQL), read consistently for
// package dataversion, with the dataset folder opened to read the files.

// SnapshotOptions tune OpenSnapshot.
type SnapshotOptions struct {
	// IndexOnly: the folder is not opened (Root is nil) and the review
	// version objects are not loaded (previews count from the index).
	IndexOnly bool
	// AllowTrashedVersions accepts selected review versions that are in
	// the recycle bin (a data version build: they were in use when the
	// data version was created and keep their rows until purged).
	AllowTrashedVersions bool
}

// Snapshot is the composed image list of a selection of a dataset.
type Snapshot struct {
	Dataset  Dataset
	Versions []Version       // the selected review versions, newest first
	Root     *os.Root        // the dataset folder; nil with IndexOnly
	Images   []SnapshotImage // in ImageOrder
}

// Close closes the dataset folder.
func (s *Snapshot) Close() error {
	if s == nil || s.Root == nil {
		return nil
	}
	return s.Root.Close()
}

// SnapshotImage is an image of a snapshot with the annotation that applies
// to it.
type SnapshotImage struct {
	ID        int64
	RelPath   string // slash-separated, in the dataset folder
	SizeBytes int64
	ModTime   time.Time // as indexed (microseconds)
	Width     *int      // from the index; nil when unknown
	Height    *int

	// The base annotation, from the index.
	AnnotationPath  *string        // the VOC file; nil: none
	AnnotationState string         // none, annotated, empty or invalid
	BaseLabels      map[string]int // label counts

	// The review version image whose annotation applies (VersionID nil:
	// the base applies).
	VersionID      *int64
	ReviewStatus   string             // of that version image
	VersionLabels  map[string]int     // its label counts
	VersionObjects []AnnotationObject // its objects; nil with IndexOnly
	VersionWidth   *int               // the image size when it was merged
	VersionHeight  *int
}

// snapshotObjectChunk is how many review version objects OpenSnapshot
// loads per query.
const snapshotObjectChunk = 1000

// errSnapshotVersionMissing: a selected review version does not exist (any
// more), belongs to another dataset or is in the recycle bin while that is
// not allowed.
var errSnapshotVersionMissing = &Error{Kind: ErrNotFound, Message: "所选审图版本不存在（可能已被删除）"}

// snapshotImagesSQL lists the composed images of a selection with the
// layer image that replaces their annotation: the rows and the condition
// of composedSQL (on top of layerSQL, so both take exactly the same
// images with the same annotation), plus the file size, the layer image's
// review status and its image size. Parameters as in layerSQL.
const snapshotImagesSQL = `WITH ` + layerSQL + `
	SELECT di.id, di.rel_path, di.size_bytes, di.mod_time, di.width, di.height, di.annotation_path,
		di.annotation_state, di.label_counts, l.version_id, l.review_status, l.labels, vi.width, vi.height
	FROM dataset_images di
	LEFT JOIN layer l ON l.rel_path = di.rel_path
	LEFT JOIN dataset_version_images vi ON vi.version_id = l.version_id AND vi.rel_path = l.rel_path
	WHERE di.dataset_id = $1 AND ($3::boolean OR (l.version_id IS NOT NULL AND ` + layerTaken + `))
	ORDER BY di.sort_key, di.id`

// OpenSnapshot reads the composed images of sel for the dataset in one
// REPEATABLE READ, READ ONLY transaction, then (unless IndexOnly) loads the
// review version objects in chunks without the recycle bin filter and
// opens the dataset folder. Errors are *Error: the dataset is missing or in
// the recycle bin (ErrNotFound); a selected review version is missing, of
// another dataset, or in the bin without AllowTrashedVersions
// (ErrNotFound, "所选审图版本不存在（可能已被删除）"); not ready
// (errVersionBuilding); purged while its objects were loaded (ErrNotFound,
// "所选审图版本“<name>”已被永久删除"); the folder is missing (ErrConflict,
// "数据集“<name>”的文件夹不存在", without the server path). Close the
// snapshot when done.
func (s *Service) OpenSnapshot(ctx context.Context, datasetID int64, sel VersionSelection, opts SnapshotOptions) (*Snapshot, error) {
	snap := &Snapshot{}
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			d, err := queryDataset(ctx, tx, datasetID, false)
			if err != nil {
				return err
			}
			d.Path = s.datasetDir(d.DirName)
			snap.Dataset = d
			if snap.Versions, err = snapshotVersions(ctx, tx, datasetID, sel, opts.AllowTrashedVersions); err != nil {
				return err
			}
			ids := make([]int64, len(snap.Versions))
			for i, v := range snap.Versions {
				ids[i] = v.ID
			}
			snap.Images, err = querySnapshotImages(ctx, tx, datasetID, ids, sel.IncludeBase)
			return err
		})
	if err != nil {
		return nil, err
	}
	if opts.IndexOnly {
		return snap, nil
	}
	if err := s.loadSnapshotObjects(ctx, snap); err != nil {
		return nil, err
	}
	if snap.Root, err = s.openSnapshotRoot(snap.Dataset); err != nil {
		return nil, err
	}
	return snap, nil
}

// snapshotVersions validates the review versions of a selection like
// resolveSelection: at least one source, at most maxSelectedVersions
// review versions, each of this dataset and ready; in the recycle bin only
// when allowTrashed. They are returned newest first.
func snapshotVersions(ctx context.Context, q querier, datasetID int64, sel VersionSelection, allowTrashed bool) ([]Version, error) {
	var ids []int64
	for _, id := range sel.VersionIDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0 && !sel.IncludeBase:
		return nil, &Error{Kind: ErrInvalid, Message: "请至少选择一个标注来源（基础标注或审图版本）"}
	case len(ids) > maxSelectedVersions:
		return nil, newError(ErrInvalid, "一次最多选择 %d 个审图版本", maxSelectedVersions)
	case len(ids) == 0:
		return []Version{}, nil
	}
	sql := "SELECT " + versionColumns + versionFrom + " WHERE v.id = ANY($1) AND v.dataset_id = $2"
	if !allowTrashed {
		sql += versionLive
	}
	versions, err := collectVersions(q.Query(ctx, sql+newestFirst, ids, datasetID))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		i := slices.IndexFunc(versions, func(v Version) bool { return v.ID == id })
		switch {
		case i < 0:
			return nil, errSnapshotVersionMissing
		case versions[i].Status != VersionReady:
			return nil, errVersionBuilding
		}
	}
	return versions, nil
}

// querySnapshotImages reads the composed images of a selection (the
// selected versions newest first) in image order.
func querySnapshotImages(ctx context.Context, q querier, datasetID int64, versionIDs []int64, includeBase bool) ([]SnapshotImage, error) {
	rows, err := q.Query(ctx, snapshotImagesSQL, datasetID, versionIDs, includeBase)
	if err != nil {
		return nil, err
	}
	images := []SnapshotImage{}
	var img SnapshotImage
	var reviewStatus *string
	_, err = pgx.ForEachRow(rows, []any{&img.ID, &img.RelPath, &img.SizeBytes, &img.ModTime, &img.Width, &img.Height,
		&img.AnnotationPath, &img.AnnotationState, &img.BaseLabels, &img.VersionID, &reviewStatus, &img.VersionLabels,
		&img.VersionWidth, &img.VersionHeight}, func() error {
		out := img
		out.ModTime = out.ModTime.UTC()
		if out.BaseLabels == nil {
			out.BaseLabels = map[string]int{}
		}
		if out.VersionID != nil {
			if reviewStatus != nil {
				out.ReviewStatus = *reviewStatus
			}
			if out.VersionLabels == nil {
				out.VersionLabels = map[string]int{}
			}
		}
		images = append(images, out)
		// Scanning into a map or pointer allocates a new one for the next
		// row only when it is nil.
		img = SnapshotImage{}
		reviewStatus = nil
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read snapshot images: %w", err)
	}
	return images, nil
}

// loadSnapshotObjects loads the objects of the review version images of a
// snapshot, a chunk at a time, outside the snapshot transaction and
// without the recycle bin filter (a version in the bin keeps its rows). A
// row gone meanwhile means its version was purged.
func (s *Service) loadSnapshotObjects(ctx context.Context, snap *Snapshot) error {
	var indexes []int32
	for i, img := range snap.Images {
		if img.VersionID != nil {
			indexes = append(indexes, int32(i))
		}
	}
	for chunk := range slices.Chunk(indexes, snapshotObjectChunk) {
		versionIDs := make([]int64, len(chunk))
		paths := make([]string, len(chunk))
		for k, i := range chunk {
			versionIDs[k] = *snap.Images[i].VersionID
			paths[k] = snap.Images[i].RelPath
		}
		rows, err := s.db.Query(ctx, `SELECT k.i, vi.objects
			FROM unnest($1::int[], $2::bigint[], $3::text[]) AS k (i, v, p)
			JOIN dataset_version_images vi ON vi.version_id = k.v AND vi.rel_path = k.p`, chunk, versionIDs, paths)
		if err != nil {
			return fmt.Errorf("load review version objects: %w", err)
		}
		var i int32
		var objects []AnnotationObject
		if _, err := pgx.ForEachRow(rows, []any{&i, &objects}, func() error {
			if objects == nil {
				objects = []AnnotationObject{}
			}
			snap.Images[i].VersionObjects = objects
			objects = nil
			return nil
		}); err != nil {
			return fmt.Errorf("load review version objects: %w", err)
		}
		for _, i := range chunk {
			if img := snap.Images[i]; img.VersionObjects == nil {
				return snapshotVersionPurged(snap.Versions, *img.VersionID)
			}
		}
	}
	return nil
}

// snapshotVersionPurged is the error for a selected review version whose
// rows disappeared after the snapshot was read.
func snapshotVersionPurged(versions []Version, id int64) error {
	name := ""
	if i := slices.IndexFunc(versions, func(v Version) bool { return v.ID == id }); i >= 0 {
		name = versions[i].Name
	}
	return newError(ErrNotFound, "所选审图版本“%s”已被永久删除", name)
}

// openSnapshotRoot opens the folder of d like openDatasetRoot, with an
// error that names the dataset instead of the server path.
func (s *Service) openSnapshotRoot(d Dataset) (*os.Root, error) {
	root, err := s.openDatasetRoot(d)
	var ue *Error
	if errors.As(err, &ue) && errors.Is(err, ErrConflict) {
		return nil, newError(ErrConflict, "数据集“%s”的文件夹不存在", d.Name)
	}
	return root, err
}
