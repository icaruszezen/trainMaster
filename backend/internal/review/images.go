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

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// Image filters (ImageQuery).
const (
	FilterAll      = "all"
	FilterReviewed = "reviewed" // approved or needs_sample
	FilterEdited   = "edited"
	FilterUnedited = "unedited"
	FilterWith     = "with"    // boxes
	FilterWithout  = "without" // boxes
	FilterFailed   = "failed"  // inference
)

// Page sizes of the image list.
const (
	DefaultImageLimit = 60
	MaxImageLimit     = 200
)

// ImageQuery selects review images; "" or "all" does not filter.
type ImageQuery struct {
	Offset    int
	Limit     int
	Status    string // pending, approved, needs_sample or reviewed
	DatasetID int64
	Label     string // exact label name of the review annotation
	Search    string // case-insensitive substring of the path in the dataset
	Edited    string // edited or unedited
	Boxes     string // with or without (boxes of the review annotation)
	Inference string // failed
}

func (q ImageQuery) validate() error {
	bad := func(msg string) error { return &Error{Kind: ErrBadRequest, Message: msg} }
	switch q.Status {
	case "", FilterAll, reviewfile.StatusPending, reviewfile.StatusApproved, reviewfile.StatusNeedsSample, FilterReviewed:
	default:
		return bad("status 参数只能是 all、pending、approved、needs_sample 或 reviewed")
	}
	switch q.Edited {
	case "", FilterAll, FilterEdited, FilterUnedited:
	default:
		return bad("edited 参数只能是 all、edited 或 unedited")
	}
	switch q.Boxes {
	case "", FilterAll, FilterWith, FilterWithout:
	default:
		return bad("boxes 参数只能是 all、with 或 without")
	}
	switch q.Inference {
	case "", FilterAll, FilterFailed:
	default:
		return bad("inference 参数只能是 all 或 failed")
	}
	if q.Offset < 0 || q.Limit < 0 || q.Limit > MaxImageLimit {
		return bad(fmt.Sprintf("offset 不能为负数，limit 必须在 1–%d 之间", MaxImageLimit))
	}
	return nil
}

// imageFilter returns the WHERE condition selecting the review's images
// that match q (not its offset and limit).
func imageFilter(reviewID int64, q ImageQuery, args *sqlArgs) string {
	where := "ri.review_id = " + args.add(reviewID)
	switch q.Status {
	case reviewfile.StatusPending, reviewfile.StatusApproved, reviewfile.StatusNeedsSample:
		where += " AND ri.status = " + args.add(q.Status)
	case FilterReviewed:
		where += " AND ri.status IN ('approved', 'needs_sample')"
	}
	if q.DatasetID > 0 {
		where += " AND ri.dataset_id = " + args.add(q.DatasetID)
	}
	if q.Label != "" {
		where += " AND ri.labels ? " + args.add(q.Label)
	}
	if q.Search != "" {
		where += " AND strpos(lower(ri.rel_path), lower(" + args.add(q.Search) + ")) > 0"
	}
	switch q.Edited {
	case FilterEdited:
		where += " AND ri.edited"
	case FilterUnedited:
		where += " AND NOT ri.edited"
	}
	switch q.Boxes {
	case FilterWith:
		where += " AND ri.object_count > 0"
	case FilterWithout:
		where += " AND ri.object_count = 0"
	}
	if q.Inference == FilterFailed {
		where += " AND ri.inference_error <> ''"
	}
	return where
}

// reviewExists fails with errReviewNotFound unless the review exists.
func (s *Service) reviewExists(ctx context.Context, q querier, id int64) error {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM reviews WHERE id = $1)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errReviewNotFound
	}
	return nil
}

// ListImages returns a page of the review's images matching q, in review
// order, and the number of matching images.
func (s *Service) ListImages(ctx context.Context, reviewID int64, q ImageQuery) ([]Image, int, error) {
	if q.Limit == 0 {
		q.Limit = DefaultImageLimit
	}
	if err := q.validate(); err != nil {
		return nil, 0, err
	}
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return nil, 0, err
	}
	var args sqlArgs
	where := imageFilter(reviewID, q, &args)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM review_images ri WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	images, err := collectImages(s.db.Query(ctx, "SELECT "+imageColumns+" FROM review_images ri WHERE "+where+
		" ORDER BY ri.position LIMIT "+args.add(q.Limit)+" OFFSET "+args.add(q.Offset), args...))
	if images == nil {
		images = []Image{}
	}
	return images, total, err
}

