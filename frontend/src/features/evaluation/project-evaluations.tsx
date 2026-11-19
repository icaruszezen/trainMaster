/**
 * 项目详情页「模型评估」标签页：展示该项目下的所有评估记录、发起新评估与快速对比入口。
 */
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, ArrowRightLeft, Gauge, Plus } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
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
  isActiveEvaluation,
  projectEvaluationsQuery,
} from "@/features/evaluation/api"
import {
  evaluationModelLabel,
  evaluationSourceSummary,
  evaluationTitle,
  formatMetricPercent,
} from "@/features/evaluation/format"
import {
  evaluationComparePath,
  evaluationDetailPath,
  evaluationsListPath,
  newEvaluationPath,
} from "@/features/evaluation/paths"
import { EvaluationStatusBadge } from "@/features/evaluation/status"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"

export function ProjectEvaluations({
  projectId,
  projectName,
}: {
  projectId: number
  projectName: string
}) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    projectEvaluationsQuery(projectId)
  )
  const items = data?.items ?? []
  const succeeded = items.filter((e) => e.status === "succeeded")
  const activeCount = items.filter(isActiveEvaluation).length
  const createUrl = newEvaluationPath({ projectId })

  if (isPending) {
    return (
      <div className="grid gap-2" aria-busy="true">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-14 rounded-xl" />
        ))}
      </div>
    )
  }

  if (error && data === undefined) {
    return (
      <LoadError
        title="无法获取项目的模型评估记录"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  }

  if (items.length === 0) {
    return (
      <Empty className="border">
        <EmptyHeader className="max-w-lg">
          <EmptyMedia variant="icon">
            <Gauge />
          </EmptyMedia>
          <EmptyTitle className="text-base">项目中还没有模型评估</EmptyTitle>
          <EmptyDescription>
            选择“{projectName}”的模型与数据版本或数据集，量化 mAP、过杀率与漏杀率，并深入定位漏检与误检样本。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" asChild>
            <Link to={createUrl}>
              <Plus />
              新建模型评估
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  }

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground tabular-nums">
          共 {formatNumber(items.length)} 次评估 · 已完成{" "}
          {formatNumber(succeeded.length)}
          {activeCount > 0 ? ` · ${formatNumber(activeCount)} 个进行中` : ""}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          {succeeded.length >= 2 && (
            <Button variant="outline" size="sm" asChild>
              <Link
                to={evaluationComparePath(
                  succeeded.slice(0, 4).map((e) => e.id)
                )}
              >
                <ArrowRightLeft />
                对比最近 {Math.min(4, succeeded.length)} 个
              </Link>
            </Button>
          )}
          <Button variant="ghost" size="sm" asChild>
            <Link to={evaluationsListPath({ projectId })}>
              在模型评估中查看
              <ArrowRight />
            </Link>
          </Button>
          <Button size="sm" asChild>
            <Link to={createUrl}>
              <Plus />
              新建评估
            </Link>
          </Button>
        </div>
      </div>

      <div className="overflow-x-auto rounded-xl border bg-card">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="pl-4">评估名称 / 模型</TableHead>
              <TableHead>评估数据源</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="text-right">mAP50</TableHead>
              <TableHead className="text-right">mAP50-95</TableHead>
              <TableHead className="text-right">F1</TableHead>
              <TableHead className="text-right">过杀 / 漏杀</TableHead>
              <TableHead className="pr-4 text-right">时间</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((ev) => {
              const isSucc = ev.status === "succeeded"
              return (
                <TableRow key={ev.id}>
                  <TableCell className="pl-4">
                    <Link
                      to={evaluationDetailPath(ev.id)}
                      className="font-medium underline-offset-4 hover:underline"
                    >
                      {evaluationTitle(ev)}
                    </Link>
                    <div className="text-xs text-muted-foreground">
                      {evaluationModelLabel(ev)}
                    </div>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {evaluationSourceSummary(ev.source)}
                  </TableCell>
                  <TableCell>
                    <EvaluationStatusBadge evaluation={ev} showStage />
                  </TableCell>
                  <TableCell className="text-right font-semibold tabular-nums">
                    {isSucc ? formatMetricPercent(ev.summary.map50) : "—"}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {isSucc ? formatMetricPercent(ev.summary.map50_95) : "—"}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {isSucc ? formatMetricPercent(ev.summary.f1) : "—"}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    {isSucc
                      ? `${ev.summary.ok_ng.ok_images > 0 ? formatMetricPercent(ev.summary.ok_ng.overkill_rate) : "—"} / ${ev.summary.ok_ng.ng_images > 0 ? formatMetricPercent(ev.summary.ok_ng.escape_rate) : "—"}`
                      : "—"}
                  </TableCell>
                  <TableCell
                    className="pr-4 text-right text-xs text-muted-foreground tabular-nums"
                    title={formatDateTime(ev.created_at)}
                  >
                    {formatRelativeTime(ev.created_at)}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
