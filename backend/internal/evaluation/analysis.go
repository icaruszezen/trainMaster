package evaluation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
)

// loadEvaluationData returns the immutable GT samples, curves, and stored
// predictions for a finished evaluation, using the in-memory LRU cache.
func (s *Service) loadEvaluationData(ctx context.Context, ev Evaluation) (*loadedEval, error) {
	if cached, ok := s.cache.get(ev.ID); ok {
		return cached, nil
	}
	loaded, err := s.loadEvaluationDataFromDisk(ctx, ev)
	if err != nil {
		return nil, err
	}
	s.cache.put(ev.ID, loaded)
	return loaded, nil
}

func (s *Service) loadEvaluationDataFromDisk(ctx context.Context, ev Evaluation) (*loadedEval, error) {
	var curves CurvesData
	if err := readJSONFile(s.curvesPath(ev.ID), &curves); err != nil && !isNotExist(err) {
		return nil, fmt.Errorf("read evaluation curves: %w", err)
	}
	if curves.PR == nil {
		curves.PR = []CurveSeries{}
	}
	if curves.F1 == nil {
		curves.F1 = []CurveSeries{}
	}
	if curves.Precision == nil {
		curves.Precision = []CurveSeries{}
	}
	if curves.Recall == nil {
		curves.Recall = []CurveSeries{}
	}

	var predFile StoredPredictionsFile
	if err := readGzipJSONFile(s.predictionsPath(ev.ID), &predFile); err != nil && !isNotExist(err) {
		return nil, fmt.Errorf("read evaluation predictions: %w", err)
	}
	predsByID := make(map[int64]StoredImagePrediction, len(predFile.Items))
	for _, it := range predFile.Items {
		predsByID[it.SampleID] = it
	}

	rows, err := s.db.Query(ctx, `SELECT id, position, sample_key, split,
			dataset_id, dataset_name, dataset_image_id, data_version_image_id,
			path, width, height, gt_objects, inference_error
		FROM evaluation_images
		WHERE evaluation_id = $1
		ORDER BY position, id`, ev.ID)
	if err != nil {
		return nil, fmt.Errorf("load evaluation images: %w", err)
	}
	defer rows.Close()

	samples := []*evalSampleData{}
	byID := map[int64]*evalSampleData{}
	byKey := map[string]*evalSampleData{}

	for rows.Next() {
		var (
			smp   evalSampleData
			gtRaw []byte
		)
		if err := rows.Scan(
			&smp.ID, &smp.Position, &smp.SampleKey, &smp.Split,
			&smp.DatasetID, &smp.DatasetName, &smp.DatasetImageID, &smp.DataVersionImageID,
			&smp.Path, &smp.Width, &smp.Height, &gtRaw, &smp.InferenceError,
		); err != nil {
			return nil, err
		}
		smp.FileName = path.Base(smp.Path)
		if len(gtRaw) > 0 {
			_ = json.Unmarshal(gtRaw, &smp.GTObjects)
		}
		if smp.GTObjects == nil {
			smp.GTObjects = []GTBox{}
		}
		if sp, ok := predsByID[smp.ID]; ok {
			smp.Predictions = sp.Predictions
			if smp.Width == 0 && sp.Width > 0 {
				smp.Width = sp.Width
			}
			if smp.Height == 0 && sp.Height > 0 {
				smp.Height = sp.Height
			}
			if smp.InferenceError == "" && sp.InferenceError != "" {
				smp.InferenceError = sp.InferenceError
			}
		}
		if smp.Predictions == nil {
			smp.Predictions = []PredBox{}
		}
		ptr := &smp
		samples = append(samples, ptr)
		byID[ptr.ID] = ptr
		byKey[ptr.SampleKey] = ptr
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	storeMin := ev.Options.StoreMinConf
	if predFile.StoreMinConf > 0 {
		storeMin = predFile.StoreMinConf
	}
	if storeMin <= 0 {
		storeMin = DefaultStoreMinConf
	}

	return &loadedEval{
		evalID:       ev.ID,
		classes:      slices.Clone(ev.Classes),
		storeMinConf: storeMin,
		curves:       curves,
		samples:      samples,
		sampleByID:   byID,
		sampleByKey:  byKey,
	}, nil
}

func resolveThresholds(ev Evaluation, storeMin float64, confPtr, iouPtr *float64) (float64, float64, error) {
	conf := ev.Options.Conf
	if conf <= 0 {
		conf = DefaultConf
	}
	if confPtr != nil {
		c := *confPtr
		if math.IsNaN(c) || math.IsInf(c, 0) {
			return 0, 0, newError(ErrBadRequest, "置信度阈值（conf）无效")
		}
		if c < storeMin-1e-6 || c > 0.99+1e-6 {
			return 0, 0, newError(ErrInvalid, "置信度阈值（conf）必须在 %.2f–0.99 之间", storeMin)
		}
		conf = math.Max(storeMin, math.Min(0.99, c))
	}

	iou := ev.Options.IoU
	if iou <= 0 {
		iou = DefaultIoU
	}
	if iouPtr != nil {
		v := *iouPtr
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, newError(ErrBadRequest, "匹配 IoU 阈值（iou）无效")
		}
		if v < 0.05-1e-6 || v > 0.99+1e-6 {
			return 0, 0, newError(ErrInvalid, "匹配 IoU 阈值（iou）必须在 0.05–0.99 之间")
		}
		iou = math.Max(0.05, math.Min(0.99, v))
	}
	return round4(conf), round4(iou), nil
}