// ImageNeighbors locates an image among the images matching q (offset and
// limit do not count) and returns the matching images before and after
// it.
func (s *Service) ImageNeighbors(ctx context.Context, reviewID, imageID int64, q ImageQuery) (Neighbors, error) {
	q.Offset, q.Limit = 0, 1
	var n Neighbors
	if err := q.validate(); err != nil {
		return n, err
	}
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return n, err
	}
	var position int
	err := s.db.QueryRow(ctx, "SELECT position FROM review_images WHERE id = $1 AND review_id = $2", imageID, reviewID).
		Scan(&position)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, errImageNotFound
	}
	if err != nil {
		return n, err
	}
	var args sqlArgs
	where := imageFilter(reviewID, q, &args)
	pos := args.add(position)
	countArgs := append(sqlArgs{}, args...)
	id := countArgs.add(imageID)
	err = s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE ri.position < `+pos+`::integer),
			coalesce(bool_or(ri.id = `+id+`::bigint), false), count(*)
		FROM review_images ri WHERE `+where, countArgs...).Scan(&n.Position, &n.Matches, &n.Total)
	if err != nil {
		return n, err
	}
	neighbor := func(cond, order string) (*Image, error) {
		img, err := scanImage(s.db.QueryRow(ctx, "SELECT "+imageColumns+" FROM review_images ri WHERE "+where+
			" AND "+cond+" ORDER BY "+order+" LIMIT 1", args...))
		if errors.Is(err, errImageNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &img, nil
	}
	if n.Prev, err = neighbor("ri.position < "+pos+"::integer", "ri.position DESC"); err != nil {
		return n, err
	}
	if n.Next, err = neighbor("ri.position > "+pos+"::integer", "ri.position"); err != nil {
		return n, err
	}
	return n, nil
}

// GetImage returns a review image with its boxes: the review annotation
// read from its VOC file now, and the predictions.
func (s *Service) GetImage(ctx context.Context, reviewID, imageID int64) (ImageDetail, error) {
	r, err := queryImageRow(ctx, s.db, reviewID, imageID, false)
	if errors.Is(err, errImageNotFound) {
		if rerr := s.reviewExists(ctx, s.db, reviewID); rerr != nil {
			return ImageDetail{}, rerr
		}
	}
	if err != nil {
		return ImageDetail{}, err
	}
	return s.imageDetail(ctx, r)
}

// annotationFile is a review VOC file as read now.
type annotationFile struct {
	data    []byte // nil: no file
	version string // "" without a file
	file    dataset.VOCFile
	err     string // why the file cannot be used, "" if fine
}

// readAnnotation reads the VOC file of a review image. A missing file (or
// review folder) is no error: the image has no annotation file.
func (s *Service) readAnnotation(reviewID int64, rel string) (annotationFile, error) {
	var a annotationFile
	root, err := os.OpenRoot(s.reviewDir(reviewID))
	if errors.Is(err, fs.ErrNotExist) {
		a.file.Objects = []dataset.AnnotationObject{}
		return a, nil
	}
	if err != nil {
		return a, fmt.Errorf("open review folder: %w", err)
	}
	defer root.Close()
	return readAnnotationIn(root, rel)
}

func readAnnotationIn(root *os.Root, rel string) (annotationFile, error) {
	a := annotationFile{file: dataset.VOCFile{Objects: []dataset.AnnotationObject{}}}
	data, err := reviewfile.ReadAnnotation(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			// Not a regular file, a folder in the way, a link out of the root:
			// unusable, but not a server failure.
			a.err = dataset.AnnotationErrorMessage(err)
			return a, nil
		}
		a.err = "标注文件无法读取：" + err.Error()
		return a, nil
	}
	a.data, a.version = data, reviewfile.AnnotationVersion(data)
	f, err := reviewfile.DecodeAnnotation(data)
	if err != nil {
		a.err = dataset.AnnotationErrorMessage(err)
		return a, nil
	}
	a.file = f
	return a, nil
}

// imageDetail completes an image row with its VOC file and dataset name.
func (s *Service) imageDetail(ctx context.Context, r imageRow) (ImageDetail, error) {
	a, err := s.readAnnotation(r.ReviewID, r.AnnotationPath)
	if err != nil {
		return ImageDetail{}, err
	}
	d := ImageDetail{Image: r.Image, Objects: a.file.Objects, Predictions: r.predictions, AnnotationError: a.err}
	d.AnnotationVersion = a.version
	err = s.db.QueryRow(ctx, `SELECT coalesce(d.name, rd.dataset_name) FROM review_datasets rd
		LEFT JOIN datasets d ON d.id = rd.dataset_id WHERE rd.review_id = $1 AND rd.dataset_id = $2`,
		r.ReviewID, r.DatasetID).Scan(&d.DatasetName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ImageDetail{}, err
	}
	return d, nil
}

// SaveInput is a review annotation to write.
type SaveInput struct {
	Objects []dataset.AnnotationObject
	// BaseVersion is the annotation_version of the file the edit started
	// from ("" when the image had none). Unless Force is set, the save
	// fails with ErrAnnotationChanged when the file is no longer that
	// version.
	BaseVersion string
	Force       bool
}

// SaveAnnotation replaces the review annotation of an image: the VOC file
// is written anew (atomically) from the objects, and the image's counts,
// labels and edited flag follow. Saving the boxes the file already has
// changes nothing. Saves of one image are serialized.
func (s *Service) SaveAnnotation(ctx context.Context, reviewID, imageID int64, in SaveInput) (ImageDetail, error) {
	if in.Objects == nil {
		in.Objects = []dataset.AnnotationObject{}
	}
	return s.changeImage(ctx, reviewID, imageID, func(r imageRow, a annotationFile) (imageChange, error) {
		if !in.Force && a.version != in.BaseVersion {
			return imageChange{}, errAnnotationChanged
		}
		objects, err := normalizeObjects(in.Objects, r.Width, r.Height)
		if err != nil {
			return imageChange{}, err
		}
		return imageChange{objects: objects, setObjects: true}, nil
	})
}

// SetStatus sets the review status of an image (pending, approved or
// needs_sample). Setting the status it has changes nothing.
func (s *Service) SetStatus(ctx context.Context, reviewID, imageID int64, status string) (ImageDetail, error) {
	if !reviewfile.ValidStatus(status) {
		return ImageDetail{}, &Error{Kind: ErrInvalid, Message: "status 必须是 pending、approved 或 needs_sample"}
	}
	return s.changeImage(ctx, reviewID, imageID, func(imageRow, annotationFile) (imageChange, error) {
		return imageChange{status: status}, nil
	})
}

// normalizeObjects validates boxes for an image as dataset saves do.
func normalizeObjects(objects []dataset.AnnotationObject, width, height *int) ([]dataset.AnnotationObject, error) {
	w, h := 0, 0
	if width != nil && height != nil {
		w, h = *width, *height
	}
	out, err := dataset.NormalizeObjects(objects, w, h)
	if err != nil {
		var de *dataset.Error
		if errors.As(err, &de) {
			return nil, &Error{Kind: ErrInvalid, Message: de.Message}
		}
		return nil, err
	}
	return out, nil
}

// imageChange is what to change on a review image.
type imageChange struct {
	objects    []dataset.AnnotationObject // the new annotation, if setObjects
	setObjects bool
	status     string // the new status; "" keeps it
}

// changed tells which kinds of change applyChange made.
type changed struct {
	status, annotation bool
}

func (c changed) any() bool { return c.status || c.annotation }

// changeImage changes a review image under its lock (see changeImageRow)
// and returns the image as changed.
func (s *Service) changeImage(ctx context.Context, reviewID, imageID int64,
	decide func(r imageRow, a annotationFile) (imageChange, error)) (ImageDetail, error) {
	row, _, err := s.changeImageRow(ctx, reviewID, imageID, decide)
	if err != nil {
		return ImageDetail{}, err
	}
	return s.imageDetail(ctx, row)
}

// changeImageRow changes a review image under its lock. decide sees the
// row (locked) and its VOC file as they are now and returns the change, or
// an error to refuse it. The VOC file is staged before the row is updated
// and renamed into place before the commit (and put back if the commit
// fails), so the file and the row agree. Each kind of change raises the
// revision by one; a change to what the image has already is no change.
// The image must be inferred. It returns the row as changed and what
// changed.
func (s *Service) changeImageRow(ctx context.Context, reviewID, imageID int64,
	decide func(r imageRow, a annotationFile) (imageChange, error)) (imageRow, changed, error) {
	unlock, err := s.lockImage(ctx, imageID)
	if err != nil {
		return imageRow{}, changed{}, err
	}
	defer unlock()
	ctx, cancel := s.detach(ctx)
	defer cancel()

	var row imageRow
	var what changed
	var restore func() // undoes the file change after a failed commit
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := lockReview(ctx, tx, reviewID, "KEY SHARE"); err != nil {
			return err
		}
		var err error
		if row, err = queryImageRow(ctx, tx, reviewID, imageID, true); err != nil {
			return err
		}
		if !row.Inferred {
			return errNotInferred
		}
		root, err := s.openReviewRoot(reviewID, true)
		if err != nil {
			return err
		}
		defer root.Close()
		a, err := readAnnotationIn(root, row.AnnotationPath)
		if err != nil {
			return err
		}
		ch, err := decide(row, a)
		if err != nil {
			return err
		}
		restore, what, err = s.applyChange(ctx, tx, root, &row, a, ch)
		return err
	})
	if err != nil {
		if restore != nil {
			restore()
		}
		return imageRow{}, changed{}, err
	}
	return row, what, nil
}

// applyChange makes a change of a locked review image in tx: it writes the
// VOC file (renamed into place; restore puts the old content back if tx
// does not commit) and updates the row, which it changes to match. It
// reports what changed.
func (s *Service) applyChange(ctx context.Context, tx pgx.Tx, root *os.Root, row *imageRow, a annotationFile,
	ch imageChange) (restore func(), what changed, err error) {
	var sets []string
	var args sqlArgs
	if ch.setObjects {
		data := reviewfile.EncodeAnnotation(row.Path, row.Width, row.Height, ch.objects)
		version := reviewfile.AnnotationVersion(data)
		if version != a.version {
			if restore, err = replaceFile(root, row.AnnotationPath, data, a.data); err != nil {
				return nil, changed{}, err
			}
			labels := reviewfile.LabelCounts(ch.objects)
			edited := !reviewfile.SameBoxes(ch.objects, row.predictions)
			sets = append(sets, "object_count = "+args.add(len(ch.objects)), "labels = "+args.add(labels),
				"edited = "+args.add(edited), "annotation_version = "+args.add(version))
			row.ObjectCount, row.Labels, row.Edited, row.AnnotationVersion = len(ch.objects), sortedLabels(labels), edited, version
			what.annotation = true
		}
	}
	if ch.status != "" && ch.status != row.Status {
		reviewed := "now()"
		if ch.status == reviewfile.StatusPending {
			reviewed = "NULL"
		}
		sets = append(sets, "status = "+args.add(ch.status), "reviewed_at = "+reviewed)
		what.status = true
	}
	if !what.any() {
		return nil, changed{}, nil
	}
	changes := 0 // one revision per kind of change
	for _, c := range []bool{what.status, what.annotation} {
		if c {
			changes++
		}
	}
	q := "UPDATE review_images SET " + strings.Join(sets, ", ") + ", revision = revision + " + args.add(changes) +
		", updated_at = now() WHERE id = " + args.add(row.ID) + " RETURNING revision, status, reviewed_at"
	if err := tx.QueryRow(ctx, q, args...).Scan(&row.Revision, &row.Status, &row.ReviewedAt); err != nil {
		if restore != nil {
			restore()
		}
		return nil, changed{}, err
	}
	row.ReviewedAt = utcPtr(row.ReviewedAt)
	return restore, what, nil
}

// replaceFile replaces the file rel inside root with data atomically.
// restore puts the old content back (old nil: removes the file).
func replaceFile(root *os.Root, rel string, data, old []byte) (restore func(), err error) {
	tmp, err := reviewfile.StageFile(root, rel, data)
	if err != nil {
		return nil, fmt.Errorf("write review annotation: %w", err)
	}
	if err := reviewfile.Rename(root, tmp, rel); err != nil {
		_ = root.Remove(filepath.FromSlash(tmp))
		return nil, fmt.Errorf("write review annotation: %w", err)
	}
	dir, name := root.Name(), filepath.FromSlash(rel)
	return func() {
		r, err := os.OpenRoot(dir)
		if err != nil {
			return
		}
		defer r.Close()
		if old == nil {
			_ = r.Remove(name)
			return
		}
		_ = reviewfile.WriteFile(r, rel, old)
	}, nil
}

// ListLabels returns the labels of the review annotations by object
// count.
func (s *Service) ListLabels(ctx context.Context, reviewID int64) ([]Label, error) {
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT l.key, sum(l.value::bigint)::bigint, count(*)
		FROM review_images ri, jsonb_each_text(ri.labels) AS l
		WHERE ri.review_id = $1
		GROUP BY l.key
		ORDER BY 2 DESC, 1`, reviewID)
	if err != nil {
		return nil, err
	}
	labels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Label, error) {
		var l Label
		err := row.Scan(&l.Name, &l.ObjectCount, &l.ImageCount)
		return l, err
	})
	if labels == nil {
		labels = []Label{}
	}
	return labels, err
}

