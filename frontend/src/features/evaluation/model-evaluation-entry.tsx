/**
 * 模型详情页上的评估入口：「评估此模型」快捷按钮、元信息链接与历史评估记录卡片。
 */
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, Gauge, Plus } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { modelEvaluationsQuery } from "@/features/evaluation/api"
import {
  evaluationModelBlocker,
  evaluationSourceSummary,
  evaluationTitle,
  formatMetricPercent,
} from "@/features/evaluation/format"
import {
  evaluationDetailPath,
  evaluationsListPath,
  newEvaluationPath,
} from "@/features/evaluation/paths"
import { EvaluationStatusBadge } from "@/features/evaluation/status"
import type { Model } from "@/features/models/api"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"

/** 模型详情页顶部的「评估此模型」快捷按钮。 */
export function EvaluateModelButton({ model }: { model: Model }) {
  const blocker = evaluationModelBlocker(model)
  if (blocker) {
    return (
      <Button variant="outline" disabled title={blocker}>
        <Gauge />
        评估此模型
      </Button>
    )
  }
  return (
    <Button variant="outline" asChild>
      <Link
        to={newEvaluationPath({
          projectId: model.project.id,
          modelId: model.id,
        })}
      >
        <Gauge />
        评估此模型
      </Link>
    </Button>
  )
}

/** 模型详情元信息栏中的历史评估数链接。 */
export function ModelEvaluationsLink({ modelId }: { modelId: number }) {
  const query = useQuery(modelEvaluationsQuery(modelId))
  const count = query.data?.total ?? 0
  if (count === 0) {
    return null
  }
  return (
    <Link
      to={evaluationsListPath({ modelId })}
      className="inline-flex items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
    >
      <Gauge className="size-3.5 shrink-0" />
      {formatNumber(count)} 次模型评估
    </Link>
  )
}

/** 模型详情页底部的评估历史记录卡片。 */
export function ModelEvaluationsCard({ model }: { model: Model }) {
  const query = useQuery(modelEvaluationsQuery(model.id))
  const items = query.data?.items ?? []
  const blocker = evaluationModelBlocker(model)

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          模型评估记录
          {items.length > 0 && (
            <span className="text-sm font-normal text-muted-foreground tabular-nums">
              {formatNumber(items.length)}
            </span>
          )}
        </CardTitle>
        <CardDescription>
          该模型在不同数据版本或数据集上的定量评估历史。
        </CardDescription>
        <CardAction className="flex items-center gap-2">
          {items.length > 0 && (
            <Button variant="ghost" size="sm" asChild>
              <Link to={evaluationsListPath({ modelId: model.id })}>
                查看全部
                <ArrowRight />
              </Link>
            </Button>
          )}
          {!blocker && (
            <Button variant="outline" size="sm" asChild>
              <Link
                to={newEvaluationPath({
                  projectId: model.project.id,
                  modelId: model.id,
                })}
              >
                <Plus />
                发起评估
              </Link>
            </Button>
          )}
        </CardAction>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            还没有针对此模型的评估记录。点击右上角「发起评估」即可在验证集上测算
            mAP、漏杀率与漏检样本。
          </p>
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>评估名称</TableHead>
                  <TableHead>数据源</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">mAP50</TableHead>
                  <TableHead className="text-right">mAP50-95</TableHead>
                  <TableHead className="text-right">F1</TableHead>
                  <TableHead className="text-right">时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.slice(0, 6).map((ev) => {
                  const isSucc = ev.status === "succeeded"
                  return (
                    <TableRow key={ev.id}>
                      <TableCell>
                        <Link
                          to={evaluationDetailPath(ev.id)}
                          className="font-medium underline-offset-4 hover:underline"
                        >
                          {evaluationTitle(ev)}
                        </Link>
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
                      <TableCell
                        className="text-right text-xs text-muted-foreground tabular-nums"
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
        )}
      </CardContent>
    </Card>
  )
}
