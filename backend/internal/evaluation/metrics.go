package evaluation

import (
	"cmp"
	"math"
	"slices"
	"strings"
)

// Pure-Go detection evaluation & threshold re-computation engine.
//
// Given snapshotted ground-truth boxes and stored per-image predictions (at
// conf >= store_min_conf), ComputeAnalysis evaluates any (conf, iou) operating
// point in milliseconds:
//   - Greedy 1-to-1 matching by descending prediction confidence:
//       Pass 1: same-class matching (IoU >= iou) -> TP
//       Pass 2: cross-class matching among remaining boxes (IoU >= iou) -> Misclassified
//       Pass 3: remaining unmatched predictions -> FP; unmatched GT -> FN
//   - Per-class & overall Precision, Recall, F1
//   - (N+1)x(N+1) confusion matrix (rows=GT + background, cols=Pred + background)
//   - Image-level industrial OK/NG 2x2 matrix (OverkillRate OK->NG, EscapeRate NG->OK)
//   - Per-image TP/FP/FN/misclassified counts & matched box details for inspection

// BackgroundLabel is the label used for the background row/column in confusion matrices.
const BackgroundLabel = "background"

// evalSampleData holds immutable GT + raw stored predictions for one sample in memory.
type evalSampleData struct {
	ID                 int64
	Position           int
	SampleKey          string
	Split              string
	DatasetID          *int64
	DatasetName        string
	DatasetImageID     *int64
	DataVersionImageID *int64
	Path               string
	FileName           string
	Width              int
	Height             int
	GTObjects          []GTBox
	Predictions        []PredBox // sorted by Conf DESC
	InferenceError     string
}

// computedSample holds the matched results for one sample at a specific (conf, iou).
type computedSample struct {
	Data               *evalSampleData
	MatchedGT          []MatchedGTBox
	MatchedPreds       []MatchedPredBox
	PredCount          int
	TPCount            int
	FPCount            int
	FNCount            int
	MisclassifiedCount int
	ImageOutcome       string
	MaxFPConf          float64
	// confusionPairs records every (gtClassIdx, predClassIdx) cell contributed
	// by this image (used for exact confusion-matrix cell drilldown).
	confusionPairs []confusionCell
}

type confusionCell struct {
	gtIdx   int
	predIdx int
}