// ListClasses returns the classes offered when editing boxes: the class
// list of the model's project when it has one, else the model's class
// names (without colors).
func (s *Service) ListClasses(ctx context.Context, reviewID int64) (ClassList, error) {
	var projectID *int64
	var ref ModelRef
	err := s.db.QueryRow(ctx, "SELECT project_id, model_ref FROM reviews WHERE id = $1", reviewID).Scan(&projectID, &ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClassList{}, errReviewNotFound
	}
	if err != nil {
		return ClassList{}, err
	}
	if projectID != nil && s.projects != nil {
		list, err := s.projects.ListClasses(ctx, *projectID)
		switch {
		case errors.Is(err, project.ErrNotFound):
			// Deleted meanwhile: fall back to the model's names.
		case err != nil:
			return ClassList{}, err
		case len(list.Classes) > 0:
			items := make([]Class, len(list.Classes))
			for i, c := range list.Classes {
				items[i] = Class{Name: c.Name, Color: c.Color}
			}
			return ClassList{Items: items, Total: len(items), Version: list.Version}, nil
		}
	}
	items := make([]Class, 0, len(ref.ClassNames))
	seen := map[string]bool{}
	for _, name := range ref.ClassNames {
		if name = cleanLabel(name, maxLabelRunes); name != "" && !seen[name] {
			seen[name] = true
			items = append(items, Class{Name: name})
		}
	}
	return ClassList{Items: items, Total: len(items), Version: ""}, nil
}

