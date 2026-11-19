package evaluation

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

const (
	maxNameRunes = 120
	maxDescRunes = 2000
)

// Deterministic fallback palette (matches frontend label-colors vibe).
var classPalette = []string{
	"#2563eb", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6",
	"#ec4899", "#06b6d4", "#84cc16", "#f97316", "#6366f1",
	"#14b8a6", "#e11d48", "#0ea5e9", "#a855f7", "#eab308",
}

func colorForClass(name string, projectColors map[string]string, fallbackIdx int) string {
	if c := projectColors[name]; c != "" {
		return c
	}
	h := sha256.Sum256([]byte(name))
	idx := (int(h[0]) + fallbackIdx*0) % len(classPalette)
	return classPalette[idx]
}

func normalizeName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", newError(ErrInvalid, "评估名称不能包含换行或控制字符")
		}
	}
	if utf8.RuneCountInString(s) > maxNameRunes {
		return "", newError(ErrInvalid, "评估名称不能超过 %d 个字符", maxNameRunes)
	}
	return s, nil
}

func normalizeDescription(raw string) (string, error) {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxDescRunes {
		return "", newError(ErrInvalid, "评估备注不能超过 %d 个字符", maxDescRunes)
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", newError(ErrInvalid, "评估备注不能包含控制字符")
		}
	}
	return s, nil
}

// NormalizeOptions validates and fills defaults for evaluation parameters.
func NormalizeOptions(in OptionsInput) (Options, error) {
	opt := Options{
		Conf:   DefaultConf,
		IoU:    DefaultIoU,
		NMSIoU: DefaultNMSIoU,
		Batch:  DefaultBatch,
		MaxDet: DefaultMaxDet,
		Half:   false,
		Device: "",
	}
	if in.Conf != nil {
		if math.IsNaN(*in.Conf) || math.IsInf(*in.Conf, 0) || *in.Conf < 0.01 || *in.Conf > 0.99 {
			return Options{}, newError(ErrInvalid, "默认置信度阈值（conf）必须在 0.01–0.99 之间")
		}
		opt.Conf = round4(*in.Conf)
	}
	if in.IoU != nil {
		if math.IsNaN(*in.IoU) || math.IsInf(*in.IoU, 0) || *in.IoU < 0.05 || *in.IoU > 0.99 {
			return Options{}, newError(ErrInvalid, "匹配 IoU 阈值（iou）必须在 0.05–0.99 之间")
		}
		opt.IoU = round4(*in.IoU)
	}
	if in.NMSIoU != nil {
		if math.IsNaN(*in.NMSIoU) || math.IsInf(*in.NMSIoU, 0) || *in.NMSIoU < 0.05 || *in.NMSIoU > 0.99 {
			return Options{}, newError(ErrInvalid, "NMS IoU 阈值（nms_iou）必须在 0.05–0.99 之间")
		}
		opt.NMSIoU = round4(*in.NMSIoU)
	}
	if len(in.ImgSize) > 0 {
		switch len(in.ImgSize) {
		case 1:
			v := in.ImgSize[0]
			if v < 32 || v > 4096 || v%32 != 0 {
				return Options{}, newError(ErrInvalid, "输入尺寸（imgsz）必须为 32–4096 且是 32 的倍数")
			}
			sz := [2]int{v, v}
			opt.ImgSize = &sz
		case 2:
			h, w := in.ImgSize[0], in.ImgSize[1]
			if h < 32 || h > 4096 || h%32 != 0 || w < 32 || w > 4096 || w%32 != 0 {
				return Options{}, newError(ErrInvalid, "输入尺寸（imgsz）的高和宽必须为 32–4096 且是 32 的倍数")
			}
			sz := [2]int{h, w}
			opt.ImgSize = &sz
		default:
			return Options{}, newError(ErrInvalid, "输入尺寸（imgsz）格式无效")
		}
	}
	if in.Batch != nil {
		if *in.Batch < 1 || *in.Batch > 256 {
			return Options{}, newError(ErrInvalid, "批大小（batch）必须在 1–256 之间")
		}
		opt.Batch = *in.Batch
	}
	if in.MaxDet != nil {
		if *in.MaxDet < 1 || *in.MaxDet > 1000 {
			return Options{}, newError(ErrInvalid, "最大检测框数（max_det）必须在 1–1000 之间")
		}
		opt.MaxDet = *in.MaxDet
	}
	if in.Half != nil {
		opt.Half = *in.Half
	}
	if in.Device != nil {
		d := strings.TrimSpace(*in.Device)
		if len(d) > 32 {
			return Options{}, newError(ErrInvalid, "计算设备（device）长度不能超过 32 个字符")
		}
		for _, r := range d {
			if unicode.IsControl(r) {
				return Options{}, newError(ErrInvalid, "计算设备（device）不能包含控制字符")
			}
		}
		opt.Device = d
	}
	opt.StoreMinConf = math.Min(DefaultStoreMinConf, opt.Conf)
	return opt, nil
}

