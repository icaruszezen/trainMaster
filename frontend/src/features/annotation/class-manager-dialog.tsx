import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowDown,
  ArrowUp,
  Import,
  Plus,
  RotateCcw,
  Trash2,
  TriangleAlert,
} from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  addClass,
  classColor,
  classNameError,
  mergeLabels,
  moveClass,
  removeClass,
  setClassColor,
} from "@/features/annotation/classes"
import { classHotkey } from "@/features/annotation/shortcuts"
import {
  classesQuery,
  datasetKeys,
  labelsQuery,
  lastYoloImportQuery,
  setDatasetClasses,
  type DatasetClass,
  type DatasetClassList,
} from "@/features/datasets/api"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"

type ClassManagerDialogProps = {
  datasetId: number
  /** 已读取的类别列表；还没有读取到时不能打开（不能把它当作空列表保存）。 */
  classes: DatasetClassList | undefined
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 编辑数据集的类别列表：增删、改名、颜色、顺序（决定数字键），从已有标签导入。 */
export function ClassManagerDialog({
  datasetId,
  classes,
  open,
  onOpenChange,
}: ClassManagerDialogProps) {
  return (
    <Dialog open={open && classes !== undefined} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[min(90dvh,760px)] flex-col gap-4 sm:max-w-lg">
        {open && classes && (
          <ClassManagerForm
            datasetId={datasetId}
            initial={classes}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

/** 保存时类别列表已被别人修改（`base_version` 不是最新的）。 */
function isListConflict(error: unknown) {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    error.code === "conflict"
  )
}

const toRows = (classes: readonly DatasetClass[]): Row[] =>
  classes.map((cls, index) => ({ ...cls, original: cls.name, key: index }))

type Row = DatasetClass & {
  /** 打开对话框时的名称（新增的为 null），用于提示改名。 */
  original: string | null
  key: number
}

function ClassManagerForm({
  datasetId,
  initial,
  onDone,
}: {
  datasetId: number
  initial: DatasetClassList
  onDone: () => void
}) {
  const queryClient = useQueryClient()
  const labels = useQuery(labelsQuery(datasetId))
  // 由 YOLO 导入的数据集：按导入时的类别顺序加入，导出时类别编号不变。
  const lastYolo = useQuery(lastYoloImportQuery(datasetId))
  // 打开对话框（或载入最新列表）时的列表：保存时带上它的版本，期间列表被
  // 别人修改过时服务端拒绝保存，不会悄悄覆盖。
  const [base, setBase] = useState(initial)
  const [rows, setRows] = useState<Row[]>(() => toRows(initial.items))
  const [nextKey, setNextKey] = useState(initial.items.length)
  const [newName, setNewName] = useState("")
  const [addError, setAddError] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (classes: DatasetClass[]) =>
      setDatasetClasses(datasetId, classes, base.version),
    onSuccess: (list) => {
      queryClient.setQueryData(datasetKeys.classes(datasetId), list)
      toast.success("类别列表已保存")
      onDone()
    },
  })
  const conflict = isListConflict(save.error)

  const reloadLatest = useMutation({
    mutationFn: () =>
      queryClient.fetchQuery({ ...classesQuery(datasetId), staleTime: 0 }),
    onSuccess: (list) => {
      setBase(list)
      setRows(toRows(list.items))
      setNextKey(list.items.length)
      save.reset()
    },
  })

  const names = rows.map((row) => row.name.trim())
  const rowErrors = rows.map((row, index) =>
    classNameError(
      row.name.trim(),
      names.filter((_, i) => i !== index)
    )
  )
  const renamed = rows.filter(
    (row) => row.original !== null && row.original !== row.name.trim()
  )
  const counts = new Map(
    (labels.data?.items ?? []).map((label) => [label.name, label.object_count])
  )
  const removedInUse = base.items.filter(
    (cls) =>
      !rows.some((row) => row.original === cls.name) &&
      (counts.get(cls.name) ?? 0) > 0
  )

  const toClasses = (list: Row[]): DatasetClass[] =>
    list.map((row) => ({ name: row.name.trim(), color: row.color }))

  const onAdd = () => {
    const result = addClass(toClasses(rows), newName)
    if (result.error !== null) {
      setAddError(result.error)
      return
    }
    setRows([
      ...rows,
      {
        name: newName.trim(),
        color: "",
        original: null,
        key: nextKey + 1,
      },
    ])
    setNextKey(nextKey + 1)
    setNewName("")
    setAddError(null)
  }

  const onImport = () => {
    const merged = mergeLabels(
      toClasses(rows),
      labels.data?.items ?? [],
      lastYolo.data?.classes ?? []
    )
    if (merged.added === 0) {
      toast.info("数据集中的标签都已在类别列表中")
      return
    }
    const added = merged.classes.slice(rows.length).map((cls, index) => ({
      ...cls,
      original: null,
      key: nextKey + 1 + index,
    }))
    setRows([...rows, ...added])
    setNextKey(nextKey + added.length)
    toast.success(`已加入 ${formatNumber(merged.added)} 个类别`)
  }

  const move = (from: number, to: number) => setRows(moveClass(rows, from, to))

  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    if (rowErrors.some((error) => error !== null)) {
      return
    }
    save.mutate(toClasses(rows))
  }

  const unknownLabels = (labels.data?.items ?? []).filter(
    (label) => !names.includes(label.name)
  )

  return (
    <form onSubmit={onSubmit} className="flex min-h-0 flex-1 flex-col gap-4">
      <DialogHeader>
        <DialogTitle>管理类别</DialogTitle>
        <DialogDescription>
          标注时可选的类别。前 10 个类别可以用数字键 1–9、0 快速切换；下载为
          YOLO 时默认按这个顺序编号（由 YOLO
          导入的数据集先沿用导入时的类别顺序）。
        </DialogDescription>
      </DialogHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-3">
        <div className="flex items-start gap-2">
          <div className="grid flex-1 gap-1">
            <Input
              value={newName}
              onChange={(event) => {
                setNewName(event.target.value)
                setAddError(null)
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                  event.preventDefault() // 不提交整个表单
                  onAdd()
                }
              }}
              placeholder="新类别名称，如 划痕"
              aria-label="新类别名称"
              aria-invalid={addError !== null}
            />
            {addError && <p className="text-xs text-destructive">{addError}</p>}
          </div>
          <Button type="button" variant="outline" onClick={onAdd}>
            <Plus />
            添加
          </Button>
        </div>

        {unknownLabels.length > 0 && (
          <div className="flex items-center justify-between gap-3 rounded-md border border-dashed px-3 py-2 text-sm">
            <span className="text-muted-foreground">
              数据集中还有 {formatNumber(unknownLabels.length)} 个标签不在列表中
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onImport}
              disabled={labels.isPending}
            >
              <Import />
              全部加入
            </Button>
          </div>
        )}

        <ol className="-mx-1 min-h-24 flex-1 overflow-y-auto px-1">
          {rows.length === 0 && (
            <li className="py-8 text-center text-sm text-muted-foreground">
              还没有类别。添加类别后，画框时默认使用当前类别。
            </li>
          )}
          {rows.map((row, index) => {
            const color = classColor({
              name: row.name.trim(),
              color: row.color,
            })
            const hotkey = classHotkey(index)
            const error = rowErrors[index]
            return (
              <li key={row.key} className="grid gap-1 py-1">
                <div className="flex items-center gap-2">
                  <span className="w-5 text-center text-xs text-muted-foreground tabular-nums">
                    {hotkey ?? ""}
                  </span>
                  <label
                    className="relative size-8 shrink-0 cursor-pointer overflow-hidden rounded-md border"
                    style={{ backgroundColor: color }}
                    title="选择颜色"
                  >
                    <input
                      type="color"
                      value={color}
                      onChange={(event) =>
                        setRows(setClassColor(rows, index, event.target.value))
                      }
                      className="absolute inset-0 cursor-pointer opacity-0"
                      aria-label={`“${row.name}”的颜色`}
                    />
                  </label>
                  <Input
                    value={row.name}
                    onChange={(event) =>
                      setRows(
                        rows.map((r, i) =>
                          i === index ? { ...r, name: event.target.value } : r
                        )
                      )
                    }
                    aria-label={`第 ${index + 1} 个类别的名称`}
                    aria-invalid={error !== null}
                    className="h-8 min-w-0 flex-1"
                  />
                  <span className="hidden w-12 text-right text-xs text-muted-foreground tabular-nums sm:inline">
                    {counts.has(row.original ?? row.name)
                      ? formatNumber(counts.get(row.original ?? row.name) ?? 0)
                      : ""}
                  </span>
                  <div className="flex shrink-0">
                    {/* 没有自定义颜色时隐藏（但占位），各行的输入框对齐。 */}
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={() =>
                        setRows(
                          rows.map((r, i) =>
                            i === index ? { ...r, color: "" } : r
                          )
                        )
                      }
                      aria-label="恢复自动配色"
                      title="恢复自动配色"
                      className={row.color === "" ? "invisible" : undefined}
                    >
                      <RotateCcw />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => move(index, index - 1)}
                      disabled={index === 0}
                      aria-label="上移"
                    >
                      <ArrowUp />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => move(index, index + 1)}
                      disabled={index === rows.length - 1}
                      aria-label="下移"
                    >
                      <ArrowDown />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => setRows(removeClass(rows, index))}
                      aria-label={`删除“${row.name}”`}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 />
                    </Button>
                  </div>
                </div>
                {error && (
                  <p className="pl-[4.25rem] text-xs text-destructive">
                    {error}
                  </p>
                )}
              </li>
            )
          })}
        </ol>

        {(renamed.length > 0 || removedInUse.length > 0) && (
          <Alert>
            <TriangleAlert />
            <AlertDescription>
              {renamed.length > 0 && (
                <p>
                  改名只修改类别列表，已保存的标注文件中仍是原来的名称（如“
                  {renamed[0].original}”），需要时请逐张修改。
                </p>
              )}
              {removedInUse.length > 0 && (
                <p>
                  删除的类别“{removedInUse[0].name}”
                  {removedInUse.length > 1
                    ? ` 等 ${removedInUse.length} 个`
                    : ""}
                  仍有标注，这些框会保留，显示为“不在类别列表中”。
                </p>
              )}
            </AlertDescription>
          </Alert>
        )}

        {save.error && (
          <div role="alert" className="grid gap-2 text-sm text-destructive">
            <p>{save.error.message}</p>
            {conflict && (
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => reloadLatest.mutate()}
                  disabled={reloadLatest.isPending}
                >
                  {reloadLatest.isPending ? <Spinner /> : <RotateCcw />}
                  载入最新列表
                </Button>
                <span className="text-xs text-muted-foreground">
                  这里还没保存的修改会被放弃
                </span>
              </div>
            )}
            {reloadLatest.error && (
              <p>无法读取最新的类别列表：{reloadLatest.error.message}</p>
            )}
          </div>
        )}
      </div>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          取消
        </Button>
        <Button
          type="submit"
          disabled={
            save.isPending ||
            conflict ||
            reloadLatest.isPending ||
            rowErrors.some((e) => e !== null)
          }
        >
          {save.isPending && <Spinner />}
          保存
        </Button>
      </DialogFooter>
    </form>
  )
}
