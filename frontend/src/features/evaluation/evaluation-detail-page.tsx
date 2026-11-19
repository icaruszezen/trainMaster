/**
 * 模型评估详情页（`/evaluation/:evaluationId`）：
 * - 进行中（queued / running）：实时轮询阶段、进度条、ETA 与取消操作
 * - 已完成（succeeded）：
 *   - 吸顶阈值调节栏：实时防抖滑块（Conf / IoU）、一键跳至最佳 F1、设为默认阈值、离线报告导出菜单、对比快捷入口
 *   - 5 张核心工业评估 KPI 卡片（mAP50、mAP50-95、P/R/F1、工业过杀/漏杀率、推理速度）
 *   - 4 个标签页：指标与曲线 / 混淆矩阵（可点击下钻） / 漏检误检样本（含框叠加检视） / 配置与详情
 */
import { useEffect, useMemo, useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  ArrowRightLeft,
  Award,
  Check,
  ChevronDown,
  ChevronUp,
  Clock,
  Download,
  ExternalLink,
  FileCode2,
  FileJson,
  FileSpreadsheet,
  Gauge,
  Grid3X3,
  Image as ImageIcon,
  OctagonX,
  Pencil,
  RotateCcw,
  Save,
  Server,
  ShieldAlert,
  SlidersHorizontal,
  Sparkles,
  Trash2,
  Zap,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { ScrollingTabsList } from "@/components/scrolling-tabs-list"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { Slider } from "@/components/ui/slider"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import { LoadError } from "@/features/datasets/load-error"
import {
  cancelEvaluation,
  deleteEvaluation,
  evaluationAnalysisQuery,
  evaluationExportUrl,
  evaluationQuery,
  evaluationsQuery,
  isActiveEvaluation,
  refreshEvaluations,
  retryEvaluation,
  updateEvaluation,
  type AnalysisResult,
  type ClassMetrics,
  type Evaluation,
  type OKNGMetrics,
} from "@/features/evaluation/api"
import { ConfusionMatrixView } from "@/features/evaluation/confusion-matrix"
import { EvaluationCurves } from "@/features/evaluation/evaluation-curves"
import {
  evaluationDurationSeconds,
  evaluationModelLabel,
  evaluationProgressPercent,
  evaluationProgressText,
  evaluationSourceSummary,
  evaluationTitle,
  formatFps,
  formatMetricPercent,
  formatMs,
  formatThreshold,
} from "@/features/evaluation/format"
import {
  evaluationComparePath,
  EVALUATIONS_PATH,
  parsePositiveId,
} from "@/features/evaluation/paths"
import { SampleBrowser } from "@/features/evaluation/sample-inspector"
import { EvaluationStatusBadge } from "@/features/evaluation/status"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import { formatDateTime, formatDuration, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type DetailTab = "overview" | "confusion" | "samples" | "config"
const DETAIL_TABS: readonly DetailTab[] = [
  "overview",
  "confusion",
  "samples",
  "config",
]

export function EvaluationDetailPage() {
  const { evaluationId } = useParams()
  const id = parsePositiveId(evaluationId)
  if (id === null) {
    return <EvaluationNotFound />
  }
  return <EvaluationDetail key={id} id={id} />
}

function EvaluationNotFound() {
  usePageTitle("评估任务不存在")
  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Gauge />
        </EmptyMedia>
        <EmptyTitle>评估任务不存在</EmptyTitle>
        <EmptyDescription>该评估记录可能已被删除，或链接有误。</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to={EVALUATIONS_PATH}>
            <ArrowLeft />
            返回模型评估列表
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

function EvaluationDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const rawTab = searchParams.get("tab")
  const tab: DetailTab = DETAIL_TABS.includes(rawTab as DetailTab)
    ? (rawTab as DetailTab)
    : "overview"

  const setTab = (next: DetailTab) => {
    setSearchParams(
      (prev) => {
        const out = new URLSearchParams(prev)
        if (next === "overview") {
          out.delete("tab")
        } else {
          out.set("tab", next)
        }
        return out
      },
      { replace: true }
    )
  }

  const evalQuery = useQuery(evaluationQuery(id))
  const ev = evalQuery.data

  // 滑块即时覆盖值 + 防抖查询覆盖值（null 表示沿用默认/URL 阈值）
  const [sliderConf, setSliderConf] = useState<number | null>(null)
  const [sliderIou, setSliderIou] = useState<number | null>(null)
  const [debouncedConf, setDebouncedConf] = useState<number | null>(null)
  const [debouncedIou, setDebouncedIou] = useState<number | null>(null)
  const [drilldown, setDrilldown] = useState<{
    gtClass: string
    predClass: string
  } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const defaultConf = ev
    ? (() => {
        const qConf = Number(searchParams.get("conf"))
        return Number.isFinite(qConf) &&
          qConf >= ev.options.store_min_conf &&
          qConf <= 0.99
          ? qConf
          : ev.options.conf
      })()
    : undefined
  const defaultIou = ev
    ? (() => {
        const qIou = Number(searchParams.get("iou"))
        return Number.isFinite(qIou) && qIou >= 0.05 && qIou <= 0.99
          ? qIou
          : ev.options.iou
      })()
    : undefined

  // 150ms 防抖将滑块值同步到后端分析请求
  useEffect(() => {
    if (sliderConf === null && sliderIou === null) {
      return
    }
    const timer = setTimeout(() => {
      if (sliderConf !== null) {
        setDebouncedConf(sliderConf)
      }
      if (sliderIou !== null) {
        setDebouncedIou(sliderIou)
      }
    }, 150)
    return () => clearTimeout(timer)
  }, [sliderConf, sliderIou])

  const activeConf = debouncedConf ?? defaultConf
  const activeIou = debouncedIou ?? defaultIou

  const analysisQuery = useQuery({
    ...evaluationAnalysisQuery(id, activeConf, activeIou),
    enabled: ev?.status === "succeeded",
  })

  // 同项目其他已完成评估（用于「加入对比」快捷入口）
  const peersQuery = useQuery({
    ...evaluationsQuery({
      projectId: ev?.project_id ?? undefined,
      status: "succeeded",
    }),
    enabled: Boolean(ev?.project_id && ev.status === "succeeded"),
  })

  const cancelMut = useMutation({
    mutationFn: () => cancelEvaluation(id),
    onSuccess: () => {
      refreshEvaluations(id)
      toast.success("已取消评估任务")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const retryMut = useMutation({
    mutationFn: () => retryEvaluation(id),
    onSuccess: () => {
      refreshEvaluations(id)
      toast.success("已重新加入评估队列")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const saveDefaultMut = useMutation({
    mutationFn: () =>
      updateEvaluation(id, {
        default_conf: activeConf,
        default_iou: activeIou,
      }),
    onSuccess: () => {
      refreshEvaluations(id)
      toast.success(
        `已保存默认工作点（Conf ${formatThreshold(activeConf ?? 0.25)} / IoU ${formatThreshold(activeIou ?? 0.5)}）`
      )
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const deleteMut = useMutation({
    mutationFn: () => deleteEvaluation(id),
    onSuccess: () => {
      refreshEvaluations(id)
      toast.success("已删除评估记录")
      navigate(EVALUATIONS_PATH, { replace: true })
    },
    onError: (err: Error) => toast.error(err.message),
  })

  usePageTitle(ev ? evaluationTitle(ev) : null)

  const comparePeerId = useMemo(() => {
    const peers = (peersQuery.data?.items ?? []).filter((p) => p.id !== id)
    if (peers.length === 0) {
      return null
    }
    const sameSource = peers.find(
      (p) =>
        ev?.source_fingerprint &&
        p.source_fingerprint === ev.source_fingerprint
    )
    return (sameSource ?? peers[0]).id
  }, [peersQuery.data, id, ev])

  if (evalQuery.isPending) {
    return (
      <div className="grid gap-4" aria-busy="true">
        <Skeleton className="h-12 w-72" />
        <Skeleton className="h-24 rounded-xl" />
        <Skeleton className="h-80 rounded-xl" />
      </div>
    )
  }

  if (evalQuery.error instanceof ApiError && evalQuery.error.status === 404) {
    return <EvaluationNotFound />
  }

  if (!ev) {
    return (
      <LoadError
        title="无法获取评估详情"
        error={evalQuery.error}
        retrying={evalQuery.isFetching}
        onRetry={() => evalQuery.refetch()}
      />
    )
  }

  const isActive = isActiveEvaluation(ev)
  const isSucc = ev.status === "succeeded"
  const durationSec = evaluationDurationSeconds(ev)
  const analysis = analysisQuery.data
  const displayConf = sliderConf ?? defaultConf ?? ev.options.conf
  const displayIou = sliderIou ?? defaultIou ?? ev.options.iou
  const minConf = Math.max(0.01, ev.options.store_min_conf || 0.05)
  const thresholdDirty =
    Math.abs(displayConf - ev.options.conf) > 0.004 ||
    Math.abs(displayIou - ev.options.iou) > 0.004

  const summary = analysis?.summary ?? ev.summary
  const okng = analysis?.ok_ng ?? ev.summary.ok_ng
  const classNames = ev.classes.map((c) => c.name)

  const handleDrilldown = (gtClass: string, predClass: string) => {
    setDrilldown({ gtClass, predClass })
    setTab("samples")
  }

  const jumpToBestF1 = () => {
    const target = Math.max(
      minConf,
      Math.min(0.99, Math.round(summary.best_f1_conf * 100) / 100)
    )
    setSliderConf(target)
    setDebouncedConf(target)
  }

  const resetToDefault = () => {
    setSliderConf(ev.options.conf)
    setSliderIou(ev.options.iou)
    setDebouncedConf(ev.options.conf)
    setDebouncedIou(ev.options.iou)
  }

  return (
    <div className="grid min-w-0 gap-5 pb-10">
      {/* 顶部返回 + 标题与主操作 */}
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={EVALUATIONS_PATH}>
            <ArrowLeft />
            返回模型评估列表
          </Link>
        </Button>

        <PageHeader
          title={evaluationTitle(ev)}
          description={ev.description || undefined}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <EvaluationStatusBadge evaluation={ev} showStage />

              {isActive && (
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={cancelMut.isPending}
                  onClick={() => cancelMut.mutate()}
                >
                  <OctagonX />
                  取消评估
                </Button>
              )}

              {(ev.status === "failed" || ev.status === "canceled") && (
                <Button
                  size="sm"
                  disabled={retryMut.isPending}
                  onClick={() => retryMut.mutate()}
                >
                  {retryMut.isPending ? <Spinner aria-hidden /> : <RotateCcw />}
                  重新评估
                </Button>
              )}

              {isSucc && (
                <>
                  <DropdownMenu modal={false}>
                    <DropdownMenuTrigger asChild>
                      <Button variant="outline" size="sm">
                        <Download />
                        导出报告
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-52">
                      <DropdownMenuItem
                        onSelect={() =>
                          window.open(
                            evaluationExportUrl(id, "html", {
                              conf: displayConf,
                              iou: displayIou,
                              inline: true,
                            }),
                            "_blank",
                            "noopener,noreferrer"
                          )
                        }
                      >
                        <ExternalLink />
                        在线预览离线 HTML 报告
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onSelect={() =>
                          startDownload(
                            evaluationExportUrl(id, "html", {
                              conf: displayConf,
                              iou: displayIou,
                            }),
                            `evaluation-${id}-report.html`
                          )
                        }
                      >
                        <FileCode2 />
                        下载单文件 HTML 报告
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onSelect={() =>
                          startDownload(
                            evaluationExportUrl(id, "json", {
                              conf: displayConf,
                              iou: displayIou,
                            }),
                            `evaluation-${id}-summary.json`
                          )
                        }
                      >
                        <FileJson />
                        下载完整指标 JSON
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onSelect={() =>
                          startDownload(
                            evaluationExportUrl(id, "classes_csv", {
                              conf: displayConf,
                              iou: displayIou,
                            }),
                            `evaluation-${id}-classes.csv`
                          )
                        }
                      >
                        <FileSpreadsheet />
                        下载各类别指标 CSV
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onSelect={() =>
                          startDownload(
                            evaluationExportUrl(id, "samples_csv", {
                              conf: displayConf,
                              iou: displayIou,
                            }),
                            `evaluation-${id}-samples.csv`
                          )
                        }
                      >
                        <FileSpreadsheet />
                        下载全量样本明细 CSV
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>

                  {comparePeerId !== null && (
                    <Button variant="outline" size="sm" asChild>
                      <Link to={evaluationComparePath([id, comparePeerId])}>
                        <ArrowRightLeft />
                        多模型对比
                      </Link>
                    </Button>
                  )}
                </>
              )}

              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setConfirmDelete(true)}
                title="删除评估记录"
              >
                <Trash2 className="size-4 text-muted-foreground hover:text-destructive" />
              </Button>
            </div>
          }
        />
      </div>

      {/* 元信息摘要栏 */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl border bg-card px-4 py-2.5 text-xs text-muted-foreground shadow-2xs">
        <span>
          项目：
          <Link
            to={`/projects/${ev.project_id}?tab=evaluations`}
            className="font-medium text-foreground underline-offset-4 hover:underline"
          >
            {ev.project_name}
          </Link>
        </span>
        <span>
          模型：
          {ev.model_exists && ev.model_id ? (
            <Link
              to={`/models/${ev.model_id}`}
              className="font-medium text-foreground underline-offset-4 hover:underline"
            >
              {evaluationModelLabel(ev)}
            </Link>
          ) : (
            <strong className="font-medium text-foreground">
              {evaluationModelLabel(ev)}
            </strong>
          )}
        </span>
        <span>
          数据源：
          {ev.source_type === "data_version" &&
          ev.data_version_id &&
          ev.data_version_live ? (
            <Link
              to={dataVersionDetailPath(ev.data_version_id)}
              className="font-medium text-foreground underline-offset-4 hover:underline"
            >
              {evaluationSourceSummary(ev.source)}
            </Link>
          ) : (
            <strong className="font-medium text-foreground">
              {evaluationSourceSummary(ev.source)}
            </strong>
          )}
          <span className="ml-1 tabular-nums">
            （{formatNumber(ev.source.total_images)} 图 /{" "}
            {formatNumber(ev.source.total_objects)} 框）
          </span>
        </span>
        {ev.node && (
          <span className="inline-flex items-center gap-1">
            <Server className="size-3.5" />
            节点：<strong className="text-foreground">{ev.node.name}</strong>
          </span>
        )}
        {durationSec !== null && (
          <span className="inline-flex items-center gap-1 tabular-nums">
            <Clock className="size-3.5" />
            耗时：
            <strong className="text-foreground">
              {formatDuration(durationSec)}
            </strong>
          </span>
        )}
        <span className="tabular-nums">
          创建于 {formatDateTime(ev.created_at)}
        </span>
      </div>

      {/* 活跃任务实时进度横幅 */}
      {isActive && (
        <Card className="border-primary/35 bg-primary/[0.02] py-4">
          <CardContent className="grid gap-3 px-5">
            <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
              <span className="flex items-center gap-2 font-medium">
                <Spinner className="size-4" aria-hidden />
                {evaluationProgressText(ev)}
              </span>
              <span className="font-mono text-xs text-muted-foreground tabular-nums">
                {evaluationProgressPercent(ev)}%
              </span>
            </div>
            <Progress value={evaluationProgressPercent(ev)} className="h-2" />
            {ev.status === "queued" && ev.workers_online === 0 && (
              <p className="text-xs text-muted-foreground">
                当前没有在线的训练节点，请前往{" "}
                <Link
                  to="/training/compute"
                  className="font-medium text-foreground underline underline-offset-4"
                >
                  计算资源
                </Link>{" "}
                启动训练节点后任务将自动开始执行。
              </p>
            )}
          </CardContent>
        </Card>
      )}

      {/* 失败详情提示 */}
      {ev.status === "failed" && <FailedAlert evaluation={ev} />}

      {/* 吸顶实时阈值调节栏（仅已完成状态） */}
      {isSucc && (
        <div className="sticky top-14 z-20 rounded-xl border bg-background/95 px-4 py-3 shadow-sm backdrop-blur-md">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
            {/* Conf 滑块 */}
            <div className="flex min-w-52 flex-1 items-center gap-3">
              <div className="shrink-0 text-xs">
                <span className="font-medium text-foreground">置信度 Conf</span>
                <span className="ml-1.5 rounded bg-primary/10 px-1.5 py-0.5 font-mono font-semibold text-primary tabular-nums">
                  {formatThreshold(displayConf)}
                </span>
              </div>
              <Slider
                value={[displayConf]}
                min={minConf}
                max={0.99}
                step={0.01}
                onValueChange={([v]) => setSliderConf(v)}
                className="flex-1"
              />
            </div>

            {/* IoU 滑块 */}
            <div className="flex min-w-48 flex-1 items-center gap-3">
              <div className="shrink-0 text-xs">
                <span className="font-medium text-foreground">匹配 IoU</span>
                <span className="ml-1.5 rounded bg-muted px-1.5 py-0.5 font-mono font-semibold text-foreground tabular-nums">
                  {formatThreshold(displayIou)}
                </span>
              </div>
              <Slider
                value={[displayIou]}
                min={0.05}
                max={0.99}
                step={0.05}
                onValueChange={([v]) => setSliderIou(v)}
                className="flex-1"
              />
            </div>

            {/* 快捷工作点按钮 */}
            <div className="flex flex-wrap items-center gap-1.5">
              {summary.best_f1 > 0 && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 text-xs"
                  onClick={jumpToBestF1}
                >
                  <Sparkles className="size-3.5 text-emerald-600" />
                  跳到最佳 F1 ({formatThreshold(summary.best_f1_conf)})
                </Button>
              )}
              {thresholdDirty && (
                <>
                  <Button
                    type="button"
                    size="sm"
                    className="h-7 text-xs"
                    disabled={saveDefaultMut.isPending}
                    onClick={() => saveDefaultMut.mutate()}
                  >
                    {saveDefaultMut.isPending ? (
                      <Spinner aria-hidden />
                    ) : (
                      <Save className="size-3.5" />
                    )}
                    设为默认阈值
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-7 text-xs"
                    onClick={resetToDefault}
                  >
                    重置
                  </Button>
                </>
              )}
              {analysisQuery.isFetching && (
                <Spinner className="size-3.5 text-muted-foreground" />
              )}
            </div>
          </div>
        </div>
      )}

      {/* 核心 KPI 卡片 */}
      {isSucc && (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
          <StatCard
            title="mAP50"
            value={formatMetricPercent(summary.map50)}
            icon={Award}
          >
            <span className="tabular-nums">
              mAP75 {formatMetricPercent(summary.map75)}
            </span>
          </StatCard>
          <StatCard
            title="mAP50-95"
            value={formatMetricPercent(summary.map50_95)}
            icon={Gauge}
          >
            <span className="tabular-nums">
              参与评估 {formatNumber(summary.evaluated_class_count)} 类
            </span>
          </StatCard>
          <StatCard
            title={`F1 (@${formatThreshold(displayConf)})`}
            value={formatMetricPercent(summary.f1)}
            icon={Activity}
          >
            <span className="tabular-nums">
              P {formatMetricPercent(summary.precision)} · R{" "}
              {formatMetricPercent(summary.recall)}
            </span>
          </StatCard>
          <StatCard
            title="漏杀率 / 过杀率"
            value={
              okng.ng_images > 0
                ? formatMetricPercent(okng.escape_rate)
                : "—"
            }
            icon={ShieldAlert}
          >
            <span className="tabular-nums">
              漏杀 {okng.escape} · 过杀 {okng.overkill} (
              {okng.ok_images > 0
                ? formatMetricPercent(okng.overkill_rate)
                : "—"}
              )
            </span>
          </StatCard>
          <StatCard
            title="端到端延迟"
            value={formatMs(summary.speed?.total_ms)}
            icon={Zap}
          >
            <span className="tabular-nums">
              {summary.speed
                ? `${formatFps(summary.speed.fps)} · 推理 ${formatMs(summary.speed.inference_ms)}`
                : "未记录测速"}
            </span>
          </StatCard>
        </div>
      )}

      {/* 主体 4 标签页 */}
      {isSucc ? (
        <Tabs
          value={tab}
          onValueChange={(v) => setTab(v as DetailTab)}
          className="grid min-w-0 gap-4"
        >
          <ScrollingTabsList value={tab}>
            <TabsTrigger value="overview" className="gap-1.5">
              <Activity className="size-4" />
              指标与曲线
            </TabsTrigger>
            <TabsTrigger value="confusion" className="gap-1.5">
              <Grid3X3 className="size-4" />
              混淆矩阵
            </TabsTrigger>
            <TabsTrigger value="samples" className="gap-1.5">
              <ImageIcon className="size-4" />
              漏检误检样本
              <Badge
                variant="secondary"
                className="h-5 px-1.5 text-[11px] tabular-nums"
              >
                {formatNumber(summary.fn + summary.fp)}
              </Badge>
            </TabsTrigger>
            <TabsTrigger value="config" className="gap-1.5">
              <SlidersHorizontal className="size-4" />
              配置与详情
            </TabsTrigger>
          </ScrollingTabsList>

          <TabsContent value="overview" className="grid gap-5">
            {analysis ? (
              <>
                <EvaluationCurves
                  curves={analysis.curves}
                  activeConf={displayConf}
                  onSelectConf={(c) => {
                    const clamped = Math.max(minConf, Math.min(0.99, c))
                    setSliderConf(clamped)
                    setDebouncedConf(clamped)
                  }}
                />
                <OKNGMatrixCard
                  okng={analysis.ok_ng}
                  onClickBucket={(bucket) => {
                    setDrilldown(null)
                    setTab("samples")
                    if (bucket === "overkill") {
                      toast.info("已切换至样本页，可筛选「过杀 (OK→NG)」查看")
                    } else if (bucket === "escape") {
                      toast.info("已切换至样本页，可筛选「漏杀 (NG→OK)」查看")
                    }
                  }}
                />
                <PerClassMetricsTable
                  classes={analysis.classes}
                  onSelectClass={(clsName) => {
                    setDrilldown({ gtClass: clsName, predClass: "background" })
                    setTab("samples")
                  }}
                />
              </>
            ) : (
              <Skeleton className="h-96 rounded-xl" />
            )}
          </TabsContent>

          <TabsContent value="confusion" className="grid gap-4">
            {analysis ? (
              <ConfusionMatrixView
                confusion={analysis.confusion}
                onDrilldown={handleDrilldown}
              />
            ) : (
              <Skeleton className="h-80 rounded-xl" />
            )}
          </TabsContent>

          <TabsContent value="samples" className="grid gap-4">
            <SampleBrowser
              evaluationId={id}
              conf={activeConf ?? ev.options.conf}
              iou={activeIou ?? ev.options.iou}
              classNames={classNames}
              drilldown={drilldown}
              onClearDrilldown={() => setDrilldown(null)}
            />
          </TabsContent>

          <TabsContent value="config" className="grid gap-4">
            <ConfigDetailTab evaluation={ev} analysis={analysis} />
          </TabsContent>
        </Tabs>
      ) : (
        <ConfigDetailTab evaluation={ev} analysis={undefined} />
      )}

      {/* 删除确认弹窗 */}
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除评估“{evaluationTitle(ev)}”？
            </AlertDialogTitle>
            <AlertDialogDescription>
              删除后将永久移除该评估的指标、曲线与推理缓存，不影响原始模型和数据集。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteMut.isPending}>
              取消
            </AlertDialogCancel>
            <Button
              variant="destructive"
              disabled={deleteMut.isPending}
              onClick={() => deleteMut.mutate()}
            >
              {deleteMut.isPending && <Spinner aria-hidden />}
              永久删除
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function FailedAlert({ evaluation }: { evaluation: Evaluation }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <Alert variant="destructive">
      <AlertTriangle className="size-4" />
      <AlertTitle>模型评估执行失败</AlertTitle>
      <AlertDescription className="grid gap-2">
        <p className="break-words">{evaluation.error || "评估进程异常退出"}</p>
        {evaluation.error_detail && (
          <div className="grid gap-1.5">
            <Button
              type="button"
              variant="outline"
              size="xs"
              className="w-fit text-foreground"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? <ChevronUp /> : <ChevronDown />}
              {expanded ? "收起异常堆栈" : "查看异常堆栈"}
            </Button>
            {expanded && (
              <pre className="max-h-64 overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap text-foreground">
                {evaluation.error_detail}
              </pre>
            )}
          </div>
        )}
      </AlertDescription>
    </Alert>
  )
}

function OKNGMatrixCard({
  okng,
  onClickBucket,
}: {
  okng: OKNGMetrics
  onClickBucket?: (bucket: "overkill" | "escape") => void
}) {
  return (
    <Card className="py-4">
      <CardHeader className="px-5 pb-3">
        <CardTitle className="text-base">
          工业质检 · 图片级 OK / NG 判定矩阵
        </CardTitle>
        <CardDescription>
          按整张图片是否有缺陷框统计（无真值框为 OK 良品，含真值框为 NG 缺陷品）；过杀率 = 过杀 / 全部 OK，漏杀率 = 漏杀 / 全部 NG。
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 px-5 lg:grid-cols-[1.2fr_1fr]">
        <div className="overflow-hidden rounded-lg border text-xs">
          <table className="w-full border-collapse">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground">
                <th className="border-r border-b px-3 py-2 text-left font-medium">
                  真实判定 \ 模型判定
                </th>
                <th className="border-r border-b px-3 py-2 text-center font-medium">
                  预测 OK（无框）
                </th>
                <th className="border-b px-3 py-2 text-center font-medium">
                  预测 NG（有框）
                </th>
              </tr>
            </thead>
            <tbody className="tabular-nums">
              <tr>
                <th className="border-r border-b bg-muted/20 px-3 py-3 text-left font-medium">
                  真实 OK（{formatNumber(okng.ok_images)} 张）
                </th>
                <td className="border-r border-b bg-emerald-500/10 px-3 py-3 text-center">
                  <div className="text-lg font-bold text-emerald-700 dark:text-emerald-400">
                    {formatNumber(okng.tn)}
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    OK 正确 (TN)
                  </div>
                </td>
                <td className="border-b bg-amber-500/10 px-3 py-3 text-center">
                  <button
                    type="button"
                    disabled={okng.overkill === 0}
                    onClick={() => onClickBucket?.("overkill")}
                    className="w-full disabled:cursor-default"
                  >
                    <div className="text-lg font-bold text-amber-800 dark:text-amber-400">
                      {formatNumber(okng.overkill)}
                    </div>
                    <div className="text-[11px] text-amber-800/80 dark:text-amber-400/80">
                      过杀 OK→NG ({formatMetricPercent(okng.overkill_rate)})
                    </div>
                  </button>
                </td>
              </tr>
              <tr>
                <th className="border-r bg-muted/20 px-3 py-3 text-left font-medium">
                  真实 NG（{formatNumber(okng.ng_images)} 张）
                </th>
                <td className="border-r bg-rose-500/10 px-3 py-3 text-center">
                  <button
                    type="button"
                    disabled={okng.escape === 0}
                    onClick={() => onClickBucket?.("escape")}
                    className="w-full disabled:cursor-default"
                  >
                    <div className="text-lg font-bold text-destructive">
                      {formatNumber(okng.escape)}
                    </div>
                    <div className="text-[11px] text-destructive/80">
                      漏杀 NG→OK ({formatMetricPercent(okng.escape_rate)})
                    </div>
                  </button>
                </td>
                <td className="bg-blue-500/10 px-3 py-3 text-center">
                  <div className="text-lg font-bold text-blue-700 dark:text-blue-400">
                    {formatNumber(okng.tp)}
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    NG 正确检出 (TP)
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div className="grid grid-cols-2 gap-2.5 text-xs tabular-nums">
          <div className="rounded-lg border p-3">
            <div className="text-muted-foreground">图片级准确率</div>
            <div className="mt-1 text-xl font-bold">
              {formatMetricPercent(okng.accuracy)}
            </div>
          </div>
          <div className="rounded-lg border p-3">
            <div className="text-muted-foreground">图片级 F1</div>
            <div className="mt-1 text-xl font-bold">
              {formatMetricPercent(okng.f1)}
            </div>
          </div>
          <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
            <div className="text-amber-800 dark:text-amber-400">过杀率 (FPR)</div>
            <div className="mt-1 text-xl font-bold text-amber-800 dark:text-amber-400">
              {okng.ok_images > 0
                ? formatMetricPercent(okng.overkill_rate)
                : "—"}
            </div>
          </div>
          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-3">
            <div className="text-destructive">漏杀率 (FNR)</div>
            <div className="mt-1 text-xl font-bold text-destructive">
              {okng.ng_images > 0 ? formatMetricPercent(okng.escape_rate) : "—"}
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

function PerClassMetricsTable({
  classes,
  onSelectClass,
}: {
  classes: readonly ClassMetrics[]
  onSelectClass?: (className: string) => void
}) {
  return (
    <Card className="py-4">
      <CardHeader className="px-5 pb-3">
        <CardTitle className="text-base">
          各类别详细检测指标（共 {classes.length} 类）
        </CardTitle>
        <CardDescription>
          按类别名称对齐模型输出与标注真值；点击任意存在漏检的类别可快速查看对应漏检样本。
        </CardDescription>
      </CardHeader>
      <CardContent className="px-0">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">类别名称</TableHead>
                <TableHead>对齐状态</TableHead>
                <TableHead className="text-right">GT 框</TableHead>
                <TableHead className="text-right">预测框</TableHead>
                <TableHead className="text-right">TP / FP / FN</TableHead>
                <TableHead className="text-right">精确率 P</TableHead>
                <TableHead className="text-right">召回率 R</TableHead>
                <TableHead className="text-right">F1</TableHead>
                <TableHead className="text-right">AP50</TableHead>
                <TableHead className="text-right">AP75</TableHead>
                <TableHead className="pr-5 text-right">AP50-95</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {classes.map((cls) => (
                <TableRow
                  key={cls.name}
                  className={cn(
                    onSelectClass &&
                      cls.fn > 0 &&
                      "cursor-pointer hover:bg-muted/50"
                  )}
                  onClick={() => {
                    if (onSelectClass && cls.fn > 0) {
                      onSelectClass(cls.name)
                    }
                  }}
                >
                  <TableCell className="pl-5 font-medium">
                    <span className="inline-flex items-center gap-2">
                      <span
                        className="inline-block size-2.5 rounded-full"
                        style={{ backgroundColor: cls.color || "#64748b" }}
                      />
                      {cls.name}
                    </span>
                  </TableCell>
                  <TableCell>
                    {cls.in_model && cls.in_gt ? (
                      <Badge
                        variant="outline"
                        className="border-emerald-500/40 bg-emerald-500/10 text-[11px] text-emerald-700 dark:text-emerald-400"
                      >
                        已对齐
                      </Badge>
                    ) : cls.in_model ? (
                      <Badge
                        variant="outline"
                        className="border-amber-500/40 bg-amber-500/10 text-[11px] text-amber-800 dark:text-amber-400"
                      >
                        仅模型有
                      </Badge>
                    ) : (
                      <Badge
                        variant="outline"
                        className="border-rose-500/40 bg-rose-500/10 text-[11px] text-rose-700 dark:text-rose-400"
                      >
                        仅标注有
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatNumber(cls.gt_count)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatNumber(cls.pred_count)}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    <span className="text-blue-700 dark:text-blue-400">
                      {cls.tp}
                    </span>
                    {" / "}
                    <span className="text-rose-700 dark:text-rose-400">
                      {cls.fp}
                    </span>
                    {" / "}
                    <span className="text-amber-700 dark:text-amber-400">
                      {cls.fn}
                    </span>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMetricPercent(cls.precision)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMetricPercent(cls.recall)}
                  </TableCell>
                  <TableCell className="text-right font-medium tabular-nums">
                    {formatMetricPercent(cls.f1)}
                  </TableCell>
                  <TableCell className="text-right font-semibold tabular-nums">
                    {formatMetricPercent(cls.ap50)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMetricPercent(cls.ap75)}
                  </TableCell>
                  <TableCell className="pr-5 text-right tabular-nums">
                    {formatMetricPercent(cls.ap50_95)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  )
}

function ConfigDetailTab({
  evaluation,
  analysis,
}: {
  evaluation: Evaluation
  analysis?: AnalysisResult
}) {
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(evaluation.name)
  const [description, setDescription] = useState(evaluation.description)

  const saveMetaMut = useMutation({
    mutationFn: () =>
      updateEvaluation(evaluation.id, {
        name: name.trim(),
        description: description.trim(),
      }),
    onSuccess: () => {
      setEditing(false)
      refreshEvaluations(evaluation.id)
      toast.success("已更新评估信息")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const speed = analysis?.summary.speed ?? evaluation.summary.speed

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card className="py-4">
        <CardHeader className="flex flex-row items-center justify-between px-5 pb-3">
          <CardTitle className="text-base">基本信息与备注</CardTitle>
          {!editing && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() => {
                setName(evaluation.name)
                setDescription(evaluation.description)
                setEditing(true)
              }}
            >
              <Pencil className="size-3.5" />
              编辑
            </Button>
          )}
        </CardHeader>
        <CardContent className="grid gap-3 px-5 text-sm">
          {editing ? (
            <div className="grid gap-3">
              <div className="grid gap-1.5">
                <Label>评估名称</Label>
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label>备注描述</Label>
                <Textarea
                  rows={3}
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>
              <div className="flex gap-2">
                <Button
                  type="button"
                  size="sm"
                  disabled={saveMetaMut.isPending}
                  onClick={() => saveMetaMut.mutate()}
                >
                  {saveMetaMut.isPending ? <Spinner aria-hidden /> : <Check />}
                  保存
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setEditing(false)}
                >
                  取消
                </Button>
              </div>
            </div>
          ) : (
            <dl className="grid gap-2 text-xs">
              <Row label="评估 ID">#{evaluation.id}</Row>
              <Row label="评估名称">{evaluationTitle(evaluation)}</Row>
              <Row label="备注说明">{evaluation.description || "—"}</Row>
              <Row label="数据源指纹">
                <span className="font-mono break-all">
                  {evaluation.source_fingerprint}
                </span>
              </Row>
              <Row label="创建时间">{formatDateTime(evaluation.created_at)}</Row>
              <Row label="完成时间">
                {evaluation.finished_at
                  ? formatDateTime(evaluation.finished_at)
                  : "—"}
              </Row>
            </dl>
          )}
        </CardContent>
      </Card>

      <Card className="py-4">
        <CardHeader className="px-5 pb-3">
          <CardTitle className="text-base">推理参数与硬件实测</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-2 px-5 text-xs">
          <dl className="grid gap-2">
            <Row label="默认工作点">
              Conf {formatThreshold(evaluation.options.conf)} · IoU{" "}
              {formatThreshold(evaluation.options.iou)}
            </Row>
            <Row label="缓存置信度下限">
              {formatThreshold(evaluation.options.store_min_conf)}（低于此值的框仅参与离线
              mAP 曲线，不参与滑块重算）
            </Row>
            <Row label="NMS / MaxDet">
              NMS IoU {formatThreshold(evaluation.options.nms_iou)} · MaxDet{" "}
              {evaluation.options.max_det}
            </Row>
            <Row label="输入尺寸 / Batch">
              {evaluation.options.imgsz
                ? `${evaluation.options.imgsz[0]}×${evaluation.options.imgsz[1]}`
                : "自动"}
              {" · "}Batch {evaluation.options.batch}
              {evaluation.options.half ? " · FP16" : " · FP32"}
            </Row>
            <Row label="执行节点与设备">
              {evaluation.node?.name || "—"}
              {speed?.device ? ` (${speed.device})` : ""}
            </Row>
            <Row label="耗时拆解">
              {speed
                ? `预处理 ${formatMs(speed.preprocess_ms)} + 推理 ${formatMs(speed.inference_ms)} + 后处理 ${formatMs(speed.postprocess_ms)} = ${formatMs(speed.total_ms)} (${formatFps(speed.fps)})`
                : "—"}
            </Row>
          </dl>
        </CardContent>
      </Card>
    </div>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="grid grid-cols-[7.5rem_1fr] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-foreground">{children}</dd>
    </div>
  )
}
