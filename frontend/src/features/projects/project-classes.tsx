import { useState, type ReactNode } from "react"
import { useMutation, type UseQueryResult } from "@tanstack/react-query"
import { Import, ListPlus, Pencil, Plus, Shapes } from "lucide-react"
import { toast } from "sonner"

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
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { classColor, colorResolver } from "@/features/annotation/classes"
import { CLASS_LIST_MAX_COUNT } from "@/features/datasets/class-list"
import { LoadError } from "@/features/datasets/load-error"
import {
  addProjectClass,
  cacheProjectClasses,
  setProjectClasses,
  type ProjectClass,
  type ProjectClassList,
  type ProjectLabel,
} from "@/features/projects/api"
import { labelsNotInList, mergeLabels } from "@/features/projects/class-edit"
import {
  ClassEditorDialog,
  isListConflict,
} from "@/features/projects/class-editor-dialog"
import type { ListResponse } from "@/lib/api"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

type ProjectClassesProps = {
  projectId: number
  /** 详情页已经发起的类别列表查询（标签页上的类别数量也来自它）。 */
  classes: UseQueryResult<ProjectClassList>
  /** 详情页已经发起的标签分布查询（项目中所有数据集合计）。 */
  labels: UseQueryResult<ListResponse<ProjectLabel>>
}

/** “全部加入” / “从数据集的标签生成”要保存的列表。 */
type MergedList = {
  classes: ProjectClass[]
  added: number
  /** 超出类别数量上限、没有加入的标签数。 */
  skipped: number
  /** 原来的列表是空的（从数据集的标签生成）。 */
  generated: boolean
}

const LIMIT_MESSAGE = `类别数量不能超过 ${formatNumber(CLASS_LIST_MAX_COUNT)} 个`

/**
 * 项目的类别列表（训练时 YOLO 的类别编号、标注时的候选类别），以及每个类别在
 * 项目数据集中的分布；数据集中已有、但不在列表里的标签列在后面，可以加入。
 */
