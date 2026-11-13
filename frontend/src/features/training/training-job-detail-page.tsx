/**
 * 训练任务详情页（`/training/jobs/:jobId`）：
 * - 顶部状态、进度横幅、报错详情与快捷操作（停止、重试、注册模型入库、复用参数新建）
 * - 4 个标签页：
 *   1. 监控与曲线（核心指标卡片 + 实时 SVG 曲线 + 逐轮指标明细表）
 *   2. 训练日志（按字节偏移增量拉取 + 自动滚屏 + 下载）
 *   3. 评估图表与产物（PR/混淆矩阵/批次检测预览图画廊 + 权重与产物文件下载）
 *   4. 训练参数（全部 86 项 YOLO 参数配置与官方默认值对照 + YAML 复制）
 */
import { useEffect, useMemo, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  Award,
  Box,
  ChevronDown,
  ChevronUp,
  Clock,
  CopyPlus,
  Download,
  FileSpreadsheet,
  FileText,
  Gauge,
  Image as ImageIcon,
  OctagonX,
  PackagePlus,
  RotateCcw,
  Search,
  Server,
  SlidersHorizontal,
  Terminal,
  X,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { ScrollingTabsList } from "@/components/scrolling-tabs-list"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsTrigger } from "@/components/ui/tabs"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import { LoadError } from "@/features/datasets/load-error"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  fetchTrainingJobLogs,
  isActiveTrainingJob,
  trainingArtifactUrl,
  trainingJobQuery,
  trainingParamsSchemaQuery,
  type ArtifactKind,
  type ParamsSchemaResponse,
  type TrainingArtifact,
  type TrainingJob,
} from "@/features/training/api"
import { CopyButton } from "@/features/training/command-block"
import {
  formatLoss,
  formatLr,
  formatMap,
  INIT_MODE_LABELS,
  jobDurationSeconds,
  TASK_SCOPE_LABELS,
  trainingArchText,
  trainingJobTitle,
  trainingProgressPercent,
  trainingProgressText,
  trainingQueueText,
} from "@/features/training/format"
import {
  newTrainingJobPath,
  parsePositiveId,
  TRAINING_JOBS_PATH,
} from "@/features/training/paths"
import { TrainingCurves } from "@/features/training/training-curves"
import {
  canRegisterAnyWeight,
  TrainingJobActionsMenu,
  useTrainingJobActions,
  type TrainingJobActions,
} from "@/features/training/training-job-actions"
import {
  countModifiedParams,
  filterParamSpecs,
  formatParamsYaml,
  formatParamValueText,
  isParamModified,
} from "@/features/training/training-params"
import { TrainingStatusBadge } from "@/features/training/training-status"
import { ApiError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatNumber,
} from "@/lib/format"
import { cn } from "@/lib/utils"

type DetailTab = "overview" | "logs" | "artifacts" | "params"
const DETAIL_TABS: readonly DetailTab[] = [
  "overview",
  "logs",
  "artifacts",
  "params",
]

const ARTIFACT_KIND_LABELS: Record<ArtifactKind, string> = {
  weight: "模型权重",
  plot: "评估图表",
  csv: "指标表格",
  yaml: "配置文件",
  log: "训练日志",
}

const MAX_LOG_BUFFER_CHARS = 512_000

