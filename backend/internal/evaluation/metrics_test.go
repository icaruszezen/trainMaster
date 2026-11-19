package evaluation

import (
	"math"
	"strings"
	"testing"
)

func almostEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

func TestBoxIoU(t *testing.T) {
	if got := BoxIoU(0, 0, 10, 10, 0, 0, 10, 10); !almostEqual(got, 1.0, 1e-6) {
		t.Fatalf("identical boxes IoU = %v, want 1.0", got)
	}
	if got := BoxIoU(0, 0, 10, 10, 20, 20, 30, 30); got != 0 {
		t.Fatalf("disjoint boxes IoU = %v, want 0", got)
	}
	// Intersection 5x10=50, union 100+100-50=150 -> 1/3.
	if got := BoxIoU(0, 0, 10, 10, 5, 0, 15, 10); !almostEqual(got, 1.0/3.0, 1e-6) {
		t.Fatalf("partial overlap IoU = %v, want 0.3333", got)
	}
	if got := BoxIoU(5, 5, 5, 5, 0, 0, 10, 10); got != 0 {
		t.Fatalf("zero-area box IoU = %v, want 0", got)
	}
}

func TestComputeAnalysisMatchingConfusionAndOKNG(t *testing.T) {
	idx0, idx1 := 0, 1
	classes := []ClassInfo{
		{Name: "scratch", Color: "#2563eb", InModel: true, InGT: true, ModelIdx: &idx0, GTCount: 2},
		{Name: "dent", Color: "#10b981", InModel: true, InGT: true, ModelIdx: &idx1, GTCount: 2},
		{Name: "rust", Color: "#f59e0b", InModel: false, InGT: true, GTCount: 1}, // GT-only class
	}

	samples := []*evalSampleData{
		// 1. NG image: 1 TP (scratch), 1 Misclassified (GT=dent, Pred=scratch), 1 extra FP (dent)
		{
			ID: 1, Position: 1, SampleKey: "img1.jpg", Path: "img1.jpg", FileName: "img1.jpg", Width: 640, Height: 480,
			GTObjects: []GTBox{
				{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 110, Ymax: 110},
				{Name: "dent", Xmin: 200, Ymin: 200, Xmax: 300, Ymax: 300},
			},
			Predictions: []PredBox{
				{Name: "scratch", Xmin: 12, Ymin: 10, Xmax: 110, Ymax: 110, Conf: 0.92},   // TP
				{Name: "scratch", Xmin: 202, Ymin: 200, Xmax: 300, Ymax: 300, Conf: 0.80}, // Misclassified dent->scratch
				{Name: "dent", Xmin: 400, Ymin: 10, Xmax: 450, Ymax: 60, Conf: 0.40},      // Pure FP
			},
		},
		// 2. NG image with GT-only class "rust" and no prediction -> Escape (NG->OK) + FN
		{
			ID: 2, Position: 2, SampleKey: "img2.jpg", Path: "img2.jpg", FileName: "img2.jpg", Width: 640, Height: 480,
			GTObjects:   []GTBox{{Name: "rust", Xmin: 50, Ymin: 50, Xmax: 150, Ymax: 150}},
			Predictions: []PredBox{},
		},
		// 3. OK image with one low-ish confidence FP (0.35) -> Overkill at conf=0.25, OK->OK at conf=0.50
		{
			ID: 3, Position: 3, SampleKey: "img3.jpg", Path: "img3.jpg", FileName: "img3.jpg", Width: 640, Height: 480,
			GTObjects:   []GTBox{},
			Predictions: []PredBox{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 60, Ymax: 60, Conf: 0.35}},
		},
		// 4. OK image with no predictions -> OK->OK (TN)
		{
			ID: 4, Position: 4, SampleKey: "img4.jpg", Path: "img4.jpg", FileName: "img4.jpg", Width: 640, Height: 480,
			GTObjects:   []GTBox{},
			Predictions: []PredBox{},
		},
		// 5. NG image: 1 scratch TP (borderline IoU ~0.60) + 1 dent FN
		{
			ID: 5, Position: 5, SampleKey: "img5.jpg", Path: "img5.jpg", FileName: "img5.jpg", Width: 640, Height: 480,
			GTObjects: []GTBox{
				{Name: "scratch", Xmin: 0, Ymin: 0, Xmax: 100, Ymax: 100},
				{Name: "dent", Xmin: 300, Ymin: 300, Xmax: 400, Ymax: 400},
			},
			Predictions: []PredBox{
				// overlap 75x100=7500, union 12500 -> IoU=0.60
				{Name: "scratch", Xmin: 25, Ymin: 0, Xmax: 125, Ymax: 100, Conf: 0.85},
			},
		},
		// 6. Failed inference image -> excluded from evaluated counts, counted as failed
		{
			ID: 6, Position: 6, SampleKey: "img6.jpg", Path: "img6.jpg", FileName: "img6.jpg",
			GTObjects:      []GTBox{{Name: "dent", Xmin: 0, Ymin: 0, Xmax: 50, Ymax: 50}},
			InferenceError: "corrupt image",
		},
	}

	run := ComputeAnalysis(42, classes, samples, CurvesData{}, 0.05, 0.25, 0.50)
	a := run.Analysis

	// Check image-level OK/NG stats (5 evaluated + 1 failed):
	// OK images = 2 (img3 overkill, img4 TN) -> OverkillRate = 0.5
	// NG images = 3 (img1 NG->NG, img2 escape, img5 NG->NG) -> EscapeRate = 1/3
	if a.OKNG.FailedImages != 1 || a.OKNG.OKImages != 2 || a.OKNG.NGImages != 3 {
		t.Fatalf("unexpected OK/NG totals: %+v", a.OKNG)
	}
	if a.OKNG.TN != 1 || a.OKNG.Overkill != 1 || a.OKNG.Escape != 1 || a.OKNG.TP != 2 {
		t.Fatalf("unexpected OK/NG matrix: %+v", a.OKNG)
	}
	if !almostEqual(a.OKNG.OverkillRate, 0.5, 1e-4) {
		t.Fatalf("OverkillRate = %v, want 0.5", a.OKNG.OverkillRate)
	}
	if !almostEqual(a.OKNG.EscapeRate, 1.0/3.0, 1e-4) {
		t.Fatalf("EscapeRate = %v, want 0.3333", a.OKNG.EscapeRate)
	}

	// Box-level counts across the 5 evaluated images:
	// GT = 2 + 1 + 0 + 0 + 2 = 5
	// Pred = 3 + 0 + 1 + 0 + 1 = 5
	// TP = 2 (img1 scratch + img5 scratch)
	// Misclassified = 1 (img1 dent->scratch)
	// FP = 1 (misclassified) + 1 (img1 dent) + 1 (img3 scratch) = 3
	// FN = 1 (misclassified) + 1 (img2 rust) + 1 (img5 dent) = 3
	if a.Summary.TotalGT != 5 || a.Summary.TotalPred != 5 || a.Summary.TP != 2 || a.Summary.FP != 3 || a.Summary.FN != 3 || a.Summary.Misclassified != 1 {
		t.Fatalf("unexpected summary counts: %+v", a.Summary)
	}

	// Verify Confusion Matrix mathematical invariants:
	// Row sum for class i == GTCount[i]; Col sum for class j == PredCount[j]; Diag == TP.
	nCls := len(classes)
	for i := 0; i < nCls; i++ {
		rowSum := 0
		for j := 0; j <= nCls; j++ {
			rowSum += a.Confusion.Matrix[i][j]
		}
		if rowSum != a.Classes[i].GTCount {
			t.Errorf("class %s confusion row sum = %d, want GTCount %d", classes[i].Name, rowSum, a.Classes[i].GTCount)
		}
		colSum := 0
		for r := 0; r <= nCls; r++ {
			colSum += a.Confusion.Matrix[r][i]
		}
		if colSum != a.Classes[i].PredCount {
			t.Errorf("class %s confusion col sum = %d, want PredCount %d", classes[i].Name, colSum, a.Classes[i].PredCount)
		}
		if a.Confusion.Matrix[i][i] != a.Classes[i].TP {
			t.Errorf("class %s confusion diag = %d, want TP %d", classes[i].Name, a.Confusion.Matrix[i][i], a.Classes[i].TP)
		}
	}
	// Check misclassified cell: GT=dent(1), Pred=scratch(0) should be 1.
	if a.Confusion.Matrix[1][0] != 1 {
		t.Errorf("confusion[dent][scratch] = %d, want 1", a.Confusion.Matrix[1][0])
	}

	// Now raise conf to 0.50: img3's FP (0.35) and img1's dent FP (0.40) disappear -> Overkill becomes 0!
	runHighConf := ComputeAnalysis(42, classes, samples, CurvesData{}, 0.05, 0.50, 0.50)
	if runHighConf.Analysis.OKNG.Overkill != 0 || runHighConf.Analysis.OKNG.OverkillRate != 0 {
		t.Fatalf("expected 0 overkill at conf=0.50, got %+v", runHighConf.Analysis.OKNG)
	}
	if runHighConf.Analysis.Summary.FP != 1 { // only the misclassified 0.80 remains
		t.Fatalf("expected FP=1 at conf=0.50, got %d", runHighConf.Analysis.Summary.FP)
	}

	// Now raise IoU to 0.75: img5's scratch (IoU=0.60) is no longer TP!
	runHighIoU := ComputeAnalysis(42, classes, samples, CurvesData{}, 0.05, 0.25, 0.75)
	if runHighIoU.Analysis.Summary.TP != 1 {
		t.Fatalf("expected TP=1 at iou=0.75, got %d", runHighIoU.Analysis.Summary.TP)
	}
}

