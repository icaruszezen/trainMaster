package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// Bounds of a new review.
const (
	MaxDatasets = 50
	MaxImages   = 200_000

	defaultConf   = 0.25
	defaultIoU    = 0.7
	defaultMaxDet = 300
)

// OptionsInput are the inference options of a new review; nil fields take
// the defaults.
type OptionsInput struct {
	Conf    *float64
	IoU     *float64
	ImgSize []int // nil: the model's training size; one value: square; two: [height, width]
	MaxDet  *int
}

// CreateInput describes a new review.
type CreateInput struct {
	Name        string // "": "<project> v<version> 审图"
	Description string
	ModelID     int64
	DatasetIDs  []int64 // in the order the user chose them
	Options     OptionsInput
}

// normalizeOptions validates the options for m and fills in the defaults.
func normalizeOptions(m model.Model, in OptionsInput) (InferenceOptions, error) {
	o := InferenceOptions{Conf: defaultConf, IoU: defaultIoU, MaxDet: defaultMaxDet}
	if in.Conf != nil {
		if !(*in.Conf >= 0.01 && *in.Conf <= 1) {
			return o, &Error{Kind: ErrInvalid, Message: "置信度阈值 conf 必须在 0.01–1 之间"}
		}
		o.Conf = roundTo(*in.Conf, 4)
	}
	if in.IoU != nil {
		if !(*in.IoU >= 0.1 && *in.IoU <= 0.95) {
			return o, &Error{Kind: ErrInvalid, Message: "IoU 阈值 iou 必须在 0.1–0.95 之间"}
		}
		o.IoU = roundTo(*in.IoU, 4)
	}
	if in.MaxDet != nil {
		if *in.MaxDet < 1 || *in.MaxDet > 1000 {
			return o, &Error{Kind: ErrInvalid, Message: "每张图片最多检测框数 max_det 必须在 1–1000 之间"}
		}
		o.MaxDet = *in.MaxDet
	}
	// The input size follows the rules of ONNX exports.
	exp, err := model.NormalizeExport(m, model.ExportRequest{Format: model.FormatONNX, ImgSize: in.ImgSize})
	if err != nil {
		var me *model.Error
		if errors.As(err, &me) {
			return o, &Error{Kind: ErrInvalid, Message: me.Message}
		}
		return o, err
	}
	o.ImgSize = exp.ImgSize
	return o, nil
}

func roundTo(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(v*p) / p
}

// normalizeDatasetIDs checks the dataset IDs of a new review and drops
// repeats, keeping the order.
func normalizeDatasetIDs(ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, &Error{Kind: ErrInvalid, Message: "数据集 ID 无效"}
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	switch {
	case len(out) == 0:
		return nil, &Error{Kind: ErrInvalid, Message: "请至少选择一个数据集"}
	case len(out) > MaxDatasets:
		return nil, newError(ErrInvalid, "一个审图任务最多选择 %d 个数据集", MaxDatasets)
	}
	return out, nil
}

// reviewModel loads the model of a new review (or of a retry) and checks
// that it can be used.
func (s *Service) reviewModel(ctx context.Context, id int64, retry bool) (model.Model, error) {
	m, err := s.models.GetModel(ctx, id)
	if errors.Is(err, model.ErrNotFound) {
		if retry {
			return m, &Error{Kind: ErrConflict, Message: "模型已被删除，无法重新识别"}
		}
		return m, &Error{Kind: ErrInvalid, Message: "所选模型不存在或已被删除，请刷新后重试"}
	}
	if err != nil {
		return m, err
	}
	switch {
	case m.FileMissing && retry:
		return m, &Error{Kind: ErrConflict, Message: "模型文件不存在，无法重新识别，请重新上传该模型"}
	case m.FileMissing:
		return m, &Error{Kind: ErrConflict, Message: "模型文件不存在，不能用于审图，请重新上传该模型"}
	case !model.HasBoxes(m.Task):
		return m, newError(ErrInvalid, "%s模型没有检测框，不能用于审图", model.TaskLabel(m.Task))
	}
	return m, nil
}

func modelRef(m model.Model) ModelRef {
	names := m.Info.ClassNames
	if names == nil {
		names = []string{}
	}
	return ModelRef{ID: m.ID, ProjectID: m.Project.ID, ProjectName: m.Project.Name, Version: m.Version,
		YOLOVersion: m.YOLOVersion, Task: m.Task, FileName: m.FileName, ClassNames: names}
}

// datasetRow is a dataset chosen for a new review.
type datasetRow struct {
	id      int64
	name    string
	dirName string
	folder  string
	images  []sourceImage
}