export function TrainingJobDetailPage() {
  const { jobId: rawJobId } = useParams()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  const jobId = parsePositiveId(rawJobId)
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

  const jobQuery = useQuery({
    ...trainingJobQuery(jobId ?? 0),
    enabled: jobId !== null,
  })
  const schemaQuery = useQuery(trainingParamsSchemaQuery())

  const { actions, dialogs, retrying } = useTrainingJobActions({
    onDeleted: () => navigate(TRAINING_JOBS_PATH, { replace: true }),
  })

  if (jobId === null) {
    return <JobNotFoundState />
  }

  if (jobQuery.isPending) {
    return (
      <div className="grid gap-4" aria-busy="true" aria-label="正在加载">
        <Skeleton className="h-12 w-72" />
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    )
  }

  if (jobQuery.error && !jobQuery.data) {
    if (jobQuery.error instanceof ApiError && jobQuery.error.status === 404) {
      return <JobNotFoundState />
    }
    return (
      <LoadError
        title="无法加载训练任务详情"
        error={jobQuery.error}
        retrying={jobQuery.isFetching}
        onRetry={() => jobQuery.refetch()}
      />
    )
  }

  const job = jobQuery.data
  if (!job) {
    return <JobNotFoundState />
  }

  const title = trainingJobTitle(job)
  const active = isActiveTrainingJob(job)
  const canRetry = job.status === "failed" || job.status === "canceled"
  const canRegister = canRegisterAnyWeight(job)
  const durationSec = jobDurationSeconds(job)

  return (
    <div className="grid min-w-0 gap-6">
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={TRAINING_JOBS_PATH}>
            <ArrowLeft />
            返回训练任务列表
          </Link>
        </Button>

        <PageHeader
          title={title}
          description={job.description || undefined}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <TrainingStatusBadge job={job} />
              {active && (
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => actions.cancel(job)}
                >
                  <OctagonX />
                  停止训练
                </Button>
              )}

              {canRetry && (
                <Button
                  size="sm"
                  disabled={retrying}
                  onClick={() => actions.retry(job)}
                >
                  {retrying ? <Spinner aria-hidden /> : <RotateCcw />}
                  重新训练
                </Button>
              )}

              {job.best_model ? (
                <Button variant="outline" size="sm" asChild>
                  <Link to={`/models/${job.best_model.id}`}>
                    <Box />
                    查看入库模型 v{job.best_model.version}
                  </Link>
                </Button>
              ) : null}

              {canRegister && (
                <Button
                  variant={job.best_model ? "outline" : "default"}
                  size="sm"
                  onClick={() => actions.registerModel(job)}
                >
                  <PackagePlus />
                  {job.best_model ? "注册检查点" : "注册模型入库"}
                </Button>
              )}

              <Button variant="outline" size="sm" asChild>
                <Link
                  to={newTrainingJobPath({
                    projectId: job.project_id,
                    from: job.id,
                  })}
                >
                  <CopyPlus />
                  复用参数新建
                </Link>
              </Button>

              <TrainingJobActionsMenu
                job={job}
                actions={actions}
                showDetails={false}
              />
            </div>
          }
        />
      </div>

      {/* 元数据摘要栏 */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl border bg-card px-4 py-3 text-xs text-muted-foreground shadow-sm">
        <span>
          项目：
          <Link
            to={`/projects/${job.project_id}`}
            className="font-medium text-foreground underline-offset-4 hover:underline"
          >
            {job.project_name}
          </Link>
        </span>
        <span>
          数据版本：
          {job.data_version_live ? (
            <Link
              to={dataVersionDetailPath(job.data_version_id)}
              className="font-medium text-foreground underline-offset-4 hover:underline"
            >
              v{job.data_version.version}（训练{" "}
              {formatNumber(job.data_version.train_count)} / 验证{" "}
              {formatNumber(job.data_version.val_count)} / 测试{" "}
              {formatNumber(job.data_version.test_count)}）
            </Link>
          ) : (
            <span className="font-medium text-foreground">
              v{job.data_version.version}（已移入回收站）
            </span>
          )}
        </span>
        <span>
          模型配置：
          <strong className="font-medium text-foreground">
            {trainingArchText(job)} · {INIT_MODE_LABELS[job.init_mode]}
          </strong>
          {job.base_model && (
            <>
              {" "}
              （
              <Link
                to={`/models/${job.base_model.id}`}
                className="text-foreground underline-offset-4 hover:underline"
              >
                基于模型 v{job.base_model.version}
              </Link>
              ）
            </>
          )}
        </span>
        {(job.node || job.result?.node_name) && (
          <span className="inline-flex items-center gap-1">
            <Server className="size-3.5" />
            节点：
            <strong className="font-medium text-foreground">
              {job.node?.name || job.result?.node_name}
            </strong>
            {job.result?.device && ` (${job.result.device})`}
          </span>
        )}
        {durationSec !== null && (
          <span className="inline-flex items-center gap-1 tabular-nums">
            <Clock className="size-3.5" />
            耗时：
            <strong className="font-medium text-foreground">
              {formatDuration(durationSec)}
            </strong>
          </span>
        )}
        <span className="tabular-nums">
          创建于 {formatDateTime(job.created_at)}
        </span>
      </div>

      {/* 活跃训练实时横幅 */}
      {active && <ActiveProgressBanner job={job} />}

      {/* 失败错误提示 */}
      {job.status === "failed" && <FailedErrorAlert job={job} />}

      {/* 详情标签页 */}
      <Tabs
        value={tab}
        onValueChange={(v) => setTab(v as DetailTab)}
        className="grid min-w-0 gap-4"
      >
        <ScrollingTabsList value={tab}>
          <TabsTrigger value="overview" className="gap-1.5">
            <Activity className="size-4" />
            监控与曲线
          </TabsTrigger>
          <TabsTrigger value="logs" className="gap-1.5">
            <Terminal className="size-4" />
            训练日志
          </TabsTrigger>
          <TabsTrigger value="artifacts" className="gap-1.5">
            <ImageIcon className="size-4" />
            评估图表与产物
            {job.artifacts.length > 0 && (
              <Badge
                variant="secondary"
                className="h-5 px-1.5 text-[11px] tabular-nums"
              >
                {job.artifacts.length}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="params" className="gap-1.5">
            <SlidersHorizontal className="size-4" />
            训练参数
          </TabsTrigger>
        </ScrollingTabsList>

        <TabsContent value="overview" className="grid gap-4">
          <OverviewTab job={job} durationSec={durationSec} />
        </TabsContent>

        <TabsContent value="logs" className="grid gap-4">
          <LogsTab key={`${job.id}:${job.attempts}`} job={job} />
        </TabsContent>

        <TabsContent value="artifacts" className="grid gap-4">
          <ArtifactsTab job={job} actions={actions} />
        </TabsContent>

        <TabsContent value="params" className="grid gap-4">
          <ParamsTab job={job} schema={schemaQuery.data} />
        </TabsContent>
      </Tabs>

      {dialogs}
    </div>
  )
}

function ActiveProgressBanner({ job }: { job: TrainingJob }) {
  const percent = trainingProgressPercent(job)
  return (
    <Card className="border-primary/30 bg-primary/[0.02] py-4">
      <CardContent className="grid gap-2.5 px-4 sm:px-6">
        <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
          <span className="flex items-center gap-2 font-medium">
            <Spinner className="size-4" aria-hidden />
            {job.status === "queued"
              ? trainingQueueText(job.queue_position, job.workers_online)
              : trainingProgressText(job)}
          </span>
          {job.metrics.gpu_mem_gb !== undefined &&
            job.metrics.gpu_mem_gb > 0 && (
              <span className="text-xs text-muted-foreground tabular-nums">
                显存占用：{job.metrics.gpu_mem_gb.toFixed(2)} GB
              </span>
            )}
        </div>
        {job.status === "running" && (
          <TaskProgress value={percent} label="训练进度" />
        )}
        {job.status === "queued" && job.workers_online === 0 && (
          <p className="text-xs text-muted-foreground">
            当前没有在线的训练节点，可前往{" "}
            <Link
              to="/training/compute"
              className="font-medium text-foreground underline underline-offset-4"
            >
              计算资源
            </Link>{" "}
            查看节点连接说明。
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function FailedErrorAlert({ job }: { job: TrainingJob }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <Alert variant="destructive">
      <AlertTriangle className="size-4" />
      <AlertTitle>训练任务执行失败</AlertTitle>
      <AlertDescription className="grid gap-2">
        <p className="break-words">{job.error || "训练进程异常退出"}</p>
        {job.error_detail && (
          <div className="grid gap-1.5">
            <Button
              type="button"
              variant="outline"
              size="xs"
              className="w-fit text-foreground"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? <ChevronUp /> : <ChevronDown />}
              {expanded ? "收起异常输出详情" : "查看异常堆栈 / 日志末尾"}
            </Button>
            {expanded && (
              <pre className="max-h-64 overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap text-foreground">
                {job.error_detail}
              </pre>
            )}
          </div>
        )}
      </AlertDescription>
    </Alert>
  )
}

function OverviewTab({
  job,
  durationSec,
}: {
  job: TrainingJob
  durationSec: number | null
}) {
  const bestMap50 = job.metrics.best_map50 ?? job.metrics.map50
  const bestMap5095 = job.metrics.best_map50_95 ?? job.metrics.map50_95
  const latestBoxLoss = job.metrics.train_box_loss
  const latestClsLoss = job.metrics.train_cls_loss

  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard title="最佳 mAP50" value={formatMap(bestMap50)} icon={Award}>
          <span>
            {job.metrics.best_epoch
              ? `最佳轮次：第 ${job.metrics.best_epoch} 轮`
              : `最新轮次 mAP50：${formatMap(job.metrics.map50)}`}
          </span>
        </StatCard>

        <StatCard
          title="最佳 mAP50-95"
          value={formatMap(bestMap5095)}
          icon={Gauge}
        >
          <span>
            精确率 {formatMap(job.metrics.precision)} · 召回率{" "}
            {formatMap(job.metrics.recall)}
          </span>
        </StatCard>

        <StatCard
          title="最新训练 Box Loss"
          value={formatLoss(latestBoxLoss)}
          icon={Activity}
        >
          <span>
            Cls {formatLoss(latestClsLoss)} · DFL{" "}
            {formatLoss(job.metrics.train_dfl_loss)}
          </span>
        </StatCard>

        <StatCard
          title="已完成轮次"
          value={`${formatNumber(job.current_epoch)} / ${formatNumber(job.total_epochs)}`}
          icon={Clock}
        >
          <span>
            {durationSec !== null
              ? `累计耗时 ${formatDuration(durationSec)}`
              : "等待训练启动"}
          </span>
        </StatCard>
      </div>

      <TrainingCurves epochs={job.epochs} />

      {job.epochs.length > 0 && (
        <Card className="py-4">
          <CardHeader className="px-4 pb-2 sm:px-6">
            <CardTitle className="text-sm font-medium">
              逐轮指标明细（共 {job.epochs.length} 轮）
            </CardTitle>
          </CardHeader>
          <CardContent className="px-0">
            <div className="max-h-96 overflow-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="pl-4 sm:pl-6">轮次</TableHead>
                    <TableHead className="text-right">训练 Box</TableHead>
                    <TableHead className="text-right">训练 Cls</TableHead>
                    <TableHead className="text-right">训练 DFL</TableHead>
                    <TableHead className="text-right">精确率 P</TableHead>
                    <TableHead className="text-right">召回率 R</TableHead>
                    <TableHead className="text-right">mAP50</TableHead>
                    <TableHead className="text-right">mAP50-95</TableHead>
                    <TableHead className="pr-4 text-right sm:pr-6">
                      学习率 LR
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {[...job.epochs].reverse().map((ep) => {
                    const isBest =
                      job.metrics.best_epoch !== undefined &&
                      ep.epoch === job.metrics.best_epoch
                    return (
                      <TableRow
                        key={ep.epoch}
                        className={cn(isBest && "bg-emerald-500/[0.06]")}
                      >
                        <TableCell className="pl-4 font-medium tabular-nums sm:pl-6">
                          Epoch {ep.epoch}
                          {isBest && (
                            <Badge
                              variant="outline"
                              className="ml-2 h-4 border-emerald-500/40 px-1 text-[10px] text-emerald-700 dark:text-emerald-400"
                            >
                              最佳
                            </Badge>
                          )}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatLoss(ep.train_box_loss)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatLoss(ep.train_cls_loss)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatLoss(ep.train_dfl_loss)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatMap(ep.precision)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatMap(ep.recall)}
                        </TableCell>
                        <TableCell className="text-right font-medium tabular-nums">
                          {formatMap(ep.map50)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatMap(ep.map50_95)}
                        </TableCell>
                        <TableCell className="pr-4 text-right tabular-nums sm:pr-6">
                          {formatLr(ep.lr)}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function LogsTab({ job }: { job: TrainingJob }) {
  const [logText, setLogText] = useState("")
  const [totalBytes, setTotalBytes] = useState(0)
  const [truncatedHead, setTruncatedHead] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [autoScroll, setAutoScroll] = useState(true)

  const nextOffsetRef = useRef<number>(-1)
  const preRef = useRef<HTMLPreElement>(null)
  const active = isActiveTrainingJob(job)

  useEffect(() => {
    let canceled = false

    const pullLogs = async () => {
      try {
        const slice = await fetchTrainingJobLogs(
          job.id,
          nextOffsetRef.current,
          131_072
        )
        if (canceled) {
          return
        }
        setTotalBytes(slice.total_bytes)
        if (nextOffsetRef.current < 0) {
          setLogText(slice.content)
          setTruncatedHead(slice.truncated)
        } else if (slice.content) {
          setLogText((prev) => {
            const combined = prev + slice.content
            if (combined.length > MAX_LOG_BUFFER_CHARS) {
              setTruncatedHead(true)
              return combined.slice(combined.length - MAX_LOG_BUFFER_CHARS)
            }
            return combined
          })
        }
        nextOffsetRef.current = slice.next_offset
        setError(null)
      } catch (err) {
        if (!canceled) {
          setError(err instanceof Error ? err.message : "加载训练日志失败")
        }
      } finally {
        if (!canceled) {
          setLoading(false)
        }
      }
    }

    void pullLogs()
    if (!active) {
      return () => {
        canceled = true
      }
    }

    const timer = setInterval(() => {
      void pullLogs()
    }, 2_000)

    return () => {
      canceled = true
      clearInterval(timer)
    }
  }, [job.id, active])

  useEffect(() => {
    if (autoScroll && preRef.current) {
      preRef.current.scrollTop = preRef.current.scrollHeight
    }
  }, [logText, autoScroll])

  const hasTrainLogFile =
    totalBytes > 0 || job.artifacts.some((a) => a.rel_path === "train.log")

  return (
    <Card className="py-4">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-3 px-4 pb-2 sm:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle className="text-sm font-medium">
            实时训练输出日志
          </CardTitle>
          {totalBytes > 0 && (
            <Badge variant="outline" className="font-normal tabular-nums">
              {formatBytes(totalBytes)}
            </Badge>
          )}
          {truncatedHead && (
            <span className="text-xs text-muted-foreground">
              （窗口仅展示末尾日志，完整内容请点击下载）
            </span>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <label className="flex cursor-pointer items-center gap-1.5 text-xs select-none">
            <Switch checked={autoScroll} onCheckedChange={setAutoScroll} />
            自动滚动到底部
          </label>
          {logText && <CopyButton text={logText} label="复制日志" />}
          {hasTrainLogFile && (
            <Button
              type="button"
              variant="outline"
              size="xs"
              onClick={() =>
                startDownload(
                  trainingArtifactUrl(job.id, "train.log", true),
                  `training-job-${job.id}.log`
                )
              }
            >
              <Download />
              下载完整日志
            </Button>
          )}
        </div>
      </CardHeader>

      <CardContent className="px-4 sm:px-6">
        {loading && !logText ? (
          <Skeleton className="h-96 w-full rounded-lg" />
        ) : error && !logText ? (
          <p className="py-8 text-center text-sm text-destructive">{error}</p>
        ) : !logText ? (
          <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
            {job.status === "queued"
              ? "任务正在排队等待训练节点领取，启动后将在此实时打印 Ultralytics 训练日志。"
              : "暂无训练日志输出。"}
          </div>
        ) : (
          <pre
            ref={preRef}
            className="h-[28rem] overflow-auto rounded-lg border bg-zinc-950 p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-zinc-100"
          >
            {logText}
          </pre>
        )}
      </CardContent>
    </Card>
  )
}

function ArtifactsTab({
  job,
  actions,
}: {
  job: TrainingJob
  actions: TrainingJobActions
}) {
  const [previewPlot, setPreviewPlot] = useState<TrainingArtifact | null>(null)

  const plots = useMemo(
    () => job.artifacts.filter((a) => a.kind === "plot"),
    [job.artifacts]
  )

  if (job.artifacts.length === 0) {
    return (
      <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
        {isActiveTrainingJob(job)
          ? "训练完成后，节点会自动回传最佳权重（best.pt / last.pt）、损失与 PR 曲线图、混淆矩阵及验证批次预览图。"
          : "该训练任务未产生可下载的产物文件。"}
      </div>
    )
  }

  return (
    <div className="grid gap-6">
      {plots.length > 0 && (
        <div className="grid gap-3">
          <h3 className="text-sm font-medium">
            评估图表与批次检测对比预览（点击放大查看）
          </h3>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {plots.map((art) => (
              <button
                key={art.rel_path}
                type="button"
                onClick={() => setPreviewPlot(art)}
                className="group grid overflow-hidden rounded-xl border bg-card text-left shadow-sm transition-colors hover:border-primary/50"
              >
                <div className="aspect-[4/3] w-full overflow-hidden bg-muted/40 p-2">
                  <img
                    src={trainingArtifactUrl(job.id, art.rel_path)}
                    alt={art.name}
                    loading="lazy"
                    className="size-full object-contain transition-transform group-hover:scale-[1.02]"
                  />
                </div>
                <div className="flex items-center justify-between gap-2 border-t px-3 py-2 text-xs">
                  <span className="truncate font-medium">{art.name}</span>
                  <span className="shrink-0 text-muted-foreground tabular-nums">
                    {formatBytes(art.size_bytes)}
                  </span>
                </div>
              </button>
            ))}
          </div>
        </div>
      )}

      <Card className="py-4">
        <CardHeader className="px-4 pb-2 sm:px-6">
          <CardTitle className="text-sm font-medium">
            全部训练产物文件（{job.artifacts.length}）
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4 sm:pl-6">文件路径</TableHead>
                <TableHead>类型</TableHead>
                <TableHead className="text-right">大小</TableHead>
                <TableHead className="pr-4 text-right sm:pr-6">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {job.artifacts.map((art) => {
                const isBest = art.rel_path === "weights/best.pt"
                const isLast = art.rel_path === "weights/last.pt"
                const registeredRef = isBest
                  ? job.best_model
                  : isLast
                    ? job.last_model
                    : null

                return (
                  <TableRow key={art.rel_path}>
                    <TableCell className="pl-4 font-mono text-xs sm:pl-6">
                      <span className="inline-flex items-center gap-2">
                        <ArtifactIcon kind={art.kind} />
                        {art.rel_path}
                      </span>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="font-normal">
                        {ARTIFACT_KIND_LABELS[art.kind]}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-right text-muted-foreground tabular-nums">
                      {formatBytes(art.size_bytes)}
                    </TableCell>
                    <TableCell className="pr-4 text-right sm:pr-6">
                      <div className="inline-flex items-center gap-1.5">
                        {registeredRef ? (
                          <Button variant="ghost" size="xs" asChild>
                            <Link to={`/models/${registeredRef.id}`}>
                              已入库 v{registeredRef.version}
                            </Link>
                          </Button>
                        ) : isBest || isLast ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="xs"
                            onClick={() =>
                              actions.registerModel(
                                job,
                                isBest ? "best" : "last"
                              )
                            }
                          >
                            <PackagePlus />
                            注册入库
                          </Button>
                        ) : null}

                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          onClick={() =>
                            startDownload(
                              trainingArtifactUrl(job.id, art.rel_path, true),
                              art.name
                            )
                          }
                        >
                          <Download />
                          下载
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog
        open={previewPlot !== null}
        onOpenChange={(open) => !open && setPreviewPlot(null)}
      >
        <DialogContent className="max-h-[90vh] sm:max-w-4xl">
          {previewPlot && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center justify-between gap-3 pr-6 text-sm">
                  <span>{previewPlot.name}</span>
                  <Button
                    type="button"
                    variant="outline"
                    size="xs"
                    onClick={() =>
                      startDownload(
                        trainingArtifactUrl(job.id, previewPlot.rel_path, true),
                        previewPlot.name
                      )
                    }
                  >
                    <Download />
                    下载原图
                  </Button>
                </DialogTitle>
              </DialogHeader>
              <div className="max-h-[75vh] overflow-auto rounded-lg border bg-muted/30 p-2">
                <img
                  src={trainingArtifactUrl(job.id, previewPlot.rel_path)}
                  alt={previewPlot.name}
                  className="mx-auto max-h-[70vh] w-auto object-contain"
                />
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ArtifactIcon({ kind }: { kind: ArtifactKind }) {
  switch (kind) {
    case "weight":
      return <Box className="size-3.5 text-primary" />
    case "plot":
      return <ImageIcon className="size-3.5 text-emerald-600" />
    case "csv":
      return <FileSpreadsheet className="size-3.5 text-amber-600" />
    default:
      return <FileText className="size-3.5 text-muted-foreground" />
  }
}

function ParamsTab({
  job,
  schema,
}: {
  job: TrainingJob
  schema: ParamsSchemaResponse | undefined
}) {
  const [query, setQuery] = useState("")
  const [onlyModified, setOnlyModified] = useState(false)

  if (!schema) {
    return <Skeleton className="h-64 rounded-xl" />
  }

  const modifiedCount = countModifiedParams(schema.specs, job.params)
  const filtered = filterParamSpecs(schema.specs, job.params, {
    group: "all",
    query,
    onlyModified,
  })
  const yamlText = formatParamsYaml(schema.specs, job.params)

  return (
    <Card className="py-4">
      <CardHeader className="flex flex-col gap-3 px-4 pb-3 sm:px-6">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle className="text-sm font-medium">
              本次训练锁定的 YOLO 全量参数
            </CardTitle>
            <Badge variant="outline" className="font-normal tabular-nums">
              共 {schema.specs.length} 项
            </Badge>
            {modifiedCount > 0 && (
              <Badge variant="secondary" className="font-normal tabular-nums">
                自定义修改 {modifiedCount} 项
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-2">
            <label className="flex cursor-pointer items-center gap-1.5 text-xs select-none">
              <Switch
                checked={onlyModified}
                onCheckedChange={setOnlyModified}
              />
              仅看已修改 ({modifiedCount})
            </label>
            <CopyButton text={yamlText} label="复制 args.yaml" />
          </div>
        </div>

        <div className="relative max-w-md">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索参数名或中文说明…"
            className="pr-8 pl-9"
          />
          {query && (
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
              onClick={() => setQuery("")}
            >
              <X />
            </Button>
          )}
        </div>
      </CardHeader>

      <CardContent className="px-0">
        {filtered.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">
            {onlyModified
              ? "本次训练完全使用了 Ultralytics 官方默认参数，没有任何修改项。"
              : "没有匹配的训练参数。"}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4 sm:pl-6">参数名称</TableHead>
                <TableHead>分组</TableHead>
                <TableHead>本次训练设定值</TableHead>
                <TableHead className="pr-4 sm:pr-6">官方默认值</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((spec) => {
                const modified = isParamModified(spec, job.params)
                const groupLabel =
                  schema.groups.find((g) => g.key === spec.group)?.label ??
                  spec.group
                const val =
                  spec.key in job.params ? job.params[spec.key] : spec.default

                return (
                  <TableRow
                    key={spec.key}
                    className={cn(modified && "bg-primary/[0.03]")}
                  >
                    <TableCell className="pl-4 sm:pl-6">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-medium">{spec.label}</span>
                        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                          {spec.key}
                        </code>
                        {spec.task_scope && (
                          <Badge
                            variant="outline"
                            className="h-4 px-1 text-[10px] font-normal"
                          >
                            {TASK_SCOPE_LABELS[spec.task_scope] ??
                              spec.task_scope}
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {groupLabel}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      <span
                        className={cn(modified && "font-semibold text-primary")}
                      >
                        {formatParamValueText(val)}
                      </span>
                    </TableCell>
                    <TableCell className="pr-4 font-mono text-xs text-muted-foreground sm:pr-6">
                      {formatParamValueText(spec.default)}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function JobNotFoundState() {
  return (
    <Empty className="border bg-card">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <AlertTriangle />
        </EmptyMedia>
        <EmptyTitle>找不到这个训练任务</EmptyTitle>
        <EmptyDescription>
          该训练任务可能已被删除，或链接中的任务编号不存在。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to={TRAINING_JOBS_PATH}>返回训练任务列表</Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