// Analyze computes real-time metrics, per-class table, confusion matrix, OK/NG
// matrix, and curves at any (conf, iou) operating point without re-running GPU.
func (s *Service) Analyze(ctx context.Context, id int64, confPtr, iouPtr *float64) (AnalysisResult, error) {
	ev, err := s.Get(ctx, id)
	if err != nil {
		return AnalysisResult{}, err
	}
	if ev.Status != StatusSucceeded {
		return AnalysisResult{}, errNotSucceeded
	}
	loaded, err := s.loadEvaluationData(ctx, ev)
	if err != nil {
		return AnalysisResult{}, err
	}
	conf, iou, err := resolveThresholds(ev, loaded.storeMinConf, confPtr, iouPtr)
	if err != nil {
		return AnalysisResult{}, err
	}
	run := ComputeAnalysis(ev.ID, loaded.classes, loaded.samples, loaded.curves, loaded.storeMinConf, conf, iou)
	return run.Analysis, nil
}

func buildSampleItem(evalID int64, cs computedSample) SampleItem {
	d := cs.Data
	item := SampleItem{
		ID:                 d.ID,
		Position:           d.Position,
		SampleKey:          d.SampleKey,
		Split:              d.Split,
		DatasetID:          d.DatasetID,
		DatasetName:        d.DatasetName,
		DatasetImageID:     d.DatasetImageID,
		DataVersionImageID: d.DataVersionImageID,
		Path:               d.Path,
		FileName:           d.FileName,
		Width:              d.Width,
		Height:             d.Height,
		GTCount:            len(d.GTObjects),
		PredCount:          cs.PredCount,
		TPCount:            cs.TPCount,
		FPCount:            cs.FPCount,
		FNCount:            cs.FNCount,
		MisclassifiedCount: cs.MisclassifiedCount,
		ImageOutcome:       cs.ImageOutcome,
		MaxFPConf:          round4(cs.MaxFPConf),
		InferenceError:     d.InferenceError,
		ThumbnailURL:       fmt.Sprintf("/api/v1/evaluations/%d/images/%d/thumbnail", evalID, d.ID),
		FileURL:            fmt.Sprintf("/api/v1/evaluations/%d/images/%d/file", evalID, d.ID),
	}
	if d.DatasetID != nil && d.DatasetImageID != nil {
		item.AnnotateURL = fmt.Sprintf("/annotation/workspace/%d?image=%d", *d.DatasetID, *d.DatasetImageID)
	}
	return item
}