export function ProjectClasses({
  projectId,
  classes,
  labels,
}: ProjectClassesProps) {
  const [editing, setEditing] = useState(false)

  // 加入一个类别：服务端只追加，不会覆盖别人同时做的修改。同一时间只加入一个
  // （见 busy），先后返回的列表才不会在缓存中互相覆盖。
  const addOne = useMutation({
    mutationFn: (name: string) => addProjectClass(projectId, name),
    onSuccess: (list, name) => {
      cacheProjectClasses(projectId, list)
      const id = list.items.findIndex((cls) => cls.name === name)
      toast.success(`已将“${name}”加入类别列表`, {
        description: id >= 0 ? `类别编号为 ${id}。` : undefined,
      })
    },
    onError: (error, name) =>
      toast.error(`无法加入“${name}”`, { description: error.message }),
  })

  // 加入多个类别：整体替换，带上读取到的版本，期间列表被别人修改过时不保存。
  const addAll = useMutation({
    mutationFn: ({ base, next }: { base: string; next: MergedList }) =>
      setProjectClasses(projectId, next.classes, base),
    onSuccess: (list, { next }) => {
      cacheProjectClasses(projectId, list)
      const notes = []
      if (next.skipped > 0) {
        notes.push(
          `${LIMIT_MESSAGE}，还有 ${formatNumber(next.skipped)} 个标签没有加入。`
        )
      }
      if (next.generated) {
        notes.push("可以在“编辑类别”中调整顺序与颜色。")
      }
      toast.success(
        next.generated
          ? `已从数据集的标签生成 ${formatNumber(next.added)} 个类别`
          : `已加入 ${formatNumber(next.added)} 个类别`,
        { description: notes.length > 0 ? notes.join("") : undefined }
      )
    },
    onError: (error) => {
      if (isListConflict(error)) {
        toast.error("类别列表已被其他人修改", {
          description: "已重新读取最新的列表，请确认后再加入。",
        })
        void classes.refetch()
        return
      }
      toast.error("无法加入类别", { description: error.message })
    },
  })
  const busy = addOne.isPending || addAll.isPending

  if (classes.isPending) {
    return <ClassesSkeleton />
  }
  if (classes.error && classes.data === undefined) {
    return (
      <LoadError
        title="无法获取类别列表"
        error={classes.error}
        retrying={classes.isFetching}
        onRetry={() => classes.refetch()}
      />
    )
  }

  const list = classes.data
  const labelItems = labels.data?.items ?? []
  const labelOf = new Map(labelItems.map((label) => [label.name, label]))
  const missing = labelsNotInList(list.items, labelItems)
  // 占比的分母是项目中的所有目标（包括不在类别列表中的）。
  const totalObjects = labelItems.reduce(
    (sum, label) => sum + label.object_count,
    0
  )
  const colorOf = colorResolver(list.items)

  const onAddAll = () => {
    const merged = mergeLabels(list.items, labelItems)
    if (merged.added === 0) {
      toast.error(LIMIT_MESSAGE)
      return
    }
    addAll.mutate({
      base: list.version,
      next: {
        classes: merged.classes,
        added: merged.added,
        skipped: missing.length - merged.added,
        generated: list.items.length === 0,
      },
    })
  }

  return (
    <div className="grid grid-cols-1 gap-4">
      {list.items.length === 0 ? (
        <Empty className="border">
          <EmptyHeader className="max-w-md">
            <EmptyMedia variant="icon">
              <Shapes />
            </EmptyMedia>
            <EmptyTitle className="text-base">还没有类别列表</EmptyTitle>
            <EmptyDescription>
              训练时按类别列表的顺序生成 YOLO 的类别编号（第一个为
              0），标注时画框后也会优先提示这些类别。
              {labelItems.length > 0 &&
                `项目的数据集中已有 ${formatNumber(labelItems.length)} 种标签，可以按目标数量从多到少生成类别列表。`}
              {labels.data?.items.length === 0 &&
                "项目中的数据集有了标注后，这里还会按类别统计目标数量、图片数量与所在的数据集。"}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent className="flex-row flex-wrap justify-center gap-2">
            {labelItems.length > 0 && (
              <Button size="sm" onClick={onAddAll} disabled={busy}>
                {addAll.isPending ? <Spinner /> : <ListPlus />}
                从数据集的标签生成
              </Button>
            )}
            <Button
              size="sm"
              variant={labelItems.length > 0 ? "outline" : "default"}
              onClick={() => setEditing(true)}
              disabled={busy}
            >
              <Plus />
              添加类别
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <Card>
          <CardHeader>
            <CardTitle>类别列表</CardTitle>
            <CardDescription>
              训练时按这个顺序生成 YOLO 的类别编号（第一个为
              0）；标注时画框后会优先提示这些类别。
            </CardDescription>
            <CardAction>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setEditing(true)}
                disabled={busy}
              >
                <Pencil />
                编辑类别
              </Button>
            </CardAction>
          </CardHeader>
          <CardContent className="px-3 sm:px-4">
            <ol className="grid grid-cols-1 gap-1" aria-label="类别列表">
              {list.items.map((cls, index) => (
                <DistributionRow
                  key={cls.name}
                  id={index}
                  name={cls.name}
                  color={classColor(cls)}
                  label={labelOf.get(cls.name)}
                  labelsPending={labels.isPending}
                  totalObjects={totalObjects}
                />
              ))}
            </ol>
          </CardContent>
        </Card>
      )}

      {labels.error && labels.data === undefined && (
        <LoadError
          title="无法获取标签分布"
          error={labels.error}
          retrying={labels.isFetching}
          onRetry={() => labels.refetch()}
        />
      )}

      {missing.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>不在类别列表中的标签</CardTitle>
            <CardDescription>
              项目的数据集中有这些标签的目标，但它们不在类别列表中，训练导出
              YOLO 时会忽略这些目标。需要训练的标签请加入类别列表。
            </CardDescription>
            {/* 列表为空时，上面的“从数据集的标签生成”就是全部加入。 */}
            {list.items.length > 0 && (
              <CardAction>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={onAddAll}
                  disabled={busy}
                >
                  {addAll.isPending ? <Spinner /> : <Import />}
                  全部加入
                </Button>
              </CardAction>
            )}
          </CardHeader>
          <CardContent className="px-3 sm:px-4">
            <ul className="grid grid-cols-1 gap-1">
              {missing.map((label) => (
                <DistributionRow
                  key={label.name}
                  name={label.name}
                  color={colorOf(label.name)}
                  label={label}
                  totalObjects={totalObjects}
                  action={
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() => addOne.mutate(label.name)}
                      disabled={busy}
                      aria-label={`将“${label.name}”加入类别列表`}
                    >
                      {addOne.isPending && addOne.variables === label.name ? (
                        <Spinner className="size-3" />
                      ) : (
                        <Plus />
                      )}
                      加入
                    </Button>
                  }
                />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <ClassEditorDialog
        projectId={projectId}
        classes={list}
        open={editing}
        onOpenChange={setEditing}
      />
    </div>
  )
}

type DistributionRowProps = {
  /** 类别列表中的行：YOLO 类别编号（列表中的位置，从 0 开始）。 */
  id?: number
  name: string
  color: string
  /** 在项目数据集中的分布；没有目标时为 undefined。 */
  label: ProjectLabel | undefined
  /** 标签分布还在读取（还不知道有没有目标）。 */
  labelsPending?: boolean
  totalObjects: number
  /** 行末的按钮。 */
  action?: ReactNode
}

/**
 * 一个类别 / 标签的分布：目标数、图片数、所在数据集数与目标占比（占比条）。
 * 窄屏时明细换到占比条下面。
 */
function DistributionRow({
  id,
  name,
  color,
  label,
  labelsPending = false,
  totalObjects,
  action,
}: DistributionRowProps) {
  const share = label ? percentOf(label.object_count, totalObjects) : 0
  const detail = label
    ? `${formatNumber(label.object_count)} 个目标 · ${formatNumber(label.image_count)} 张图片 · ${formatNumber(label.dataset_count)} 个数据集`
    : null
  // 有类别编号时，占比条与明细对齐到颜色点（编号占 w-6，再加 gap-3）。
  const indent = id === undefined ? undefined : "pl-9"

  return (
    <li className="grid grid-cols-1 gap-2 rounded-lg px-3 py-2.5">
      <div className="flex items-center gap-3">
        {id !== undefined && (
          <span
            className="w-6 shrink-0 text-right font-mono text-xs text-muted-foreground tabular-nums"
            title={`类别编号 ${id}`}
          >
            {id}
          </span>
        )}
        <span
          className="size-2.5 shrink-0 rounded-full"
          style={{ backgroundColor: color }}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {name}
        </span>
        {detail !== null ? (
          <>
            <span className="hidden text-xs text-muted-foreground tabular-nums sm:inline">
              {detail}
            </span>
            <span className="w-14 text-right text-sm font-medium tabular-nums">
              {formatPercent(share, 1)}
            </span>
          </>
        ) : labelsPending ? (
          <Skeleton className="h-4 w-24" />
        ) : (
          <>
            <span className="text-xs text-muted-foreground">暂无目标</span>
            {/* 占比一栏留空，与其它行的明细对齐。 */}
            <span className="w-14" aria-hidden />
          </>
        )}
        {action}
      </div>
      {detail !== null && (
        <>
          <div className={indent}>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full transition-[width] duration-500"
                style={{
                  width: `${Math.max(share, 0.5)}%`,
                  backgroundColor: color,
                }}
              />
            </div>
          </div>
          <span
            className={cn(
              "text-xs text-muted-foreground tabular-nums sm:hidden",
              indent
            )}
          >
            {detail}
          </span>
        </>
      )}
    </li>
  )
}

function ClassesSkeleton() {
  return (
    <Card aria-busy="true" aria-label="正在加载">
      <CardHeader>
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-4 w-96 max-w-full" />
      </CardHeader>
      <CardContent className="grid gap-5">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="grid gap-2">
            <div className="flex justify-between">
              <Skeleton className="h-4 w-28" />
              <Skeleton className="h-4 w-36" />
            </div>
            <Skeleton className="h-2 w-full" />
          </div>
        ))}
      </CardContent>
    </Card>
  )
}
