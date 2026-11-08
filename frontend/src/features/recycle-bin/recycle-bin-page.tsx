/**
 * 回收站（`/system/recycle-bin`）：删除的数据集、审图版本、数据版本与模型保留
 * 7 天，期间可以恢复，到期后由后台任务自动永久删除。保留期是强制的，这里没有
 * “永久删除”与“清空回收站”。桌面宽度为表格，手机宽度为卡片。
 */
import { useMemo, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  Boxes,
  Database,
  FolderX,
  GitBranch,
  Hourglass,
  Layers,
  ListFilter,
  RotateCcw,
  Search,
  SearchX,
  ShieldCheck,
  Trash2,
  X,
  type LucideIcon,
} from "lucide-react"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { LoadError } from "@/features/datasets/load-error"
import { modelTaskText } from "@/features/models/model-list"
import { modelArchLabel } from "@/features/models/yolo-versions"
import { recycleBinQuery, type RecycleBin } from "@/features/recycle-bin/api"
import {
  dataVersionAddedBytes,
  filterRecycleItems,
  purgeCountdown,
  RECYCLE_RETENTION_DAYS,
  recycleItems,
  recycleKindLabels,
  remainingFraction,
  restoreBlockedReason,
  type RecycleItem,
  type RecycleKind,
} from "@/features/recycle-bin/recycle-format"
import { useRestoreActions } from "@/features/recycle-bin/restore-actions"
import { useMediaQuery } from "@/hooks/use-media-query"
import { useNow } from "@/hooks/use-now"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatRelativeTime,
} from "@/lib/format"
import { cn } from "@/lib/utils"

const kindIcons: Record<RecycleKind, LucideIcon> = {
  dataset: Database,
  version: GitBranch,
  data_version: Layers,
  model: Boxes,
}

export function RecycleBinPage() {
  const bin = useQuery(recycleBinQuery())
  const [search, setSearch] = useState("")
  const [kind, setKind] = useState<RecycleKind | null>(null)
  const now = useNow(60_000)
  const { restore, pendingKey, dialogs } = useRestoreActions()

  const all = useMemo(
    () => (bin.data ? recycleItems(bin.data) : []),
    [bin.data]
  )
  const items = useMemo(
    () => filterRecycleItems(all, { kind, q: search }),
    [all, kind, search]
  )
  const retentionDays = bin.data?.retention_days ?? RECYCLE_RETENTION_DAYS
  const isFiltered = kind !== null || search.trim() !== ""

  return (
    <>
      <PageHeader
        title="回收站"
        description={`删除的数据集、审图版本、数据版本与模型（包括删除项目时一并删除的数据版本与模型）在这里保留 ${retentionDays} 天，期间可以随时恢复；到期后自动永久删除。`}
      />

      <Alert role="note">
        <ShieldCheck />
        <AlertTitle>保留期是强制的</AlertTitle>
        <AlertDescription>
          回收站中的内容不能提前永久删除，误删的东西总有 {retentionDays}{" "}
          天可以找回；这期间它们仍占用磁盘空间。
        </AlertDescription>
      </Alert>

      {bin.isPending ? (
        <RecycleBinSkeleton />
      ) : bin.data === undefined ? (
        <LoadError
          title="无法获取回收站"
          error={bin.error}
          retrying={bin.isFetching}
          onRetry={() => bin.refetch()}
        />
      ) : all.length === 0 ? (
        <Empty className="border bg-card">
          <EmptyHeader className="max-w-md">
            <EmptyMedia variant="icon">
              <Trash2 />
            </EmptyMedia>
            <EmptyTitle>回收站是空的</EmptyTitle>
            <EmptyDescription>
              删除的数据集、审图版本、数据版本与模型会在这里保留 {retentionDays}{" "}
              天，期间可以恢复。
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <>
          <RecycleSummary bin={bin.data} items={all} now={now} />

          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索名称、数据集或说明"
                aria-label="搜索回收站"
              />
              {search && (
                <InputGroupAddon align="inline-end">
                  <InputGroupButton
                    size="icon-xs"
                    onClick={() => setSearch("")}
                    aria-label="清除搜索"
                  >
                    <X />
                  </InputGroupButton>
                </InputGroupAddon>
              )}
            </InputGroup>
            <Select
              value={kind ?? "all"}
              onValueChange={(next) =>
                setKind(next === "all" ? null : (next as RecycleKind))
              }
            >
              <SelectTrigger className="w-36" aria-label="按类型筛选">
                <ListFilter className="text-muted-foreground" />
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部类型</SelectItem>
                <SelectSeparator />
                {(Object.keys(recycleKindLabels) as RecycleKind[]).map(
                  (value) => (
                    <SelectItem key={value} value={value}>
                      {recycleKindLabels[value]}
                    </SelectItem>
                  )
                )}
              </SelectContent>
            </Select>
            <p className="text-sm text-muted-foreground tabular-nums">
              {isFiltered
                ? `找到 ${formatNumber(items.length)} 项`
                : `共 ${formatNumber(items.length)} 项`}
            </p>
          </div>

          {items.length === 0 ? (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <SearchX />
                </EmptyMedia>
                <EmptyTitle className="text-base">没有匹配的内容</EmptyTitle>
                <EmptyDescription>
                  回收站中没有符合条件的
                  {kind ? recycleKindLabels[kind] : "内容"}。
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setSearch("")
                    setKind(null)
                  }}
                >
                  清除筛选
                </Button>
              </EmptyContent>
            </Empty>
          ) : (
            <RecycleList
              items={items}
              now={now}
              pendingKey={pendingKey}
              onRestore={restore}
            />
          )}
        </>
      )}

      {dialogs}
    </>
  )
}

