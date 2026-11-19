/**
 * 模型评估管理列表页（`/evaluation`）：
 * - 按项目、数据源类型、状态、关键字筛选评估任务
 * - 展示核心指标（mAP50、mAP50-95、F1、工业过杀/漏杀率、推理速度）与实时进度
 * - 勾选 2–6 个已完成评估后浮出底部对比条，一键进入多模型对比
 * - 支持取消、重试、删除与新建评估入口
 */
import { useMemo, useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  ArrowRightLeft,
  Award,
  Ellipsis,
  ExternalLink,
  Gauge,
  OctagonX,
  Plus,
  RotateCcw,
  Search,
  ShieldAlert,
  Trash2,
  X,
  Zap,
} from "lucide-react"
import { Link, useNavigate, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
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
import { Checkbox } from "@/components/ui/checkbox"
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
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { LoadError } from "@/features/datasets/load-error"
import {
  cancelEvaluation,
  deleteEvaluation,
  evaluationsQuery,
  isActiveEvaluation,
  refreshEvaluations,
  retryEvaluation,
  type Evaluation,
  type EvaluationSourceType,
  type EvaluationStatus,
} from "@/features/evaluation/api"
import {
  evaluationModelLabel,
  evaluationProgressPercent,
  evaluationProgressText,
  evaluationSourceSummary,
  evaluationTitle,
  formatFps,
  formatMetricPercent,
  formatMs,
} from "@/features/evaluation/format"
import {
  evaluationComparePath,
  evaluationDetailPath,
  newEvaluationPath,
  parsePositiveId,
} from "@/features/evaluation/paths"
import { EvaluationStatusBadge } from "@/features/evaluation/status"
import { projectsQuery } from "@/features/projects/api"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"

export function EvaluationsPage() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  const projectId = parsePositiveId(searchParams.get("project"))
  const modelId = parsePositiveId(searchParams.get("model"))
  const sourceType =
    (searchParams.get("source_type") as EvaluationSourceType | null) ?? ""
  const status =
    (searchParams.get("status") as EvaluationStatus | null) ?? ""
  const [search, setSearch] = useState(searchParams.get("q") ?? "")

  const [selectedIds, setSelectedIds] = useState<number[]>([])
  const [deleting, setDeleting] = useState<Evaluation | null>(null)

  const projects = useQuery(projectsQuery())
  const query = useQuery(
    evaluationsQuery({
      projectId,
      modelId,
      sourceType,
      status,
      q: search,
    })
  )

  const cancelMut = useMutation({
    mutationFn: (id: number) => cancelEvaluation(id),
    onSuccess: (ev) => {
      refreshEvaluations(ev.id)
      toast.success("已取消评估任务")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const retryMut = useMutation({
    mutationFn: (id: number) => retryEvaluation(id),
    onSuccess: (ev) => {
      refreshEvaluations(ev.id)
      toast.success("已重新加入评估队列")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: number) => deleteEvaluation(id),
    onSuccess: (_, id) => {
      setDeleting(null)
      setSelectedIds((prev) => prev.filter((x) => x !== id))
      refreshEvaluations(id)
      toast.success("已删除评估记录")
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const items = useMemo(() => query.data?.items ?? [], [query.data])

  const stats = useMemo(() => {
    const active = items.filter(isActiveEvaluation).length
    const succeeded = items.filter((e) => e.status === "succeeded")
    const bestMap =
      succeeded.length > 0
        ? Math.max(...succeeded.map((e) => e.summary.map50))
        : null
    const bestEval =
      succeeded.length > 0
        ? succeeded.reduce((best, cur) =>
            cur.summary.map50 > best.summary.map50 ? cur : best
          )
        : null
    return {
      total: items.length,
      active,
      succeeded: succeeded.length,
      bestMap,
      bestEval,
    }
  }, [items])

  const selectedEvaluations = useMemo(
    () => items.filter((it) => selectedIds.includes(it.id)),
    [items, selectedIds]
  )

  const sameSourceSelected = useMemo(() => {
    if (selectedEvaluations.length < 2) {
      return false
    }
    const fp = selectedEvaluations[0].source_fingerprint
    return Boolean(
      fp && selectedEvaluations.every((e) => e.source_fingerprint === fp)
    )
  }, [selectedEvaluations])

  const updateParam = (key: string, value: string | null) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (!value) {
          next.delete(key)
        } else {
          next.set(key, value)
        }
        return next
      },
      { replace: true }
    )
  }

  const toggleSelect = (ev: Evaluation) => {
    if (ev.status !== "succeeded") {
      return
    }
    setSelectedIds((prev) => {
      if (prev.includes(ev.id)) {
        return prev.filter((id) => id !== ev.id)
      }
      if (prev.length >= 6) {
        toast.info("最多同时选择 6 个评估任务进行对比")
        return prev
      }
      return [...prev, ev.id]
    })
  }

  return (
    <div className="grid min-w-0 gap-6 pb-16">
      <PageHeader
        title="模型评估"
        description="在冻结数据版本或标注数据集上全面量化检测精度、过杀/漏杀指标与推理延迟，实时调节置信度/IoU 阈值定位漏检与误检，并支持多模型横向对比与离线报告导出。"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {stats.succeeded >= 2 && (
              <Button variant="outline" asChild>
                <Link
                  to={evaluationComparePath(
                    selectedIds.length >= 2
                      ? selectedIds
                      : items
                          .filter((e) => e.status === "succeeded")
                          .slice(0, 2)
                          .map((e) => e.id)
                  )}
                >
                  <ArrowRightLeft />
                  多模型对比
                </Link>
              </Button>
            )}
            <Button asChild>
              <Link
                to={newEvaluationPath({
                  projectId: projectId ?? undefined,
                  modelId: modelId ?? undefined,
                })}
              >
                <Plus />
                新建评估
              </Link>
            </Button>
          </div>
        }
      />

      {/* 顶部概览统计 */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard
          title="评估总数"
          value={formatNumber(stats.total)}
          icon={Gauge}
        >
          <span className="tabular-nums">
            已完成 {formatNumber(stats.succeeded)} · 进行中{" "}
            {formatNumber(stats.active)}
          </span>
        </StatCard>
        <StatCard
          title="最高 mAP50"
          value={formatMetricPercent(stats.bestMap)}
          icon={Award}
        >
          {stats.bestEval ? (
            <Link
              to={evaluationDetailPath(stats.bestEval.id)}
              className="truncate underline-offset-4 hover:text-foreground hover:underline"
            >
              {evaluationTitle(stats.bestEval)}
            </Link>
          ) : (
            <span>暂无已完成评估</span>
          )}
        </StatCard>
        <StatCard
          title="最低漏杀率 (NG→OK)"
          value={
            stats.succeeded > 0
              ? formatMetricPercent(
                  Math.min(
                    ...items
                      .filter(
                        (e) =>
                          e.status === "succeeded" &&
                          e.summary.ok_ng.ng_images > 0
                      )
                      .map((e) => e.summary.ok_ng.escape_rate)
                  )
                )
              : "—"
          }
          icon={ShieldAlert}
        >
          <span>工业质检核心红线指标</span>
        </StatCard>
        <StatCard
          title="最快推理速度"
          value={(() => {
            const speeds = items
              .filter(
                (e) =>
                  e.status === "succeeded" &&
                  e.summary.speed &&
                  e.summary.speed.fps > 0
              )
              .map((e) => e.summary.speed!.fps)
            return speeds.length > 0 ? formatFps(Math.max(...speeds)) : "—"
          })()}
          icon={Zap}
        >
          <span>GPU / CPU 实测端到端吞吐</span>
        </StatCard>
      </div>

      {/* 筛选栏 */}
      <div className="flex flex-wrap items-center gap-2.5">
        <div className="relative min-w-52 flex-1 sm:max-w-72">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索评估名称、项目或模型…"
            className="pl-8"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch("")}
              className="absolute top-1/2 right-2.5 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              aria-label="清除搜索"
            >
              <X className="size-3.5" />
            </button>
          )}
        </div>

        <Select
          value={projectId ? String(projectId) : "__all__"}
          onValueChange={(v) =>
            updateParam("project", v === "__all__" ? null : v)
          }
        >
          <SelectTrigger className="w-44">
            <SelectValue placeholder="按项目筛选" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__">全部项目</SelectItem>
            {projects.data?.items.map((p) => (
              <SelectItem key={p.id} value={String(p.id)}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={sourceType || "__all__"}
          onValueChange={(v) =>
            updateParam("source_type", v === "__all__" ? null : v)
          }
        >
          <SelectTrigger className="w-36">
            <SelectValue placeholder="数据源类型" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__">全部数据源</SelectItem>
            <SelectItem value="data_version">数据版本</SelectItem>
            <SelectItem value="datasets">数据集</SelectItem>
          </SelectContent>
        </Select>

        <Select
          value={status || "__all__"}
          onValueChange={(v) =>
            updateParam("status", v === "__all__" ? null : v)
          }
        >
          <SelectTrigger className="w-32">
            <SelectValue placeholder="状态" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__">全部状态</SelectItem>
            <SelectItem value="succeeded">已完成</SelectItem>
            <SelectItem value="running">评估中</SelectItem>
            <SelectItem value="queued">排队中</SelectItem>
            <SelectItem value="failed">失败</SelectItem>
            <SelectItem value="canceled">已取消</SelectItem>
          </SelectContent>
        </Select>

        {(projectId || modelId || sourceType || status || search) && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setSearch("")
              setSearchParams({}, { replace: true })
            }}
          >
            重置筛选
          </Button>
        )}
      </div>

      {/* 列表内容 */}
      {query.isPending ? (
        <div className="grid gap-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-16 w-full rounded-xl" />
          ))}
        </div>
      ) : query.error && !query.data ? (
        <LoadError
          title="无法加载模型评估列表"
          error={query.error}
          retrying={query.isFetching}
          onRetry={() => query.refetch()}
        />
      ) : items.length === 0 ? (
        <Empty className="border">
          <EmptyHeader className="max-w-lg">
            <EmptyMedia variant="icon">
              <Gauge />
            </EmptyMedia>
            <EmptyTitle>还没有模型评估记录</EmptyTitle>
            <EmptyDescription>
              选择一个或多个已入库模型以及验证集（数据版本或数据集），即可自动下发至在线训练节点运行批量推理，获取 mAP、PR/F1 曲线、混淆矩阵、过杀/漏杀统计与漏检样本定位。
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button asChild>
              <Link to={newEvaluationPath()}>
                <Plus />
                新建模型评估
              </Link>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10 pl-4">
                  <span className="sr-only">选择对比</span>
                </TableHead>
                <TableHead className="min-w-52">评估名称 / 模型</TableHead>
                <TableHead className="min-w-44">评估数据源</TableHead>
                <TableHead className="w-28">状态</TableHead>
                <TableHead className="text-right">mAP50</TableHead>
                <TableHead className="text-right">mAP50-95</TableHead>
                <TableHead className="text-right">F1</TableHead>
                <TableHead className="text-right">过杀 / 漏杀</TableHead>
                <TableHead className="text-right">速度</TableHead>
                <TableHead className="w-28 text-right">时间</TableHead>
                <TableHead className="w-12 pr-3" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((ev) => {
                const isSucc = ev.status === "succeeded"
                const isActive = isActiveEvaluation(ev)
                const checked = selectedIds.includes(ev.id)
                return (
                  <TableRow
                    key={ev.id}
                    className={checked ? "bg-primary/[0.04]" : undefined}
                  >
                    <TableCell className="pl-4">
                      <Checkbox
                        checked={checked}
                        disabled={!isSucc}
                        onCheckedChange={() => toggleSelect(ev)}
                        aria-label={`勾选对比：${evaluationTitle(ev)}`}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="grid min-w-0 gap-0.5">
                        <Link
                          to={evaluationDetailPath(ev.id)}
                          className="truncate font-medium text-foreground underline-offset-4 hover:underline"
                        >
                          {evaluationTitle(ev)}
                        </Link>
                        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                          <Link
                            to={`/projects/${ev.project_id}?tab=evaluations`}
                            className="hover:text-foreground hover:underline"
                          >
                            {ev.project_name}
                          </Link>
                          <span>·</span>
                          <span>{evaluationModelLabel(ev)}</span>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="grid gap-0.5 text-xs">
                        <span className="font-medium text-foreground">
                          {evaluationSourceSummary(ev.source)}
                        </span>
                        <span className="text-muted-foreground tabular-nums">
                          {formatNumber(ev.source.total_images)} 张（OK{" "}
                          {formatNumber(ev.source.ok_images)} / NG{" "}
                          {formatNumber(ev.source.ng_images)}）
                        </span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="grid gap-1">
                        <EvaluationStatusBadge evaluation={ev} showStage />
                        {isActive && (
                          <div className="grid gap-0.5">
                            <Progress
                              value={evaluationProgressPercent(ev)}
                              className="h-1.5 w-28"
                            />
                            <span className="text-[10px] text-muted-foreground tabular-nums">
                              {evaluationProgressText(ev)}
                            </span>
                          </div>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-right font-semibold tabular-nums">
                      {isSucc ? formatMetricPercent(ev.summary.map50) : "—"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {isSucc ? formatMetricPercent(ev.summary.map50_95) : "—"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {isSucc ? (
                        <div>
                          <div className="font-medium">
                            {formatMetricPercent(ev.summary.f1)}
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            @{ev.options.conf.toFixed(2)}
                          </div>
                        </div>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell className="text-right text-xs tabular-nums">
                      {isSucc ? (
                        <div className="grid gap-0.5">
                          <span className="text-amber-700 dark:text-amber-400">
                            过杀{" "}
                            {ev.summary.ok_ng.ok_images > 0
                              ? formatMetricPercent(
                                  ev.summary.ok_ng.overkill_rate
                                )
                              : "—"}
                          </span>
                          <span className="text-destructive">
                            漏杀{" "}
                            {ev.summary.ok_ng.ng_images > 0
                              ? formatMetricPercent(
                                  ev.summary.ok_ng.escape_rate
                                )
                              : "—"}
                          </span>
                        </div>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell className="text-right text-xs tabular-nums">
                      {isSucc && ev.summary.speed ? (
                        <div>
                          <div className="font-medium">
                            {formatMs(ev.summary.speed.total_ms)}
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            {formatFps(ev.summary.speed.fps)}
                          </div>
                        </div>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell
                      className="text-right text-xs text-muted-foreground tabular-nums"
                      title={formatDateTime(ev.created_at)}
                    >
                      {formatRelativeTime(ev.created_at)}
                    </TableCell>
                    <TableCell className="pr-3 text-right">
                      <DropdownMenu modal={false}>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label="更多操作"
                          >
                            <Ellipsis />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-40">
                          <DropdownMenuItem asChild>
                            <Link to={evaluationDetailPath(ev.id)}>
                              <ExternalLink />
                              查看评估详情
                            </Link>
                          </DropdownMenuItem>
                          {isActive && (
                            <DropdownMenuItem
                              onSelect={() => cancelMut.mutate(ev.id)}
                            >
                              <OctagonX />
                              取消评估
                            </DropdownMenuItem>
                          )}
                          {(ev.status === "failed" ||
                            ev.status === "canceled") && (
                            <DropdownMenuItem
                              onSelect={() => retryMut.mutate(ev.id)}
                            >
                              <RotateCcw />
                              重新评估
                            </DropdownMenuItem>
                          )}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            variant="destructive"
                            onSelect={() => setDeleting(ev)}
                          >
                            <Trash2 />
                            删除记录
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}

      {/* 底部多选对比浮条 */}
      {selectedIds.length > 0 && (
        <div className="fixed right-6 bottom-5 left-6 z-40 mx-auto flex max-w-3xl flex-wrap items-center justify-between gap-3 rounded-xl border bg-background/95 px-4 py-3 shadow-lg backdrop-blur-md">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Badge variant="default" className="tabular-nums">
              已选 {selectedIds.length} / 6
            </Badge>
            <span className="text-muted-foreground">
              {selectedIds.length < 2
                ? "请至少勾选 2 个已完成的评估任务进行多模型对比"
                : sameSourceSelected
                  ? "所选评估数据源完全一致，支持同源逐图差异对比！"
                  : "所选评估数据源不同，支持核心指标与曲线横向对比"}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setSelectedIds([])}
            >
              清空选择
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={selectedIds.length < 2}
              onClick={() => navigate(evaluationComparePath(selectedIds))}
            >
              <ArrowRightLeft />
              对比所选 ({selectedIds.length})
            </Button>
          </div>
        </div>
      )}

      {/* 删除确认弹窗 */}
      <AlertDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除评估“{deleting ? evaluationTitle(deleting) : ""}”？
            </AlertDialogTitle>
            <AlertDialogDescription>
              删除后将永久移除该评估任务的指标、曲线与推理缓存文件；不会影响原始模型与数据集。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteMut.isPending}>
              取消
            </AlertDialogCancel>
            <Button
              variant="destructive"
              disabled={deleteMut.isPending}
              onClick={() => deleting && deleteMut.mutate(deleting.id)}
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