func buildSampleDetail(evalID int64, cs computedSample, sourceMissing bool) SampleDetail {
	return SampleDetail{
		SampleItem:    buildSampleItem(evalID, cs),
		GTObjects:     cs.MatchedGT,
		Predictions:   cs.MatchedPreds,
		SourceMissing: sourceMissing,
	}
}

func filterAndSortSamples(evalID int64, run evaluatedRun, f SampleFilter) ([]SampleItem, error) {
	errType := strings.TrimSpace(strings.ToLower(f.ErrorType))
	if errType == "" {
		errType = "all"
	}
	validTypes := []string{"all", "any_error", "fn", "fp", "misclassified", "overkill", "escape", "perfect", "failed"}
	if !slices.Contains(validTypes, errType) {
		return nil, newError(ErrBadRequest, "错误类型筛选参数无效：%s", f.ErrorType)
	}

	sortMode := strings.TrimSpace(strings.ToLower(f.Sort))
	if sortMode == "" {
		sortMode = "position"
	}
	validSorts := []string{"position", "fn_desc", "fp_desc", "max_fp_conf_desc", "errors_desc"}
	if !slices.Contains(validSorts, sortMode) {
		return nil, newError(ErrBadRequest, "排序参数无效：%s", f.Sort)
	}

	// Resolve optional confusion-matrix drilldown indices.
	wantGTIdx := -1
	if f.GTClass != "" {
		if f.GTClass == BackgroundLabel {
			wantGTIdx = run.BgIdx
		} else if idx, ok := run.ClassIdx[f.GTClass]; ok {
			wantGTIdx = idx
		} else {
			return []SampleItem{}, nil
		}
	}
	wantPredIdx := -1
	if f.PredClass != "" {
		if f.PredClass == BackgroundLabel {
			wantPredIdx = run.BgIdx
		} else if idx, ok := run.ClassIdx[f.PredClass]; ok {
			wantPredIdx = idx
		} else {
			return []SampleItem{}, nil
		}
	}

	searchLower := strings.ToLower(strings.TrimSpace(f.Search))
	matched := make([]computedSample, 0, len(run.Samples))

	for _, cs := range run.Samples {
		switch errType {
		case "all":
		case "any_error":
			if cs.FPCount == 0 && cs.FNCount == 0 && cs.MisclassifiedCount == 0 && cs.ImageOutcome != OutcomeFailed {
				continue
			}
		case "fn":
			if cs.FNCount == 0 {
				continue
			}
		case "fp":
			if cs.FPCount == 0 {
				continue
			}
		case "misclassified":
			if cs.MisclassifiedCount == 0 {
				continue
			}
		case "overkill":
			if cs.ImageOutcome != OutcomeOverkill {
				continue
			}
		case "escape":
			if cs.ImageOutcome != OutcomeEscape {
				continue
			}
		case "perfect":
			if cs.FPCount > 0 || cs.FNCount > 0 || cs.MisclassifiedCount > 0 || cs.ImageOutcome == OutcomeFailed {
				continue
			}
		case "failed":
			if cs.ImageOutcome != OutcomeFailed {
				continue
			}
		}

		if f.Class != "" {
			hasClass := false
			for _, g := range cs.MatchedGT {
				if g.Name == f.Class {
					hasClass = true
					break
				}
			}
			if !hasClass {
				for _, p := range cs.MatchedPreds {
					if p.Name == f.Class {
						hasClass = true
						break
					}
				}
			}
			if !hasClass {
				continue
			}
		}

		if wantGTIdx >= 0 || wantPredIdx >= 0 {
			hitCell := false
			for _, cell := range cs.confusionPairs {
				if (wantGTIdx < 0 || cell.gtIdx == wantGTIdx) && (wantPredIdx < 0 || cell.predIdx == wantPredIdx) {
					hitCell = true
					break
				}
			}
			if !hitCell {
				continue
			}
		}

		if searchLower != "" {
			if !strings.Contains(strings.ToLower(cs.Data.Path), searchLower) &&
				!strings.Contains(strings.ToLower(cs.Data.FileName), searchLower) &&
				!strings.Contains(strings.ToLower(cs.Data.DatasetName), searchLower) {
				continue
			}
		}
		matched = append(matched, cs)
	}

	switch sortMode {
	case "fn_desc":
		slices.SortStableFunc(matched, func(a, b computedSample) int {
			return cmp.Or(cmp.Compare(b.FNCount, a.FNCount), cmp.Compare(b.FPCount, a.FPCount), cmp.Compare(a.Data.Position, b.Data.Position))
		})
	case "fp_desc":
		slices.SortStableFunc(matched, func(a, b computedSample) int {
			return cmp.Or(cmp.Compare(b.FPCount, a.FPCount), cmp.Compare(b.MaxFPConf, a.MaxFPConf), cmp.Compare(a.Data.Position, b.Data.Position))
		})
	case "max_fp_conf_desc":
		slices.SortStableFunc(matched, func(a, b computedSample) int {
			return cmp.Or(cmp.Compare(b.MaxFPConf, a.MaxFPConf), cmp.Compare(b.FPCount, a.FPCount), cmp.Compare(a.Data.Position, b.Data.Position))
		})
	case "errors_desc":
		slices.SortStableFunc(matched, func(a, b computedSample) int {
			ea := a.FNCount + a.FPCount + a.MisclassifiedCount
			eb := b.FNCount + b.FPCount + b.MisclassifiedCount
			return cmp.Or(cmp.Compare(eb, ea), cmp.Compare(b.FNCount, a.FNCount), cmp.Compare(a.Data.Position, b.Data.Position))
		})
	default:
		// already ordered by position
	}

	out := make([]SampleItem, len(matched))
	for i, cs := range matched {
		out[i] = buildSampleItem(evalID, cs)
	}
	return out, nil
}

