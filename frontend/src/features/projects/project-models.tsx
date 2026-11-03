import { useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, Boxes, Upload } from "lucide-react"
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
import { projectModelsQuery } from "@/features/models/api"
import { useModelActions } from "@/features/models/model-actions"
import { ModelList } from "@/features/models/model-list"
import { UploadModelDialog } from "@/features/models/upload-model-dialog"
import type { Project } from "@/features/projects/api"
import { formatBytes, formatNumber } from "@/lib/format"

/**
 * 项目的模型：按版本倒序（最新的在前），可以上传新版本。之后平台上训练出的
 * 模型也会出现在这里（来源为“平台训练”）。
 */
export function ProjectModels({ project }: { project: Project }) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    projectModelsQuery(project.id)
  )
  const { actions, dialogs } = useModelActions()
  const [uploadOpen, setUploadOpen] = useState(false)
  // 每次打开上传对话框换一个 key，表单从头开始。
  const [uploadKey, setUploadKey] = useState(0)

  const models = useMemo(
    () => [...(data?.items ?? [])].sort((a, b) => b.version - a.version),
    [data]
  )
  const totalBytes = models.reduce((sum, model) => sum + model.size_bytes, 0)

  const openUpload = () => {
    setUploadKey((key) => key + 1)
    setUploadOpen(true)
  }

  let content
  if (isPending) {
    content = (
      <div className="overflow-hidden rounded-xl border">
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex items-center gap-3 border-b p-4">
            <Skeleton className="size-9 rounded-md" />
            <div className="grid flex-1 gap-2">
              <Skeleton className="h-4 w-24" />
              <Skeleton className="h-3 w-64 max-w-full" />
            </div>
          </div>
        ))}
      </div>
    )
  } else if (error && data === undefined) {
    content = (
      <LoadError
        title="无法获取项目中的模型"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  } else if (models.length === 0) {
    content = (
      <Empty className="border">
        <EmptyHeader className="max-w-lg">
          <EmptyMedia variant="icon">
            <Boxes />
          </EmptyMedia>
          <EmptyTitle className="text-base">项目中还没有模型</EmptyTitle>
          <EmptyDescription>
            上传为这个项目训练的 YOLO 模型（.pt 文件），每次上传按 v1、v2…
            递增版本号；下载时可以选择 PT 或自动转换的
            ONNX。之后在平台上训练的模型也会出现在这里。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" onClick={openUpload}>
            <Upload />
            上传模型
          </Button>
        </EmptyContent>
      </Empty>
    )
  } else {
    content = (
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-muted-foreground tabular-nums">
            共 {formatNumber(models.length)} 个版本 · 最新 v{models[0].version}{" "}
            · {formatBytes(totalBytes)}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/models?project=${project.id}`}>
                在模型仓库中查看
                <ArrowRight />
              </Link>
            </Button>
            <Button size="sm" onClick={openUpload}>
              <Upload />
              上传新版本
            </Button>
          </div>
        </div>
        <ModelList models={models} actions={actions} showProject={false} />
      </div>
    )
  }

  return (
    <>
      {content}
      {dialogs}
      <UploadModelDialog
        key={uploadKey}
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        projectId={project.id}
      />
    </>
  )
}
