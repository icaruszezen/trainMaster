/**
 * 项目详情页“数据版本”标签页的内容：项目的数据版本（新的在前）与“生成数据
 * 版本”的入口。
 */
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, Layers, Plus } from "lucide-react"
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
import { LoadError } from "@/features/datasets/load-error"
import {
  dataVersionsQuery,
  type DataVersion,
} from "@/features/data-versions/api"
import { useDataVersionActions } from "@/features/data-versions/data-version-actions"
import { DataVersionList } from "@/features/data-versions/data-version-list"
import { dataVersionTitle } from "@/features/data-versions/format"
import {
  dataVersionsPath,
  newDataVersionPath,
} from "@/features/data-versions/paths"
import { useBuildWatchNavigate } from "@/features/data-versions/use-build-watch-navigate"
import { formatNumber } from "@/lib/format"

/** 版本号最大的数据版本（列表按创建时间排序，编号以版本号为准）。 */
export function latestDataVersion(versions: readonly DataVersion[]) {
  return versions.reduce<DataVersion | null>(
    (latest, version) =>
      latest === null || version.version > latest.version ? version : latest,
    null
  )
}

/** 标签页顶部的概况，如“共 3 个 · 最新 数据版本 v4”。 */
export function projectDataVersionsSummary(versions: readonly DataVersion[]) {
  const latest = latestDataVersion(versions)
  return latest
    ? `共 ${formatNumber(versions.length)} 个 · 最新 ${dataVersionTitle(latest)}`
    : "还没有数据版本"
}

export function ProjectDataVersions({
  projectId,
  projectName,
}: {
  projectId: number
  projectName: string
}) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    dataVersionsQuery(projectId)
  )
  const { actions, dialogs } = useDataVersionActions()
  useBuildWatchNavigate()
  const createPath = newDataVersionPath({ projectId })
  const versions = data?.items ?? []

  let content
  if (isPending) {
    content = (
      <div
        className="overflow-hidden rounded-xl border"
        aria-busy="true"
        aria-label="正在加载"
      >
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex items-center gap-3 border-b p-4">
            <Skeleton className="size-9 rounded-md" />
            <div className="grid flex-1 gap-2">
              <Skeleton className="h-4 w-28" />
              <Skeleton className="h-3 w-64 max-w-full" />
            </div>
          </div>
        ))}
      </div>
    )
  } else if (error && data === undefined) {
    content = (
      <LoadError
        title="无法获取项目的数据版本"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  } else if (versions.length === 0) {
    content = (
      <Empty className="border">
        <EmptyHeader className="max-w-lg">
          <EmptyMedia variant="icon">
            <Layers />
          </EmptyMedia>
          <EmptyTitle className="text-base">项目中还没有数据版本</EmptyTitle>
          <EmptyDescription>
            从“{projectName}
            ”的数据集生成冻结的训练数据：类别编号按项目的类别列表，划分训练 /
            验证 / 测试集，生成后可以直接训练或下载为 YOLO / VOC / COCO。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" asChild>
            <Link to={createPath}>
              <Plus />
              生成数据版本
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  } else {
    content = (
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-muted-foreground tabular-nums">
            {projectDataVersionsSummary(versions)}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link to={dataVersionsPath(projectId)}>
                在数据版本中查看
                <ArrowRight />
              </Link>
            </Button>
            <Button size="sm" asChild>
              <Link to={createPath}>
                <Plus />
                生成数据版本
              </Link>
            </Button>
          </div>
        </div>
        <DataVersionList
          versions={versions}
          actions={actions}
          showProject={false}
        />
      </div>
    )
  }

  return (
    <>
      {content}
      {dialogs}
    </>
  )
}
