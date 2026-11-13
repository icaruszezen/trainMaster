/**
 * 数据版本详情（`/datasets/versions/:versionId`）：标题、备注、状态与操作；
 * 排队 / 生成中 / 失败时的状态说明；统计卡片；“概览”（各划分的类别分布、
 * 数据集与标注来源、设置、未纳入的图片、存储、与上一个版本的对比）与“图片”
 * 两个标签页（`?tab=images`，筛选条件也在地址栏中）。
 *
 * 编号不是正整数时直接显示“不存在”，不发请求。
 */
import { useEffect, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  CircleAlert,
  Clock,
  CopyPlus,
  Download,
  FolderKanban,
  GitCompareArrows,
  HardDrive,
  Images,
  Layers,
  Rocket,
  RotateCcw,
  Shapes,
  SquareDashed,
  Trash2,
  TriangleAlert,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ClassIdPreview } from "@/features/datasets/class-list-field"
import { LoadError } from "@/features/datasets/load-error"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  dataVersionQuery,
  isActiveDataVersion,
  SPLITS,
  type DataVersion,
  type DataVersionImageFilters,
} from "@/features/data-versions/api"
import { watchDataVersionBuild } from "@/features/data-versions/build-watch"
import { ClassDistribution } from "@/features/data-versions/class-distribution"
import {
  DataVersionActionsMenu,
  useDataVersionActions,
} from "@/features/data-versions/data-version-actions"
import { DataVersionImages } from "@/features/data-versions/data-version-images"
import { DataVersionStatusBadge } from "@/features/data-versions/data-version-status"
import {
  augmentationText,
  classSplitWarnings,
  compareText,
  customScopeCount,
  dataVersionAddedBytes,
  dataVersionFullTitle,
  dataVersionTitle,
  DATA_VERSION_STAGE_LABELS,
  emptyClasses,
  GROUP_BY_DESCRIPTIONS,
  GROUP_BY_LABELS,
  negativesText,
  progressPercent,
  progressText,
  queueText,
  SKIPPED_HINTS,
  skippedEntries,
  skippedTotal,
  splitRatioText,
  splitSummary,
  storageCountsText,
  storageText,
  TRAINING_SCOPE_HINTS,
  TRAINING_SCOPE_LABELS,
  versionSplitCounts,
} from "@/features/data-versions/format"
import {
  imageFilterParams,
  parseImageFilters,
} from "@/features/data-versions/images"
import {
  DATA_VERSIONS_PATH,
  dataVersionDetailPath,
  dataVersionsPath,
  newDataVersionPath,
  parsePositiveId,
  projectDataVersionsPath,
} from "@/features/data-versions/paths"
import { WarningList } from "@/features/data-versions/preview-panel"
import { useBuildWatchNavigate } from "@/features/data-versions/use-build-watch-navigate"
import {
  RECYCLE_BIN_PATH,
  RECYCLE_RETENTION_DAYS,
} from "@/features/recycle-bin/recycle-format"
import { newTrainingJobPath } from "@/features/training/paths"
import { useMediaQuery } from "@/hooks/use-media-query"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatRelativeTime,
} from "@/lib/format"
import { cn } from "@/lib/utils"

export function DataVersionDetailPage() {
  const { versionId } = useParams()
  const id = parsePositiveId(versionId)
  if (id === null) {
    return <DataVersionNotFound />
  }
  // 切换版本时整体重置页面状态（对话框、查看器等）。
  return <DataVersionDetail key={id} id={id} />
}

export function DataVersionNotFound() {
  usePageTitle("数据版本不存在")
  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Layers />
        </EmptyMedia>
        <EmptyTitle>数据版本不存在</EmptyTitle>
        <EmptyDescription>
          该数据版本可能已被删除，或者链接有误。移入回收站的数据版本保留{" "}
          {RECYCLE_RETENTION_DAYS} 天，期间可以恢复。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <div className="flex flex-wrap justify-center gap-2">
          <Button asChild>
            <Link to={DATA_VERSIONS_PATH}>
              <ArrowLeft />
              返回数据版本列表
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link to={RECYCLE_BIN_PATH}>
              <Trash2 />
              查看回收站
            </Link>
          </Button>
        </div>
      </EmptyContent>
    </Empty>
  )
}

