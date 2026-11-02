import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, DatabaseZap, ImagePlus } from "lucide-react"
import { Link, Navigate, useParams, useSearchParams } from "react-router"

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
  AnnotationEditor,
  lastImageKey,
} from "@/features/annotation/annotation-editor"
import { editorImageQuery, fetchImageAt } from "@/features/annotation/api"
import {
  NO_FILTERS,
  parseId,
  updateParams,
} from "@/features/annotation/workspace-params"
import { datasetQuery, type Dataset } from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import { usePageTitle } from "@/hooks/use-page-title"
import { queryClient } from "@/lib/query-client"
import { ApiError } from "@/lib/api"
import { readPreference } from "@/lib/storage"

/** `/annotation/workspace/:datasetId?image=`：标注一个数据集的图片。 */
export function AnnotationEditorPage() {
  const { datasetId } = useParams()
  const id = parseId(datasetId)
  if (id === null) {
    return <Missing title="数据集不存在" />
  }
  return <EditorForDataset key={id} id={id} />
}

function EditorForDataset({ id }: { id: number }) {
  const query = useQuery(datasetQuery(id))
  const [params] = useSearchParams()
  const imageId = parseId(params.get("image"))
  usePageTitle(query.data ? `标注：${query.data.name}` : null)

  if (query.isPending) {
    return <EditorSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <Missing title="数据集不存在" />
  }
  if (query.data === undefined) {
    return (
      <div className="p-4 md:p-6">
        <LoadError
          title="无法获取数据集"
          error={query.error}
          retrying={query.isFetching}
          onRetry={() => query.refetch()}
        />
      </div>
    )
  }
  if (imageId === null) {
    return <StartImage dataset={query.data} />
  }
  return <AnnotationEditor dataset={query.data} />
}

/**
 * 没有指定图片时从哪一张开始：上次在这个数据集中标注的图片，否则第一张
 * 未标注的图片，都没有时第一张。
 */
async function findStartImage(datasetId: number) {
  const last = parseId(readPreference(lastImageKey(datasetId)))
  if (last !== null) {
    try {
      const detail = await queryClient.fetchQuery(
        editorImageQuery(datasetId, last)
      )
      return detail.id
    } catch {
      // 图片已不在数据集中：从第一张未标注的开始
    }
  }
  const unannotated = await fetchImageAt(
    datasetId,
    { ...NO_FILTERS, status: "unannotated" },
    0
  )
  if (unannotated) {
    return unannotated.id
  }
  const first = await fetchImageAt(datasetId, NO_FILTERS, 0)
  return first?.id ?? null
}

function StartImage({ dataset }: { dataset: Dataset }) {
  const [params] = useSearchParams()
  const start = useQuery({
    queryKey: ["annotation", "start", dataset.id],
    queryFn: () => findStartImage(dataset.id),
    staleTime: 0,
    gcTime: 0,
    enabled: dataset.image_count > 0,
  })

  if (dataset.image_count === 0 || start.data === null) {
    return (
      <Empty className="min-h-[60svh]">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <ImagePlus />
          </EmptyMedia>
          <EmptyTitle>“{dataset.name}”中还没有图片</EmptyTitle>
          <EmptyDescription>
            先在数据集页面导入图片，再回来标注。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button asChild>
            <Link to={`/datasets/${dataset.id}`}>
              <ArrowLeft />
              前往数据集
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  }
  if (start.error) {
    return (
      <div className="p-4 md:p-6">
        <LoadError
          title="无法获取图片列表"
          error={start.error}
          retrying={start.isFetching}
          onRetry={() => start.refetch()}
        />
      </div>
    )
  }
  if (start.data === undefined) {
    return <EditorSkeleton />
  }
  return (
    <Navigate
      replace
      to={{ search: `?${updateParams(params, { image: start.data })}` }}
    />
  )
}

function EditorSkeleton() {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-12 items-center gap-2 border-b px-2">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="ml-auto h-8 w-32" />
      </div>
      <div className="flex min-h-0 flex-1">
        <Skeleton className="m-4 flex-1 rounded-lg" />
      </div>
    </div>
  )
}

function Missing({ title }: { title: string }) {
  usePageTitle(title)
  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <DatabaseZap />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>
          该数据集可能已被删除，或者链接有误。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to="/annotation/workspace">
            <ArrowLeft />
            选择数据集
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