// BoxIoU computes intersection-over-union of two axis-aligned boxes [xmin, ymin, xmax, ymax].
func BoxIoU(ax1, ay1, ax2, ay2, bx1, by1, bx2, by2 float64) float64 {
	ix1 := math.Max(ax1, bx1)
	iy1 := math.Max(ay1, by1)
	ix2 := math.Min(ax2, bx2)
	iy2 := math.Min(ay2, by2)
	iw := ix2 - ix1
	ih := iy2 - iy1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := iw * ih
	areaA := math.Max(0, ax2-ax1) * math.Max(0, ay2-ay1)
	areaB := math.Max(0, bx2-bx1) * math.Max(0, by2-by1)
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func safeDiv(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return num / den
}

func f1Score(p, r float64) float64 {
	if p+r <= 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func round4(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10000) / 10000
}

// matchSample performs greedy 1-to-1 matching on a single image at (conf, iou).
func matchSample(sample *evalSampleData, classIdx map[string]int, bgIdx int, conf, iou float64) computedSample {
	res := computedSample{Data: sample}
	if sample.InferenceError != "" {
		res.ImageOutcome = OutcomeFailed
		res.MatchedGT = make([]MatchedGTBox, len(sample.GTObjects))
		for i, g := range sample.GTObjects {
			res.MatchedGT[i] = MatchedGTBox{GTBox: g, Index: i, Matched: false, Match: MatchFN}
		}
		res.MatchedPreds = []MatchedPredBox{}
		res.FNCount = len(sample.GTObjects)
		return res
	}

	// Filter predictions by confidence threshold; keep descending conf order.
	filtered := make([]PredBox, 0, len(sample.Predictions))
	for _, p := range sample.Predictions {
		if p.Conf >= conf {
			filtered = append(filtered, p)
		}
	}
	// Ensure deterministic descending order even if input was unsorted.
	slices.SortStableFunc(filtered, func(a, b PredBox) int {
		return cmp.Or(cmp.Compare(b.Conf, a.Conf), strings.Compare(a.Name, b.Name))
	})

	nGT := len(sample.GTObjects)
	nPred := len(filtered)
	res.PredCount = nPred
	res.MatchedGT = make([]MatchedGTBox, nGT)
	for i, g := range sample.GTObjects {
		res.MatchedGT[i] = MatchedGTBox{GTBox: g, Index: i, Matched: false, Match: MatchFN}
	}
	res.MatchedPreds = make([]MatchedPredBox, nPred)
	for i, p := range filtered {
		res.MatchedPreds[i] = MatchedPredBox{PredBox: p, Match: MatchFP}
	}

	gtUsed := make([]bool, nGT)
	predUsed := make([]bool, nPred)

	// Pass 1: same-class greedy matching (standard COCO / Ultralytics TP matching).
	for pi, p := range filtered {
		bestGI := -1
		bestIoU := iou
		for gi, g := range sample.GTObjects {
			if gtUsed[gi] || g.Name != p.Name {
				continue
			}
			v := BoxIoU(p.Xmin, p.Ymin, p.Xmax, p.Ymax, float64(g.Xmin), float64(g.Ymin), float64(g.Xmax), float64(g.Ymax))
			if v >= bestIoU && (bestGI < 0 || v > bestIoU) {
				bestIoU = v
				bestGI = gi
			}
		}
		if bestGI >= 0 {
			gtUsed[bestGI] = true
			predUsed[pi] = true
			giCopy := bestGI
			piCopy := pi
			res.MatchedPreds[pi].Match = MatchTP
			res.MatchedPreds[pi].MatchedGTIdx = &giCopy
			res.MatchedPreds[pi].MatchedGTName = sample.GTObjects[bestGI].Name
			res.MatchedPreds[pi].IoU = round4(bestIoU)

			res.MatchedGT[bestGI].Matched = true
			res.MatchedGT[bestGI].Match = MatchTP
			res.MatchedGT[bestGI].MatchedPredIdx = &piCopy
			res.MatchedGT[bestGI].MatchedPredName = p.Name
			res.MatchedGT[bestGI].MatchedPredConf = round4(p.Conf)
			res.MatchedGT[bestGI].IoU = round4(bestIoU)

			res.TPCount++
			if ci, ok := classIdx[p.Name]; ok {
				res.confusionPairs = append(res.confusionPairs, confusionCell{gtIdx: ci, predIdx: ci})
			}
		}
	}

	// Pass 2: cross-class matching among remaining unmatched boxes (misclassifications).
	for pi, p := range filtered {
		if predUsed[pi] {
			continue
		}
		bestGI := -1
		bestIoU := iou
		for gi, g := range sample.GTObjects {
			if gtUsed[gi] {
				continue
			}
			v := BoxIoU(p.Xmin, p.Ymin, p.Xmax, p.Ymax, float64(g.Xmin), float64(g.Ymin), float64(g.Xmax), float64(g.Ymax))
			if v >= bestIoU && (bestGI < 0 || v > bestIoU) {
				bestIoU = v
				bestGI = gi
			}
		}
		if bestGI >= 0 {
			gtUsed[bestGI] = true
			predUsed[pi] = true
			giCopy := bestGI
			piCopy := pi
			res.MatchedPreds[pi].Match = MatchMisclassified
			res.MatchedPreds[pi].MatchedGTIdx = &giCopy
			res.MatchedPreds[pi].MatchedGTName = sample.GTObjects[bestGI].Name
			res.MatchedPreds[pi].IoU = round4(bestIoU)

			res.MatchedGT[bestGI].Matched = true
			res.MatchedGT[bestGI].Match = MatchMisclassified
			res.MatchedGT[bestGI].MatchedPredIdx = &piCopy
			res.MatchedGT[bestGI].MatchedPredName = p.Name
			res.MatchedGT[bestGI].MatchedPredConf = round4(p.Conf)
			res.MatchedGT[bestGI].IoU = round4(bestIoU)

			res.MisclassifiedCount++
			// Standard object-detection accounting: a misclassified box is an FP for
			// the predicted class and an FN for the ground-truth class.
			res.FPCount++
			res.FNCount++
			if p.Conf > res.MaxFPConf {
				res.MaxFPConf = p.Conf
			}
			gci, gok := classIdx[sample.GTObjects[bestGI].Name]
			pci, pok := classIdx[p.Name]
			if gok && pok {
				res.confusionPairs = append(res.confusionPairs, confusionCell{gtIdx: gci, predIdx: pci})
			}
		}
	}

	// Pass 3: remaining unmatched predictions -> FP (background -> predClass).
	for pi, p := range filtered {
		if predUsed[pi] {
			continue
		}
		res.FPCount++
		if p.Conf > res.MaxFPConf {
			res.MaxFPConf = p.Conf
		}
		if pci, ok := classIdx[p.Name]; ok {
			res.confusionPairs = append(res.confusionPairs, confusionCell{gtIdx: bgIdx, predIdx: pci})
		}
	}

	// Pass 4: remaining unmatched GT -> FN (gtClass -> background).
	for gi, g := range sample.GTObjects {
		if gtUsed[gi] {
			continue
		}
		res.FNCount++
		if gci, ok := classIdx[g.Name]; ok {
			res.confusionPairs = append(res.confusionPairs, confusionCell{gtIdx: gci, predIdx: bgIdx})
		}
	}

	// Image-level industrial OK/NG outcome.
	gtDefect := nGT > 0
	predDefect := nPred > 0
	switch {
	case !gtDefect && !predDefect:
		res.ImageOutcome = OutcomeOKOK
	case !gtDefect && predDefect:
		res.ImageOutcome = OutcomeOverkill
	case gtDefect && !predDefect:
		res.ImageOutcome = OutcomeEscape
	default:
		res.ImageOutcome = OutcomeNGNG
	}
	return res
}

// evaluatedRun holds the full computed state of an evaluation at (conf, iou).
type evaluatedRun struct {
	Analysis AnalysisResult
	Samples  []computedSample
	ClassIdx map[string]int
	BgIdx    int
}

// ComputeAnalysis evaluates all samples at (conf, iou) and combines them with
// threshold-independent curves/mAP from curves.json.
func ComputeAnalysis(
	evalID int64,
	classes []ClassInfo,
	samples []*evalSampleData,
	curves CurvesData,
	storeMinConf, conf, iou float64,
) evaluatedRun {
	nCls := len(classes)
	classIdx := make(map[string]int, nCls)
	labels := make([]string, nCls+1)
	clsMetrics := make([]ClassMetrics, nCls)

	unmatchedModel := 0
	unmatchedGT := 0
	evalClassCount := 0
	for i, c := range classes {
		classIdx[c.Name] = i
		labels[i] = c.Name
		cm := ClassMetrics{
			Name:    c.Name,
			Color:   c.Color,
			InModel: c.InModel,
			InGT:    c.InGT,
		}
		if c.InModel && !c.InGT {
			unmatchedModel++
		}
		if c.InGT && !c.InModel {
			unmatchedGT++
		}
		if c.InGT && c.GTCount > 0 {
			evalClassCount++
		}
		if v, ok := curves.ClassAP50[c.Name]; ok {
			vv := round4(v)
			cm.AP50 = &vv
		}
		if v, ok := curves.ClassAP75[c.Name]; ok {
			vv := round4(v)
			cm.AP75 = &vv
		}
		if v, ok := curves.ClassAP50_95[c.Name]; ok {
			vv := round4(v)
			cm.AP50_95 = &vv
		}
		clsMetrics[i] = cm
	}
	bgIdx := nCls
	labels[bgIdx] = BackgroundLabel

	matrix := make([][]int, nCls+1)
	for i := range matrix {
		matrix[i] = make([]int, nCls+1)
	}

	computed := make([]computedSample, len(samples))
	var okng OKNGMetrics
	okng.TotalImages = len(samples)

	var totalGT, totalPred, totalTP, totalFP, totalFN, totalMisclass int
	evaluatedImages := 0

	for i, s := range samples {
		cs := matchSample(s, classIdx, bgIdx, conf, iou)
		computed[i] = cs

		if cs.ImageOutcome == OutcomeFailed {
			okng.FailedImages++
			continue
		}
		evaluatedImages++
		switch cs.ImageOutcome {
		case OutcomeOKOK:
			okng.OKImages++
			okng.TN++
		case OutcomeOverkill:
			okng.OKImages++
			okng.Overkill++
		case OutcomeEscape:
			okng.NGImages++
			okng.Escape++
		case OutcomeNGNG:
			okng.NGImages++
			okng.TP++
		}

		totalGT += len(s.GTObjects)
		totalPred += cs.PredCount
		totalTP += cs.TPCount
		totalFP += cs.FPCount
		totalFN += cs.FNCount
		totalMisclass += cs.MisclassifiedCount

		for _, g := range cs.MatchedGT {
			if ci, ok := classIdx[g.Name]; ok {
				clsMetrics[ci].GTCount++
				if g.Match == MatchTP {
					clsMetrics[ci].TP++
				} else {
					clsMetrics[ci].FN++
				}
			}
		}
		for _, p := range cs.MatchedPreds {
			if ci, ok := classIdx[p.Name]; ok {
				clsMetrics[ci].PredCount++
				if p.Match != MatchTP {
					clsMetrics[ci].FP++
				}
			}
		}
		for _, cell := range cs.confusionPairs {
			matrix[cell.gtIdx][cell.predIdx]++
		}
	}

	// Finalize per-class P/R/F1.
	for i := range clsMetrics {
		cm := &clsMetrics[i]
		cm.Precision = round4(safeDiv(float64(cm.TP), float64(cm.TP+cm.FP)))
		cm.Recall = round4(safeDiv(float64(cm.TP), float64(cm.TP+cm.FN)))
		cm.F1 = round4(f1Score(cm.Precision, cm.Recall))
	}

	// Finalize image-level OK/NG rates.
	okng.OverkillRate = round4(safeDiv(float64(okng.Overkill), float64(okng.OKImages)))
	okng.EscapeRate = round4(safeDiv(float64(okng.Escape), float64(okng.NGImages)))
	okng.Accuracy = round4(safeDiv(float64(okng.TP+okng.TN), float64(evaluatedImages)))
	okng.Precision = round4(safeDiv(float64(okng.TP), float64(okng.TP+okng.Overkill)))
	okng.Recall = round4(safeDiv(float64(okng.TP), float64(okng.TP+okng.Escape)))
	okng.F1 = round4(f1Score(okng.Precision, okng.Recall))

	// Overall object-level Precision / Recall / F1 (micro across all boxes so
	// FPs on model-only classes and OK-only datasets are faithfully reflected).
	overallP := round4(safeDiv(float64(totalTP), float64(totalTP+totalFP)))
	overallR := round4(safeDiv(float64(totalTP), float64(totalTP+totalFN)))
	overallF1 := round4(f1Score(overallP, overallR))

	// If trainer didn't supply curves (e.g., synthetic tests), compute fallback curves
	// from the retained predictions so curves & mAP are never empty when data exists.
	if len(curves.PR) == 0 && len(samples) > 0 && curves.MAP50 == 0 && totalGT > 0 {
		curves = ComputeCurvesFromSamples(classes, samples, curves.Speed)
		for i := range clsMetrics {
			name := clsMetrics[i].Name
			if v, ok := curves.ClassAP50[name]; ok {
				vv := round4(v)
				clsMetrics[i].AP50 = &vv
			}
			if v, ok := curves.ClassAP75[name]; ok {
				vv := round4(v)
				clsMetrics[i].AP75 = &vv
			}
			if v, ok := curves.ClassAP50_95[name]; ok {
				vv := round4(v)
				clsMetrics[i].AP50_95 = &vv
			}
		}
	}

	summary := SummaryMetrics{
		Conf:                  round4(conf),
		IoU:                   round4(iou),
		MAP50:                 round4(curves.MAP50),
		MAP75:                 round4(curves.MAP75),
		MAP50_95:              round4(curves.MAP50_95),
		Precision:             overallP,
		Recall:                overallR,
		F1:                    overallF1,
		BestF1:                round4(curves.BestF1),
		BestF1Conf:            round4(curves.BestF1Conf),
		TotalImages:           len(samples),
		EvaluatedImages:       evaluatedImages,
		FailedImages:          okng.FailedImages,
		TotalGT:               totalGT,
		TotalPred:             totalPred,
		TP:                    totalTP,
		FP:                    totalFP,
		FN:                    totalFN,
		Misclassified:         totalMisclass,
		OKNG:                  okng,
		Speed:                 curves.Speed,
		EvaluatedClassCount:   evalClassCount,
		UnmatchedModelClasses: unmatchedModel,
		UnmatchedGTClasses:    unmatchedGT,
	}

	return evaluatedRun{
		Analysis: AnalysisResult{
			EvaluationID: evalID,
			Conf:         round4(conf),
			IoU:          round4(iou),
			StoreMinConf: round4(storeMinConf),
			Summary:      summary,
			Classes:      clsMetrics,
			Confusion: ConfusionMatrix{
				Labels: labels,
				Matrix: matrix,
			},
			OKNG:   okng,
			Curves: curves,
		},
		Samples:  computed,
		ClassIdx: classIdx,
		BgIdx:    bgIdx,
	}
}

// ComputeCurvesFromSamples computes exact COCO/Ultralytics-style AP50, AP75,
// AP50:95 and 101-point PR / F1 / P / R curves in pure Go. Used as a fallback
// and in Go unit tests; on real GPU runs the Python trainer runs the identical
// calculation over conf >= 0.001 predictions before filtering to store_min_conf.
func ComputeCurvesFromSamples(classes []ClassInfo, samples []*evalSampleData, speed *SpeedMetrics) CurvesData {
	iouThresholds := [10]float64{0.50, 0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85, 0.90, 0.95}
	type detRecord struct {
		conf  float64
		tpMat [10]bool
	}

	gtByClass := make(map[string]int, len(classes))
	detsByClass := make(map[string][]detRecord, len(classes))
	colorByClass := make(map[string]string, len(classes))
	for _, c := range classes {
		colorByClass[c.Name] = c.Color
	}

	for _, s := range samples {
		if s.InferenceError != "" {
			continue
		}
		// Group GT and predictions by class for per-class IoU matching across all 10 thresholds.
		gtPerCls := make(map[string][]GTBox)
		for _, g := range s.GTObjects {
			gtPerCls[g.Name] = append(gtPerCls[g.Name], g)
			gtByClass[g.Name]++
		}
		predPerCls := make(map[string][]PredBox)
		for _, p := range s.Predictions {
			predPerCls[p.Name] = append(predPerCls[p.Name], p)
		}
		for clsName, preds := range predPerCls {
			slices.SortStableFunc(preds, func(a, b PredBox) int {
				return cmp.Compare(b.Conf, a.Conf)
			})
			gts := gtPerCls[clsName]
			if len(gts) == 0 {
				for _, p := range preds {
					detsByClass[clsName] = append(detsByClass[clsName], detRecord{conf: p.Conf})
				}
				continue
			}
			// Precompute IoU matrix [len(preds)][len(gts)].
			ious := make([][]float64, len(preds))
			for pi, p := range preds {
				row := make([]float64, len(gts))
				for gi, g := range gts {
					row[gi] = BoxIoU(p.Xmin, p.Ymin, p.Xmax, p.Ymax, float64(g.Xmin), float64(g.Ymin), float64(g.Xmax), float64(g.Ymax))
				}
				ious[pi] = row
			}
			recs := make([]detRecord, len(preds))
			for pi, p := range preds {
				recs[pi].conf = p.Conf
			}
			for ti, thr := range iouThresholds {
				used := make([]bool, len(gts))
				for pi := range preds {
					bestGI := -1
					bestV := thr
					for gi := range gts {
						if used[gi] {
							continue
						}
						v := ious[pi][gi]
						if v >= bestV && (bestGI < 0 || v > bestV) {
							bestV = v
							bestGI = gi
						}
					}
					if bestGI >= 0 {
						used[bestGI] = true
						recs[pi].tpMat[ti] = true
					}
				}
			}
			detsByClass[clsName] = append(detsByClass[clsName], recs...)
		}
	}

	const gridN = 101
	xGrid := make([]float64, gridN)
	for i := 0; i < gridN; i++ {
		xGrid[i] = float64(i) / float64(gridN-1)
	}

	out := CurvesData{
		PR:           []CurveSeries{},
		F1:           []CurveSeries{},
		Precision:    []CurveSeries{},
		Recall:       []CurveSeries{},
		ClassAP50:    map[string]float64{},
		ClassAP75:    map[string]float64{},
		ClassAP50_95: map[string]float64{},
		Speed:        speed,
	}

	var (
		sumAP50, sumAP75, sumAP50_95 float64
		gtClassCount                 int
		meanPR                       = make([]float64, gridN)
		meanP                        = make([]float64, gridN)
		meanR                        = make([]float64, gridN)
		meanF1                       = make([]float64, gridN)
	)

	for _, c := range classes {
		nGT := gtByClass[c.Name]
		if nGT == 0 {
			continue
		}
		gtClassCount++
		recs := detsByClass[c.Name]
		slices.SortStableFunc(recs, func(a, b detRecord) int {
			return cmp.Compare(b.conf, a.conf)
		})

		var ap10 [10]float64
		var pCurve50, rCurve50, confs []float64
		if len(recs) > 0 {
			confs = make([]float64, len(recs))
			for i, r := range recs {
				confs[i] = r.conf
			}
			for ti := 0; ti < 10; ti++ {
				tpc := make([]float64, len(recs))
				fpc := make([]float64, len(recs))
				tpSum, fpSum := 0.0, 0.0
				for i, r := range recs {
					if r.tpMat[ti] {
						tpSum++
					} else {
						fpSum++
					}
					tpc[i] = tpSum
					fpc[i] = fpSum
				}
				rec := make([]float64, len(recs))
				prec := make([]float64, len(recs))
				for i := range recs {
					rec[i] = tpc[i] / float64(nGT)
					prec[i] = tpc[i] / (tpc[i] + fpc[i])
				}
				ap10[ti] = computeAP101(rec, prec)
				if ti == 0 {
					rCurve50 = rec
					pCurve50 = prec
				}
			}
		}
		var apMean float64
		for _, v := range ap10 {
			apMean += v
		}
		apMean /= 10.0

		out.ClassAP50[c.Name] = round4(ap10[0])
		out.ClassAP75[c.Name] = round4(ap10[5])
		out.ClassAP50_95[c.Name] = round4(apMean)
		sumAP50 += ap10[0]
		sumAP75 += ap10[5]
		sumAP50_95 += apMean

		// Sample PR curve (x=recall 0..1 -> precision) and P/R/F1 vs conf (x=conf 0..1).
		prY := samplePRCurve(rCurve50, pCurve50, xGrid)
		pY := sampleVsConf(confs, pCurve50, xGrid, 1.0)
		rY := sampleVsConf(confs, rCurve50, xGrid, 0.0)
		f1Y := make([]float64, gridN)
		for i := 0; i < gridN; i++ {
			f1Y[i] = round4(f1Score(pY[i], rY[i]))
			meanPR[i] += prY[i]
			meanP[i] += pY[i]
			meanR[i] += rY[i]
			meanF1[i] += f1Y[i]
		}
		out.PR = append(out.PR, CurveSeries{Name: c.Name, Color: colorByClass[c.Name], X: xGrid, Y: prY})
		out.Precision = append(out.Precision, CurveSeries{Name: c.Name, Color: colorByClass[c.Name], X: xGrid, Y: pY})
		out.Recall = append(out.Recall, CurveSeries{Name: c.Name, Color: colorByClass[c.Name], X: xGrid, Y: rY})
		out.F1 = append(out.F1, CurveSeries{Name: c.Name, Color: colorByClass[c.Name], X: xGrid, Y: f1Y})
	}

	if gtClassCount > 0 {
		out.MAP50 = round4(sumAP50 / float64(gtClassCount))
		out.MAP75 = round4(sumAP75 / float64(gtClassCount))
		out.MAP50_95 = round4(sumAP50_95 / float64(gtClassCount))

		bestF1 := -1.0
		bestConf := DefaultConf
		for i := 0; i < gridN; i++ {
			meanPR[i] = round4(meanPR[i] / float64(gtClassCount))
			meanP[i] = round4(meanP[i] / float64(gtClassCount))
			meanR[i] = round4(meanR[i] / float64(gtClassCount))
			meanF1[i] = round4(meanF1[i] / float64(gtClassCount))
			if meanF1[i] > bestF1 {
				bestF1 = meanF1[i]
				bestConf = xGrid[i]
			}
		}
		if bestF1 < 0 {
			bestF1 = 0
		}
		out.BestF1 = round4(bestF1)
		out.BestF1Conf = round4(bestConf)

		// Prepend "all" aggregate curve first for convenient plotting & multi-model overlay.
		out.PR = append([]CurveSeries{{Name: "all", Color: "#2563eb", X: xGrid, Y: meanPR}}, out.PR...)
		out.Precision = append([]CurveSeries{{Name: "all", Color: "#f59e0b", X: xGrid, Y: meanP}}, out.Precision...)
		out.Recall = append([]CurveSeries{{Name: "all", Color: "#8b5cf6", X: xGrid, Y: meanR}}, out.Recall...)
		out.F1 = append([]CurveSeries{{Name: "all", Color: "#10b981", X: xGrid, Y: meanF1}}, out.F1...)
	}
	return out
}

// computeAP101 implements Ultralytics/COCO 101-point precision envelope integration.
func computeAP101(rec, prec []float64) float64 {
	if len(rec) == 0 {
		return 0
	}
	mrec := make([]float64, len(rec)+2)
	mpre := make([]float64, len(prec)+2)
	mrec[0], mpre[0] = 0.0, 0.0
	copy(mrec[1:], rec)
	copy(mpre[1:], prec)
	mrec[len(mrec)-1], mpre[len(mpre)-1] = 1.0, 0.0

	// Precision envelope: monotonically non-increasing from right to left.
	for i := len(mpre) - 2; i >= 0; i-- {
		if mpre[i] < mpre[i+1] {
			mpre[i] = mpre[i+1]
		}
	}
	// 101-point interpolation (COCO / Ultralytics interp).
	sum := 0.0
	for k := 0; k <= 100; k++ {
		r := float64(k) / 100.0
		// Find first index where mrec[idx] >= r.
		idx := 0
		for idx < len(mrec) && mrec[idx] < r {
			idx++
		}
		if idx < len(mrec) {
			sum += mpre[idx]
		}
	}
	return sum / 101.0
}

func samplePRCurve(rec, prec, xGrid []float64) []float64 {
	y := make([]float64, len(xGrid))
	if len(rec) == 0 {
		return y
	}
	// Monotonic precision envelope.
	env := make([]float64, len(prec))
	copy(env, prec)
	for i := len(env) - 2; i >= 0; i-- {
		if env[i] < env[i+1] {
			env[i] = env[i+1]
		}
	}
	j := 0
	for i, r := range xGrid {
		for j < len(rec) && rec[j] < r {
			j++
		}
		if j < len(rec) {
			y[i] = round4(env[j])
		} else {
			y[i] = 0
		}
	}
	return y
}

// sampleVsConf interpolates a metric curve (P or R) recorded at descending confs
// onto ascending xGrid (0.00..1.00). highEndVal is returned when conf > max(confs)
// (1.0 for Precision, 0.0 for Recall, matching Ultralytics conventions).
func sampleVsConf(confs, vals, xGrid []float64, highEndVal float64) []float64 {
	y := make([]float64, len(xGrid))
	if len(confs) == 0 {
		return y
	}
	// confs is descending; walk from highest conf to lowest.
	for i, c := range xGrid {
		// Find last index with confs[idx] >= c.
		lo, hi := 0, len(confs)-1
		best := -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if confs[mid] >= c {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best < 0 {
			y[i] = round4(highEndVal)
		} else {
			y[i] = round4(vals[best])
		}
	}
	return y
}
