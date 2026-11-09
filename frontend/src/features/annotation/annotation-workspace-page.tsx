import { useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  Database,
  ImageOff,
  Search,
  SearchX,
  X,
} from "lucide-react"
import { Link } from "react-router"

import { PageHeader } from "@/components/page-header"
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
import { Kbd } from "@/components/ui/kbd"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { lastImageKey } from "@/features/annotation/annotation-editor"
import { workspacePath } from "@/features/annotation/workspace-params"
import { datasetsQuery, type Dataset } from "@/features/datasets/api"
import { DatasetStatusBadge } from "@/features/datasets/dataset-status-picker"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import { unannotatedCount } from "@/features/datasets/images"
import { LoadError } from "@/features/datasets/load-error"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"
import { readPreference } from "@/lib/storage"

/** `/annotation/workspace`：选择要标注的数据集。 */
export function AnnotationWorkspacePage() {
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim())
  const query = useQuery({
    ...datasetsQuery(q),
    placeholderData: keepPreviousData,
  })
  const datasets = query.data?.items ?? []

  return (
    <>
      <PageHeader
        title="标注工作台"
        description="选择一个数据集，按固定顺序逐张画矩形框，画好后选择类别：候选依次是数据集所属项目的类别列表和数据集中已有的类别。标注保存为数据集文件夹中与图片同名的 VOC（.xml）文件，可以继续用 LabelImg 等工具打开。"
      />
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <Kbd>W</Kbd> 画框
        </span>
        <span className="flex items-center gap-1.5">
          <Kbd>Enter</Kbd> 确定类别（默认沿用上一个）
        </span>
        <span className="flex items-center gap-1.5">
          <Kbd>1</Kbd>–<Kbd>9</Kbd> 切换类别（项目的类别列表在前）
        </span>
        <span className="flex items-center gap-1.5">
          <Kbd>A</Kbd> / <Kbd>D</Kbd> 上一张 / 下一张（自动保存）
        </span>
        <span className="flex items-center gap-1.5">
          <Kbd>?</Kbd> 全部快捷键
        </span>
      </div>

      <InputGroup className="w-full sm:w-72">
        <InputGroupAddon>
          <Search />
        </InputGroupAddon>
        <InputGroupInput
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="搜索数据集"
          aria-label="搜索数据集"
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

      {query.isPending ? (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }, (_, index) => (
            <Skeleton key={index} className="h-28 rounded-xl" />
          ))}
        </div>
      ) : query.error && !query.data ? (
        <LoadError
          title="无法获取数据集列表"
          error={query.error}
          retrying={query.isFetching}
          onRetry={() => query.refetch()}
        />
      ) : datasets.length === 0 ? (
        q ? (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchX />
              </EmptyMedia>
              <EmptyTitle className="text-base">没有匹配的数据集</EmptyTitle>
              <EmptyDescription>换个关键词试试。</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Database />
              </EmptyMedia>
              <EmptyTitle className="text-base">还没有数据集</EmptyTitle>
              <EmptyDescription>
                先创建数据集并导入图片，再回来标注。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button asChild>
                <Link to="/datasets">前往数据集</Link>
              </Button>
            </EmptyContent>
          </Empty>
        )
      ) : (
        <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {datasets.map((dataset) => (
            <li key={dataset.id}>
              <DatasetRow dataset={dataset} />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function DatasetRow({ dataset }: { dataset: Dataset }) {
  const todo = unannotatedCount(dataset)
  // 有标注文件且能解析的图片（含空标注）；无法解析的不算完成。
  const done = dataset.annotated_count + dataset.empty_count
  const percent = percentOf(done, dataset.image_count)
  const resumed = readPreference(lastImageKey(dataset.id)) !== null
  const cover = dataset.covers[0]
  const disabled = dataset.image_count === 0

  const content = (
    <>
      <div className="flex size-16 shrink-0 items-center justify-center overflow-hidden rounded-md bg-muted">
        {cover ? (
          <DatasetThumbnail
            datasetId={dataset.id}
            image={cover}
            className="size-full"
          />
        ) : (
          <ImageOff className="size-5 text-muted-foreground" />
        )}
      </div>
      <div className="grid min-w-0 flex-1 gap-1.5">
        <div className="flex items-center gap-2">
          <span className="truncate font-medium">{dataset.name}</span>
          <DatasetStatusBadge status={dataset.status} />
          {dataset.directory_missing && (
            <Badge variant="destructive" className="shrink-0">
              文件夹缺失
            </Badge>
          )}
        </div>
        <Progress value={percent} aria-label="已标注的比例" className="h-1.5" />
        <p className="text-xs text-muted-foreground tabular-nums">
          {disabled
            ? "还没有图片"
            : `已标注 ${formatNumber(done)} / ${formatNumber(dataset.image_count)}（${formatPercent(percent)}）· 未标注 ${formatNumber(todo)}`}
        </p>
      </div>
      {!disabled && (
        <span className="flex shrink-0 items-center gap-1 text-sm text-muted-foreground group-hover:text-foreground">
          <span className="hidden sm:inline">{resumed ? "继续" : "开始"}</span>
          <ArrowRight className="size-4" />
        </span>
      )}
    </>
  )

  const className =
    "group flex items-center gap-3 rounded-xl border bg-card p-3 text-card-foreground transition-colors"
  if (disabled) {
    return <div className={`${className} opacity-70`}>{content}</div>
  }
  return (
    <Link
      to={workspacePath(dataset.id)}
      className={`${className} outline-none hover:bg-muted/50 focus-visible:ring-[3px] focus-visible:ring-ring/50`}
      aria-label={`标注“${dataset.name}”`}
    >
      {content}
    </Link>
  )
}