type DetailTab = "overview" | "images"

/** 标签页与图片筛选条件保存在地址栏中，刷新、分享链接、前进后退都能保留。 */
function useDetailSearchParams() {
  const [params, setParams] = useSearchParams()
  const tab: DetailTab = params.get("tab") === "images" ? "images" : "overview"
  const filters = parseImageFilters(params)

  const update = (changes: Record<string, string>) => {
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        for (const [key, value] of Object.entries(changes)) {
          // 默认值不写进地址栏。
          if (value === "" || (key === "tab" && value === "overview")) {
            next.delete(key)
          } else {
            next.set(key, value)
          }
        }
        return next
      },
      { replace: true }
    )
  }

  return {
    tab,
    filters,
    setTab: (value: DetailTab) => update({ tab: value }),
    setFilters: (changes: Partial<DataVersionImageFilters>) =>
      update(imageFilterParams(changes)),
  }
}

function DataVersionDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const query = useQuery(dataVersionQuery(id))
  const search = useDetailSearchParams()
  const { actions, dialogs, retrying } = useDataVersionActions({
    onDeleted: (deleted) =>
      navigate(dataVersionsPath(deleted.project_id), { replace: true }),
  })
  useBuildWatchNavigate()

  const version = query.data
  usePageTitle(version ? dataVersionFullTitle(version) : undefined)

  // 打开排队或生成中的版本时也开始监视：离开页面后完成时仍会提示。
  useEffect(() => {
    if (version && isActiveDataVersion(version)) {
      watchDataVersionBuild(version)
    }
  }, [version])

  if (query.isPending) {
    return <DetailSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <DataVersionNotFound />
  }
  // 后台刷新失败时继续显示已加载的数据。
  if (version === undefined) {
    return (
      <LoadError
        title="无法获取数据版本"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const ready = version.status === "ready"

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        <PageHeader
          title={dataVersionFullTitle(version)}
          description={
            version.note ? (
              <span className="break-words whitespace-pre-line">
                {version.note}
              </span>
            ) : undefined
          }
          actions={
            <>
              {ready && (
                <>
                  <Button variant="outline" asChild>
                    <Link
                      to={newTrainingJobPath({
                        projectId: version.project_id,
                        dataVersionId: version.id,
                      })}
                    >
                      <Rocket />
                      发起训练
                    </Link>
                  </Button>
                  <Button variant="outline" asChild>
                    <Link
                      to={newDataVersionPath({
                        projectId: version.project_id,
                        from: version.id,
                      })}
                    >
                      <CopyPlus />
                      <span className="hidden sm:inline">以此为模板新建</span>
                      <span className="sm:hidden">新建</span>
                    </Link>
                  </Button>
                  <Button onClick={() => actions.download(version)}>
                    <Download />
                    下载
                  </Button>
                </>
              )}
              {version.status === "failed" && (
                <Button
                  onClick={() => actions.retry(version)}
                  disabled={retrying}
                >
                  {retrying ? <Spinner aria-hidden /> : <RotateCcw />}
                  重新生成
                </Button>
              )}
              <DataVersionActionsMenu
                version={version}
                actions={actions}
                showDetails={false}
                className="size-9"
              />
            </>
          }
        />
        <DetailMeta version={version} />
      </div>

      <StatusNotice
        version={version}
        retrying={retrying}
        onRetry={() => actions.retry(version)}
      />

      {ready && <DetailStats version={version} />}

      <Tabs
        value={search.tab}
        onValueChange={(value) => search.setTab(value as DetailTab)}
        className="gap-4"
      >
        <TabsList>
          <TabsTrigger value="overview" className="px-3">
            概览
          </TabsTrigger>
          <TabsTrigger value="images" className="px-3">
            图片
            {ready && (
              <span className="text-xs text-muted-foreground tabular-nums">
                {formatNumber(version.image_count)}
              </span>
            )}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <DataVersionOverview version={version} />
        </TabsContent>
        <TabsContent value="images">
          {ready ? (
            <DataVersionImages
              version={version}
              filters={search.filters}
              onFiltersChange={search.setFilters}
            />
          ) : (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  {version.status === "failed" ? <CircleAlert /> : <Images />}
                </EmptyMedia>
                <EmptyTitle className="text-base">
                  {version.status === "failed"
                    ? "生成失败，没有图片"
                    : "生成完成后可以查看图片"}
                </EmptyTitle>
                <EmptyDescription>
                  {version.status === "failed"
                    ? "重新生成成功后，图片会显示在这里。"
                    : "生成完成后，可以按划分、类别与数据集查看纳入的每一张图片与标注框。"}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          )}
        </TabsContent>
      </Tabs>

      {dialogs}
    </>
  )
}