// ListSamples filters, sorts, and paginates samples of a succeeded evaluation
// at the requested (conf, iou) thresholds.
func (s *Service) ListSamples(ctx context.Context, id int64, f SampleFilter) ([]SampleItem, int, error) {
	ev, err := s.Get(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if ev.Status != StatusSucceeded {
		return nil, 0, errNotSucceeded
	}
	loaded, err := s.loadEvaluationData(ctx, ev)
	if err != nil {
		return nil, 0, err
	}
	conf, iou, err := resolveThresholds(ev, loaded.storeMinConf, f.Conf, f.IoU)
	if err != nil {
		return nil, 0, err
	}
	run := ComputeAnalysis(ev.ID, loaded.classes, loaded.samples, loaded.curves, loaded.storeMinConf, conf, iou)
	all, err := filterAndSortSamples(ev.ID, run, f)
	if err != nil {
		return nil, 0, err
	}
	total := len(all)

	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultSampleLimit
	}
	if limit > MaxSampleLimit {
		limit = MaxSampleLimit
	}
	if offset >= total {
		return []SampleItem{}, total, nil
	}
	end := min(offset+limit, total)
	return all[offset:end], total, nil
}

// GetSample returns matched GT & prediction boxes for one sample at (conf, iou).
func (s *Service) GetSample(ctx context.Context, evalID, sampleID int64, confPtr, iouPtr *float64) (SampleDetail, error) {
	ev, err := s.Get(ctx, evalID)
	if err != nil {
		return SampleDetail{}, err
	}
	if ev.Status != StatusSucceeded {
		return SampleDetail{}, errNotSucceeded
	}
	loaded, err := s.loadEvaluationData(ctx, ev)
	if err != nil {
		return SampleDetail{}, err
	}
	smp, ok := loaded.sampleByID[sampleID]
	if !ok {
		return SampleDetail{}, errSampleNotFound
	}
	conf, iou, err := resolveThresholds(ev, loaded.storeMinConf, confPtr, iouPtr)
	if err != nil {
		return SampleDetail{}, err
	}
	classIdx := make(map[string]int, len(loaded.classes))
	for i, c := range loaded.classes {
		classIdx[c.Name] = i
	}
	cs := matchSample(smp, classIdx, len(loaded.classes), conf, iou)

	// Probe whether the source image is still reachable on disk/storage.
	sourceMissing := false
	if imgFile, oerr := s.openSampleImageFile(ctx, evalID, sampleID, false, ""); oerr != nil {
		if errors.Is(oerr, ErrNotFound) {
			sourceMissing = true
		}
	} else {
		_ = imgFile.Close()
	}

	return buildSampleDetail(evalID, cs, sourceMissing), nil
}