// snapshotSample is an in-memory sample collected during preview or creation.
type snapshotSample struct {
	SampleKey          string
	Split              string
	DatasetID          *int64
	DatasetName        string
	DatasetImageID     *int64
	DataVersionImageID *int64
	Path               string
	Width              int
	Height             int
	GTObjects          []GTBox
}

type resolvedSource struct {
	SourceRef     SourceRef
	Fingerprint   string
	DataVersionID *int64
	GTLabelCounts map[string]int
	GTLabelImages map[string]int
	Samples       []snapshotSample // populated when loadSamples == true
	Warnings      []string
}

func normalizeSplits(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"val"}, nil
	}
	valid := []string{"train", "val", "test"}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(strings.ToLower(s))
		if !slices.Contains(valid, s) {
			return nil, newError(ErrInvalid, "无效的数据划分：%s（可选 train、val、test）", s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// Canonical order: val, test, train (deterministic fingerprint).
	order := map[string]int{"val": 0, "test": 1, "train": 2}
	slices.SortFunc(out, func(a, b string) int { return cmp.Compare(order[a], order[b]) })
	return out, nil
}

// resolveModels loads and validates the selected models.
func (s *Service) resolveModels(ctx context.Context, ids []int64, requireAtLeastOne bool) ([]ModelRef, error) {
	dedup := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, newError(ErrInvalid, "模型 ID 无效")
		}
		if !slices.Contains(dedup, id) {
			dedup = append(dedup, id)
		}
	}
	if requireAtLeastOne && len(dedup) == 0 {
		return nil, newError(ErrInvalid, "请至少选择一个待评估的模型")
	}
	if len(dedup) > MaxModelsPerBatch {
		return nil, newError(ErrInvalid, "单次最多批量评估 %d 个模型", MaxModelsPerBatch)
	}
	refs := make([]ModelRef, 0, len(dedup))
	for _, id := range dedup {
		m, err := s.models.GetModel(ctx, id)
		if err != nil {
			var me *model.Error
			if errors.As(err, &me) && errors.Is(me.Kind, model.ErrNotFound) {
				return nil, newError(ErrInvalid, "所选模型 #%d 不存在或已移入回收站", id)
			}
			return nil, err
		}
		if m.FileMissing {
			return nil, newError(ErrConflict, "模型 %s v%d 的权重文件缺失，无法评估", m.Project.Name, m.Version)
		}
		if !model.HasBoxes(m.Task) {
			return nil, newError(ErrInvalid, "模型 %s v%d 的任务类型为“%s”，当前仅支持带检测框的模型（检测/分割/姿态/旋转框）", m.Project.Name, m.Version, model.TaskLabel(m.Task))
		}
		names := slices.Clone(m.Info.ClassNames)
		if names == nil {
			names = []string{}
		}
		refs = append(refs, ModelRef{
			ID:          m.ID,
			ProjectID:   m.Project.ID,
			ProjectName: m.Project.Name,
			Version:     m.Version,
			YOLOVersion: m.YOLOVersion,
			Task:        m.Task,
			Scale:       m.Info.Scale,
			FileName:    m.FileName,
			SizeBytes:   m.SizeBytes,
			SHA256:      m.SHA256,
			ImageSize:   m.Info.ImageSize,
			ClassNames:  names,
		})
	}
	return refs, nil
}

