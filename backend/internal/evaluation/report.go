package evaluation

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
)

// ExportFile holds a generated report ready for HTTP download.
type ExportFile struct {
	ContentType string
	FileName    string
	Data        []byte
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

var comparePalette = []string{
	"#2563eb", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#06b6d4",
}

// ExportEvaluationReport generates an offline HTML, JSON, classes_csv, or
// samples_csv report for a succeeded evaluation at the specified filter/thresholds.
func (s *Service) ExportEvaluationReport(ctx context.Context, id int64, format string, f SampleFilter) (ExportFile, error) {
	ev, err := s.Get(ctx, id)
	if err != nil {
		return ExportFile{}, err
	}
	if ev.Status != StatusSucceeded {
		return ExportFile{}, errNotSucceeded
	}
	loaded, err := s.loadEvaluationData(ctx, ev)
	if err != nil {
		return ExportFile{}, err
	}
	conf, iou, err := resolveThresholds(ev, loaded.storeMinConf, f.Conf, f.IoU)
	if err != nil {
		return ExportFile{}, err
	}
	run := ComputeAnalysis(ev.ID, loaded.classes, loaded.samples, loaded.curves, loaded.storeMinConf, conf, iou)

	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "html"
	}
	safeBase := fmt.Sprintf("evaluation-%d", ev.ID)

	switch format {
	case "json":
		samples, err := filterAndSortSamples(ev.ID, run, f)
		if err != nil {
			return ExportFile{}, err
		}
		payload := struct {
			GeneratedAt time.Time      `json:"generated_at"`
			Evaluation  Evaluation     `json:"evaluation"`
			Analysis    AnalysisResult `json:"analysis"`
			Samples     []SampleItem   `json:"samples"`
		}{
			GeneratedAt: time.Now().UTC(),
			Evaluation:  ev,
			Analysis:    run.Analysis,
			Samples:     samples,
		}
		data, merr := json.MarshalIndent(payload, "", "  ")
		if merr != nil {
			return ExportFile{}, merr
		}
		return ExportFile{
			ContentType: "application/json; charset=utf-8",
			FileName:    safeBase + ".json",
			Data:        data,
		}, nil

	case "classes_csv":
		data, cerr := renderClassesCSV(run.Analysis)
		if cerr != nil {
			return ExportFile{}, cerr
		}
		return ExportFile{
			ContentType: "text/csv; charset=utf-8",
			FileName:    safeBase + "-classes.csv",
			Data:        data,
		}, nil

	case "samples_csv":
		samples, err := filterAndSortSamples(ev.ID, run, f)
		if err != nil {
			return ExportFile{}, err
		}
		data, cerr := renderSamplesCSV(samples)
		if cerr != nil {
			return ExportFile{}, cerr
		}
		return ExportFile{
			ContentType: "text/csv; charset=utf-8",
			FileName:    safeBase + "-samples.csv",
			Data:        data,
		}, nil

	case "html":
		errFilter := f
		if errFilter.ErrorType == "" {
			errFilter.ErrorType = "any_error"
		}
		if errFilter.Sort == "" {
			errFilter.Sort = "errors_desc"
		}
		errSamples, err := filterAndSortSamples(ev.ID, run, errFilter)
		if err != nil {
			return ExportFile{}, err
		}
		if len(errSamples) > 50 {
			errSamples = errSamples[:50]
		}
		htmlBytes := RenderEvaluationHTML(ev, run.Analysis, errSamples)
		return ExportFile{
			ContentType: "text/html; charset=utf-8",
			FileName:    safeBase + "-report.html",
			Data:        htmlBytes,
		}, nil

	default:
		return ExportFile{}, newError(ErrBadRequest, "不支持的导出格式：%s（可选 html、json、classes_csv、samples_csv）", format)
	}
}

// ExportCompareReport generates an offline HTML or CSV comparison report for 2–6 evaluations.
func (s *Service) ExportCompareReport(ctx context.Context, ids []int64, format, mode string, confPtr, iouPtr *float64) (ExportFile, error) {
	cmpRes, err := s.Compare(ctx, ids, mode, confPtr, iouPtr)
	if err != nil {
		return ExportFile{}, err
	}
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "html"
	}
	idParts := make([]string, len(cmpRes.Items))
	for i, it := range cmpRes.Items {
		idParts[i] = strconv.FormatInt(it.Evaluation.ID, 10)
	}
	base := "evaluation-compare-" + strings.Join(idParts, "-")

	switch format {
	case "csv":
		data, cerr := renderCompareCSV(cmpRes)
		if cerr != nil {
			return ExportFile{}, cerr
		}
		return ExportFile{
			ContentType: "text/csv; charset=utf-8",
			FileName:    base + ".csv",
			Data:        data,
		}, nil
	case "html":
		return ExportFile{
			ContentType: "text/html; charset=utf-8",
			FileName:    base + ".html",
			Data:        RenderCompareHTML(cmpRes),
		}, nil
	default:
		return ExportFile{}, newError(ErrBadRequest, "不支持的对比导出格式：%s（可选 html、csv）", format)
	}
}

