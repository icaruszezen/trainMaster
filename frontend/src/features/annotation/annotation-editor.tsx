import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useSearchParams } from "react-router"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import {
  AnnotationCanvas,
  type CanvasHandle,
} from "@/features/annotation/annotation-canvas"
import {
  applySavedDetail,
  editorImageQuery,
  fetchImageAt,
  persistAnnotation,
  type SaveContext,
} from "@/features/annotation/api"
import {
  AnnotationSaver,
  isConflictError,
  type SaverState,
} from "@/features/annotation/autosave"
import { ClassManagerDialog } from "@/features/annotation/class-manager-dialog"
import { colorResolver } from "@/features/annotation/classes"
import {
  cycleSelection,
  documentReducer,
  duplicateBox,
  emptyDocument,
  pasteObjects,
  removeObject,
  toAnnotationObject,
  updateObject,
  type EditorObject,
} from "@/features/annotation/document"
import {
  EditorToolbar,
  type ViewOptions,
} from "@/features/annotation/editor-toolbar"
import {
  moveBox,
  resizeEdges,
  type Box,
  type Size,
  type Viewport,
} from "@/features/annotation/geometry"
import { ImageListPanel } from "@/features/annotation/image-list-panel"
import { LabelPicker } from "@/features/annotation/label-picker"
import {
  allowsRepeat,
  commandForKey,
  isMac,
  shouldIgnoreKey,
  type Command,
  type Tool,
} from "@/features/annotation/shortcuts"
import { ShortcutsDialog } from "@/features/annotation/shortcuts-dialog"
import {
  ClassPanel,
  ImageInfo,
  NoticeBox,
  ObjectPanel,
  type Notice,
} from "@/features/annotation/side-panel"
import {
  NO_FILTERS,
  parseFilters,
  updateParams,
} from "@/features/annotation/workspace-params"
import {
  classesQuery,
  datasetKeys,
  deleteAnnotation,
  imageNeighborsQuery,
  saveAnnotation,
  setDatasetClasses,
  thumbnailUrl,
  type AnnotationObject,
  type Dataset,
  type DatasetClass,
  type DatasetImage,
  type DatasetImageDetail,
  type ImageFilters,
} from "@/features/datasets/api"
import { formatNumber } from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"

const VIEW_KEY = "trainmaster.annotation.view"
const TOOL_KEY = "trainmaster.annotation.tool"
const classKey = (datasetId: number) =>
  `trainmaster.annotation.class.${datasetId}`
export const lastImageKey = (datasetId: number) =>
  `trainmaster.annotation.last.${datasetId}`

const DEFAULT_VIEW: ViewOptions = {
  showLabels: true,
  showBoxes: true,
  keepViewport: true,
  brightness: 1,
  contrast: 1,
}

function readView(): ViewOptions {
  try {
    const saved = JSON.parse(readPreference(VIEW_KEY) ?? "{}") as unknown
    if (typeof saved !== "object" || saved === null) {
      return DEFAULT_VIEW
    }
    const s = saved as Partial<Record<keyof ViewOptions, unknown>>
    const bool = (value: unknown, fallback: boolean) =>
      typeof value === "boolean" ? value : fallback
    const ratio = (value: unknown) =>
      typeof value === "number" && value >= 0.2 && value <= 3 ? value : 1
    return {
      showLabels: bool(s.showLabels, true),
      showBoxes: true, // 每次打开都显示框
      keepViewport: bool(s.keepViewport, true),
      brightness: ratio(s.brightness),
      contrast: ratio(s.contrast),
    }
  } catch {
    return DEFAULT_VIEW
  }
}

function readTool(): Tool {
  const value = readPreference(TOOL_KEY)
  return value === "rect" || value === "pan" ? value : "select"
}

const EMPTY_CLASSES: DatasetClass[] = []

/** 正在编辑的图片：载入时的详情；`token` 在重新读取时递增。 */
type Loaded = { detail: DatasetImageDetail; token: number }

type Picker =
  /** 画了新框但还没有类别（类别列表为空）：选择后才加入。 */
  | { mode: "new"; box: Box }
  /** 修改选中框的类别。 */
  | { mode: "relabel"; uid: string }
  /** 切换当前类别。 */
  | { mode: "active" }

type Carry = { size: Size; viewport: Viewport } | null

function imageSizeOf(image: {
  width: number | null
  height: number | null
}): Size | null {
  return image.width && image.height
    ? { width: image.width, height: image.height }
    : null
}

/**
 * 标注工作台：一个数据集中按固定顺序逐张标注。当前图片与筛选条件在地址栏
 * （`?image=12&status=unannotated`），刷新、分享链接都能回到同一张。
 */