function DetailMeta({ version }: { version: DataVersion }) {
  const duration =
    version.started_at && version.finished_at
      ? Math.max(
          0,
          (Date.parse(version.finished_at) - Date.parse(version.started_at)) /
            1000
        )
      : null
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <DataVersionStatusBadge version={version} />
      <Link
        to={projectDataVersionsPath(version.project_id)}
        className="inline-flex max-w-full min-w-0 items-center gap-1 text-foreground underline-offset-4 hover:underline"
      >
        <FolderKanban className="size-3.5 shrink-0" />
        <span className="truncate">{version.project_name}</span>
      </Link>
      <span title={formatDateTime(version.created_at)}>
        创建于 {formatRelativeTime(version.created_at)}
      </span>
      {version.finished_at && (
        <span title={formatDateTime(version.finished_at)}>
          {version.status === "failed" ? "失败于" : "完成于"}{" "}
          {formatRelativeTime(version.finished_at)}
          {duration !== null &&
            version.status === "ready" &&
            `，用时 ${formatDuration(Math.round(duration))}`}
        </span>
      )}
    </div>
  )
}

function StatusNotice({
  version,
  retrying,
  onRetry,
}: {
  version: DataVersion
  retrying: boolean
  onRetry: () => void
}) {
  switch (version.status) {
    case "queued":
      return (
        <Alert>
          <Clock />
          <AlertTitle>排队中 · {queueText(version.queue_position)}</AlertTitle>
          <AlertDescription>
            <p>
              同一时间只生成一个数据版本，前面的完成后自动开始。可以离开这个页面，完成后会提示。
            </p>
          </AlertDescription>
        </Alert>
      )
    case "building": {
      const progress = version.progress
      return (
        <Alert>
          <Spinner aria-hidden />
          <AlertTitle>
            正在生成
            {progress && ` · ${DATA_VERSION_STAGE_LABELS[progress.stage]}`}
          </AlertTitle>
          <AlertDescription>
            <div className="grid w-full gap-1.5">
              <TaskProgress
                value={progressPercent(progress)}
                label="生成进度"
              />
              <p className="tabular-nums">
                {progress ? progressText(progress) : "正在开始…"}
              </p>
              <p>生成在后台进行，可以离开这个页面，完成后会提示。</p>
            </div>
          </AlertDescription>
        </Alert>
      )
    }
    case "failed":
      return (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>生成失败</AlertTitle>
          <AlertDescription>
            <p className="break-words">{version.error || "未知错误"}</p>
            <p>
              重新生成使用同样的类别与设置，按数据集现在的内容生成；如果问题出在数据集（如文件夹不存在），先在数据集中处理。
            </p>
            <Button
              variant="outline"
              size="sm"
              className="mt-1 text-foreground"
              onClick={onRetry}
              disabled={retrying}
            >
              {retrying ? <Spinner aria-hidden /> : <RotateCcw />}
              重新生成
            </Button>
          </AlertDescription>
        </Alert>
      )
    default:
      return null
  }
}