func renderClassesCSV(a AnalysisResult) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(utf8BOM)
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"类别名称", "对齐状态", "标注框数(GT)", "预测框数(Pred)",
		"TP", "FP", "FN", "Precision", "Recall", "F1", "AP50", "AP75", "AP50-95",
	})
	for _, c := range a.Classes {
		status := "已对齐"
		if c.InModel && !c.InGT {
			status = "仅模型有"
		} else if !c.InModel && c.InGT {
			status = "仅标注有"
		}
		_ = w.Write([]string{
			c.Name,
			status,
			strconv.Itoa(c.GTCount),
			strconv.Itoa(c.PredCount),
			strconv.Itoa(c.TP),
			strconv.Itoa(c.FP),
			strconv.Itoa(c.FN),
			fmt.Sprintf("%.4f", c.Precision),
			fmt.Sprintf("%.4f", c.Recall),
			fmt.Sprintf("%.4f", c.F1),
			fmtOptFloat(c.AP50),
			fmtOptFloat(c.AP75),
			fmtOptFloat(c.AP50_95),
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func renderSamplesCSV(samples []SampleItem) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(utf8BOM)
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"序号", "图片路径", "文件名", "划分", "所属数据集",
		"图像判定", "GT数", "预测数", "TP", "FP", "FN", "误分类数", "最高误检置信度", "推理错误",
	})
	for _, s := range samples {
		_ = w.Write([]string{
			strconv.Itoa(s.Position),
			s.Path,
			s.FileName,
			s.Split,
			s.DatasetName,
			outcomeLabelZH(s.ImageOutcome),
			strconv.Itoa(s.GTCount),
			strconv.Itoa(s.PredCount),
			strconv.Itoa(s.TPCount),
			strconv.Itoa(s.FPCount),
			strconv.Itoa(s.FNCount),
			strconv.Itoa(s.MisclassifiedCount),
			fmt.Sprintf("%.4f", s.MaxFPConf),
			s.InferenceError,
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func renderCompareCSV(res CompareResult) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(utf8BOM)
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"评估ID", "评估名称", "项目", "模型版本", "架构", "数据来源",
		"置信度阈值", "IoU阈值",
		"mAP50", "mAP75", "mAP50-95", "Precision", "Recall", "F1", "Best F1", "Best Conf",
		"过杀率(OK->NG)", "漏杀率(NG->OK)", "图级准确率",
		"单图耗时(ms)", "FPS",
	})
	for _, it := range res.Items {
		s := it.Summary
		msStr, fpsStr := "", ""
		if s.Speed != nil {
			msStr = fmt.Sprintf("%.2f", s.Speed.TotalMs)
			fpsStr = fmt.Sprintf("%.1f", s.Speed.FPS)
		}
		_ = w.Write([]string{
			strconv.FormatInt(it.Evaluation.ID, 10),
			it.Evaluation.Name,
			it.Evaluation.ProjectName,
			fmt.Sprintf("v%d", it.Evaluation.Model.Version),
			it.Evaluation.Model.YOLOVersion + " " + it.Evaluation.Model.Scale,
			sourceSummaryText(it.Evaluation.Source),
			fmt.Sprintf("%.2f", it.Conf),
			fmt.Sprintf("%.2f", it.IoU),
			fmt.Sprintf("%.4f", s.MAP50),
			fmt.Sprintf("%.4f", s.MAP75),
			fmt.Sprintf("%.4f", s.MAP50_95),
			fmt.Sprintf("%.4f", s.Precision),
			fmt.Sprintf("%.4f", s.Recall),
			fmt.Sprintf("%.4f", s.F1),
			fmt.Sprintf("%.4f", s.BestF1),
			fmt.Sprintf("%.2f", s.BestF1Conf),
			fmt.Sprintf("%.4f", it.OKNG.OverkillRate),
			fmt.Sprintf("%.4f", it.OKNG.EscapeRate),
			fmt.Sprintf("%.4f", it.OKNG.Accuracy),
			msStr,
			fpsStr,
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func fmtOptFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.4f", *v)
}

func fmtPct(v float64) string {
	return fmt.Sprintf("%.2f%%", v*100)
}

func outcomeLabelZH(o string) string {
	switch o {
	case OutcomeOKOK:
		return "OK→OK (正确放行)"
	case OutcomeOverkill:
		return "OK→NG (过杀)"
	case OutcomeEscape:
		return "NG→OK (漏杀)"
	case OutcomeNGNG:
		return "NG→NG (正确检出)"
	case OutcomeFailed:
		return "推理失败"
	default:
		return o
	}
}

func sourceSummaryText(src SourceRef) string {
	if src.Type == SourceDataVersion && src.DataVersion != nil {
		return fmt.Sprintf("数据版本 %s v%d [%s]", src.DataVersion.ProjectName, src.DataVersion.Version, strings.Join(src.DataVersion.Splits, "/"))
	}
	if len(src.Datasets) == 1 {
		return fmt.Sprintf("数据集 %s", src.Datasets[0].DatasetName)
	}
	if len(src.Datasets) > 1 {
		return fmt.Sprintf("%d 个数据集 (%s 等)", len(src.Datasets), src.Datasets[0].DatasetName)
	}
	return "数据集"
}

// ---- Offline HTML Report Rendering (zero external dependencies) ----

const reportBaseCSS = `
:root {
  --bg: #f8fafc; --card: #ffffff; --ink: #0f172a; --muted: #64748b;
  --border: #e2e8f0; --primary: #2563eb; --ok: #059669; --warn: #d97706; --bad: #dc2626;
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 28px; background: var(--bg); color: var(--ink);
  font: 14px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
}
.container { max-width: 1180px; margin: 0 auto; }
.header {
  display: flex; justify-content: space-between; align-items: flex-start; gap: 16px;
  background: var(--card); border: 1px solid var(--border); border-radius: 12px;
  padding: 20px 24px; margin-bottom: 18px;
}
.header h1 { margin: 0 0 6px; font-size: 21px; font-weight: 700; }
.meta { color: var(--muted); font-size: 13px; display: flex; flex-wrap: wrap; gap: 8px 18px; }
.badge {
  display: inline-block; padding: 2px 8px; border-radius: 999px; font-size: 12px; font-weight: 600;
  background: #eff6ff; color: #1d4ed8; border: 1px solid #bfdbfe;
}
.badge.ok { background: #ecfdf5; color: #047857; border-color: #a7f3d0; }
.badge.warn { background: #fffbeb; color: #b45309; border-color: #fde68a; }
.badge.bad { background: #fef2f2; color: #b91c1c; border-color: #fecaca; }
.badge.muted { background: #f1f5f9; color: #475569; border-color: #cbd5e1; }
.print-btn {
  padding: 8px 14px; border-radius: 8px; border: 1px solid var(--border); background: #fff;
  color: var(--ink); font-size: 13px; font-weight: 600; cursor: pointer; white-space: nowrap;
}
.print-btn:hover { background: #f1f5f9; }
.kpi-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-bottom: 18px; }
.kpi {
  background: var(--card); border: 1px solid var(--border); border-radius: 10px; padding: 14px 16px;
}
.kpi .label { font-size: 12px; color: var(--muted); }
.kpi .val { font-size: 24px; font-weight: 700; font-variant-numeric: tabular-nums; margin-top: 4px; }
.kpi .sub { font-size: 12px; color: var(--muted); margin-top: 2px; }
.section {
  background: var(--card); border: 1px solid var(--border); border-radius: 12px;
  padding: 18px 20px; margin-bottom: 18px;
}
.section h2 { margin: 0 0 12px; font-size: 16px; font-weight: 700; }
.curves-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
.curve-card { border: 1px solid var(--border); border-radius: 10px; padding: 12px; background: #fff; }
.curve-title { font-size: 13px; font-weight: 600; margin-bottom: 6px; display: flex; justify-content: space-between; }
.legend { display: flex; flex-wrap: wrap; gap: 6px 12px; margin-top: 8px; font-size: 12px; color: var(--muted); }
.legend-item { display: inline-flex; align-items: center; gap: 5px; }
.legend-dot { width: 10px; height: 3px; border-radius: 2px; display: inline-block; }
table { width: 100%; border-collapse: collapse; font-size: 13px; font-variant-numeric: tabular-nums; }
th, td { padding: 8px 10px; border-bottom: 1px solid var(--border); text-align: left; }
th { background: #f8fafc; color: #334155; font-weight: 600; }
td.num, th.num { text-align: right; }
.best { color: var(--ok); font-weight: 700; background: #ecfdf5; }
.delta-pos { color: var(--ok); font-size: 11px; margin-left: 4px; }
.delta-neg { color: var(--bad); font-size: 11px; margin-left: 4px; }
.okng-layout { display: grid; grid-template-columns: 1fr 1.2fr; gap: 18px; align-items: center; }
.matrix-wrap { overflow-x: auto; }
.cm-table th, .cm-table td { text-align: center; border: 1px solid var(--border); padding: 6px 8px; min-width: 56px; }
.footer { text-align: center; color: var(--muted); font-size: 12px; margin-top: 24px; }
@media (max-width: 860px) {
  .kpi-grid { grid-template-columns: repeat(2, 1fr); }
  .curves-grid, .okng-layout { grid-template-columns: 1fr; }
}
@media print {
  body { background: #fff; padding: 0; }
  .print-btn { display: none; }
  .section, .header, .kpi { break-inside: avoid; box-shadow: none; }
}
`

// RenderEvaluationHTML builds a self-contained offline HTML report with embedded SVG curves.
func RenderEvaluationHTML(ev Evaluation, a AnalysisResult, topErrors []SampleItem) []byte {
	var b strings.Builder
	esc := html.EscapeString
	s := a.Summary

	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>模型评估报告 - " + esc(ev.Name) + "</title>")
	b.WriteString("<style>" + reportBaseCSS + "</style></head><body><div class=\"container\">")

	// Header
	b.WriteString("<div class=\"header\"><div>")
	b.WriteString("<h1>" + esc(ev.Name) + "</h1>")
	b.WriteString("<div class=\"meta\">")
	b.WriteString(fmt.Sprintf("<span><strong>项目：</strong>%s</span>", esc(ev.ProjectName)))
	b.WriteString(fmt.Sprintf("<span><strong>模型：</strong>%s v%d (%s %s · %s)</span>",
		esc(ev.Model.ProjectName), ev.Model.Version, esc(ev.Model.YOLOVersion), esc(ev.Model.Scale), esc(ev.Model.Task)))
	b.WriteString(fmt.Sprintf("<span><strong>数据来源：</strong>%s（%d 张，OK %d / NG %d，%d 标注框）</span>",
		esc(sourceSummaryText(ev.Source)), ev.Source.TotalImages, ev.Source.OKImages, ev.Source.NGImages, ev.Source.TotalObjects))
	b.WriteString(fmt.Sprintf("<span><strong>评估阈值：</strong>Conf %.2f · IoU %.2f</span>", a.Conf, a.IoU))
	b.WriteString(fmt.Sprintf("<span><strong>导出时间：</strong>%s</span>", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("</div></div>")
	b.WriteString("<button class=\"print-btn\" onclick=\"window.print()\">打印 / 另存为 PDF</button>")
	b.WriteString("</div>")

	// KPI Grid
	b.WriteString("<div class=\"kpi-grid\">")
	writeKPICard(&b, "mAP@50", fmtPct(s.MAP50), fmt.Sprintf("mAP@75 %s", fmtPct(s.MAP75)))
	writeKPICard(&b, "mAP@50:95", fmtPct(s.MAP50_95), fmt.Sprintf("有效类别 %d", s.EvaluatedClassCount))
	writeKPICard(&b, "Precision / Recall", fmt.Sprintf("%s / %s", fmtPct(s.Precision), fmtPct(s.Recall)),
		fmt.Sprintf("TP %d · FP %d · FN %d", s.TP, s.FP, s.FN))
	writeKPICard(&b, "F1 分数", fmtPct(s.F1), fmt.Sprintf("最佳 F1 %s @ Conf %.2f", fmtPct(s.BestF1), s.BestF1Conf))
	writeKPICard(&b, "过杀率 (OK→NG)", fmtPct(a.OKNG.OverkillRate), fmt.Sprintf("过杀 %d / %d 张 OK 图", a.OKNG.Overkill, a.OKNG.OKImages))
	writeKPICard(&b, "漏杀率 (NG→OK)", fmtPct(a.OKNG.EscapeRate), fmt.Sprintf("漏杀 %d / %d 张 NG 图", a.OKNG.Escape, a.OKNG.NGImages))
	writeKPICard(&b, "图级判定准确率", fmtPct(a.OKNG.Accuracy), fmt.Sprintf("图级 F1 %s", fmtPct(a.OKNG.F1)))
	if s.Speed != nil {
		subParts := []string{fmt.Sprintf("%.1f FPS", s.Speed.FPS)}
		if dev := strings.TrimSpace(s.Speed.Device); dev != "" {
			subParts = append(subParts, esc(dev))
		}
		if s.Speed.BatchSize > 0 {
			subParts = append(subParts, fmt.Sprintf("batch %d", s.Speed.BatchSize))
		}
		writeKPICard(&b, "推理速度", fmt.Sprintf("%.1f ms/图", s.Speed.TotalMs),
			strings.Join(subParts, " · "))
	} else {
		writeKPICard(&b, "样本概况", fmt.Sprintf("%d 张", s.EvaluatedImages), fmt.Sprintf("误分类框 %d", s.Misclassified))
	}
	b.WriteString("</div>")

	// 4 SVG Curves
	b.WriteString("<div class=\"section\"><h2>评估曲线（全阈值 Conf=0.001 精确计算）</h2><div class=\"curves-grid\">")
	writeCurveCard(&b, "PR 曲线 (Precision-Recall)", "Recall", "Precision", a.Curves.PR, nil)
	writeCurveCard(&b, "F1 - 置信度曲线", "Confidence", "F1", a.Curves.F1, &a.Conf)
	writeCurveCard(&b, "Precision - 置信度曲线", "Confidence", "Precision", a.Curves.Precision, &a.Conf)
	writeCurveCard(&b, "Recall - 置信度曲线", "Confidence", "Recall", a.Curves.Recall, &a.Conf)
	b.WriteString("</div></div>")

	// OK / NG Industrial Matrix
	b.WriteString("<div class=\"section\"><h2>工业质检图像级 OK/NG 统计</h2><div class=\"okng-layout\">")
	b.WriteString("<div><table><thead><tr><th>真实 \\ 预测</th><th class=\"num\">预测 OK (无框)</th><th class=\"num\">预测 NG (有框)</th><th class=\"num\">合计</th></tr></thead><tbody>")
	b.WriteString(fmt.Sprintf("<tr><th>真实 OK (良品)</th><td class=\"num best\">%d (TN)</td><td class=\"num\" style=\"color:#b45309;font-weight:600\">%d (过杀)</td><td class=\"num\">%d</td></tr>",
		a.OKNG.TN, a.OKNG.Overkill, a.OKNG.OKImages))
	b.WriteString(fmt.Sprintf("<tr><th>真实 NG (缺陷)</th><td class=\"num\" style=\"color:#b91c1c;font-weight:600\">%d (漏杀)</td><td class=\"num best\">%d (TP)</td><td class=\"num\">%d</td></tr>",
		a.OKNG.Escape, a.OKNG.TP, a.OKNG.NGImages))
	b.WriteString("</tbody></table></div>")
	b.WriteString(fmt.Sprintf(`<div style="color:#334155;font-size:13px">
		<p style="margin:0 0 8px"><strong>判定规则：</strong>任意目标框置信度 ≥ %.2f 即判为 NG（有缺陷），否则判为 OK（良品）。</p>
		<p style="margin:0 0 6px">过杀率 = OK→NG / 真实 OK 总数 = <strong>%s</strong>；漏杀率 = NG→OK / 真实 NG 总数 = <strong>%s</strong>。</p>
		<p style="margin:0">图级 Precision <strong>%s</strong> · 图级 Recall <strong>%s</strong> · 误分类框 <strong>%d</strong>。</p>
	</div>`, a.Conf, fmtPct(a.OKNG.OverkillRate), fmtPct(a.OKNG.EscapeRate), fmtPct(a.OKNG.Precision), fmtPct(a.OKNG.Recall), s.Misclassified))
	b.WriteString("</div></div>")

	// Per-Class Metrics Table
	b.WriteString("<div class=\"section\"><h2>各类别详细指标</h2><div class=\"matrix-wrap\"><table>")
	b.WriteString("<thead><tr><th>类别</th><th>对齐状态</th><th class=\"num\">GT</th><th class=\"num\">Pred</th><th class=\"num\">TP</th><th class=\"num\">FP</th><th class=\"num\">FN</th><th class=\"num\">Precision</th><th class=\"num\">Recall</th><th class=\"num\">F1</th><th class=\"num\">AP50</th><th class=\"num\">AP75</th><th class=\"num\">AP50-95</th></tr></thead><tbody>")
	for _, c := range a.Classes {
		badge := `<span class="badge ok">已对齐</span>`
		if c.InModel && !c.InGT {
			badge = `<span class="badge warn">仅模型有</span>`
		} else if !c.InModel && c.InGT {
			badge = `<span class="badge bad">仅标注有</span>`
		}
		b.WriteString(fmt.Sprintf("<tr><td><span class=\"legend-dot\" style=\"background:%s;height:10px;border-radius:50%%;margin-right:6px\"></span>%s</td><td>%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%s</td><td class=\"num\">%s</td><td class=\"num\"><strong>%s</strong></td><td class=\"num\">%s</td><td class=\"num\">%s</td><td class=\"num\">%s</td></tr>",
			esc(c.Color), esc(c.Name), badge,
			c.GTCount, c.PredCount, c.TP, c.FP, c.FN,
			fmtPct(c.Precision), fmtPct(c.Recall), fmtPct(c.F1),
			fmtOptPct(c.AP50), fmtOptPct(c.AP75), fmtOptPct(c.AP50_95),
		))
	}
	b.WriteString("</tbody></table></div></div>")

	// Confusion Matrix
	if len(a.Confusion.Labels) > 0 {
		b.WriteString("<div class=\"section\"><h2>混淆矩阵（行 = 真实标签，列 = 预测标签）</h2><div class=\"matrix-wrap\">")
		writeConfusionMatrixTable(&b, a.Confusion)
		b.WriteString("</div></div>")
	}

	// Top Error Samples
	b.WriteString(fmt.Sprintf("<div class=\"section\"><h2>典型漏检 / 误检样本（前 %d 张）</h2>", len(topErrors)))
	if len(topErrors) == 0 {
		b.WriteString("<p style=\"color:#059669;margin:0\">当前阈值下无漏检、误检或误分类样本。</p>")
	} else {
		b.WriteString("<div class=\"matrix-wrap\"><table><thead><tr><th>#</th><th>图片路径</th><th>图像判定</th><th class=\"num\">GT</th><th class=\"num\">Pred</th><th class=\"num\">TP</th><th class=\"num\">FP</th><th class=\"num\">FN</th><th class=\"num\">误分类</th><th class=\"num\">最高误检置信度</th></tr></thead><tbody>")
		for _, smp := range topErrors {
			b.WriteString(fmt.Sprintf("<tr><td>%d</td><td>%s</td><td>%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%.2f</td></tr>",
				smp.Position, esc(smp.Path), esc(outcomeLabelZH(smp.ImageOutcome)),
				smp.GTCount, smp.PredCount, smp.TPCount, smp.FPCount, smp.FNCount, smp.MisclassifiedCount, smp.MaxFPConf,
			))
		}
		b.WriteString("</tbody></table></div>")
	}
	b.WriteString("</div>")

	b.WriteString("<div class=\"footer\">由 trainMaster 模型评估模块自动生成 · 离线自包含报告（无外部依赖）</div>")
	b.WriteString("</div></body></html>")
	return []byte(b.String())
}

// RenderCompareHTML builds a self-contained offline HTML multi-model comparison report.
func RenderCompareHTML(res CompareResult) []byte {
	var b strings.Builder
	esc := html.EscapeString

	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>多模型评估对比报告</title>")
	b.WriteString("<style>" + reportBaseCSS + "</style></head><body><div class=\"container\">")

	modeText := "各自默认阈值"
	if res.Mode == "unified" && res.Conf != nil && res.IoU != nil {
		modeText = fmt.Sprintf("统一阈值（Conf %.2f · IoU %.2f）", *res.Conf, *res.IoU)
	}
	sourceBadge := `<span class="badge warn">不同数据源</span>`
	if res.SameSource {
		sourceBadge = `<span class="badge ok">同一数据源（可公平对比）</span>`
	}

	b.WriteString("<div class=\"header\"><div>")
	b.WriteString(fmt.Sprintf("<h1>多模型评估对比报告（%d 个模型）</h1>", len(res.Items)))
	b.WriteString("<div class=\"meta\">")
	b.WriteString("<span>" + sourceBadge + "</span>")
	b.WriteString("<span><strong>阈值模式：</strong>" + esc(modeText) + "</span>")
	b.WriteString("<span><strong>基准模型：</strong>" + esc(res.Items[0].Evaluation.Name) + "</span>")
	b.WriteString("<span><strong>导出时间：</strong>" + time.Now().Format("2006-01-02 15:04:05") + "</span>")
	b.WriteString("</div></div>")
	b.WriteString("<button class=\"print-btn\" onclick=\"window.print()\">打印 / 另存为 PDF</button>")
	b.WriteString("</div>")

	// Core KPI comparison table
	b.WriteString("<div class=\"section\"><h2>核心指标横向对比</h2><div class=\"matrix-wrap\"><table><thead><tr><th>指标</th>")
	for i, it := range res.Items {
		color := comparePalette[i%len(comparePalette)]
		b.WriteString(fmt.Sprintf("<th class=\"num\"><span class=\"legend-dot\" style=\"background:%s;height:10px;border-radius:50%%;margin-right:5px\"></span>%s<div style=\"font-weight:400;font-size:11px;color:#64748b\">%s v%d · Conf %.2f</div></th>",
			color, esc(it.Evaluation.Name), esc(it.Evaluation.Model.ProjectName), it.Evaluation.Model.Version, it.Conf))
	}
	b.WriteString("</tr></thead><tbody>")

	type metricRow struct {
		label      string
		higherGood bool
		isPct      bool
		unit       string
		extract    func(CompareItem) (float64, bool)
	}
	rows := []metricRow{
		{"mAP@50", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.MAP50, true }},
		{"mAP@50:95", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.MAP50_95, true }},
		{"mAP@75", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.MAP75, true }},
		{"Precision", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.Precision, true }},
		{"Recall", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.Recall, true }},
		{"F1 分数", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.F1, true }},
		{"最佳 F1", true, true, "", func(it CompareItem) (float64, bool) { return it.Summary.BestF1, true }},
		{"过杀率 (OK→NG)", false, true, "", func(it CompareItem) (float64, bool) { return it.OKNG.OverkillRate, it.OKNG.OKImages > 0 }},
		{"漏杀率 (NG→OK)", false, true, "", func(it CompareItem) (float64, bool) { return it.OKNG.EscapeRate, it.OKNG.NGImages > 0 }},
		{"图级准确率", true, true, "", func(it CompareItem) (float64, bool) { return it.OKNG.Accuracy, true }},
		{"单图耗时", false, false, " ms", func(it CompareItem) (float64, bool) {
			if it.Summary.Speed == nil {
				return 0, false
			}
			return it.Summary.Speed.TotalMs, true
		}},
		{"吞吐量", true, false, " FPS", func(it CompareItem) (float64, bool) {
			if it.Summary.Speed == nil {
				return 0, false
			}
			return it.Summary.Speed.FPS, true
		}},
	}

	for _, r := range rows {
		vals := make([]float64, len(res.Items))
		okArr := make([]bool, len(res.Items))
		bestVal := 0.0
		hasBest := false
		for i, it := range res.Items {
			v, ok := r.extract(it)
			vals[i], okArr[i] = v, ok
			if !ok {
				continue
			}
			if !hasBest || (r.higherGood && v > bestVal) || (!r.higherGood && v < bestVal) {
				bestVal = v
				hasBest = true
			}
		}
		b.WriteString("<tr><td><strong>" + esc(r.label) + "</strong></td>")
		for i := range res.Items {
			if !okArr[i] {
				b.WriteString("<td class=\"num\" style=\"color:#94a3b8\">—</td>")
				continue
			}
			cls := "num"
			if hasBest && math.Abs(vals[i]-bestVal) < 1e-6 {
				cls = "num best"
			}
			valStr := fmt.Sprintf("%.2f%s", vals[i], r.unit)
			if r.isPct {
				valStr = fmtPct(vals[i])
			}
			deltaHTML := ""
			if i > 0 && okArr[0] {
				d := vals[i] - vals[0]
				if math.Abs(d) >= 1e-4 {
					good := (r.higherGood && d > 0) || (!r.higherGood && d < 0)
					dCls := "delta-neg"
					if good {
						dCls = "delta-pos"
					}
					if r.isPct {
						deltaHTML = fmt.Sprintf("<span class=\"%s\">(%+.2f%%)</span>", dCls, d*100)
					} else {
						deltaHTML = fmt.Sprintf("<span class=\"%s\">(%+.1f)</span>", dCls, d)
					}
				}
			}
			b.WriteString(fmt.Sprintf("<td class=\"%s\">%s%s</td>", cls, valStr, deltaHTML))
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table></div></div>")

	// Overlaid PR & F1 SVG Curves
	prSeries := make([]CurveSeries, 0, len(res.Items))
	f1Series := make([]CurveSeries, 0, len(res.Items))
	for i, it := range res.Items {
		color := comparePalette[i%len(comparePalette)]
		if it.PRAll != nil {
			cp := *it.PRAll
			cp.Color = color
			prSeries = append(prSeries, cp)
		}
		if it.F1All != nil {
			cp := *it.F1All
			cp.Color = color
			f1Series = append(f1Series, cp)
		}
	}
	if len(prSeries) > 0 || len(f1Series) > 0 {
		b.WriteString("<div class=\"section\"><h2>多模型曲线叠加对比</h2><div class=\"curves-grid\">")
		writeCurveCard(&b, "PR 曲线叠加（All Classes）", "Recall", "Precision", prSeries, nil)
		writeCurveCard(&b, "F1 - 置信度曲线叠加（All Classes）", "Confidence", "F1", f1Series, res.Conf)
		b.WriteString("</div></div>")
	}

	// Per-class AP50 & F1 comparison
	if len(res.ClassNames) > 0 {
		b.WriteString("<div class=\"section\"><h2>各类别 AP50 / F1 对比</h2><div class=\"matrix-wrap\"><table><thead><tr><th>类别</th>")
		for _, it := range res.Items {
			b.WriteString("<th class=\"num\">" + esc(it.Evaluation.Name) + "</th>")
		}
		b.WriteString("</tr></thead><tbody>")
		for _, clsName := range res.ClassNames {
			b.WriteString("<tr><td><strong>" + esc(clsName) + "</strong></td>")
			for _, it := range res.Items {
				var found *ClassMetrics
				for j := range it.Classes {
					if it.Classes[j].Name == clsName {
						found = &it.Classes[j]
						break
					}
				}
				if found == nil {
					b.WriteString("<td class=\"num\" style=\"color:#94a3b8\">—</td>")
				} else {
					b.WriteString(fmt.Sprintf("<td class=\"num\">AP50 %s · F1 %s</td>", fmtOptPct(found.AP50), fmtPct(found.F1)))
				}
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</tbody></table></div></div>")
	}

	b.WriteString("<div class=\"footer\">由 trainMaster 模型评估模块自动生成 · 离线自包含对比报告</div>")
	b.WriteString("</div></body></html>")
	return []byte(b.String())
}

func fmtOptPct(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmtPct(*v)
}

func writeKPICard(b *strings.Builder, label, val, sub string) {
	b.WriteString("<div class=\"kpi\">")
	b.WriteString("<div class=\"label\">" + html.EscapeString(label) + "</div>")
	b.WriteString("<div class=\"val\">" + html.EscapeString(val) + "</div>")
	b.WriteString("<div class=\"sub\">" + sub + "</div>")
	b.WriteString("</div>")
}

func writeCurveCard(b *strings.Builder, title, xLabel, yLabel string, series []CurveSeries, markerX *float64) {
	b.WriteString("<div class=\"curve-card\"><div class=\"curve-title\"><span>" + html.EscapeString(title) + "</span></div>")
	b.WriteString(renderCurveSVG(series, xLabel, yLabel, markerX))
	if len(series) > 0 {
		b.WriteString("<div class=\"legend\">")
		limit := min(len(series), 10)
		for i := 0; i < limit; i++ {
			s := series[i]
			c := s.Color
			if c == "" {
				c = comparePalette[i%len(comparePalette)]
			}
			label := s.Name
			if label == "all" {
				label = "全部平均 (all)"
			}
			b.WriteString(fmt.Sprintf("<span class=\"legend-item\"><span class=\"legend-dot\" style=\"background:%s\"></span>%s</span>",
				html.EscapeString(c), html.EscapeString(label)))
		}
		if len(series) > limit {
			b.WriteString(fmt.Sprintf("<span class=\"legend-item\">… 共 %d 条曲线</span>", len(series)))
		}
		b.WriteString("</div>")
	}
	b.WriteString("</div>")
}

func renderCurveSVG(series []CurveSeries, xLabel, yLabel string, markerX *float64) string {
	const (
		w    = 520.0
		h    = 240.0
		padL = 44.0
		padR = 16.0
		padT = 14.0
		padB = 34.0
	)
	plotW := w - padL - padR
	plotH := h - padT - padB

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" style="display:block;height:auto">`, w, h))
	b.WriteString(fmt.Sprintf(`<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="#f8fafc" stroke="#e2e8f0" stroke-width="1"/>`, padL, padT, plotW, plotH))

	// Gridlines & tick labels at 0.0, 0.2, 0.4, 0.6, 0.8, 1.0
	for i := 0; i <= 5; i++ {
		t := float64(i) * 0.2
		gx := padL + t*plotW
		gy := padT + (1-t)*plotH
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e2e8f0" stroke-dasharray="3,3"/>`, gx, padT, gx, padT+plotH))
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e2e8f0" stroke-dasharray="3,3"/>`, padL, gy, padL+plotW, gy))
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-size="10" fill="#64748b">%.1f</text>`, gx, padT+plotH+14, t))
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="end" font-size="10" fill="#64748b">%.1f</text>`, padL-6, gy+3, t))
	}

	// Axis labels
	b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-size="11" fill="#475569">%s</text>`, padL+plotW/2, h-4, html.EscapeString(xLabel)))
	b.WriteString(fmt.Sprintf(`<text x="12" y="%.1f" text-anchor="middle" font-size="11" fill="#475569" transform="rotate(-90 12 %.1f)">%s</text>`, padT+plotH/2, padT+plotH/2, html.EscapeString(yLabel)))

	// Optional vertical threshold marker
	if markerX != nil && *markerX >= 0 && *markerX <= 1 {
		mx := padL + (*markerX)*plotW
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#0f172a" stroke-width="1.2" stroke-dasharray="4,3"/>`, mx, padT, mx, padT+plotH))
	}

	// Polylines: draw per-class lighter first, "all" bold last.
	for i := len(series) - 1; i >= 0; i-- {
		s := series[i]
		n := min(len(s.X), len(s.Y))
		if n == 0 {
			continue
		}
		color := s.Color
		if color == "" {
			color = comparePalette[i%len(comparePalette)]
		}
		strokeW := "1.4"
		opacity := "0.7"
		if s.Name == "all" || len(series) <= 6 {
			strokeW = "2.2"
			opacity = "1"
		}
		var pts strings.Builder
		for j := 0; j < n; j++ {
			if j > 0 {
				pts.WriteByte(' ')
			}
			px := padL + math.Max(0, math.Min(1, s.X[j]))*plotW
			py := padT + (1-math.Max(0, math.Min(1, s.Y[j])))*plotH
			pts.WriteString(fmt.Sprintf("%.1f,%.1f", px, py))
		}
		b.WriteString(fmt.Sprintf(`<polyline fill="none" stroke="%s" stroke-width="%s" stroke-opacity="%s" points="%s"/>`,
			html.EscapeString(color), strokeW, opacity, pts.String()))
	}
	b.WriteString("</svg>")
	return b.String()
}

