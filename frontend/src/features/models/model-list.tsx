/**
 * 模型列表：桌面宽度为表格（窄一些时隐藏次要的列），手机宽度为卡片。模型仓库页
 * 与项目详情的“模型”标签页共用；项目页中不显示项目列。
 */
import type { MouseEvent } from "react"
import { Boxes, FileWarning } from "lucide-react"
import { Link, useNavigate } from "react-router"

import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Model } from "@/features/models/api"
import {
  ModelActionsMenu,
  type ModelActions,
} from "@/features/models/model-actions"
import { sourceLabels } from "@/features/models/model-info"
import { modelArchLabel, taskLabel } from "@/features/models/yolo-versions"
import { useMediaQuery } from "@/hooks/use-media-query"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatRelativeTime,
} from "@/lib/format"
import { cn } from "@/lib/utils"

/** 任务与类别数，如“检测 · 2 类”。 */
export function modelTaskText(model: Model) {
  const classes = model.info.class_names.length
  return classes > 0
    ? `${taskLabel(model.task)} · ${formatNumber(classes)} 类`
    : taskLabel(model.task)
}

/** 训练（导出默认）的输入尺寸，未知时为“—”。 */
export function modelInputText(model: Model) {
  return model.info.image_size !== null ? String(model.info.image_size) : "—"
}

export function VersionBadge({
  version,
  className,
}: {
  version: number
  className?: string
}) {
  return (
    <Badge
      variant="secondary"
      className={cn("font-mono font-medium tabular-nums", className)}
    >
      v{version}
    </Badge>
  )
}

function FileMissingBadge() {
  return (
    <Badge variant="destructive" className="font-normal">
      <FileWarning />
      文件缺失
    </Badge>
  )
}

type ModelListProps = {
  models: Model[]
  actions: ModelActions
  /** 显示所属项目（模型仓库页）；项目详情页中为 false。 */
  showProject?: boolean
}

export function ModelList(props: ModelListProps) {
  // 与 Tailwind 的 md: 一致：手机宽度显示卡片。
  const wide = useMediaQuery("(min-width: 48rem)")
  return wide ? <ModelTable {...props} /> : <ModelCards {...props} />
}

function ModelTable({ models, actions, showProject = true }: ModelListProps) {
  const navigate = useNavigate()

  // 点击行的空白处进入详情；链接、按钮与（经 portal 渲染的）菜单各自处理。
  const onRowClick = (event: MouseEvent<HTMLTableRowElement>, id: number) => {
    const target = event.target as HTMLElement
    if (
      !event.currentTarget.contains(target) ||
      target.closest("a, button, [role=menuitem]")
    ) {
      return
    }
    navigate(`/models/${id}`)
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">
              {showProject ? "项目" : "版本"}
            </TableHead>
            {showProject && <TableHead>版本</TableHead>}
            <TableHead>模型</TableHead>
            <TableHead>任务</TableHead>
            <TableHead
              className={cn(
                "hidden text-right",
                showProject ? "xl:table-cell" : "lg:table-cell"
              )}
            >
              输入尺寸
            </TableHead>
            <TableHead className="hidden text-right lg:table-cell">
              大小
            </TableHead>
            <TableHead className="hidden xl:table-cell">来源</TableHead>
            <TableHead className="hidden lg:table-cell">上传时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {models.map((model) => (
            <TableRow
              key={model.id}
              className="cursor-pointer"
              onClick={(event) => onRowClick(event, model.id)}
            >
              {/* 名称列占满剩余宽度，长名称截断而不是挤压其它列。 */}
              <TableCell className="w-full max-w-0 pl-4">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
                    <Boxes className="size-4" />
                  </div>
                  <div className="grid min-w-0 gap-1">
                    <span className="flex min-w-0 items-center gap-2">
                      <Link
                        to={`/models/${model.id}`}
                        className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                        title={showProject ? model.project.name : undefined}
                      >
                        {showProject ? model.project.name : `v${model.version}`}
                      </Link>
                      {model.file_missing && <FileMissingBadge />}
                    </span>
                    <span
                      className="truncate text-xs text-muted-foreground"
                      title={model.description || model.file_name}
                    >
                      {model.description || model.file_name}
                    </span>
                  </div>
                </div>
              </TableCell>
              {showProject && (
                <TableCell>
                  <VersionBadge version={model.version} />
                </TableCell>
              )}
              <TableCell className="font-medium whitespace-nowrap">
                {modelArchLabel(model)}
              </TableCell>
              <TableCell className="whitespace-nowrap text-muted-foreground">
                {modelTaskText(model)}
              </TableCell>
              <TableCell
                className={cn(
                  "hidden text-right tabular-nums",
                  showProject ? "xl:table-cell" : "lg:table-cell"
                )}
              >
                {modelInputText(model)}
              </TableCell>
              <TableCell className="hidden text-right tabular-nums lg:table-cell">
                {formatBytes(model.size_bytes)}
              </TableCell>
              <TableCell className="hidden xl:table-cell">
                <Badge variant="outline" className="font-normal">
                  {sourceLabels[model.source] ?? model.source}
                </Badge>
              </TableCell>
              <TableCell
                className="hidden text-muted-foreground lg:table-cell"
                title={formatDateTime(model.created_at)}
              >
                {formatRelativeTime(model.created_at)}
              </TableCell>
              <TableCell className="pr-4 text-right">
                <ModelActionsMenu model={model} actions={actions} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function ModelCards({ models, actions, showProject = true }: ModelListProps) {
  return (
    <ul className="grid gap-3">
      {models.map((model) => (
        <li key={model.id}>
          <article className="relative grid gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
            <div className="flex items-start gap-3">
              <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted text-muted-foreground">
                <Boxes className="size-4" />
              </div>
              <div className="grid min-w-0 flex-1 gap-0.5">
                <h3 className="flex min-w-0 items-center gap-2 leading-snug font-medium">
                  {/* 伸展的链接覆盖整张卡片；菜单按钮通过 z-[1] 浮在它上面。 */}
                  <Link
                    to={`/models/${model.id}`}
                    className="truncate outline-none after:absolute after:inset-0 after:content-['']"
                  >
                    {showProject ? model.project.name : `v${model.version}`}
                  </Link>
                  {showProject && (
                    <VersionBadge
                      version={model.version}
                      className="shrink-0"
                    />
                  )}
                </h3>
                <p className="truncate text-xs text-muted-foreground">
                  {model.description || model.file_name}
                </p>
              </div>
              <ModelActionsMenu
                model={model}
                actions={actions}
                className="relative z-[1] -mt-1 -mr-2"
              />
            </div>
            <div className="flex flex-wrap gap-1.5">
              <Badge variant="outline">{modelArchLabel(model)}</Badge>
              <Badge variant="outline" className="font-normal">
                {modelTaskText(model)}
              </Badge>
              {model.info.image_size !== null && (
                <Badge variant="outline" className="font-normal tabular-nums">
                  输入 {model.info.image_size}
                </Badge>
              )}
              {model.file_missing && <FileMissingBadge />}
            </div>
            <div className="flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
              <span className="truncate tabular-nums">
                {formatBytes(model.size_bytes)} ·{" "}
                {sourceLabels[model.source] ?? model.source}
              </span>
              <span
                className="shrink-0"
                title={formatDateTime(model.created_at)}
              >
                {formatRelativeTime(model.created_at)}
              </span>
            </div>
          </article>
        </li>
      ))}
    </ul>
  )
}