export function AnnotationEditor({ dataset }: { dataset: Dataset }) {
  const datasetId = dataset.id
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()
  const imageId = Number(params.get("image"))
  const filters = useMemo(() => parseFilters(params), [params])
  const [mac] = useState(isMac)
  const mod = mac ? "⌘" : "Ctrl"

  const classesQ = useQuery(classesQuery(datasetId))
  const classes = classesQ.data?.items ?? EMPTY_CLASSES
  const detailQ = useQuery(editorImageQuery(datasetId, imageId))
  const neighborsQ = useQuery(imageNeighborsQuery(datasetId, imageId, filters))

  // 跨图片保持的设置。
  const [tool, setToolState] = useState<Tool>(readTool)
  const [activeClass, setActiveClassState] = useState<string | null>(() =>
    readPreference(classKey(datasetId))
  )
  const [view, setViewState] = useState<ViewOptions>(readView)
  const [hiddenClasses, setHiddenClasses] = useState<ReadonlySet<string>>(
    () => new Set()
  )
  const [carry, setCarry] = useState<Carry>(null)
  const [zoom, setZoom] = useState<number | null>(null)
  const [naturalSize, setNaturalSize] = useState<{
    id: number
    size: Size
  } | null>(null)

  // 当前图片的编辑状态。
  const [doc, dispatch] = useReducer(documentReducer, emptyDocument)
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [highlight, setHighlight] = useState<string | null>(null)
  const [picker, setPicker] = useState<Picker | null>(null)
  const [unlocked, setUnlocked] = useState<number | null>(null)
  const [saveState, setSaveState] = useState<SaverState>({
    status: "clean",
    error: null,
  })
  const [savedAt, setSavedAt] = useState<Date | null>(null)
  const [navigating, setNavigating] = useState(false)

  // 对话框与面板。
  const [helpOpen, setHelpOpen] = useState(false)
  const [classDialogOpen, setClassDialogOpen] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [imagesOpen, setImagesOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)

  const canvasRef = useRef<CanvasHandle>(null)
  const positionRef = useRef<HTMLInputElement>(null)
  const sessionRef = useRef<SaveContext | null>(null)
  const clipboard = useRef<{ imageId: number; objects: AnnotationObject[] }>({
    imageId: 0,
    objects: [],
  })
  const lastViewport = useRef<{ size: Size; viewport: Viewport } | null>(null)
  const uidCounter = useRef(0)

  const [saver] = useState(
    () =>
      new AnnotationSaver<SaveContext>({
        save: async ({ objects, baseVersion, force, context }) => {
          const saved = await persistAnnotation(
            objects,
            baseVersion,
            force,
            context
          )
          if (!saved) {
            return baseVersion
          }
          applySavedDetail(queryClient, context.datasetId, saved)
          return saved.annotation_version
        },
        onChange: (state) => {
          setSaveState(state)
          if (state.status === "saved") {
            setSavedAt(new Date())
          }
        },
      })
  )

  // 换了图片：载入它的框（在渲染中调整状态，避免先渲染一次旧图片的框）。
  const detail = detailQ.data?.id === imageId ? detailQ.data : undefined
  if (detail && loaded?.detail.id !== detail.id) {
    setLoaded({ detail, token: (loaded?.token ?? 0) + 1 })
    dispatch({ type: "load", objects: detail.objects, key: String(detail.id) })
    setHighlight(null)
    setPicker(null)
  }
  const current = loaded && loaded.detail.id === imageId ? loaded.detail : null

  useEffect(() => {
    if (!loaded) {
      return
    }
    const { detail: d } = loaded
    const context: SaveContext = {
      datasetId,
      imageId: d.id,
      hadFile: d.annotation_version !== "",
      negative: false,
    }
    sessionRef.current = context
    saver.reset(d.objects, d.annotation_version, context)
    writePreference(lastImageKey(datasetId), String(d.id))
  }, [loaded, saver, datasetId])

  useEffect(() => {
    if (loaded) {
      saver.update(doc.objects)
    }
  }, [doc.objects, loaded, saver])

  // 离开工作台时保存未保存的修改。
  useEffect(
    () => () => {
      void saver.flush().then((ok) => {
        if (!ok && saver.isDirty()) {
          toast.error("离开标注工作台前，最后的修改没有保存成功", {
            description: saver.getState().error ?? undefined,
          })
        }
      })
    },
    [saver]
  )

  // 关闭 / 刷新页面：有未保存的修改时提示，并尽量用 keepalive 请求补发。
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      const { status } = saver.getState()
      if (saver.isDirty() || status === "saving") {
        event.preventDefault()
      }
    }
    const onPageHide = () => {
      const pending = saver.pending()
      if (!pending || saver.getState().status === "saving") {
        return
      }
      const { context } = pending
      if (
        pending.objects.length === 0 &&
        !context.hadFile &&
        !context.negative
      ) {
        return
      }
      void saveAnnotation(
        context.datasetId,
        context.imageId,
        { objects: pending.objects, base_version: pending.baseVersion },
        { keepalive: true }
      ).catch(() => undefined)
    }
    window.addEventListener("beforeunload", onBeforeUnload)
    window.addEventListener("pagehide", onPageHide)
    return () => {
      window.removeEventListener("beforeunload", onBeforeUnload)
      window.removeEventListener("pagehide", onPageHide)
    }
  }, [saver])

  // 预加载前后的图片（详情与大图）。
  const prev = neighborsQ.data?.prev ?? null
  const next = neighborsQ.data?.next ?? null
  useEffect(() => {
    for (const image of [next, prev]) {
      if (image) {
        void queryClient.prefetchQuery(editorImageQuery(datasetId, image.id))
        new Image().src = thumbnailUrl(datasetId, image, "lg")
      }
    }
  }, [datasetId, next, prev, queryClient])

  const colorFor = useMemo(() => colorResolver(classes), [classes])
  const size =
    (current && imageSizeOf(current)) ??
    (naturalSize && naturalSize.id === imageId ? naturalSize.size : null)

  // 保存后缓存中的最新详情（状态、标注文件路径）；`current` 是开始编辑时的。
  const latest = detailQ.data?.id === imageId ? detailQ.data : current

  // ---- 只读的原因 ----
  const notices: Notice[] = []
  let readOnly = false
  if (current && latest) {
    if (dataset.directory_missing) {
      readOnly = true
      notices.push({
        tone: "error",
        message: "数据集文件夹不存在，无法保存标注。",
      })
    } else if (latest.annotation_state === "invalid") {
      if (unlocked !== current.id) {
        readOnly = true
      }
      notices.push({
        tone: "warning",
        message: (
          <>
            标注文件无法解析：{latest.annotation_error}
            {unlocked !== current.id
              ? "。为避免误覆盖，暂不能编辑。"
              : "。保存时会用新的标注覆盖它。"}
          </>
        ),
        action:
          unlocked !== current.id ? (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setUnlocked(current.id)}
            >
              重新标注（保存时覆盖这个文件）
            </Button>
          ) : undefined,
      })
    } else if (latest.annotation_error) {
      notices.push({ tone: "warning", message: latest.annotation_error })
    }
    if (latest.annotation_blocked) {
      readOnly = true
      notices.push({ tone: "error", message: latest.annotation_blocked })
    }
    if (latest.annotation_unsupported > 0) {
      notices.push({
        tone: "info",
        message: `标注文件中还有 ${formatNumber(latest.annotation_unsupported)} 个不是矩形框（如多边形）或缺少坐标的目标，这里不显示，保存时原样保留。`,
      })
    }
    if (!size) {
      readOnly = true // 图片尺寸未知（还在读取或无法读取）
    }
  }
  // ---- 设置 ----
  const setTool = (value: Tool) => {
    if (value === "rect" && readOnly) {
      toast.info("这张图片当前不能编辑")
      return
    }
    setToolState(value)
    writePreference(TOOL_KEY, value)
  }
  const setView = (value: ViewOptions) => {
    setViewState(value)
    writePreference(VIEW_KEY, JSON.stringify(value))
  }
  const setActiveClass = (name: string) => {
    setActiveClassState(name)
    writePreference(classKey(datasetId), name)
  }
  const effectiveClass =
    activeClass !== null && classes.some((cls) => cls.name === activeClass)
      ? activeClass
      : (classes[0]?.name ?? null)

  // ---- 编辑 ----
  const newUid = () => {
    uidCounter.current += 1
    return `n${uidCounter.current}`
  }
  const change = (objects: EditorObject[], select?: string | null) => {
    if (readOnly) {
      return
    }
    dispatch({ type: "change", objects, select })
  }
  const select = (uid: string | null) => dispatch({ type: "select", uid })
  const selectedObject = doc.objects.find((o) => o.uid === doc.selected)

  const addObject = (box: Box, name: string) => {
    const object: EditorObject = {
      ...box,
      name,
      difficult: false,
      truncated: false,
      uid: newUid(),
    }
    change([...doc.objects, object], object.uid)
  }

  const addClassToList = async (name: string) => {
    if (classes.some((cls) => cls.name === name)) {
      return true
    }
    try {
      const list = await setDatasetClasses(datasetId, [
        ...classes,
        { name, color: "" },
      ])
      queryClient.setQueryData(datasetKeys.classes(datasetId), list)
      toast.success(`已把“${name}”加入类别列表`)
      return true
    } catch (error) {
      toast.error("无法保存类别列表", {
        description: error instanceof Error ? error.message : undefined,
      })
      return false
    }
  }

  const relabel = (uid: string, name: string) =>
    change(updateObject(doc.objects, uid, { name }))

  const onCreate = (box: Box) => {
    if (effectiveClass === null) {
      setPicker({ mode: "new", box })
      return
    }
    addObject(box, effectiveClass)
  }

  const onPick = async (name: string, created: boolean) => {
    const target = picker
    setPicker(null)
    if (!target) {
      return
    }
    if (created && !(await addClassToList(name))) {
      return
    }
    if (target.mode === "new") {
      addObject(target.box, name)
      setActiveClass(name)
    } else if (target.mode === "relabel") {
      relabel(target.uid, name)
    } else {
      setActiveClass(name)
      toast(`当前类别：${name}`, { duration: 1200 })
    }
  }

  const pickClass = (name: string) => {
    if (selectedObject && !readOnly) {
      relabel(selectedObject.uid, name)
    } else {
      setActiveClass(name)
    }
  }

  const toggleFlag = (flag: "difficult" | "truncated") => {
    if (!selectedObject) {
      toast.info("请先选中一个框")
      return
    }
    if (readOnly) {
      return
    }
    const value = !selectedObject[flag]
    change(updateObject(doc.objects, selectedObject.uid, { [flag]: value }))
    const label =
      flag === "difficult" ? "难例（difficult）" : "截断（truncated）"
    toast(value ? `已标记为${label}` : `已取消${label}`, { duration: 1200 })
  }

  const copy = () => {
    const objects = selectedObject ? [selectedObject] : doc.objects
    if (objects.length === 0) {
      toast.info("这张图片上没有框可以复制")
      return
    }
    clipboard.current = {
      imageId,
      objects: objects.map(toAnnotationObject),
    }
    toast(`已复制 ${formatNumber(objects.length)} 个框`, { duration: 1200 })
  }

  const paste = (source = clipboard.current) => {
    if (!size || readOnly) {
      return
    }
    if (source.objects.length === 0) {
      toast.info("还没有复制框（先选中框按 Ctrl+C）")
      return
    }
    const sameImage = source.imageId === imageId
    const shifted = sameImage
      ? source.objects.map((object) => ({
          ...object,
          ...duplicateBox(object, size),
        }))
      : source.objects
    const { objects, dropped } = pasteObjects(shifted, size, newUid)
    if (objects.length > 0) {
      change([...doc.objects, ...objects], objects.at(-1)?.uid ?? null)
    }
    if (dropped > 0) {
      toast.warning(
        `${formatNumber(dropped)} 个框在这张图片的范围之外，没有粘贴`
      )
    }
  }

  const copyPrevious = async () => {
    try {
      const neighbors = await queryClient.fetchQuery(
        imageNeighborsQuery(datasetId, imageId, NO_FILTERS)
      )
      if (!neighbors.prev) {
        toast.info("这是第一张图片")
        return
      }
      const previous = await queryClient.fetchQuery(
        editorImageQuery(datasetId, neighbors.prev.id)
      )
      if (previous.objects.length === 0) {
        toast.info(`上一张（${previous.file_name}）没有框`)
        return
      }
      paste({ imageId: previous.id, objects: previous.objects })
      toast.success(
        `已沿用上一张的 ${formatNumber(previous.objects.length)} 个框`
      )
    } catch (error) {
      toast.error("无法读取上一张图片的标注", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  // ---- 保存 ----
  const reload = async () => {
    try {
      const fresh = await queryClient.fetchQuery({
        ...editorImageQuery(datasetId, imageId),
        staleTime: 0,
      })
      setLoaded({ detail: fresh, token: (loaded?.token ?? 0) + 1 })
      dispatch({ type: "load", objects: fresh.objects, key: String(fresh.id) })
      setUnlocked(null)
      toast.success("已重新读取标注文件")
    } catch (error) {
      toast.error("无法读取标注文件", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  const overwrite = async () => {
    if (await saver.overwrite()) {
      toast.success("已用你的版本覆盖标注文件")
    }
  }

  const markNegative = async () => {
    const session = sessionRef.current
    if (!session || readOnly) {
      return
    }
    if (doc.objects.length > 0) {
      toast.info("图片上还有框：删除所有框后才能标为空标注")
      return
    }
    if (!(await saver.flush())) {
      return
    }
    try {
      session.negative = true
      const saved = await saveAnnotation(datasetId, session.imageId, {
        objects: [],
        base_version: saver.getVersion(),
      })
      applySavedDetail(queryClient, datasetId, saved)
      saver.rebase([], saved.annotation_version)
      toast.success("已标为空标注（没有目标）")
    } catch (error) {
      session.negative = false
      if (isConflictError(error)) {
        toast.error("标注文件已被修改，请重新读取后再试")
      } else {
        toast.error("保存失败", {
          description: error instanceof Error ? error.message : undefined,
        })
      }
    }
  }

  const clearFile = async () => {
    const session = sessionRef.current
    setConfirmClear(false)
    if (!session) {
      return
    }
    if (!(await saver.flush())) {
      toast.error("请先处理保存失败或冲突")
      return
    }
    try {
      const saved = await deleteAnnotation(
        datasetId,
        session.imageId,
        saver.getVersion()
      )
      applySavedDetail(queryClient, datasetId, saved)
      setLoaded({ detail: saved, token: (loaded?.token ?? 0) + 1 })
      dispatch({ type: "load", objects: [], key: String(saved.id) })
      toast.success("已删除标注文件，图片恢复为未标注")
    } catch (error) {
      toast.error(
        isConflictError(error)
          ? "标注文件已被修改，请重新读取后再试"
          : "删除失败",
        {
          description:
            !isConflictError(error) && error instanceof Error
              ? error.message
              : undefined,
        }
      )
    }
  }

  const save = async () => {
    const session = sessionRef.current
    if (
      session &&
      !readOnly &&
      !saver.isDirty() &&
      doc.objects.length === 0 &&
      saver.getVersion() === ""
    ) {
      // 与 LabelImg 一致：没有框的图片按保存即写入空标注。
      await markNegative()
      return
    }
    const ok = await saver.flush()
    if (ok && saver.getState().status !== "clean") {
      toast.success("已保存", { duration: 1200 })
    }
  }

  if (saveState.status === "conflict") {
    notices.unshift({
      tone: "warning",
      message:
        "这张图片的标注文件已被修改（可能有其他人同时在标注它，或在平台外用其它工具改过）。",
      action: (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => void reload()}>
            载入最新版本（放弃我的修改）
          </Button>
          <Button size="sm" variant="outline" onClick={() => void overwrite()}>
            用我的版本覆盖
          </Button>
        </div>
      ),
    })
  } else if (saveState.status === "error") {
    notices.unshift({
      tone: "error",
      message: `保存失败：${saveState.error ?? "未知错误"}`,
      action: (
        <Button size="sm" variant="outline" onClick={() => void saver.flush()}>
          重试
        </Button>
      ),
    })
  }
  // 冲突、保存失败、只读原因等需要处理的提示放在最显眼的位置。
  const urgentNotices = notices.filter((notice) => notice.tone !== "info")

  // ---- 切换图片 ----
  const openImage = (id: number) => {
    setCarry(view.keepViewport ? lastViewport.current : null)
    setParams((previous) => updateParams(previous, { image: id }))
    setImagesOpen(false)
  }

  const navigateTo = async (target: number | null | undefined) => {
    if (!target || target === imageId || navigating) {
      return
    }
    setNavigating(true)
    try {
      const ok = await saver.flush()
      if (!ok && saver.isDirty()) {
        const state = saver.getState()
        if (state.status === "conflict") {
          toast.error("请先处理这张图片的保存冲突")
        } else {
          toast.error("保存失败，没有切换图片", {
            description: state.error ?? undefined,
            action: {
              label: "放弃修改并切换",
              onClick: () => openImage(target),
            },
          })
        }
        return
      }
      openImage(target)
    } finally {
      setNavigating(false)
    }
  }

  const step = async (direction: 1 | -1, unannotated: boolean) => {
    try {
      const neighbors = await queryClient.fetchQuery(
        imageNeighborsQuery(
          datasetId,
          imageId,
          unannotated ? { status: "unannotated", label: "", q: "" } : filters
        )
      )
      const target = direction === 1 ? neighbors.next : neighbors.prev
      if (!target) {
        toast.info(
          unannotated
            ? direction === 1
              ? "后面没有未标注的图片了"
              : "前面没有未标注的图片了"
            : direction === 1
              ? "已经是最后一张"
              : "已经是第一张"
        )
        return
      }
      await navigateTo(target.id)
    } catch (error) {
      toast.error("无法获取相邻的图片", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  const gotoIndex = async (index: number) => {
    try {
      const image = await fetchImageAt(datasetId, filters, index)
      if (image) {
        await navigateTo(image.id)
      }
    } catch (error) {
      toast.error("无法跳转", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  const setFilters = (changes: Partial<ImageFilters>) =>
    setParams((previous) => updateParams(previous, changes), { replace: true })

  // ---- 快捷键 ----
  const run = (command: Command) => {
    switch (command.type) {
      case "tool":
        setTool(command.tool)
        return
      case "image":
        void step(command.direction, command.unannotated)
        return
      case "goto":
        positionRef.current?.focus()
        return
      case "save":
        void save()
        return
      case "undo":
        if (!readOnly) dispatch({ type: "undo" })
        return
      case "redo":
        if (!readOnly) dispatch({ type: "redo" })
        return
      case "copy":
        copy()
        return
      case "paste":
        paste()
        return
      case "duplicate":
        if (selectedObject && size && !readOnly) {
          const object = {
            ...selectedObject,
            ...duplicateBox(selectedObject, size),
            uid: newUid(),
          }
          change([...doc.objects, object], object.uid)
        }
        return
      case "delete":
        if (selectedObject && !readOnly) {
          change(removeObject(doc.objects, selectedObject.uid), null)
        }
        return
      case "cycle": {
        const uid = cycleSelection(
          doc.objects.filter(
            (o) => view.showBoxes && !hiddenClasses.has(o.name)
          ),
          doc.selected,
          command.direction
        )
        select(uid)
        const object = doc.objects.find((o) => o.uid === uid)
        if (object) {
          canvasRef.current?.reveal(object)
        }
        return
      }
      case "escape":
        if (picker) {
          setPicker(null)
        } else if (canvasRef.current?.cancelDrawing()) {
          // 已取消正在画的框
        } else if (doc.selected !== null) {
          select(null)
        } else if (tool !== "select") {
          setTool("select")
        }
        return
      case "class": {
        const cls = classes[command.index]
        if (!cls) {
          toast.info(
            classes.length === 0
              ? "还没有类别，请先在右侧添加类别"
              : `只有 ${classes.length} 个类别`
          )
          return
        }
        if (selectedObject && !readOnly) {
          relabel(selectedObject.uid, cls.name)
        } else {
          setActiveClass(cls.name)
          toast(`当前类别：${cls.name}`, { duration: 1200 })
        }
        return
      }
      case "pickClass":
        setPicker(
          selectedObject && !readOnly
            ? { mode: "relabel", uid: selectedObject.uid }
            : { mode: "active" }
        )
        return
      case "toggleDifficult":
        toggleFlag("difficult")
        return
      case "toggleTruncated":
        toggleFlag("truncated")
        return
      case "nudge":
        if (selectedObject && size && !readOnly) {
          change(
            updateObject(
              doc.objects,
              selectedObject.uid,
              moveBox(selectedObject, command.dx, command.dy, size)
            )
          )
        }
        return
      case "resize":
        if (selectedObject && size && !readOnly) {
          change(
            updateObject(
              doc.objects,
              selectedObject.uid,
              resizeEdges(selectedObject, command.dx, command.dy, size)
            )
          )
        }
        return
      case "zoom":
        canvasRef.current?.zoom(command.direction)
        return
      case "fit":
        canvasRef.current?.fit()
        return
      case "actualSize":
        canvasRef.current?.actualSize()
        return
      case "zoomToSelection":
        if (selectedObject) {
          canvasRef.current?.zoomToBox(selectedObject)
        }
        return
      case "toggleLabels":
        setView({ ...view, showLabels: !view.showLabels })
        return
      case "toggleBoxes":
        setView({ ...view, showBoxes: !view.showBoxes })
        return
      case "negative":
        void markNegative()
        return
      case "help":
        setHelpOpen(true)
        return
    }
  }

  const runRef = useRef(run)
  useEffect(() => {
    runRef.current = run
  })
  const hasSelectionRef = useRef(false)
  useEffect(() => {
    hasSelectionRef.current = doc.selected !== null
  })

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const command = commandForKey(event, {
        mac,
        hasSelection: hasSelectionRef.current,
      })
      if (!command || shouldIgnoreKey(event, command)) {
        return
      }
      event.preventDefault()
      if (event.repeat && !allowsRepeat(command)) {
        return
      }
      runRef.current(command)
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [mac])

  const sizeWidth = size?.width ?? 0
  const sizeHeight = size?.height ?? 0
  const onViewportChange = useCallback(
    (viewport: Viewport) => {
      setZoom(viewport.scale)
      if (sizeWidth > 0 && sizeHeight > 0) {
        lastViewport.current = {
          size: { width: sizeWidth, height: sizeHeight },
          viewport,
        }
      }
    },
    [sizeWidth, sizeHeight]
  )
  const onSizeKnown = useCallback(
    (known: Size) => setNaturalSize({ id: imageId, size: known }),
    [imageId]
  )

  // ---- 渲染 ----
  const neighbors = neighborsQ.data
  const position = neighbors?.matches ? neighbors.position : null
  const initialViewport =
    carry &&
    size &&
    carry.size.width === size.width &&
    carry.size.height === size.height
      ? carry.viewport
      : null

  const imagesPanel = (
    <ImageListPanel
      dataset={dataset}
      filters={filters}
      onFiltersChange={setFilters}
      currentId={imageId}
      position={neighbors ? neighbors.position : null}
      onOpen={(image: DatasetImage) => void navigateTo(image.id)}
    />
  )

  const sidePanel = current ? (
    <div className="grid content-start gap-5 p-3">
      {urgentNotices.length > 0 && (
        <div className="grid gap-2">
          {urgentNotices.map((notice, index) => (
            <NoticeBox key={index} notice={notice} />
          ))}
        </div>
      )}
      <ClassPanel
        classes={classes}
        objects={doc.objects}
        activeClass={effectiveClass}
        colorFor={colorFor}
        hiddenClasses={hiddenClasses}
        hasSelection={selectedObject !== undefined}
        readOnly={readOnly}
        onPick={pickClass}
        onToggleHidden={(name) => {
          const nextHidden = new Set(hiddenClasses)
          if (nextHidden.has(name)) {
            nextHidden.delete(name)
          } else {
            nextHidden.add(name)
          }
          setHiddenClasses(nextHidden)
        }}
        onAddToList={(name) => void addClassToList(name)}
        onManage={() => setClassDialogOpen(true)}
      />
      <ObjectPanel
        objects={doc.objects}
        selected={doc.selected}
        classes={classes}
        colorFor={colorFor}
        size={size}
        readOnly={readOnly}
        onSelect={(uid) => {
          select(uid)
          const object = doc.objects.find((o) => o.uid === uid)
          if (object) {
            canvasRef.current?.reveal(object)
          }
        }}
        onHighlight={setHighlight}
        onChange={(uid, patch) => change(updateObject(doc.objects, uid, patch))}
        onDelete={(uid) => change(removeObject(doc.objects, uid), null)}
      />
      <ImageInfo
        detail={latest ?? current}
        size={size}
        notices={notices.filter((notice) => notice.tone === "info")}
      />
    </div>
  ) : null

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <EditorToolbar
        datasetId={datasetId}
        datasetName={dataset.name}
        fileName={current?.file_name ?? "加载中…"}
        path={current?.path ?? ""}
        position={position}
        total={neighbors?.total ?? null}
        positionRef={positionRef}
        onGoto={(index) => void gotoIndex(index)}
        canPrev={prev !== null}
        canNext={next !== null}
        navigating={navigating}
        onPrev={() => void step(-1, false)}
        onNext={() => void step(1, false)}
        onNextUnannotated={() => void step(1, true)}
        tool={tool}
        onToolChange={setTool}
        readOnly={readOnly}
        canUndo={!readOnly && doc.past.length > 0}
        canRedo={!readOnly && doc.future.length > 0}
        onUndo={() => dispatch({ type: "undo" })}
        onRedo={() => dispatch({ type: "redo" })}
        zoom={zoom}
        onZoom={(direction) => canvasRef.current?.zoom(direction)}
        onFit={() => canvasRef.current?.fit()}
        onActualSize={() => canvasRef.current?.actualSize()}
        view={view}
        onViewChange={setView}
        saveState={saveState}
        savedAt={savedAt}
        onRetrySave={() => void saver.flush()}
        canCopyPrevious={!readOnly && size !== null}
        onCopyPrevious={() => void copyPrevious()}
        canMarkNegative={!readOnly && doc.objects.length === 0}
        onMarkNegative={() => void markNegative()}
        canClearFile={
          !readOnly && latest !== null && latest.annotation_version !== ""
        }
        onClearFile={() => setConfirmClear(true)}
        onReload={() => void reload()}
        onManageClasses={() => setClassDialogOpen(true)}
        onHelp={() => setHelpOpen(true)}
        onOpenImages={() => setImagesOpen(true)}
        onOpenPanel={() => setPanelOpen(true)}
        mod={mod}
      />

      <div className="flex min-h-0 flex-1">
        <aside className="hidden w-64 shrink-0 flex-col border-r xl:flex">
          {imagesPanel}
        </aside>

        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
          {urgentNotices.length > 0 && (
            <div className="absolute inset-x-2 top-2 z-10 grid gap-2 lg:hidden">
              <NoticeBox notice={urgentNotices[0]} />
            </div>
          )}
          {current ? (
            <AnnotationCanvas
              key={`${current.id}:${loaded?.token ?? 0}`}
              ref={canvasRef}
              datasetId={datasetId}
              image={current}
              size={size}
              objects={doc.objects}
              selected={doc.selected}
              tool={tool}
              readOnly={readOnly}
              colorFor={colorFor}
              showBoxes={view.showBoxes}
              showLabels={view.showLabels}
              hiddenClasses={hiddenClasses}
              highlight={highlight}
              adjustments={view}
              initialViewport={initialViewport}
              onViewportChange={onViewportChange}
              onSizeKnown={onSizeKnown}
              onSelect={select}
              onCreate={onCreate}
              onRejectBox={() =>
                toast.info("框太小（宽或高不足 1 像素），已忽略", {
                  duration: 1500,
                })
              }
              onCommitBox={(uid, box) =>
                change(updateObject(doc.objects, uid, box))
              }
              overlay={(anchor) => {
                if (!picker) {
                  return null
                }
                const box =
                  picker.mode === "new"
                    ? picker.box
                    : picker.mode === "relabel"
                      ? doc.objects.find((o) => o.uid === picker.uid)
                      : undefined
                const point = box ? anchor(box) : null
                return (
                  <LabelPicker
                    classes={classes}
                    colorFor={colorFor}
                    current={
                      picker.mode === "relabel"
                        ? (doc.objects.find((o) => o.uid === picker.uid)
                            ?.name ?? null)
                        : effectiveClass
                    }
                    title={
                      picker.mode === "new"
                        ? "选择这个框的类别"
                        : picker.mode === "relabel"
                          ? "修改类别"
                          : "切换当前类别"
                    }
                    onPick={(name, created) => void onPick(name, created)}
                    onClose={() => setPicker(null)}
                    className="absolute"
                    style={
                      point
                        ? {
                            left: Math.max(
                              8,
                              Math.min(point.x, window.innerWidth - 280)
                            ),
                            top: point.y + 6,
                          }
                        : {
                            left: "50%",
                            top: 16,
                            transform: "translateX(-50%)",
                          }
                    }
                  />
                )
              }}
            />
          ) : detailQ.error ? (
            <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
              <p className="text-sm text-destructive">
                无法打开这张图片：{detailQ.error.message}
              </p>
              <Button variant="outline" onClick={() => void detailQ.refetch()}>
                重试
              </Button>
            </div>
          ) : (
            <Skeleton className="m-4 flex-1 rounded-lg" />
          )}
        </div>

        <aside className="hidden w-72 shrink-0 overflow-y-auto border-l lg:block">
          {sidePanel}
        </aside>
      </div>

      <Sheet open={imagesOpen} onOpenChange={setImagesOpen}>
        <SheetContent side="left" className="flex w-80 flex-col gap-0 p-0">
          <SheetHeader className="border-b">
            <SheetTitle>图片列表</SheetTitle>
            <SheetDescription className="sr-only">
              按固定顺序列出的图片，单击打开
            </SheetDescription>
          </SheetHeader>
          {imagesPanel}
        </SheetContent>
      </Sheet>
      <Sheet open={panelOpen} onOpenChange={setPanelOpen}>
        <SheetContent side="right" className="w-80 gap-0 overflow-y-auto p-0">
          <SheetHeader className="border-b">
            <SheetTitle>类别与目标</SheetTitle>
            <SheetDescription className="sr-only">
              类别列表、本图的框与图片信息
            </SheetDescription>
          </SheetHeader>
          {sidePanel}
        </SheetContent>
      </Sheet>

      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} mac={mac} />
      <ClassManagerDialog
        datasetId={datasetId}
        classes={classes}
        open={classDialogOpen}
        onOpenChange={setClassDialogOpen}
      />
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除这张图片的标注文件？</AlertDialogTitle>
            <AlertDialogDescription>
              {latest?.annotation_path ?? "标注文件"}
              会从数据集文件夹中删除，图片恢复为“未标注”。这一步不能撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void clearFile()}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