// OpenSampleImage opens the original image file for one evaluation sample.
func (s *Service) OpenSampleImage(ctx context.Context, evalID, sampleID int64) (*ImageFile, error) {
	return s.openSampleImageFile(ctx, evalID, sampleID, false, "")
}

// OpenSampleThumbnail opens a thumbnail for one evaluation sample.
func (s *Service) OpenSampleThumbnail(ctx context.Context, evalID, sampleID int64, size string) (*ImageFile, error) {
	if size == "" {
		size = dataset.ThumbSmall
	}
	return s.openSampleImageFile(ctx, evalID, sampleID, true, size)
}

func (s *Service) openSampleImageFile(ctx context.Context, evalID, sampleID int64, thumb bool, thumbSize string) (*ImageFile, error) {
	var (
		dvID       *int64
		dviID      *int64
		datasetID  *int64
		datasetImg *int64
	)
	err := s.db.QueryRow(ctx, `SELECT e.data_version_id, ei.data_version_image_id, ei.dataset_id, ei.dataset_image_id
		FROM evaluation_images ei
		JOIN evaluations e ON e.id = ei.evaluation_id
		WHERE ei.evaluation_id = $1 AND ei.id = $2`, evalID, sampleID).Scan(
		&dvID, &dviID, &datasetID, &datasetImg,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errSampleNotFound
	}
	if err != nil {
		return nil, err
	}

	// Try DataVersion file first when the evaluation came from a DataVersion.
	if dvID != nil && dviID != nil && s.dataVersions != nil {
		var (
			dvf *dataversion.ImageFile
			err error
		)
		if thumb {
			dvf, err = s.dataVersions.OpenThumbnail(ctx, *dvID, *dviID, thumbSize)
		} else {
			dvf, err = s.dataVersions.OpenImage(ctx, *dvID, *dviID)
		}
		if err == nil {
			modTime := time.Time{}
			if fi, serr := dvf.File.Stat(); serr == nil {
				modTime = fi.ModTime()
			}
			return &ImageFile{
				File:        dvf.File,
				ContentType: dvf.ContentType,
				Name:        dvf.Name,
				ModTime:     modTime,
			}, nil
		}
		var de *dataversion.Error
		if !(errors.As(err, &de) && errors.Is(de.Kind, dataversion.ErrNotFound)) {
			// Fall through only on not-found; surface unexpected errors unless dataset fallback exists.
			if datasetID == nil || datasetImg == nil {
				return nil, errImageMissing
			}
		}
	}

	// Dataset source (or fallback when DataVersion was purged while original dataset remains).
	if datasetID != nil && datasetImg != nil && s.datasets != nil {
		var (
			dsf *dataset.ImageFile
			err error
		)
		if thumb {
			dsf, err = s.datasets.OpenThumbnail(ctx, *datasetID, *datasetImg, thumbSize)
		} else {
			dsf, err = s.datasets.OpenImage(ctx, *datasetID, *datasetImg)
		}
		if err == nil {
			return &ImageFile{
				File:        dsf.File,
				ContentType: dsf.ContentType,
				Name:        dsf.Name,
				ModTime:     dsf.ModTime,
			}, nil
		}
		return nil, errImageMissing
	}

	return nil, errImageMissing
}

