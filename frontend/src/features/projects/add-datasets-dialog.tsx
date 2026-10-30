import { useMemo, useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowRight, Database } from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { datasetsQuery } from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import {
  addProjectDatasets,
  projectKeys,
  refreshMembership,
  type Project,
} from "@/features/projects/api"
import { DatasetPicker } from "@/features/projects/dataset-picker"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

type AddDatasetsDialogProps = {
  project: Project
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 把已有的数据集添加到项目中（多选）。 */
export function AddDatasetsDialog({
  project,
  open,
  onOpenChange,
}: AddDatasetsDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <AddDatasetsForm project={project} onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function AddDatasetsForm({
  project,
  onDone,
}: {
  project: Project
  onDone: () => void
}) {
  const datasets = useQuery(datasetsQuery())
  const [selected, setSelected] = useState<Set<number>>(new Set())

  // 已在项目中的数据集不再列出。
  const candidates = useMemo(
    () =>
      datasets.data?.items.filter(
        (dataset) => !dataset.projects.some((p) => p.id === project.id)
      ) ?? [],
    [datasets.data, project.id]
  )

  const mutation = useMutation({
    mutationFn: (ids: number[]) => addProjectDatasets(project.id, ids),
    onSuccess: (updated, ids) => {
      queryClient.setQueryData(projectKeys.detail(updated.id), updated)
      refreshMembership()
      toast.success(
        `已将 ${formatNumber(ids.length)} 个数据集添加到“${updated.name}”`
      )
      onDone()
    },
    // 多半是所选数据集刚被删除：刷新后列表中不再有它，重试即可。
    onError: () => refreshMembership(),
  })

  // 列表刷新后已不存在（或已被加入）的选择不提交。
  const ids = candidates
    .filter((dataset) => selected.has(dataset.id))
    .map((dataset) => dataset.id)

  return (
    <div className="grid gap-5">
      <DialogHeader>
        <DialogTitle>添加数据集</DialogTitle>
        <DialogDescription>
          选择要纳入项目“{project.name}
          ”的数据集。数据集仍然可以同时属于其它项目。
        </DialogDescription>
      </DialogHeader>

      {datasets.isPending ? (
        <div className="grid gap-2">
          {[0, 1, 2, 3].map((row) => (
            <Skeleton key={row} className="h-11 w-full" />
          ))}
        </div>
      ) : datasets.data === undefined ? (
        <LoadError
          title="无法获取数据集列表"
          error={datasets.error}
          retrying={datasets.isFetching}
          onRetry={() => datasets.refetch()}
        />
      ) : candidates.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
          <Database className="size-5" />
          <p>
            {datasets.data.items.length === 0
              ? "还没有数据集，请先在数据集页面上传图片或添加已有目录。"
              : "所有数据集都已在该项目中。需要更多数据时，请先在数据集页面新建数据集。"}
          </p>
          <Button variant="outline" size="sm" asChild>
            <Link to="/datasets">
              前往数据集
              <ArrowRight />
            </Link>
          </Button>
        </div>
      ) : (
        <DatasetPicker
          datasets={candidates}
          selected={selected}
          onSelectedChange={setSelected}
          disabled={mutation.isPending}
          label="选择要添加的数据集"
        />
      )}

      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}
      <DialogFooter>
        <Button
          variant="outline"
          onClick={onDone}
          disabled={mutation.isPending}
        >
          取消
        </Button>
        <Button
          onClick={() => mutation.mutate(ids)}
          disabled={mutation.isPending || ids.length === 0}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          {ids.length > 0 ? `添加 ${formatNumber(ids.length)} 个` : "添加"}
        </Button>
      </DialogFooter>
    </div>
  )
}