function DetailStats({ version }: { version: DataVersion }) {
  const negatives = SPLITS.reduce(
    (sum, split) => sum + version.stats.negatives[split],
    0
  )
  const empty = emptyClasses(version.stats.labels)
  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="图片"
        value={formatNumber(version.image_count)}
        icon={Images}
      >
        <span className="tabular-nums">
          {splitSummary(versionSplitCounts(version))}
        </span>
        {version.augmented_count > 0 && (
          <span className="tabular-nums">
            含增强图片 {formatNumber(version.augmented_count)} 张
          </span>
        )}
      </StatCard>
      <StatCard
        title="目标"
        value={formatNumber(version.object_count)}
        icon={SquareDashed}
      >
        <span className="tabular-nums">
          负样本 {formatNumber(negatives)} 张
        </span>
      </StatCard>
      <StatCard
        title="类别"
        value={formatNumber(version.classes.length)}
        icon={Shapes}
      >
        {empty.length > 0 ? (
          <span className="text-amber-700 tabular-nums dark:text-amber-500">
            {formatNumber(empty.length)} 个类别没有目标
          </span>
        ) : (
          <span className="tabular-nums">
            编号 0–{formatNumber(Math.max(0, version.classes.length - 1))}
          </span>
        )}
      </StatCard>
      <StatCard
        title="新增占用"
        value={formatBytes(dataVersionAddedBytes(version))}
        icon={HardDrive}
      >
        <span className="tabular-nums">
          {version.stats.storage.linked > 0
            ? "其余文件与数据集共用"
            : storageCountsText(version.stats.storage)}
        </span>
      </StatCard>
    </div>
  )
}

function OverviewCard({
  title,
  description,
  className,
  children,
}: {
  title: string
  description?: ReactNode
  className?: string
  children: ReactNode
}) {
  return (
    <Card className={cn("min-w-0 gap-4 py-5", className)}>
      <CardHeader className="px-4 sm:px-6">
        <CardTitle className="text-base">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="grid min-w-0 gap-3 px-4 sm:px-6">
        {children}
      </CardContent>
    </Card>
  )
}

export function DataVersionOverview({ version }: { version: DataVersion }) {
  // 手机宽度只显示各划分的目标数，放不下图片数。
  const wide = useMediaQuery("(min-width: 40rem)")
  const ready = version.status === "ready"
  const { split } = version.config
  const showTest = split.test > 0 || version.test_count > 0
  const warnings = ready
    ? [
        ...classSplitWarnings(version.stats.labels, split.test).map((w) => ({
          key: `${w.split}:${w.name}`,
          text: w.text,
          tone: "warning" as const,
        })),
        ...emptyClasses(version.stats.labels).map((name) => ({
          key: `empty:${name}`,
          text: `“${name}”在这个数据版本中没有目标`,
          tone: "warning" as const,
        })),
      ]
    : []

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <OverviewCard
        title="类别"
        className="lg:col-span-2"
        description={
          ready
            ? "各划分中每个类别的目标数（宽屏时还有图片数），只统计原图，不含增强图片。"
            : "创建时冻结的类别列表，顺序即 YOLO 的类别编号。生成完成后显示各划分的分布。"
        }
      >
        {ready && version.stats.labels.length > 0 ? (
          <>
            <ClassDistribution
              labels={version.stats.labels}
              negatives={version.stats.negatives}
              showTest={showTest}
              showImages={wide}
            />
            {warnings.length > 0 && <WarningList warnings={warnings} />}
          </>
        ) : (
          <ClassIdPreview classes={version.classes.map((cls) => cls.name)} />
        )}
      </OverviewCard>

      <OverviewCard
        title="数据集与标注来源"
        description="生成时从这些数据集读取图片；审图版本中有的图片用审图标注。"
      >
        <SourceList version={version} />
      </OverviewCard>

      <OverviewCard title="设置">
        <SettingsList version={version} />
      </OverviewCard>

      {ready && (
        <>
          <OverviewCard
            title="未纳入"
            description="选中的数据集中没有纳入这个数据版本的图片与框。"
          >
            <SkippedList version={version} />
          </OverviewCard>
          <OverviewCard title="存储">
            <StorageInfo version={version} />
          </OverviewCard>
        </>
      )}

      {ready && version.stats.compare && (
        <OverviewCard title="与上一个数据版本对比" className="lg:col-span-2">
          <p className="flex items-start gap-1.5 text-sm text-pretty">
            <GitCompareArrows className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0">
              {compareText(version.stats.compare)}
            </span>
          </p>
          <p className="text-xs text-muted-foreground">
            按数据集与图片在数据集中的路径对应；划分变化可能来自比例、随机种子或分组的不同。
          </p>
          <div>
            <Button variant="outline" size="sm" asChild>
              <Link to={dataVersionDetailPath(version.stats.compare.id)}>
                查看{dataVersionTitle(version.stats.compare)}
              </Link>
            </Button>
          </div>
        </OverviewCard>
      )}
    </div>
  )
}