// Compare compares 2..MaxCompareItems succeeded evaluations side by side,
// either at each evaluation's saved default threshold ("default") or at a
// single unified (conf, iou) ("unified").
func (s *Service) Compare(ctx context.Context, ids []int64, mode string, confPtr, iouPtr *float64) (CompareResult, error) {
	dedup := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return CompareResult{}, newError(ErrBadRequest, "对比的评估任务 ID 无效")
		}
		if !slices.Contains(dedup, id) {
			dedup = append(dedup, id)
		}
	}
	if len(dedup) < 2 || len(dedup) > MaxCompareItems {
		return CompareResult{}, newError(ErrInvalid, "请选择 2–%d 个已完成的评估任务进行对比", MaxCompareItems)
	}

	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "default"
	}
	if mode != "default" && mode != "unified" {
		return CompareResult{}, newError(ErrBadRequest, "阈值模式无效：%s（可选 default、unified）", mode)
	}

	evals := make([]Evaluation, len(dedup))
	loadeds := make([]*loadedEval, len(dedup))
	maxStoreMin := 0.0
	for i, id := range dedup {
		ev, err := s.Get(ctx, id)
		if err != nil {
			return CompareResult{}, err
		}
		if ev.Status != StatusSucceeded {
			return CompareResult{}, newError(ErrConflict, "评估任务“%s”尚未完成，无法加入对比", ev.Name)
		}
		loaded, err := s.loadEvaluationData(ctx, ev)
		if err != nil {
			return CompareResult{}, err
		}
		evals[i] = ev
		loadeds[i] = loaded
		if loaded.storeMinConf > maxStoreMin {
			maxStoreMin = loaded.storeMinConf
		}
	}

	var unifiedConf, unifiedIoU *float64
	if mode == "unified" {
		c := DefaultConf
		if confPtr != nil {
			c = *confPtr
		} else if evals[0].Options.Conf > 0 {
			c = evals[0].Options.Conf
		}
		if math.IsNaN(c) || math.IsInf(c, 0) || c < maxStoreMin-1e-6 || c > 0.99+1e-6 {
			return CompareResult{}, newError(ErrInvalid, "统一置信度阈值必须在 %.2f–0.99 之间", maxStoreMin)
		}
		c = round4(math.Max(maxStoreMin, math.Min(0.99, c)))

		v := DefaultIoU
		if iouPtr != nil {
			v = *iouPtr
		} else if evals[0].Options.IoU > 0 {
			v = evals[0].Options.IoU
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.05-1e-6 || v > 0.99+1e-6 {
			return CompareResult{}, newError(ErrInvalid, "统一匹配 IoU 阈值必须在 0.05–0.99 之间")
		}
		v = round4(math.Max(0.05, math.Min(0.99, v)))
		unifiedConf = &c
		unifiedIoU = &v
	}

	sameSource := evals[0].SourceFingerprint != ""
	fp := evals[0].SourceFingerprint
	seenClass := map[string]bool{}
	classNames := []string{}
	items := make([]CompareItem, len(evals))

	for i, ev := range evals {
		if ev.SourceFingerprint != fp || ev.SourceFingerprint == "" {
			sameSource = false
		}
		var cPtr, iPtr *float64
		if mode == "unified" {
			cPtr, iPtr = unifiedConf, unifiedIoU
		}
		conf, iou, err := resolveThresholds(ev, loadeds[i].storeMinConf, cPtr, iPtr)
		if err != nil {
			return CompareResult{}, err
		}
		run := ComputeAnalysis(ev.ID, loadeds[i].classes, loadeds[i].samples, loadeds[i].curves, loadeds[i].storeMinConf, conf, iou)

		for _, c := range run.Analysis.Classes {
			if !seenClass[c.Name] {
				seenClass[c.Name] = true
				classNames = append(classNames, c.Name)
			}
		}

		var prAll, f1All *CurveSeries
		for _, s := range run.Analysis.Curves.PR {
			if s.Name == "all" {
				cp := s
				cp.Name = ev.Name
				prAll = &cp
				break
			}
		}
		for _, s := range run.Analysis.Curves.F1 {
			if s.Name == "all" {
				cp := s
				cp.Name = ev.Name
				f1All = &cp
				break
			}
		}

		items[i] = CompareItem{
			Evaluation: ev,
			Conf:       conf,
			IoU:        iou,
			Summary:    run.Analysis.Summary,
			Classes:    run.Analysis.Classes,
			OKNG:       run.Analysis.OKNG,
			PRAll:      prAll,
			F1All:      f1All,
			Confusion:  run.Analysis.Confusion,
		}
	}

	res := CompareResult{
		Mode:       mode,
		Conf:       unifiedConf,
		IoU:        unifiedIoU,
		SameSource: sameSource,
		ClassNames: classNames,
		Items:      items,
	}
	if sameSource {
		res.SourceFingerprint = fp
	}
	return res, nil
}