// sourceImage is a dataset image when a review is created.
type sourceImage struct {
	id            int64
	relPath       string
	size          int64
	modTime       time.Time
	width, height *int
}

// maxImages is MaxImages; tests lower it.
var maxImages = MaxImages

// createAttempts bounds the retries of a creation whose images changed
// meanwhile (a scan removed one between reading and inserting it).
const createAttempts = 3

// CreateReview creates a review: it snapshots the images of the datasets
// (datasets in the given order, each in image order), assigns their paths
// in the review folder, writes the folder with its review.json and queues
// the inference.
func (s *Service) CreateReview(ctx context.Context, in CreateInput) (Review, error) {
	name := strings.TrimSpace(in.Name)
	if name != "" {
		var err error
		if name, err = normalizeName(name); err != nil {
			return Review{}, err
		}
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return Review{}, err
	}
	datasetIDs, err := normalizeDatasetIDs(in.DatasetIDs)
	if err != nil {
		return Review{}, err
	}
	if in.ModelID <= 0 {
		return Review{}, &Error{Kind: ErrInvalid, Message: "请选择模型"}
	}
	m, err := s.reviewModel(ctx, in.ModelID, false)
	if err != nil {
		return Review{}, err
	}
	options, err := normalizeOptions(m, in.Options)
	if err != nil {
		return Review{}, err
	}
	if name == "" {
		name = defaultName(m.Project.Name, m.Version)
	}

	ctx, cancel := s.detach(ctx) // the folder is written inside the transaction
	defer cancel()
	var id int64
	for attempt := 1; ; attempt++ {
		id, err = s.insertReview(ctx, name, desc, m, options, datasetIDs)
		var pe *pgconn.PgError
		if err != nil && errors.As(err, &pe) && pe.Code == "23503" && attempt < createAttempts {
			s.log.Info("dataset images changed while creating a review; trying again", "attempt", attempt)
			continue
		}
		break
	}
	if err != nil {
		return Review{}, err
	}
	s.models.NotifyTasks()
	r, err := s.GetReview(ctx, id)
	if err == nil {
		s.log.Info("review created", "review_id", id, "model_id", m.ID, "datasets", len(datasetIDs),
			"images", r.Counts.Total)
	}
	return r, err
}

// insertReview creates the rows and the folder of a review in one
// transaction and returns its ID.
func (s *Service) insertReview(ctx context.Context, name, desc string, m model.Model, options InferenceOptions,
	datasetIDs []int64) (int64, error) {
	var id int64
	var dir string // the review folder, once created
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		datasets, err := lockDatasets(ctx, tx, datasetIDs)
		if err != nil {
			return err
		}
		// The model must still exist (a delete waits for this transaction).
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM models WHERE id = $1 FOR KEY SHARE)", m.ID).
			Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return &Error{Kind: ErrInvalid, Message: "所选模型不存在或已被删除，请刷新后重试"}
		}
		if err := loadImages(ctx, tx, datasets); err != nil {
			return err
		}

		// Folders, then the paths of every image in review order.
		used := map[string]bool{}
		var layout []reviewfile.LayoutImage
		var total int
		for _, d := range datasets {
			d.folder = reviewfile.Folder(d.dirName, d.id, func(key string) bool { return used[key] })
			used[reviewfile.FoldKey(d.folder)] = true
			for _, img := range d.images {
				layout = append(layout, reviewfile.LayoutImage{Folder: d.folder, RelPath: img.relPath})
			}
			total += len(d.images)
		}
		placements := reviewfile.Layout(layout)

		ref := modelRef(m)
		if err := tx.QueryRow(ctx, `INSERT INTO reviews (name, description, model_id, project_id, model_ref, options, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'queued') RETURNING id`,
			name, desc, m.ID, m.Project.ID, ref, options).Scan(&id); err != nil {
			return err
		}
		dsRows := make([][]any, len(datasets))
		for i, d := range datasets {
			dsRows[i] = []any{id, d.id, i, d.folder, d.name}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"review_datasets"},
			[]string{"review_id", "dataset_id", "position", "folder", "dataset_name"}, pgx.CopyFromRows(dsRows)); err != nil {
			return fmt.Errorf("insert review datasets: %w", err)
		}
		pos := 0
		imgRows := make([][]any, 0, total)
		for _, d := range datasets {
			for _, img := range d.images {
				p := placements[pos]
				imgRows = append(imgRows, []any{id, d.id, img.id, pos, img.relPath, path.Base(img.relPath), p.Image,
					p.Annotation, dataset.ImageVersion(img.size, img.modTime), img.width, img.height})
				pos++
			}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"review_images"},
			[]string{"review_id", "dataset_id", "image_id", "position", "rel_path", "file_name", "package_path",
				"annotation_path", "image_version", "width", "height"}, pgx.CopyFromRows(imgRows)); err != nil {
			return fmt.Errorf("insert review images: %w", err)
		}

		dir = s.reviewDir(id)
		s.busy.add(dir)
		return s.createReviewFolder(dir, taskDescription{
			ReviewID: id, Name: name, Description: desc, Model: ref, Options: options,
			Datasets: manifestDatasets(datasets), ImageCount: total, CreatedAt: s.clock().UTC(),
		})
	})
	if dir != "" {
		defer s.busy.remove(dir)
		if err != nil {
			if rerr := os.RemoveAll(dir); rerr != nil {
				s.log.Warn("remove review folder after failed creation", "path", dir, "err", rerr)
			}
		}
	}
	return id, err
}