function SourceList({ version }: { version: DataVersion }) {
  const ready = version.status === "ready"
  if (version.sources.length === 0) {
    return <p className="text-sm text-muted-foreground">没有数据集。</p>
  }
  return (
    <ul className="grid gap-3">
      {version.sources.map((source) => (
        <li
          key={source.dataset_id}
          className="grid min-w-0 gap-1.5 border-b pb-3 last:border-b-0 last:pb-0"
        >
          <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
            <span className="flex min-w-0 items-center gap-1.5 text-sm font-medium">
              {source.dataset_deleted ? (
                <span className="truncate" title={source.dataset_name}>
                  {source.dataset_name}
                </span>
              ) : (
                <Link
                  to={`/datasets/${source.dataset_id}`}
                  className="truncate underline-offset-4 hover:underline"
                  title={source.dataset_name}
                >
                  {source.dataset_name}
                </Link>
              )}
              {source.dataset_deleted && (
                <Badge
                  variant="outline"
                  className="shrink-0 font-normal text-muted-foreground"
                >
                  数据集已删除
                </Badge>
              )}
            </span>
            {ready && (
              <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                纳入 {formatNumber(source.image_count)} 张
              </span>
            )}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {source.scope !== version.config.scope && (
              <Badge
                variant="outline"
                className="border-primary/40 bg-primary/5 font-normal"
                title={TRAINING_SCOPE_HINTS[source.scope]}
              >
                {TRAINING_SCOPE_LABELS[source.scope]}
              </Badge>
            )}
            {source.include_base && (
              <Badge variant="secondary" className="font-normal">
                基础标注
              </Badge>
            )}
            {source.versions.map((reviewVersion) => (
              <Badge
                key={reviewVersion.id}
                variant="outline"
                className="max-w-full font-normal"
                title={`审图版本 ${reviewVersion.name}`}
              >
                <span className="truncate">{reviewVersion.name}</span>
              </Badge>
            ))}
          </div>
          <p className="truncate font-mono text-xs text-muted-foreground">
            images/{source.dir_name}/
          </p>
        </li>
      ))}
    </ul>
  )
}