function RecycleSummary({
  bin,
  items,
  now,
}: {
  bin: RecycleBin
  items: RecycleItem[]
  now: number
}) {
  const soonest = items.reduce<RecycleItem | null>(
    (first, item) =>
      first === null || Date.parse(item.purgeAt) < Date.parse(first.purgeAt)
        ? item
        : first,
    null
  )
  const countdown = soonest && purgeCountdown(soonest.purgeAt, now)
  const datasetBytes = bin.datasets.reduce(
    (sum, dataset) => sum + dataset.total_bytes,
    0
  )
  const modelBytes = bin.models.reduce(
    (sum, model) => sum + model.size_bytes,
    0
  )
  const dataVersionBytes = bin.data_versions.reduce(
    (sum, version) => sum + dataVersionAddedBytes(version),
    0
  )

  return (
    // 五张卡片：手机两列（最后一张占满一行），中等宽度三列，宽屏一行。
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-3 xl:grid-cols-5">
      <StatCard
        title="数据集"
        value={formatNumber(bin.datasets.length)}
        icon={Database}
      >
        <span className="tabular-nums">
          {bin.datasets.length > 0
            ? `图片 ${formatBytes(datasetBytes)}`
            : "没有删除的数据集"}
        </span>
      </StatCard>
      <StatCard
        title="审图版本"
        value={formatNumber(bin.versions.length)}
        icon={GitBranch}
      >
        <span>
          {bin.versions.length > 0 ? "恢复后编号不变" : "没有删除的审图版本"}
        </span>
      </StatCard>
      <StatCard
        title="数据版本"
        value={formatNumber(bin.data_versions.length)}
        icon={Layers}
      >
        <span className="tabular-nums">
          {bin.data_versions.length > 0
            ? `新增占用 ${formatBytes(dataVersionBytes)}`
            : "没有删除的数据版本"}
        </span>
      </StatCard>
      <StatCard
        title="模型"
        value={formatNumber(bin.models.length)}
        icon={Boxes}
      >
        <span className="tabular-nums">
          {bin.models.length > 0
            ? `模型文件 ${formatBytes(modelBytes)}`
            : "没有删除的模型"}
        </span>
      </StatCard>
      {/* 列宽用 minmax(0,1fr)：很长的名称在卡片中截断，不撑宽这一行。 */}
      <div className="col-span-2 grid grid-cols-[minmax(0,1fr)] lg:col-span-1">
        <StatCard
          title="最早到期"
          value={
            soonest ? (
              <span title={formatDateTime(soonest.purgeAt)}>
                {countdown?.text.replace("后永久删除", "后") ?? "—"}
              </span>
            ) : (
              "—"
            )
          }
          icon={Hourglass}
        >
          <span className="truncate" title={soonest?.title}>
            {soonest
              ? `${recycleKindLabels[soonest.kind]}“${soonest.title}”`
              : "—"}
          </span>
        </StatCard>
      </div>
    </div>
  )
}