func writeConfusionMatrixTable(b *strings.Builder, cm ConfusionMatrix) {
	maxVal := 1
	for _, row := range cm.Matrix {
		for _, v := range row {
			if v > maxVal {
				maxVal = v
			}
		}
	}
	b.WriteString("<table class=\"cm-table\"><thead><tr><th>真实 \\ 预测</th>")
	for _, lbl := range cm.Labels {
		name := lbl
		if name == BackgroundLabel {
			name = "背景 (漏检)"
		}
		b.WriteString("<th>" + html.EscapeString(name) + "</th>")
	}
	b.WriteString("<th>行合计</th></tr></thead><tbody>")
	colTotals := make([]int, len(cm.Labels))
	for r, row := range cm.Matrix {
		lbl := cm.Labels[r]
		if lbl == BackgroundLabel {
			lbl = "背景 (误检)"
		}
		b.WriteString("<tr><th>" + html.EscapeString(lbl) + "</th>")
		rowSum := 0
		for c, v := range row {
			rowSum += v
			colTotals[c] += v
			bg := "#ffffff"
			fg := "#64748b"
			if v > 0 {
				ratio := float64(v) / float64(maxVal)
				alpha := 0.12 + 0.72*ratio
				if r == c && r < len(cm.Labels)-1 {
					bg = fmt.Sprintf("rgba(37, 99, 235, %.2f)", alpha)
				} else {
					bg = fmt.Sprintf("rgba(239, 68, 68, %.2f)", alpha*0.85)
				}
				if ratio > 0.55 {
					fg = "#ffffff"
				} else {
					fg = "#0f172a"
				}
			}
			b.WriteString(fmt.Sprintf(`<td style="background:%s;color:%s;font-weight:%s">%d</td>`,
				bg, fg, map[bool]string{true: "600", false: "400"}[v > 0], v))
		}
		b.WriteString(fmt.Sprintf("<td style=\"background:#f8fafc;font-weight:600\">%d</td></tr>", rowSum))
	}
	b.WriteString("<tr><th>列合计</th>")
	grand := 0
	for _, ct := range colTotals {
		grand += ct
		b.WriteString(fmt.Sprintf("<td style=\"background:#f8fafc;font-weight:600\">%d</td>", ct))
	}
	b.WriteString(fmt.Sprintf("<td style=\"background:#f1f5f9;font-weight:700\">%d</td></tr></tbody></table>", grand))
}