func TestComputeCurvesFromSamplesExactCOCO(t *testing.T) {
	idx0 := 0
	classes := []ClassInfo{{Name: "defect", Color: "#2563eb", InModel: true, InGT: true, ModelIdx: &idx0, GTCount: 1}}

	// Case 1: Perfect box -> AP50 = 1.0, AP75 = 1.0, AP50-95 = 1.0.
	perfect := []*evalSampleData{{
		ID:          1,
		GTObjects:   []GTBox{{Name: "defect", Xmin: 0, Ymin: 0, Xmax: 100, Ymax: 100}},
		Predictions: []PredBox{{Name: "defect", Xmin: 0, Ymin: 0, Xmax: 100, Ymax: 100, Conf: 0.9}},
	}}
	c1 := ComputeCurvesFromSamples(classes, perfect, nil)
	if !almostEqual(c1.MAP50, 1.0, 1e-4) || !almostEqual(c1.MAP75, 1.0, 1e-4) || !almostEqual(c1.MAP50_95, 1.0, 1e-4) {
		t.Fatalf("perfect detector mAP = (%v, %v, %v), want (1, 1, 1)", c1.MAP50, c1.MAP75, c1.MAP50_95)
	}
	if len(c1.PR) != 2 || c1.PR[0].Name != "all" || len(c1.PR[0].X) != 101 {
		t.Fatalf("expected 'all' + 1 class 101-point PR series, got %d series", len(c1.PR))
	}
	if !almostEqual(c1.BestF1, 1.0, 1e-4) {
		t.Fatalf("perfect detector BestF1 = %v, want 1.0", c1.BestF1)
	}

	// Case 2: Single prediction with IoU = 0.60 exactly:
	// Matches thresholds 0.50, 0.55, 0.60 (3 of 10) -> AP50=1.0, AP75=0.0, AP50-95=0.30.
	mid := []*evalSampleData{{
		ID:          1,
		GTObjects:   []GTBox{{Name: "defect", Xmin: 0, Ymin: 0, Xmax: 100, Ymax: 100}},
		Predictions: []PredBox{{Name: "defect", Xmin: 25, Ymin: 0, Xmax: 125, Ymax: 100, Conf: 0.85}},
	}}
	c2 := ComputeCurvesFromSamples(classes, mid, nil)
	if !almostEqual(c2.MAP50, 1.0, 1e-4) || !almostEqual(c2.MAP75, 0.0, 1e-4) || !almostEqual(c2.MAP50_95, 0.30, 1e-4) {
		t.Fatalf("IoU=0.60 detector mAP = (%v, %v, %v), want (1.0, 0.0, 0.30)", c2.MAP50, c2.MAP75, c2.MAP50_95)
	}
}