type RecycleListProps = {
  items: RecycleItem[]
  now: number
  /** 正在恢复的一项（`RecycleItem.key`）。 */
  pendingKey: string | null
  onRestore: (item: RecycleItem) => void
}

function RecycleList(props: RecycleListProps) {
  // 与 Tailwind 的 md: 一致：手机宽度显示卡片。
  const wide = useMediaQuery("(min-width: 48rem)")
  return wide ? <RecycleTable {...props} /> : <RecycleCards {...props} />
}

function RecycleTable({ items, now, pendingKey, onRestore }: RecycleListProps) {
  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">名称</TableHead>
            <TableHead>类型</TableHead>
            <TableHead className="hidden lg:table-cell">删除时间</TableHead>
            <TableHead>保留期</TableHead>
            <TableHead className="w-24 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => (
            <TableRow key={item.key}>
              {/* 名称列占满剩余宽度，长名称截断而不是挤压其它列。 */}
              <TableCell className="w-full max-w-0 pl-4">
                <div className="flex min-w-0 items-center gap-3">
                  <KindIcon kind={item.kind} />
                  <div className="grid min-w-0 gap-1">
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate font-medium" title={item.title}>
                        {item.title}
                      </span>
                      <ItemBadges item={item} />
                    </span>
                    <ItemDetails item={item} className="truncate" />
                  </div>
                </div>
              </TableCell>
              <TableCell className="whitespace-nowrap text-muted-foreground">
                {recycleKindLabels[item.kind]}
              </TableCell>
              <TableCell
                className="hidden whitespace-nowrap text-muted-foreground lg:table-cell"
                title={formatDateTime(item.deletedAt)}
              >
                {formatRelativeTime(item.deletedAt, now)}
              </TableCell>
              <TableCell className="min-w-36">
                <PurgeCountdown item={item} now={now} />
              </TableCell>
              <TableCell className="pr-4 text-right">
                <RestoreButton
                  item={item}
                  pending={pendingKey === item.key}
                  disabled={pendingKey !== null}
                  onRestore={onRestore}
                />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function RecycleCards({ items, now, pendingKey, onRestore }: RecycleListProps) {
  return (
    <ul className="grid gap-3">
      {items.map((item) => (
        <li key={item.key}>
          <article className="grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm">
            <div className="flex items-start gap-3">
              <KindIcon kind={item.kind} />
              <div className="grid min-w-0 flex-1 gap-0.5">
                <h3
                  className="truncate leading-snug font-medium"
                  title={item.title}
                >
                  {item.title}
                </h3>
                <p className="text-xs text-muted-foreground">
                  {recycleKindLabels[item.kind]} · 删除于{" "}
                  <span title={formatDateTime(item.deletedAt)}>
                    {formatRelativeTime(item.deletedAt, now)}
                  </span>
                </p>
              </div>
              <RestoreButton
                item={item}
                pending={pendingKey === item.key}
                disabled={pendingKey !== null}
                onRestore={onRestore}
                className="-mt-1 -mr-1"
              />
            </div>
            <ItemDetails item={item} />
            <div className="flex flex-wrap gap-1.5 empty:hidden">
              <ItemBadges item={item} />
            </div>
            <div className="border-t pt-3">
              <PurgeCountdown item={item} now={now} />
            </div>
          </article>
        </li>
      ))}
    </ul>
  )
}

function KindIcon({ kind }: { kind: RecycleKind }) {
  const Icon = kindIcons[kind]
  return (
    <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
      <Icon className="size-4" />
    </div>
  )
}

/**
 * 说明行：数据集的大小与恢复后回到哪里、审图版本的数据集、数据版本的图片数与
 * 备注、模型的结构与大小。
 */
function ItemDetails({
  item,
  className,
}: {
  item: RecycleItem
  className?: string
}) {
  let parts: (string | null)[]
  switch (item.kind) {
    case "dataset": {
      const { dataset } = item
      const projects = dataset.restore_projects.map((project) => project.name)
      parts = [
        `${formatNumber(dataset.image_count)} 张图片`,
        formatBytes(dataset.total_bytes),
        projects.length > 0 ? `恢复后回到项目 ${projects.join("、")}` : null,
        dataset.review_count > 0
          ? `${formatNumber(dataset.review_count)} 个审图任务`
          : null,
      ]
      break
    }
    case "version": {
      const { version } = item
      parts = [
        `数据集“${version.dataset_name}”`,
        `${formatNumber(version.image_count)} 张图片`,
        version.note || null,
      ]
      break
    }
    case "data_version": {
      const { dataVersion } = item
      parts = [
        `${formatNumber(dataVersion.image_count)} 张图片`,
        dataVersion.augmented_count > 0
          ? `含 ${formatNumber(dataVersion.augmented_count)} 张增强图片`
          : null,
        `新增占用 ${formatBytes(dataVersionAddedBytes(dataVersion))}`,
        dataVersion.note || null,
      ]
      break
    }
    case "model": {
      const { model } = item
      parts = [
        modelArchLabel(model),
        modelTaskText(model),
        formatBytes(model.size_bytes),
        model.description || model.file_name,
      ]
      break
    }
  }
  const text = parts.filter((part): part is string => part !== null).join(" · ")
  return (
    <p
      className={cn("text-xs text-muted-foreground tabular-nums", className)}
      title={text}
    >
      {text}
    </p>
  )
}

function ItemBadges({ item }: { item: RecycleItem }) {
  const badges: ReactNode[] = []
  if (item.kind === "dataset") {
    if (item.dataset.delete_files) {
      badges.push(
        <Badge key="files" variant="outline" className="font-normal">
          到期删除文件夹
        </Badge>
      )
    }
    if (item.dataset.directory_missing) {
      badges.push(
        <Badge key="missing" variant="destructive" className="font-normal">
          <FolderX />
          文件夹缺失
        </Badge>
      )
    }
  }
  if (item.kind === "version" && item.version.dataset_deleted) {
    badges.push(
      <Badge key="dataset" variant="outline" className="font-normal">
        数据集也在回收站中
      </Badge>
    )
  }
  if (item.kind === "data_version" && item.dataVersion.project_deleted) {
    badges.push(
      <Badge key="project" variant="outline" className="font-normal">
        项目也在回收站中
      </Badge>
    )
  }
  if (item.kind === "model" && item.model.project_deleted) {
    badges.push(
      <Badge key="project" variant="outline" className="font-normal">
        项目随之删除
      </Badge>
    )
  }
  return badges.length > 0 ? <>{badges}</> : null
}

function PurgeCountdown({ item, now }: { item: RecycleItem; now: number }) {
  const countdown = purgeCountdown(item.purgeAt, now)
  const left = remainingFraction(item.deletedAt, item.purgeAt, now)
  return (
    <div
      className="grid gap-1.5"
      title={`${formatDateTime(item.purgeAt)} 永久删除`}
    >
      <span
        className={cn(
          "text-xs whitespace-nowrap tabular-nums",
          countdown.urgent ? "text-destructive" : "text-muted-foreground"
        )}
      >
        {countdown.text}
      </span>
      <Progress
        value={left * 100}
        aria-label={`剩余保留期：${countdown.text}`}
        className={cn(
          "h-1.5",
          countdown.urgent &&
            "bg-destructive/20 *:data-[slot=progress-indicator]:bg-destructive"
        )}
      />
    </div>
  )
}

function RestoreButton({
  item,
  pending,
  disabled,
  onRestore,
  className,
}: {
  item: RecycleItem
  pending: boolean
  disabled: boolean
  onRestore: (item: RecycleItem) => void
  className?: string
}) {
  const blocked = restoreBlockedReason(item)
  const button = (
    <Button
      variant="outline"
      size="sm"
      onClick={() => onRestore(item)}
      disabled={disabled || blocked !== null}
      aria-label={`恢复${recycleKindLabels[item.kind]}：${item.title}`}
      className={blocked === null ? className : undefined}
    >
      {pending ? <Spinner aria-hidden /> : <RotateCcw />}
      恢复
    </Button>
  )
  if (blocked === null) {
    return button
  }
  // 禁用的按钮不触发指针事件：用外层元素接收悬停与焦点。
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className={cn("inline-flex", className)}>
          {button}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-64">{blocked}</TooltipContent>
    </Tooltip>
  )
}

function RecycleBinSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3 xl:grid-cols-5">
        {[0, 1, 2, 3, 4].map((index) => (
          <Skeleton
            key={index}
            className="h-[118px] rounded-xl last:col-span-2 lg:last:col-span-1"
          />
        ))}
      </div>
      <Skeleton className="h-9 w-full max-w-md" />
      <Skeleton className="h-64 rounded-xl" />
    </div>
  )
}
