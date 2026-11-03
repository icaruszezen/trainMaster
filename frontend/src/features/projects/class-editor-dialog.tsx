import { useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowDown,
  ArrowUp,
  FileText,
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
import { classColor, classNameError } from "@/features/annotation/classes"
import {
  CLASS_LIST_MAX_COUNT,
  parseClassList,
  readClassFile,
} from "@/features/datasets/class-list"
import {
  cacheProjectClasses,
  projectClassesQuery,
  projectLabelsQuery,
  setProjectClasses,
  type ProjectClass,
  type ProjectClassList,
} from "@/features/projects/api"
import {
  addClass,
  arrangeByNames,
  type ArrangeResult,
  mergeLabels,
  moveClass,
  removeClass,
  setClassColor,
} from "@/features/projects/class-edit"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"

type ClassEditorDialogProps = {
  projectId: number
  /** 已读取的类别列表；还没有读取到时不能打开（不能把它当作空列表保存）。 */
  classes: ProjectClassList | undefined
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 编辑项目的类别列表：增删、改名、颜色、顺序（训练时 YOLO 的类别编号），
 * 按 classes.txt 排列，从项目数据集已有的标签加入。
 */
export function ClassEditorDialog({
  projectId,
  classes,
  open,
  onOpenChange,
}: ClassEditorDialogProps) {
  return (
    <Dialog open={open && classes !== undefined} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[min(90dvh,760px)] flex-col gap-4 sm:max-w-lg">
        {open && classes && (
          <ClassEditorForm
            projectId={projectId}
            initial={classes}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

/** 保存时类别列表已被别人修改（`base_version` 不是最新的）。 */
export function isListConflict(error: unknown) {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    error.code === "conflict"
  )
}

const toRows = (classes: readonly ProjectClass[]): Row[] =>
  classes.map((cls, index) => ({ ...cls, original: cls.name, key: index }))

type Row = ProjectClass & {
  /** 打开对话框时的名称（新增的为 null），用于提示改名。 */
  original: string | null
  key: number
}

function ClassEditorForm({
  projectId,
  initial,
  onDone,
}: {
  projectId: number
  initial: ProjectClassList
  onDone: () => void
}) {
  const queryClient = useQueryClient()
  const labels = useQuery(projectLabelsQuery(projectId))
  // 打开对话框（或载入最新列表）时的列表：保存时带上它的版本，期间列表被
  // 别人修改过时服务端拒绝保存，不会悄悄覆盖。
  const [base, setBase] = useState(initial)
  const [rows, setRows] = useState<Row[]>(() => toRows(initial.items))
  const [nextKey, setNextKey] = useState(initial.items.length)
  const [newName, setNewName] = useState("")
  const [addError, setAddError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const save = useMutation({
    mutationFn: (classes: ProjectClass[]) =>
      setProjectClasses(projectId, classes, base.version),
    onSuccess: (list) => {
      cacheProjectClasses(projectId, list)
      toast.success("类别列表已保存")
      onDone()
    },
  })
  const conflict = isListConflict(save.error)

  const reloadLatest = useMutation({
    mutationFn: () =>
      queryClient.fetchQuery({
        ...projectClassesQuery(projectId),
        staleTime: 0,
      }),
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

  const toClasses = (list: Row[]): ProjectClass[] =>
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
    const merged = mergeLabels(toClasses(rows), labels.data?.items ?? [])
    if (merged.added === 0) {
      toast.info(
        rows.length >= CLASS_LIST_MAX_COUNT
          ? `类别数量不能超过 ${CLASS_LIST_MAX_COUNT} 个`
          : "数据集中的标签都已在类别列表中"
      )
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

  /** 按 classes.txt 排列：文件中的类别按文件顺序排在最前，其余的排在后面。 */
  const onReadFile = async (file: File) => {
    let text: string
    try {
      text = await readClassFile(file)
    } catch (cause) {
      toast.error(`无法读取 ${file.name}`, {
        description: cause instanceof Error ? cause.message : undefined,
      })
      return
    }
    const parsed = parseClassList(text)
    if (parsed.error !== null) {
      toast.error(`无法读取 ${file.name}`, { description: parsed.error })
      return
    }
    let key = nextKey
    const arranged = arrangeByNames(rows, parsed.classes, (name) => {
      key += 1
      return { name, color: "", original: null, key }
    })
    const unchanged =
      arranged.classes.length === rows.length &&
      arranged.classes.every((row, index) => row === rows[index])
    if (unchanged) {
      toast.info(`类别顺序已与 ${file.name} 一致`)
      return
    }
    setRows(arranged.classes)
    setNextKey(key)
    reportArranged(file.name, parsed.classes.length, arranged)
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
        <DialogTitle>编辑类别</DialogTitle>
        <DialogDescription>
          训练时 YOLO 的类别编号按这个顺序（第 1 个为 0）；标注时作为候选类别。
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
              项目的数据集中还有 {formatNumber(unknownLabels.length)}{" "}
              个标签不在列表中
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

        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-xs text-muted-foreground tabular-nums">
            {rows.length > 0
              ? `共 ${formatNumber(rows.length)} 个类别（编号 0–${formatNumber(rows.length - 1)}）`
              : "还没有类别"}
          </span>
          <Button
            type="button"
            variant="outline"
            size="xs"
            onClick={() => fileInputRef.current?.click()}
            title="按 classes.txt 的顺序排列类别，沿用已有模型或其它工具的类别编号"
          >
            <FileText />
            读取 classes.txt
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".txt,.names,text/plain"
            className="hidden"
            tabIndex={-1}
            aria-hidden
            onChange={(event) => {
              const file = event.target.files?.[0]
              event.target.value = "" // 同一个文件可以再次选择
              if (file) {
                void onReadFile(file)
              }
            }}
          />
        </div>

        <ol className="-mx-1 min-h-24 flex-1 overflow-y-auto px-1">
          {rows.length === 0 && (
            <li className="py-8 text-center text-sm text-muted-foreground">
              添加类别，或读取已有的 classes.txt。
            </li>
          )}
          {rows.map((row, index) => {
            const color = classColor({
              name: row.name.trim(),
              color: row.color,
            })
            const error = rowErrors[index]
            const count = counts.get(row.original ?? row.name.trim())
            return (
              <li key={row.key} className="grid gap-1 py-1">
                <div className="flex items-center gap-2">
                  <span
                    className="w-6 shrink-0 text-right font-mono text-xs text-muted-foreground tabular-nums"
                    title={`类别编号 ${index}`}
                  >
                    {index}
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
                    aria-label={`类别 ${index} 的名称`}
                    aria-invalid={error !== null}
                    className="h-8 min-w-0 flex-1"
                  />
                  <span
                    className="hidden w-12 text-right text-xs text-muted-foreground tabular-nums sm:inline"
                    title={
                      count === undefined
                        ? undefined
                        : `${formatNumber(count)} 个目标`
                    }
                  >
                    {count === undefined ? "" : formatNumber(count)}
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
                  <p className="pl-[4.5rem] text-xs text-destructive">
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
                  {renamed[0].original}”），需要时请逐张修改；修改之前，训练
                  导出时会忽略这些目标。
                </p>
              )}
              {removedInUse.length > 0 && (
                <p>
                  删除的类别“{removedInUse[0].name}”
                  {removedInUse.length > 1
                    ? ` 等 ${removedInUse.length} 个`
                    : ""}
                  仍有标注：标注文件中的框会保留，但训练导出时会忽略这些目标。
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

/** 按 classes.txt 排列后的提示：新加入了几个、文件中没有的几个排在最后。 */
function reportArranged(
  fileName: string,
  fileCount: number,
  { added, rest, dropped }: ArrangeResult<unknown>
) {
  const details: string[] = []
  if (added > 0) {
    details.push(`新加入 ${formatNumber(added)} 个类别`)
  }
  if (rest > 0) {
    details.push(`文件中没有的 ${formatNumber(rest)} 个类别排在最后`)
  }
  if (dropped > 0) {
    details.push(
      `类别数量不能超过 ${formatNumber(CLASS_LIST_MAX_COUNT)} 个，最后的 ${formatNumber(dropped)} 个类别没有保留（不想要这次修改时点“取消”）`
    )
  }
  const title = `已按 ${fileName} 排列 ${formatNumber(fileCount)} 个类别`
  const description = details.length > 0 ? `${details.join("；")}。` : undefined
  if (dropped > 0) {
    toast.warning(title, { description })
  } else {
    toast.success(title, { description })
  }
}