func TestOfflineReportRendering(t *testing.T) {
	idx0 := 0
	classes := []ClassInfo{{Name: "scratch", Color: "#2563eb", InModel: true, InGT: true, ModelIdx: &idx0, GTCount: 1}}
	samples := []*evalSampleData{{
		ID: 1, Position: 1, SampleKey: "a.jpg", Path: "val/a.jpg", FileName: "a.jpg",
		GTObjects:   []GTBox{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 100, Ymax: 100}},
		Predictions: []PredBox{{Name: "scratch", Xmin: 10, Ymin: 10, Xmax: 100, Ymax: 100, Conf: 0.9}},
	}}
	run := ComputeAnalysis(1, classes, samples, CurvesData{}, 0.05, 0.25, 0.50)
	ev := Evaluation{
		ID: 1, ProjectName: "焊点质检", Name: "v2 验收评估",
		Model:  ModelRef{ProjectName: "焊点质检", Version: 2, YOLOVersion: "yolo11", Scale: "m", Task: "detect"},
		Source: SourceRef{Type: SourceDataVersion, TotalImages: 1, NGImages: 1, TotalObjects: 1},
	}

	htmlDoc := string(RenderEvaluationHTML(ev, run.Analysis, nil))
	if !strings.Contains(htmlDoc, "<svg") {
		t.Fatalf("HTML report missing embedded SVG charts")
	}
	if strings.Contains(htmlDoc, "http://") || strings.Contains(htmlDoc, "https://") {
		t.Fatalf("HTML report must be strictly offline with zero external URLs")
	}
	if !strings.Contains(htmlDoc, "window.print()") {
		t.Fatalf("HTML report missing print-to-PDF support")
	}

	csvBytes, err := renderClassesCSV(run.Analysis)
	if err != nil {
		t.Fatalf("renderClassesCSV error: %v", err)
	}
	if len(csvBytes) < 3 || csvBytes[0] != 0xEF || csvBytes[1] != 0xBB || csvBytes[2] != 0xBF {
		t.Fatalf("classes CSV missing UTF-8 BOM")
	}

	cmpHTML := string(RenderCompareHTML(CompareResult{
		Mode: "default", SameSource: true, ClassNames: []string{"scratch"},
		Items: []CompareItem{
			{Evaluation: ev, Conf: 0.25, IoU: 0.50, Summary: run.Analysis.Summary, Classes: run.Analysis.Classes, OKNG: run.Analysis.OKNG, PRAll: &run.Analysis.Curves.PR[0], F1All: &run.Analysis.Curves.F1[0]},
			{Evaluation: Evaluation{ID: 2, Name: "v3 候选", Model: ev.Model}, Conf: 0.25, IoU: 0.50, Summary: run.Analysis.Summary, Classes: run.Analysis.Classes, OKNG: run.Analysis.OKNG, PRAll: &run.Analysis.Curves.PR[0], F1All: &run.Analysis.Curves.F1[0]},
		},
	}))
	if !strings.Contains(cmpHTML, "<svg") || strings.Contains(cmpHTML, "https://") {
		t.Fatalf("compare HTML report invalid or has external URL")
	}
}
