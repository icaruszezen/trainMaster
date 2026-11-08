/**
 * 数据版本列表：桌面宽度为表格（窄一些时隐藏次要的列），手机宽度为卡片。
 * 数据版本页与项目详情的“数据版本”标签页共用；项目页中不显示项目列。
 */
import type { MouseEvent } from "react"
import { Layers } from "lucide-react"
import { Link, useNavigate } from "react-router"

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { DataVersion } from "@/features/data-versions/api"
import {
  DataVersionActionsMenu,
  type DataVersionActions,
} from "@/features/data-versions/data-version-actions"
import {
  DataVersionActivity,
  DataVersionStatusBadge,
} from "@/features/data-versions/data-version-status"
import {
  dataVersionTitle,
  noteFirstLine,
  splitSummary,
  versionSplitCounts,
} from "@/features/data-versions/format"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

type DataVersionListProps = {
  versions: DataVersion[]
  actions: DataVersionActions
  /** 显示所属项目（数据版本页）；项目详情页中为 false。 */
  showProject?: boolean
}

export function DataVersionList(props: DataVersionListProps) {
  // 与 Tailwind 的 md: 一致：手机宽度显示卡片。
  const wide = useMediaQuery("(min-width: 48rem)")
  return wide ? (
    <DataVersionTable {...props} />
  ) : (
    <DataVersionCards {...props} />
  )
}

/** 图片数（已生成时），如“训练 800 · 验证 200 · 测试 0”。 */
function countsText(version: DataVersion) {
  return version.status === "ready"
    ? splitSummary(versionSplitCounts(version))
    : "—"
}

function DataVersionTable({
  versions,
  actions,
  showProject = true,
}: DataVersionListProps) {
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
    navigate(dataVersionDetailPath(id))
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">数据版本</TableHead>
            {showProject && (
              <TableHead className="hidden lg:table-cell">项目</TableHead>
            )}
            <TableHead>状态</TableHead>
            <TableHead className="hidden lg:table-cell">图片</TableHead>
            <TableHead className="hidden text-right xl:table-cell">
              类别
            </TableHead>
            <TableHead className="hidden xl:table-cell">创建时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {versions.map((version) => (
            <TableRow
              key={version.id}
              className="cursor-pointer"
              onClick={(event) => onRowClick(event, version.id)}
            >
              {/* 名称列占满剩余宽度，长备注截断而不是挤压其它列。 */}
              <TableCell className="w-full max-w-0 pl-4">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
                    <Layers className="size-4" />
                  </div>
                  <div className="grid min-w-0 gap-1">
                    <Link
                      to={dataVersionDetailPath(version.id)}
                      className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                    >
                      {dataVersionTitle(version)}
                      {showProject && (
                        <span className="font-normal text-muted-foreground lg:hidden">
                          {" "}
                          · {version.project_name}
                        </span>
                      )}
                    </Link>
                    <span
                      className="truncate text-xs text-muted-foreground"
                      title={version.note}
                    >
                      {noteFirstLine(version.note)}
                    </span>
                  </div>
                </div>
              </TableCell>
              {showProject && (
                <TableCell className="hidden max-w-48 lg:table-cell">
                  <span className="block truncate" title={version.project_name}>
                    {version.project_name}
                  </span>
                </TableCell>
              )}
              <TableCell>
                <div className="grid w-36 gap-1.5 lg:w-44">
                  <DataVersionStatusBadge version={version} className="w-fit" />
                  {version.status === "failed" ? (
                    <p
                      className="truncate text-xs text-destructive"
                      title={version.error}
                    >
                      {version.error}
                    </p>
                  ) : (
                    <DataVersionActivity version={version} />
                  )}
                </div>
              </TableCell>
              <TableCell className="hidden whitespace-nowrap text-muted-foreground tabular-nums lg:table-cell">
                {countsText(version)}
              </TableCell>
              <TableCell className="hidden text-right tabular-nums xl:table-cell">
                {formatNumber(version.classes.length)}
              </TableCell>
              <TableCell
                className="hidden whitespace-nowrap text-muted-foreground xl:table-cell"
                title={formatDateTime(version.created_at)}
              >
                {formatRelativeTime(version.created_at)}
              </TableCell>
              <TableCell className="pr-4 text-right">
                <DataVersionActionsMenu version={version} actions={actions} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

export function DataVersionCards({
  versions,
  actions,
  showProject = true,
}: DataVersionListProps) {
  return (
    <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
      {versions.map((version) => (
        <li key={version.id}>
          <article className="relative grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
            <div className="flex items-start gap-3">
              <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted text-muted-foreground">
                <Layers className="size-4" />
              </div>
              <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)] gap-0.5">
                <h3 className="truncate leading-snug font-medium">
                  {/* 伸展的链接覆盖整张卡片；菜单按钮通过 z-[1] 浮在它上面。 */}
                  <Link
                    to={dataVersionDetailPath(version.id)}
                    className="outline-none after:absolute after:inset-0 after:content-['']"
                  >
                    {dataVersionTitle(version)}
                  </Link>
                </h3>
                <p className="truncate text-xs text-muted-foreground">
                  {showProject ? `${version.project_name} · ` : ""}
                  {noteFirstLine(version.note)}
                </p>
              </div>
              <DataVersionActionsMenu
                version={version}
                actions={actions}
                className="relative z-[1] -mt-1 -mr-2"
              />
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              <DataVersionStatusBadge version={version} />
            </div>
            {version.status === "failed" && version.error && (
              <p className="line-clamp-2 text-xs break-words text-destructive">
                {version.error}
              </p>
            )}
            <DataVersionActivity version={version} />
            <div className="flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
              <span
                className={cn(
                  "truncate tabular-nums",
                  version.status !== "ready" && "text-muted-foreground/70"
                )}
              >
                {version.status === "ready"
                  ? countsText(version)
                  : `${formatNumber(version.classes.length)} 个类别`}
              </span>
              <span
                className="shrink-0"
                title={formatDateTime(version.created_at)}
              >
                {formatRelativeTime(version.created_at)}
              </span>
            </div>
          </article>
        </li>
      ))}
    </ul>
  )
}