// resolveSource validates and inspects either a DataVersion or Datasets source.
// When loadSamples is false (preview), VOC files on disk are not read.
func (s *Service) resolveSource(ctx context.Context, in SourceInput, loadSamples bool) (*resolvedSource, error) {
	switch in.Type {
	case SourceDataVersion:
		return s.resolveDataVersionSource(ctx, in, loadSamples)
	case SourceDatasets:
		return s.resolveDatasetsSource(ctx, in, loadSamples)
	default:
		return nil, newError(ErrInvalid, "请选择评估数据来源（数据版本或数据集）")
	}
}

func (s *Service) resolveDataVersionSource(ctx context.Context, in SourceInput, loadSamples bool) (*resolvedSource, error) {
	if in.DataVersionID <= 0 {
		return nil, newError(ErrInvalid, "请选择一个已就绪的数据版本")
	}
	splits, err := normalizeSplits(in.Splits)
	if err != nil {
		return nil, err
	}
	dv, err := s.dataVersions.Get(ctx, in.DataVersionID)
	if err != nil {
		var de *dataversion.Error
		if errors.As(err, &de) && errors.Is(de.Kind, dataversion.ErrNotFound) {
			return nil, newError(ErrInvalid, "所选数据版本不存在或已移入回收站")
		}
		return nil, err
	}
	if dv.Status != dataversion.StatusReady {
		return nil, newError(ErrConflict, "数据版本 v%d 尚未生成完成，无法用于评估", dv.Version)
	}

	rows, err := s.db.Query(ctx, `SELECT dvi.id, dvi.position, dvi.split, dvi.path, dvi.width, dvi.height,
			dvi.objects, dvi.object_count, dvi.labels, dvi.dataset_id, dvi.source_path,
			coalesce(d.name, ''), di.id
		FROM data_version_images dvi
		LEFT JOIN datasets d ON d.id = dvi.dataset_id AND d.deleted_at IS NULL
		LEFT JOIN dataset_images di ON di.dataset_id = dvi.dataset_id AND di.rel_path = dvi.source_path
		WHERE dvi.version_id = $1 AND dvi.split = ANY($2)
		ORDER BY dvi.position, dvi.id`, dv.ID, splits)
	if err != nil {
		return nil, fmt.Errorf("query data version images for evaluation: %w", err)
	}
	defer rows.Close()

	res := &resolvedSource{
		DataVersionID: &dv.ID,
		GTLabelCounts: map[string]int{},
		GTLabelImages: map[string]int{},
		Warnings:      []string{},
	}
	var totalImages, okImages, ngImages, totalObjects int
	dsNameByID := map[int64]string{}
	for _, src := range dv.Sources {
		dsNameByID[src.DatasetID] = src.DatasetName
	}

	for rows.Next() {
		var (
			dviID      int64
			pos        int
			split      string
			imgPath    string
			w, h       int
			objRaw     []byte
			objCount   int
			labelsMap  map[string]int
			datasetID  int64
			sourcePath string
			liveDSName string
			liveImgID  *int64
		)
		if err := rows.Scan(&dviID, &pos, &split, &imgPath, &w, &h, &objRaw, &objCount, &labelsMap,
			&datasetID, &sourcePath, &liveDSName, &liveImgID); err != nil {
			return nil, err
		}
		totalImages++
		if totalImages > MaxSamplesPerEval {
			return nil, newError(ErrInvalid, "单次评估样本数不能超过 %d 张", MaxSamplesPerEval)
		}
		if objCount == 0 {
			okImages++
		} else {
			ngImages++
			totalObjects += objCount
		}
		for lbl, cnt := range labelsMap {
			if cnt > 0 {
				res.GTLabelCounts[lbl] += cnt
				res.GTLabelImages[lbl]++
			}
		}
		if loadSamples {
			var objs []GTBox
			if len(objRaw) > 0 {
				_ = json.Unmarshal(objRaw, &objs)
			}
			if objs == nil {
				objs = []GTBox{}
			}
			dsName := liveDSName
			if dsName == "" {
				dsName = dsNameByID[datasetID]
			}
			dsIDCopy := datasetID
			dviIDCopy := dviID
			res.Samples = append(res.Samples, snapshotSample{
				SampleKey:          imgPath,
				Split:              split,
				DatasetID:          &dsIDCopy,
				DatasetName:        dsName,
				DatasetImageID:     liveImgID,
				DataVersionImageID: &dviIDCopy,
				Path:               imgPath,
				Width:              w,
				Height:             h,
				GTObjects:          objs,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totalImages == 0 {
		return nil, newError(ErrInvalid, "所选划分（%s）中没有可评估的图片", strings.Join(splits, " / "))
	}

	// Compute deterministic fingerprint for same-source multi-model comparison.
	fpRaw := fmt.Sprintf("dv:%d:%s", dv.ID, strings.Join(splits, ","))
	sum := sha256.Sum256([]byte(fpRaw))
	res.Fingerprint = "dv-" + hex.EncodeToString(sum[:8])

	var dvDirName string
	_ = s.db.QueryRow(ctx, "SELECT dir_name FROM data_versions WHERE id = $1", dv.ID).Scan(&dvDirName)

	res.SourceRef = SourceRef{
		Type: SourceDataVersion,
		DataVersion: &DataVersionSourceRef{
			ID:          dv.ID,
			ProjectID:   dv.ProjectID,
			ProjectName: dv.ProjectName,
			Version:     dv.Version,
			DirName:     dvDirName,
			Splits:      splits,
			ImageCount:  totalImages,
			OKCount:     okImages,
			NGCount:     ngImages,
			ObjectCount: totalObjects,
		},
		TotalImages:  totalImages,
		OKImages:     okImages,
		NGImages:     ngImages,
		TotalObjects: totalObjects,
	}
	if totalObjects == 0 {
		res.Warnings = append(res.Warnings, "所选样本全部为无缺陷的负样本（OK 图），将仅统计过杀率（OK→NG）与误检框，mAP 为 0")
	}
	return res, nil
}

func (s *Service) resolveDatasetsSource(ctx context.Context, in SourceInput, loadSamples bool) (*resolvedSource, error) {
	if len(in.Datasets) == 0 {
		return nil, newError(ErrInvalid, "请至少选择一个数据集")
	}
	if len(in.Datasets) > MaxDatasetsPerEval {
		return nil, newError(ErrInvalid, "单次评估最多选择 %d 个数据集", MaxDatasetsPerEval)
	}
	seenDS := map[int64]bool{}
	res := &resolvedSource{
		GTLabelCounts: map[string]int{},
		GTLabelImages: map[string]int{},
		Warnings:      []string{},
	}

	dsRefs := make([]DatasetSourceRef, 0, len(in.Datasets))
	var fpParts []string
	var totalImages, okImages, ngImages, totalObjects, skippedInvalid int

	for _, dsIn := range in.Datasets {
		if dsIn.DatasetID <= 0 {
			return nil, newError(ErrInvalid, "数据集 ID 无效")
		}
		if seenDS[dsIn.DatasetID] {
			return nil, newError(ErrInvalid, "不能重复选择同一个数据集")
		}
		seenDS[dsIn.DatasetID] = true

		includeBase := true
		if dsIn.IncludeBase != nil {
			includeBase = *dsIn.IncludeBase
		}
		mode := dsIn.ImageMode
		if mode == "" {
			mode = ImageModeAnnotated
		}
		if mode != ImageModeAnnotated && mode != ImageModeBackground && mode != ImageModeAll {
			return nil, newError(ErrInvalid, "样本筛选模式无效：%s", mode)
		}
		includeEmpty := true
		if dsIn.IncludeEmpty != nil {
			includeEmpty = *dsIn.IncludeEmpty
		}
		// Background and all modes need base images when no review version is selected.
		if !includeBase && len(dsIn.VersionIDs) == 0 {
			return nil, newError(ErrInvalid, "每个数据集请至少勾选基础标注或一个审图版本")
		}

		sel := dataset.VersionSelection{
			IncludeBase: includeBase,
			VersionIDs:  dsIn.VersionIDs,
		}
		snap, err := s.datasets.OpenSnapshot(ctx, dsIn.DatasetID, sel, dataset.SnapshotOptions{
			IndexOnly: !loadSamples,
		})
		if err != nil {
			var de *dataset.Error
			if errors.As(err, &de) {
				switch de.Kind {
				case dataset.ErrNotFound:
					return nil, newError(ErrInvalid, "%s", de.Message)
				case dataset.ErrInvalid:
					return nil, newError(ErrInvalid, "%s", de.Message)
				default:
					return nil, newError(ErrConflict, "%s", de.Message)
				}
			}
			return nil, err
		}

		err = func() error {
			defer snap.Close()
			verRefs := make([]VersionRef, len(snap.Versions))
			verIDStrs := make([]string, len(snap.Versions))
			for i, v := range snap.Versions {
				verRefs[i] = VersionRef{ID: v.ID, Name: v.Name}
				verIDStrs[i] = strconv.FormatInt(v.ID, 10)
			}
			fpParts = append(fpParts, fmt.Sprintf("%d:%t:%s:%s:%t",
				snap.Dataset.ID, includeBase, strings.Join(verIDStrs, ","), mode, includeEmpty))

			var dsImgCount, dsOK, dsNG, dsObjs int
			for _, img := range snap.Images {
				if !dataset.IsTrainableImage(img.RelPath) {
					continue
				}
				// Determine annotation state & labels/objects.
				var (
					hasReview = img.VersionID != nil
					labels    map[string]int
					objCount  int
					isInvalid bool
					isNone    bool
					isEmpty   bool
				)
				if hasReview {
					labels = img.VersionLabels
					for _, n := range labels {
						objCount += n
					}
					isEmpty = objCount == 0
				} else {
					switch img.AnnotationState {
					case "annotated":
						labels = img.BaseLabels
						for _, n := range labels {
							objCount += n
						}
						if objCount == 0 {
							isEmpty = true
						}
					case "empty":
						isEmpty = true
					case "invalid":
						isInvalid = true
					default:
						isNone = true
					}
				}

				if isInvalid {
					skippedInvalid++
					continue
				}
				switch mode {
				case ImageModeAnnotated:
					if isNone || (isEmpty && !includeEmpty) {
						continue
					}
				case ImageModeBackground:
					if objCount > 0 {
						continue
					}
				case ImageModeAll:
					// include both annotated and background/unannotated
				}

				var gtObjs []GTBox
				w, h := 0, 0
				if img.Width != nil {
					w = *img.Width
				}
				if img.Height != nil {
					h = *img.Height
				}
				if loadSamples {
					if hasReview {
						gtObjs = slices.Clone(img.VersionObjects)
						if w == 0 && img.VersionWidth != nil {
							w = *img.VersionWidth
						}
						if h == 0 && img.VersionHeight != nil {
							h = *img.VersionHeight
						}
					} else if img.AnnotationPath != nil && objCount > 0 && snap.Root != nil {
						f, oerr := snap.Root.Open(*img.AnnotationPath)
						if oerr != nil {
							skippedInvalid++
							continue
						}
						raw, rerr := io.ReadAll(io.LimitReader(f, dataset.MaxAnnotationBytes+1))
						_ = f.Close()
						if rerr != nil || int64(len(raw)) > dataset.MaxAnnotationBytes {
							skippedInvalid++
							continue
						}
						voc, verr := dataset.DecodeVOC(raw)
						if verr != nil {
							skippedInvalid++
							continue
						}
						gtObjs = voc.Objects
						if w == 0 && voc.Width > 0 {
							w = voc.Width
						}
						if h == 0 && voc.Height > 0 {
							h = voc.Height
						}
					}
					if gtObjs == nil {
						gtObjs = []GTBox{}
					}
					// Recount accurately from actual decoded VOC objects.
					objCount = len(gtObjs)
					labels = map[string]int{}
					for _, g := range gtObjs {
						labels[g.Name]++
					}
					if mode == ImageModeAnnotated && objCount == 0 && !includeEmpty {
						continue
					}
					if mode == ImageModeBackground && objCount > 0 {
						continue
					}
				}

				totalImages++
				if totalImages > MaxSamplesPerEval {
					return newError(ErrInvalid, "单次评估样本数不能超过 %d 张", MaxSamplesPerEval)
				}
				dsImgCount++
				if objCount == 0 {
					okImages++
					dsOK++
				} else {
					ngImages++
					dsNG++
					totalObjects += objCount
					dsObjs += objCount
				}
				for lbl, cnt := range labels {
					if cnt > 0 {
						res.GTLabelCounts[lbl] += cnt
						res.GTLabelImages[lbl]++
					}
				}
				if loadSamples {
					dsID := snap.Dataset.ID
					imgID := img.ID
					dispPath := img.RelPath
					if len(in.Datasets) > 1 {
						dispPath = path.Join(snap.Dataset.Name, img.RelPath)
					}
					res.Samples = append(res.Samples, snapshotSample{
						SampleKey:      fmt.Sprintf("ds-%d:%s", snap.Dataset.ID, img.RelPath),
						DatasetID:      &dsID,
						DatasetName:    snap.Dataset.Name,
						DatasetImageID: &imgID,
						Path:           dispPath,
						Width:          w,
						Height:         h,
						GTObjects:      gtObjs,
					})
				}
			}
			dsRefs = append(dsRefs, DatasetSourceRef{
				DatasetID:    snap.Dataset.ID,
				DatasetName:  snap.Dataset.Name,
				DirName:      snap.Dataset.DirName,
				IncludeBase:  includeBase,
				Versions:     verRefs,
				ImageMode:    mode,
				IncludeEmpty: includeEmpty,
				ImageCount:   dsImgCount,
				OKCount:      dsOK,
				NGCount:      dsNG,
				ObjectCount:  dsObjs,
			})
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}

	if totalImages == 0 {
		return nil, newError(ErrInvalid, "所选数据集与样本筛选条件下没有可评估的图片（若测试纯良品图集，请选择“仅无标注背景图”或“整个数据集”）")
	}

	// Deterministic fingerprint: sort per-dataset descriptors + sample keys hash if loaded.
	slices.Sort(fpParts)
	sum := sha256.Sum256([]byte("ds|" + strings.Join(fpParts, "|") + "|" + strconv.Itoa(totalImages)))
	res.Fingerprint = "ds-" + hex.EncodeToString(sum[:8])

	res.SourceRef = SourceRef{
		Type:         SourceDatasets,
		Datasets:     dsRefs,
		TotalImages:  totalImages,
		OKImages:     okImages,
		NGImages:     ngImages,
		TotalObjects: totalObjects,
	}
	if skippedInvalid > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("已自动跳过 %d 张标注文件损坏或无法解析的图片", skippedInvalid))
	}
	if totalObjects == 0 {
		res.Warnings = append(res.Warnings, "所选样本全部为无缺陷的负样本（OK 图），将仅统计过杀率（OK→NG）与误检框，mAP 为 0")
	}
	return res, nil
}

// buildAlignedClasses creates the union class list matching model classes and GT classes by name.
func buildAlignedClasses(m ModelRef, gtCounts, gtImages map[string]int, projectColors map[string]string) ([]ClassInfo, int, int, int) {
	seen := map[string]bool{}
	classes := make([]ClassInfo, 0, len(m.ClassNames)+len(gtCounts))
	matched, modelOnly, gtOnly := 0, 0, 0

	// Model classes first (preserving the model's natural order).
	for idx, name := range m.ClassNames {
		if seen[name] {
			continue
		}
		seen[name] = true
		idxCopy := idx
		gtCnt := gtCounts[name]
		inGT := gtCnt > 0
		if inGT {
			matched++
		} else {
			modelOnly++
		}
		classes = append(classes, ClassInfo{
			Name:     name,
			Color:    colorForClass(name, projectColors, len(classes)),
			InModel:  true,
			InGT:     inGT,
			ModelIdx: &idxCopy,
			GTCount:  gtCnt,
			GTImages: gtImages[name],
		})
	}

	// Remaining GT-only classes sorted by GT count DESC, then name.
	gtExtra := make([]string, 0)
	for name, cnt := range gtCounts {
		if cnt > 0 && !seen[name] {
			gtExtra = append(gtExtra, name)
		}
	}
	slices.SortFunc(gtExtra, func(a, b string) int {
		return cmp.Or(cmp.Compare(gtCounts[b], gtCounts[a]), strings.Compare(a, b))
	})
	for _, name := range gtExtra {
		gtOnly++
		classes = append(classes, ClassInfo{
			Name:     name,
			Color:    colorForClass(name, projectColors, len(classes)),
			InModel:  false,
			InGT:     true,
			GTCount:  gtCounts[name],
			GTImages: gtImages[name],
		})
	}
	return classes, matched, modelOnly, gtOnly
}

func (s *Service) projectClassColors(ctx context.Context, projectID int64) map[string]string {
	out := map[string]string{}
	if s.projects == nil || projectID <= 0 {
		return out
	}
	list, err := s.projects.ListClasses(ctx, projectID)
	if err != nil {
		return out
	}
	for _, c := range list.Classes {
		if c.Color != "" {
			out[c.Name] = c.Color
		}
	}
	return out
}

// Preview validates selected models and data source, returning sample counts
// and per-model class alignment diagnostics without creating any DB rows.
func (s *Service) Preview(ctx context.Context, in PreviewInput) (PreviewResult, error) {
	models, err := s.resolveModels(ctx, in.ModelIDs, false)
	if err != nil {
		return PreviewResult{}, err
	}
	src, err := s.resolveSource(ctx, in.Source, false)
	if err != nil {
		return PreviewResult{}, err
	}

	gtLabels := make([]PreviewLabelStat, 0, len(src.GTLabelCounts))
	for name, cnt := range src.GTLabelCounts {
		if cnt > 0 {
			gtLabels = append(gtLabels, PreviewLabelStat{
				Name:        name,
				ObjectCount: cnt,
				ImageCount:  src.GTLabelImages[name],
			})
		}
	}
	slices.SortFunc(gtLabels, func(a, b PreviewLabelStat) int {
		return cmp.Or(cmp.Compare(b.ObjectCount, a.ObjectCount), strings.Compare(a.Name, b.Name))
	})

	colorCache := map[int64]map[string]string{}
	modelPreviews := make([]ModelAlignmentPreview, 0, len(models))
	warnings := slices.Clone(src.Warnings)
	for _, m := range models {
		colors, ok := colorCache[m.ProjectID]
		if !ok {
			colors = s.projectClassColors(ctx, m.ProjectID)
			colorCache[m.ProjectID] = colors
		}
		classes, matched, modelOnly, gtOnly := buildAlignedClasses(m, src.GTLabelCounts, src.GTLabelImages, colors)
		modelPreviews = append(modelPreviews, ModelAlignmentPreview{
			Model:          m,
			Classes:        classes,
			MatchedCount:   matched,
			ModelOnlyCount: modelOnly,
			GTOnlyCount:    gtOnly,
		})
		if len(gtLabels) > 0 && matched == 0 {
			warnings = append(warnings, fmt.Sprintf("模型 %s v%d 与所选评估数据的类别名称完全没有交集，评估结果将全部计为漏检与误检", m.ProjectName, m.Version))
		}
	}

	return PreviewResult{
		Source:   src.SourceRef,
		GTLabels: gtLabels,
		Models:   modelPreviews,
		Warnings: warnings,
	}, nil
}

func defaultEvalName(m ModelRef, src SourceRef) string {
	var srcPart string
	if src.Type == SourceDataVersion && src.DataVersion != nil {
		srcPart = fmt.Sprintf("数据版本 v%d (%s)", src.DataVersion.Version, strings.Join(src.DataVersion.Splits, "+"))
	} else if len(src.Datasets) == 1 {
		srcPart = src.Datasets[0].DatasetName
	} else {
		srcPart = fmt.Sprintf("%d 个数据集", len(src.Datasets))
	}
	name := fmt.Sprintf("%s v%d · %s", m.ProjectName, m.Version, srcPart)
	runes := []rune(name)
	if len(runes) > maxNameRunes {
		return string(runes[:maxNameRunes])
	}
	return name
}

// Helper to ensure unused pgx import never causes issues if removed later.
var _ = pgx.ErrNoRows