function SettingsList({ version }: { version: DataVersion }) {
  const { config } = version
  const { split } = config
  const ready = version.status === "ready"
  return (
    <dl className="grid gap-2.5 text-sm">
      <InfoRow label="划分比例">
        <span className="tabular-nums">{splitRatioText(split)}</span>
      </InfoRow>
      <InfoRow label="随机种子">
        <span className="font-mono tabular-nums">{split.seed}</span>
      </InfoRow>
      <InfoRow label="分组">
        <span title={GROUP_BY_DESCRIPTIONS[split.group_by]}>
          {GROUP_BY_LABELS[split.group_by]}
        </span>
      </InfoRow>
      <InfoRow label="按文件夹">
        {split.use_folders ? (
          <>
            沿用数据集中 train / val / test 文件夹的划分
            {ready && version.stats.by_folder > 0 && (
              <span className="text-muted-foreground tabular-nums">
                （{formatNumber(version.stats.by_folder)} 张）
              </span>
            )}
          </>
        ) : (
          "不沿用，全部按比例划分"
        )}
      </InfoRow>
      <InfoRow label="参与训练">
        <span title={TRAINING_SCOPE_HINTS[config.scope]}>
          {TRAINING_SCOPE_LABELS[config.scope]}
        </span>
        {customScopeCount(version) > 0 && (
          <span className="text-muted-foreground tabular-nums">
            （{formatNumber(customScopeCount(version))} 个数据集单独设置）
          </span>
        )}
      </InfoRow>
      <InfoRow label="负样本">{negativesText(version)}</InfoRow>
      <InfoRow label="忽略的标签">
        {config.ignore_labels.length > 0 ? (
          <span className="flex flex-wrap gap-1">
            {config.ignore_labels.map((label) => (
              <Badge
                key={label}
                variant="outline"
                className="max-w-full font-normal"
              >
                <span className="truncate">{label}</span>
              </Badge>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">无</span>
        )}
      </InfoRow>
      <InfoRow label="增强">
        <span className="text-pretty">
          {augmentationText(config.augmentation)}
        </span>
      </InfoRow>
    </dl>
  )
}

function SkippedList({ version }: { version: DataVersion }) {
  const { stats } = version
  const entries = skippedEntries(stats.skipped)
  if (
    entries.length === 0 &&
    stats.dropped_objects === 0 &&
    stats.degenerate_objects === 0
  ) {
    return (
      <p className="text-sm text-muted-foreground">
        选中的图片全部纳入，没有去掉的框。
      </p>
    )
  }
  const total = skippedTotal(stats.skipped)
  return (
    <div className="grid gap-2">
      {total > 0 && (
        <p className="text-sm tabular-nums">
          共 {formatNumber(total)} 张图片没有纳入
        </p>
      )}
      <ul className="grid gap-2 text-sm">
        {entries.map((entry) => (
          <li key={entry.key} className="grid gap-0.5">
            <span className="flex items-baseline justify-between gap-2">
              <span className="flex min-w-0 items-center gap-1.5">
                {entry.key === "unlisted" && (
                  <TriangleAlert className="size-3.5 shrink-0 text-amber-700 dark:text-amber-500" />
                )}
                <span className="min-w-0">{entry.label}</span>
              </span>
              <span className="shrink-0 tabular-nums">
                {formatNumber(entry.count)} 张
              </span>
            </span>
            {SKIPPED_HINTS[entry.key] && (
              <span className="text-xs text-pretty text-muted-foreground">
                {SKIPPED_HINTS[entry.key]}
              </span>
            )}
          </li>
        ))}
        {stats.dropped_objects > 0 && (
          <li className="flex items-baseline justify-between gap-2">
            <span className="min-w-0">忽略的标签的框</span>
            <span className="shrink-0 tabular-nums">
              {formatNumber(stats.dropped_objects)} 个
            </span>
          </li>
        )}
        {stats.degenerate_objects > 0 && (
          <li className="flex items-baseline justify-between gap-2">
            <span className="min-w-0">超出图片、没有面积的框</span>
            <span className="shrink-0 tabular-nums">
              {formatNumber(stats.degenerate_objects)} 个
            </span>
          </li>
        )}
      </ul>
    </div>
  )
}

function StorageInfo({ version }: { version: DataVersion }) {
  const { storage } = version.stats
  return (
    <div className="grid gap-2 text-sm">
      <p className="font-medium tabular-nums">{storageText(storage)}</p>
      <p className="text-muted-foreground tabular-nums">
        {storageCountsText(storage)}
      </p>
      <p className="text-xs text-pretty text-muted-foreground tabular-nums">
        全部图片文件 {formatBytes(version.total_bytes)}
        。硬链接的原图与数据集共用磁盘空间：导入或删除数据集不会改变这个数据版本，但直接覆盖写入数据集中的文件（如用
        cp 覆盖）会同时改变它。
      </p>
      {storage.orientation_fixed > 0 && (
        <p className="text-xs text-pretty text-muted-foreground tabular-nums">
          其中 {formatNumber(storage.orientation_fixed)} 张原图带有 EXIF
          方向标记，复制后重置了标记（像素不变），使训练工具读到的方向与标注一致。
        </p>
      )}
    </div>
  )
}

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[5rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

function DetailSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid gap-3">
        <Skeleton className="h-8 w-72 max-w-full" />
        <Skeleton className="h-4 w-96 max-w-full" />
        <Skeleton className="h-5 w-80 max-w-full" />
      </div>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-40" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-48 rounded-xl lg:col-span-2" />
        <Skeleton className="h-40 rounded-xl" />
        <Skeleton className="h-40 rounded-xl" />
      </div>
    </div>
  )
}