// imageSource returns the dataset image of a review image.
func (s *Service) imageSource(ctx context.Context, reviewID, imageID int64) (datasetID, datasetImageID int64, err error) {
	err = s.db.QueryRow(ctx, "SELECT dataset_id, image_id FROM review_images WHERE id = $1 AND review_id = $2",
		imageID, reviewID).Scan(&datasetID, &datasetImageID)
	if errors.Is(err, pgx.ErrNoRows) {
		if rerr := s.reviewExists(ctx, s.db, reviewID); rerr != nil {
			return 0, 0, rerr
		}
		return 0, 0, errImageNotFound
	}
	return datasetID, datasetImageID, err
}

// OpenImage opens the original file of a review image (in its dataset).
// Problems with the file itself are *dataset.Error values.
func (s *Service) OpenImage(ctx context.Context, reviewID, imageID int64) (*dataset.ImageFile, error) {
	datasetID, datasetImageID, err := s.imageSource(ctx, reviewID, imageID)
	if err != nil {
		return nil, err
	}
	return s.datasets.OpenImage(ctx, datasetID, datasetImageID)
}

// OpenThumbnail returns the dataset thumbnail of a review image (size sm
// or lg).
func (s *Service) OpenThumbnail(ctx context.Context, reviewID, imageID int64, size string) (*dataset.ImageFile, error) {
	datasetID, datasetImageID, err := s.imageSource(ctx, reviewID, imageID)
	if err != nil {
		return nil, err
	}
	return s.datasets.OpenThumbnail(ctx, datasetID, datasetImageID, size)
}