// lockDatasets locks the datasets of a new review (so none is deleted
// before the review commits) and returns them in the given order.
func lockDatasets(ctx context.Context, tx pgx.Tx, ids []int64) ([]*datasetRow, error) {
	rows, err := tx.Query(ctx, "SELECT id, name, dir_name FROM datasets WHERE id = ANY($1) FOR KEY SHARE", ids)
	if err != nil {
		return nil, err
	}
	found := map[int64]*datasetRow{}
	var d datasetRow
	if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.name, &d.dirName}, func() error {
		c := d
		found[d.id] = &c
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]*datasetRow, len(ids))
	for i, id := range ids {
		if out[i] = found[id]; out[i] == nil {
			return nil, newError(ErrInvalid, "所选数据集 #%d 不存在或已被删除，请刷新后重试", id)
		}
	}
	return out, nil
}

// loadImages reads the images of the datasets in image order, after
// checking their number.
func loadImages(ctx context.Context, tx pgx.Tx, datasets []*datasetRow) error {
	ids := make([]int64, len(datasets))
	for i, d := range datasets {
		ids[i] = d.id
	}
	var total int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM dataset_images WHERE dataset_id = ANY($1)", ids).Scan(&total); err != nil {
		return err
	}
	switch {
	case total == 0:
		return &Error{Kind: ErrInvalid, Message: "所选数据集中没有图片"}
	case total > maxImages:
		return newError(ErrInvalid, "所选数据集共有 %d 张图片，一个审图任务最多 %d 张，请少选一些数据集", total, maxImages)
	}
	for _, d := range datasets {
		rows, err := tx.Query(ctx, `SELECT id, rel_path, size_bytes, mod_time, width, height
			FROM dataset_images WHERE dataset_id = $1 ORDER BY `+dataset.ImageOrder, d.id)
		if err != nil {
			return err
		}
		var img sourceImage
		if _, err := pgx.ForEachRow(rows, []any{&img.id, &img.relPath, &img.size, &img.modTime, &img.width, &img.height},
			func() error {
				d.images = append(d.images, img)
				return nil
			}); err != nil {
			return err
		}
	}
	return nil
}

func manifestDatasets(datasets []*datasetRow) []reviewfile.ManifestDataset {
	out := make([]reviewfile.ManifestDataset, len(datasets))
	for i, d := range datasets {
		out[i] = reviewfile.ManifestDataset{ID: d.id, Name: d.name, Folder: d.folder}
	}
	return out
}

// taskDescription is the review.json of a review folder: what the review
// is, so that a copy or backup of reviews/ explains itself without the
// database (like model.json). It is written once, at creation.
type taskDescription struct {
	ReviewID    int64                        `json:"review_id"`
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Model       ModelRef                     `json:"model"`
	Options     InferenceOptions             `json:"options"`
	Datasets    []reviewfile.ManifestDataset `json:"datasets"`
	ImageCount  int                          `json:"image_count"`
	CreatedAt   time.Time                    `json:"created_at"`
}

// createReviewFolder creates the folder of a new review with its
// review.json. A folder left at that path (by a database that was reset)
// is moved aside for the janitor first.
func (s *Service) createReviewFolder(dir string, td taskDescription) error {
	if _, err := os.Lstat(dir); err == nil {
		aside := filepath.Join(s.reviewsDir(), fmt.Sprintf("%sstale-%d-%d", deletingPrefix, td.ReviewID, s.clock().UnixNano()))
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("move old review folder aside: %w", err)
		}
		s.log.Warn("moved an old folder with the name of a new review aside", "path", dir, "to", aside)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check review folder: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create review folder: %w", err)
	}
	data, err := json.MarshalIndent(td, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", reviewfile.ManifestFile, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open review folder: %w", err)
	}
	defer root.Close()
	if err := reviewfile.WriteFile(root, reviewfile.ManifestFile, append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", reviewfile.ManifestFile, err)
	}
	return nil
}