// CompareDiff compares two same-source succeeded evaluations image by image so
// engineers can inspect where target improved or regressed vs base.
func (s *Service) CompareDiff(
	ctx context.Context,
	baseID, targetID int64,
	mode string,
	confPtr, iouPtr *float64,
	diffType, search string,
	offset, limit int,
) (CompareDiffResult, error) {
	if baseID <= 0 || targetID <= 0 || baseID == targetID {
		return CompareDiffResult{}, newError(ErrInvalid, "请选择两个不同的评估任务进行逐图差异对比")
	}
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "default"
	}
	if mode != "default" && mode != "unified" {
		return CompareDiffResult{}, newError(ErrBadRequest, "阈值模式无效：%s", mode)
	}
	diffType = strings.TrimSpace(strings.ToLower(diffType))
	if diffType == "" {
		diffType = "changed"
	}
	validDiff := []string{"all", "changed", "improved", "regressed", "fp_reduced", "fp_added", "outcome_changed", "same"}
	if !slices.Contains(validDiff, diffType) {
		return CompareDiffResult{}, newError(ErrBadRequest, "差异筛选参数无效：%s", diffType)
	}

	baseEv, err := s.Get(ctx, baseID)
	if err != nil {
		return CompareDiffResult{}, err
	}
	targetEv, err := s.Get(ctx, targetID)
	if err != nil {
		return CompareDiffResult{}, err
	}
	if baseEv.Status != StatusSucceeded || targetEv.Status != StatusSucceeded {
		return CompareDiffResult{}, errNotSucceeded
	}
	if baseEv.SourceFingerprint == "" || baseEv.SourceFingerprint != targetEv.SourceFingerprint {
		return CompareDiffResult{}, newError(ErrConflict, "仅相同数据来源的评估任务支持逐图差异对比")
	}

	baseLoaded, err := s.loadEvaluationData(ctx, baseEv)
	if err != nil {
		return CompareDiffResult{}, err
	}
	targetLoaded, err := s.loadEvaluationData(ctx, targetEv)
	if err != nil {
		return CompareDiffResult{}, err
	}

	var bConfPtr, bIoUPtr, tConfPtr, tIoUPtr *float64
	if mode == "unified" {
		bConfPtr, bIoUPtr = confPtr, iouPtr
		tConfPtr, tIoUPtr = confPtr, iouPtr
	}
	baseConf, baseIoU, err := resolveThresholds(baseEv, baseLoaded.storeMinConf, bConfPtr, bIoUPtr)
	if err != nil {
		return CompareDiffResult{}, err
	}
	targetConf, targetIoU, err := resolveThresholds(targetEv, targetLoaded.storeMinConf, tConfPtr, tIoUPtr)
	if err != nil {
		return CompareDiffResult{}, err
	}

	baseRun := ComputeAnalysis(baseEv.ID, baseLoaded.classes, baseLoaded.samples, baseLoaded.curves, baseLoaded.storeMinConf, baseConf, baseIoU)
	targetRun := ComputeAnalysis(targetEv.ID, targetLoaded.classes, targetLoaded.samples, targetLoaded.curves, targetLoaded.storeMinConf, targetConf, targetIoU)

	targetByKey := make(map[string]computedSample, len(targetRun.Samples))
	for _, cs := range targetRun.Samples {
		targetByKey[cs.Data.SampleKey] = cs
	}

	counts := map[string]int{
		"all":             0,
		"changed":         0,
		"improved":        0,
		"regressed":       0,
		"fp_reduced":      0,
		"fp_added":        0,
		"outcome_changed": 0,
		"same":            0,
	}
	searchLower := strings.ToLower(strings.TrimSpace(search))
	filtered := []CompareDiffItem{}

	for _, bcs := range baseRun.Samples {
		tcs, ok := targetByKey[bcs.Data.SampleKey]
		if !ok {
			continue
		}
		dFN := tcs.FNCount - bcs.FNCount
		dFP := tcs.FPCount - bcs.FPCount
		dTP := tcs.TPCount - bcs.TPCount
		outcomeChanged := bcs.ImageOutcome != tcs.ImageOutcome

		cat := "same"
		switch {
		case dFN < 0:
			cat = "improved"
		case dFN > 0:
			cat = "regressed"
		case dFP < 0:
			cat = "fp_reduced"
		case dFP > 0:
			cat = "fp_added"
		case outcomeChanged || dTP != 0 || tcs.MisclassifiedCount != bcs.MisclassifiedCount:
			cat = "outcome_changed"
		}

		counts["all"]++
		if cat == "same" {
			counts["same"]++
		} else {
			counts["changed"]++
		}
		if cat != "same" && cat != "outcome_changed" {
			counts[cat]++
		}
		if outcomeChanged {
			counts["outcome_changed"]++
		}

		// Filter by diffType.
		keep := false
		switch diffType {
		case "all":
			keep = true
		case "changed":
			keep = cat != "same"
		case "outcome_changed":
			keep = outcomeChanged
		default:
			keep = cat == diffType
		}
		if !keep {
			continue
		}
		if searchLower != "" &&
			!strings.Contains(strings.ToLower(bcs.Data.Path), searchLower) &&
			!strings.Contains(strings.ToLower(bcs.Data.FileName), searchLower) {
			continue
		}

		filtered = append(filtered, CompareDiffItem{
			SampleKey:    bcs.Data.SampleKey,
			Path:         bcs.Data.Path,
			FileName:     bcs.Data.FileName,
			Width:        bcs.Data.Width,
			Height:       bcs.Data.Height,
			GTCount:      len(bcs.Data.GTObjects),
			DiffCategory: cat,
			Base:         buildSampleDetail(baseEv.ID, bcs, false),
			Target:       buildSampleDetail(targetEv.ID, tcs, false),
			DeltaFN:      dFN,
			DeltaFP:      dFP,
			DeltaTP:      dTP,
		})
	}

	// Sort most impactful differences first.
	slices.SortStableFunc(filtered, func(a, b CompareDiffItem) int {
		sa := absInt(a.DeltaFN)*3 + absInt(a.DeltaFP)*2 + absInt(a.DeltaTP)
		sb := absInt(b.DeltaFN)*3 + absInt(b.DeltaFP)*2 + absInt(b.DeltaTP)
		return cmp.Or(cmp.Compare(sb, sa), cmp.Compare(a.Base.Position, b.Base.Position))
	})

	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	var page []CompareDiffItem
	if offset >= total {
		page = []CompareDiffItem{}
	} else {
		page = filtered[offset:min(offset+limit, total)]
	}

	return CompareDiffResult{
		BaseID:   baseID,
		TargetID: targetID,
		Counts:   counts,
		Items:    page,
		Total:    total,
	}, nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
