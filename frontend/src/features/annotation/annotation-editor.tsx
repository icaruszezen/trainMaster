import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type MouseEvent,
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
  cancelImageReads,
  editorImageQuery,
  fetchImageAt,
  markImageOutdated,
  openableDetail,
  persistAnnotation,
  saveEditorAnnotation,
  savedDetail,
  type SaveContext,
} from "@/features/annotation/api"
import {
  ANNOTATION_UNPATCHABLE,
  AnnotationSaver,
  isConflictError,
  type SaverState,
} from "@/features/annotation/autosave"
import {
  buildClassOptions,
  colorResolver,
  unlistedNames,
} from "@/features/annotation/classes"
import { UNHANDLED, useBoxEditing } from "@/features/annotation/box-editing"
import {
  documentReducer,
  emptyDocument,
  removeObject,
  updateObject,
} from "@/features/annotation/document"
import {
  candidateStatusOf,
  IMAGES_QUERY,
  imageSizeOf,
  PANEL_QUERY,
  readTool,
  readView,
  writeTool,
  writeView,
  type Carry,
  type Picker,
  type ViewOptions,
} from "@/features/annotation/editor-common"
import { EditorToolbar } from "@/features/annotation/editor-toolbar"
import type { Size, Viewport } from "@/features/annotation/geometry"
import { ImageListPanel } from "@/features/annotation/image-list-panel"
import {
  UnsavedChangesDialog,
  useSaveGuard,
} from "@/features/annotation/save-guard"
import {
  focusAfterDialog,
  focusCanvas,
  isMac,
  type Command,
  type Tool,
} from "@/features/annotation/shortcuts"
import { ShortcutsDialog } from "@/features/annotation/shortcuts-dialog"
import {
  ImageInfo,
  NoticeBox,
  ObjectPanel,
  type Notice,
} from "@/features/annotation/side-panel"
import { useEditorKeys } from "@/features/annotation/use-editor-keys"
import {
  NO_FILTERS,
  parseFilters,
  updateParams,
} from "@/features/annotation/workspace-params"
import {
  deleteAnnotation,
  fileUrl,
  imageNeighborsQuery,
  labelsQuery,
  thumbnailUrl,
  type Dataset,
  type DatasetImage,
  type DatasetImageDetail,
  type DatasetLabel,
  type ImageFilters,
} from "@/features/datasets/api"
import { datasetClassesQuery, type ProjectClass } from "@/features/projects/api"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatNumber } from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"

const classKey = (datasetId: number) =>
  `trainmaster.annotation.class.${datasetId}`
export const lastImageKey = (datasetId: number) =>
  `trainmaster.annotation.last.${datasetId}`

const EMPTY_CLASSES: ProjectClass[] = []
const EMPTY_LABELS: DatasetLabel[] = []

/** 正在编辑的图片：载入时的详情；`token` 在重新读取时递增。 */
type Loaded = { detail: DatasetImageDetail; token: number }

/** 文档的 key：哪张图片、第几次载入（异步操作完成时据此判断是否还是同一份）。 */
const docKey = (loaded: Loaded) => `${loaded.detail.id}:${loaded.token}`

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

  // 候选类别：数据集所属项目的类别列表（在项目页面中管理）与数据集中已有的
  // 标签。读取中、读取失败时照样可以画框、输入类别名称，只是候选少一些。
  const projectClassesQ = useQuery(datasetClassesQuery(datasetId))
  const labelsQ = useQuery(labelsQuery(datasetId))
  const projectClasses = projectClassesQ.data?.items ?? EMPTY_CLASSES
  const datasetLabels = labelsQ.data?.items ?? EMPTY_LABELS
  const candidateStatus = candidateStatusOf([projectClassesQ, labelsQ])
  const detailQ = useQuery(editorImageQuery(datasetId, imageId))
  const neighborsQ = useQuery(imageNeighborsQuery(datasetId, imageId, filters))

  // 跨图片保持的设置。
  const [tool, setToolState] = useState<Tool>(readTool)
  const [activeClass, setActiveClassState] = useState<string | null>(() =>
    readPreference(classKey(datasetId))
  )
  const [view, setViewState] = useState<ViewOptions>(readView)
  // 这次标注中输入的新类别名称：保存后数据集的标签统计才会刷新，在此之前（包括
  // 切换到别的图片后）它们也是候选。
  const [typedNames, setTypedNames] = useState<readonly string[]>([])
  // 这次标注中给出过的、不在类别列表中的候选，保持它们的顺序（见
  // buildClassOptions 的 shown）：数字键对应的类别不会因为目标数的变化而改变。
  const [shownNames, setShownNames] = useState<readonly string[]>([])
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
  const [confirmClear, setConfirmClear] = useState(false)
  const [imagesOpen, setImagesOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)
  // 宽屏时面板固定在两侧，否则放在弹出的面板中；只渲染一份（元素 id 不重复）。
  const panelDocked = useMediaQuery(PANEL_QUERY)
  const imagesDocked = useMediaQuery(IMAGES_QUERY)

  const canvasRef = useRef<CanvasHandle>(null)
  const positionRef = useRef<HTMLInputElement>(null)
  const sessionRef = useRef<SaveContext | null>(null)
  const lastViewport = useRef<{ size: Size; viewport: Viewport } | null>(null)

  const [saver] = useState(
    () =>
      new AnnotationSaver<SaveContext>({
        save: (request) => saveEditorAnnotation(queryClient, request),
        onChange: (state) => {
          setSaveState(state)
          if (state.status === "saved") {
            setSavedAt(new Date())
          }
        },
      })
  )

  // 换了图片：载入它的框（在渲染中调整状态，避免先渲染一次旧图片的框）。
  // 缓存中的详情过期、正在重新读取时等读取完成（openableDetail）。
  const detail = openableDetail(detailQ, imageId)
  if (detail && loaded?.detail.id !== detail.id) {
    const next = { detail, token: (loaded?.token ?? 0) + 1 }
    setLoaded(next)
    dispatch({ type: "load", objects: detail.objects, key: docKey(next) })
    setHighlight(null)
    setPicker(null)
  }
  const current = loaded && loaded.detail.id === imageId ? loaded.detail : null

  // 异步操作（读取上一张、重新读取等）完成时判断是否还在同一次编辑中：没有
  // 切换图片、没有重新读取。在提交时同步更新，异步回调读到的总是最新的。
  const loadedRef = useRef<Loaded | null>(null)
  const imageIdRef = useRef(imageId)
  useLayoutEffect(() => {
    loadedRef.current = loaded
    imageIdRef.current = imageId
  })
  const isCurrent = (start: Loaded | null): start is Loaded =>
    start !== null &&
    loadedRef.current === start &&
    imageIdRef.current === start.detail.id

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

  // 离开这张图片、离开工作台、关闭页面前先保存（见 useSaveGuard）。上一张 /
  // 下一张等按钮自己先保存（navigateTo），保存成功后不再拦截。
  const { blocker, leavePromptOpen } = useSaveGuard(saver, {
    leaveError: "离开标注工作台前，最后的修改没有保存成功",
    // 与平时的保存规则相同（包括删除本次创建、框又全部删掉的文件）。
    persist: (pending, init) =>
      persistAnnotation(
        pending.objects,
        pending.baseVersion,
        false,
        pending.context,
        init
      ),
  })

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

  const colorFor = useMemo(
    () => colorResolver(projectClasses),
    [projectClasses]
  )
  // 类别选择框、数字键（前 10 个）、框的类别下拉框共用的候选，见 buildClassOptions。
  const classOptions = useMemo(
    () =>
      buildClassOptions({
        projectClasses,
        datasetLabels,
        // 标签统计里可能还没有的名称：这次输入的、当前类别（上次用的，可能还没有
        // 保存过）、本图上的。
        extraNames: [
          ...typedNames,
          ...(activeClass === null ? [] : [activeClass]),
          ...doc.objects.map((object) => object.name),
        ],
        shown: shownNames,
      }),
    [
      projectClasses,
      datasetLabels,
      typedNames,
      activeClass,
      doc.objects,
      shownNames,
    ]
  )
  // 候选都读取到之后才记下顺序：第一次记下的就是按目标数排好的顺序，而不是
  // 读取完成前只有本图类别的顺序。记下后再算一次结果不变，不会反复更新。
  if (projectClassesQ.data && labelsQ.data) {
    const unlisted = unlistedNames(classOptions)
    if (
      unlisted.length !== shownNames.length ||
      unlisted.some((name, index) => name !== shownNames[index])
    ) {
      setShownNames(unlisted)
    }
  }
  const size =
    (current && imageSizeOf(current)) ??
    (naturalSize && naturalSize.id === imageId ? naturalSize.size : null)

  // 缓存中的最新详情（保存后、保存冲突后重新读取的状态、标注文件路径等）；
  // `current` 是开始编辑时的。
  const latest = detailQ.data?.id === imageId ? detailQ.data : current

  /**
   * 提示中的按钮：先把焦点移到画布上，再执行操作。按钮在处理后随提示消失，
   * 焦点会落到 body 上（画布因重新载入而更换时，新的画布随后自己获得焦点）。
   * 按钮在窄屏时弹出的面板中时不移动（focusCanvas）。
   */
  const fromNotice = (event: MouseEvent<HTMLElement>) =>
    focusCanvas(event.currentTarget)

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
              onClick={(event) => {
                fromNotice(event)
                setUnlocked(current.id)
              }}
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
    writeTool(value)
  }
  const setView = (value: ViewOptions) => {
    setViewState(value)
    writeView(value)
  }
  const setActiveClass = (name: string) => {
    setActiveClassState(name)
    writePreference(classKey(datasetId), name)
  }

  // ---- 编辑 ----
  // 正在载入下一张图片时（current 为 null）框列表还是上一张的，不能修改。
  const editable = current !== null && !readOnly
  const boxes = useBoxEditing({
    doc,
    dispatch,
    editable,
    size,
    imageId,
    view,
    onViewChange: setView,
    tool,
    onToolChange: setTool,
    picker,
    onPickerChange: setPicker,
    classOptions,
    candidateStatus,
    flagUnlisted: projectClasses.length > 0,
    activeClass,
    onActiveClassChange: setActiveClass,
    onTypedName: (name: string) =>
      setTypedNames((names) =>
        names.includes(name) ? names : [...names, name]
      ),
    canvasRef,
    mod,
  })
  const { change, select, activeObject } = boxes

  const copyPrevious = async () => {
    const start = loaded
    const key = doc.key
    if (!editable) {
      return
    }
    try {
      const neighbors = await queryClient.fetchQuery(
        imageNeighborsQuery(datasetId, imageId, NO_FILTERS)
      )
      if (!isCurrent(start)) {
        return // 已切换到别的图片
      }
      if (!neighbors.prev) {
        toast.info("这是第一张图片")
        return
      }
      const previous = await queryClient.fetchQuery(
        editorImageQuery(datasetId, neighbors.prev.id)
      )
      if (!isCurrent(start)) {
        return
      }
      if (previous.objects.length === 0) {
        toast.info(`上一张（${previous.file_name}）没有框`)
        return
      }
      boxes.paste({ imageId: previous.id, objects: previous.objects }, key)
      toast.success(
        `已沿用上一张的 ${formatNumber(previous.objects.length)} 个框`
      )
    } catch (error) {
      if (isCurrent(start)) {
        toast.error("无法读取上一张图片的标注", {
          description: error instanceof Error ? error.message : undefined,
        })
      }
    }
  }

  // ---- 保存 ----
  const reload = async () => {
    const start = loaded
    try {
      const fresh = await queryClient.fetchQuery({
        ...editorImageQuery(datasetId, imageId),
        staleTime: 0,
      })
      if (!isCurrent(start)) {
        return // 已切换到别的图片
      }
      const next = { detail: fresh, token: start.token + 1 }
      setLoaded(next)
      dispatch({ type: "load", objects: fresh.objects, key: docKey(next) })
      setUnlocked(null)
      toast.success("已重新读取标注文件")
    } catch (error) {
      if (isCurrent(start)) {
        toast.error("无法读取标注文件", {
          description: error instanceof Error ? error.message : undefined,
        })
      }
    }
  }

  const overwrite = async () => {
    if (await saver.overwrite()) {
      toast.success("已用你的版本覆盖标注文件")
    }
  }

  const markNegative = async () => {
    const session = sessionRef.current
    if (!session || !editable || saver.getContext() !== session) {
      return
    }
    if (doc.objects.length > 0) {
      toast.info("图片上还有框：删除所有框后才能标为空标注")
      return
    }
    if (saver.getState().status === "conflict") {
      toast.error("请先处理这张图片的保存冲突")
      return
    }
    // 通过保存器写入没有目标的文件：切换图片、离开前会等它完成（失败时同样
    // 不离开），结果只用于这张图片。
    session.negative = true
    const ok = await saver.saveNow()
    if (saver.getContext() !== session) {
      return // 已切换到别的图片
    }
    if (ok) {
      toast.success("已标为空标注（没有目标）")
      return
    }
    const state = saver.getState()
    if (state.status === "conflict") {
      toast.error("标注文件已被修改，请重新读取后再试")
    } else if (state.status === "error") {
      toast.error("保存失败", { description: state.error ?? undefined })
    }
  }

  const clearFile = async () => {
    const session = sessionRef.current
    const start = loaded
    setConfirmClear(false)
    if (!session) {
      return
    }
    if (!(await saver.flush())) {
      if (isCurrent(start)) {
        toast.error("请先处理保存失败或冲突")
      }
      return
    }
    if (!isCurrent(start) || saver.getContext() !== session) {
      return // 已切换到别的图片
    }
    try {
      await cancelImageReads(queryClient, datasetId, session.imageId)
      const saved = await deleteAnnotation(
        datasetId,
        session.imageId,
        saver.getVersion()
      )
      await applySavedDetail(queryClient, datasetId, saved)
      if (!isCurrent(start)) {
        return
      }
      const next = { detail: saved, token: start.token + 1 }
      setLoaded(next)
      // 还有别的标注文件（如 Annotations/ 中被图片旁的文件取代的那个）时，
      // 图片改用它，和重新扫描的结果一致。
      dispatch({ type: "load", objects: saved.objects, key: docKey(next) })
      toast.success(
        saved.annotation_state === "none"
          ? "已删除标注文件，图片恢复为未标注"
          : `已删除标注文件，图片现在使用另一个标注文件 ${saved.annotation_path ?? ""}`
      )
    } catch (error) {
      if (isConflictError(error)) {
        markImageOutdated(queryClient, datasetId, session.imageId)
      }
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
      editable &&
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
          <Button
            size="sm"
            variant="outline"
            onClick={(event) => {
              fromNotice(event)
              void reload()
            }}
          >
            载入最新版本（放弃我的修改）
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={(event) => {
              fromNotice(event)
              void overwrite()
            }}
          >
            用我的版本覆盖
          </Button>
        </div>
      ),
    })
  } else if (saveState.status === "error") {
    notices.unshift({
      tone: "error",
      message: `保存失败：${saveState.error ?? "未知错误"}`,
      action:
        saveState.code === ANNOTATION_UNPATCHABLE ? (
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={(event) => {
                fromNotice(event)
                void reload()
              }}
            >
              载入文件（放弃我的修改）
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={(event) => {
                fromNotice(event)
                void saver.regenerate()
              }}
            >
              仍然保存（重新生成文件）
            </Button>
          </div>
        ) : (
          <Button
            size="sm"
            variant="outline"
            onClick={(event) => {
              fromNotice(event)
              void saver.flush()
            }}
          >
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

  /**
   * 放弃这张图片上没有保存成功的修改（“放弃修改并切换”“放弃修改并离开”）：
   * 之后不再保存它们，切换、离开时也不再拦截；编辑器中的框回到最后保存的内容。
   * 只重置保存器的话，要打开的图片读取失败（如服务无法访问）、又回到这张图片
   * 时不会重新载入，放弃的框还在画布上，下次修改会把它们一起保存。
   */
  const discardChanges = () => {
    saver.discard()
    const start = loadedRef.current
    if (!start) {
      return
    }
    // 不用载入时的框：之前保存成功的修改不能一起丢掉。
    const saved = savedDetail(queryClient, datasetId, start.detail)
    const next = { detail: saved, token: start.token + 1 }
    setLoaded(next)
    dispatch({ type: "load", objects: saved.objects, key: docKey(next) })
    setHighlight(null)
    setPicker(null)
  }

  // 用户选择放弃这张图片上没有保存成功的修改并切换。
  const discardAndOpen = (from: number, id: number) => {
    if (imageIdRef.current !== from) {
      return // 提示过期：已经在别的图片上了
    }
    discardChanges()
    openImage(id)
  }

  const navigateTo = async (target: number | null | undefined) => {
    if (
      !target ||
      target === imageId ||
      navigating ||
      blocker.state !== "unblocked" // 正在处理另一次离开
    ) {
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
              onClick: (event) => {
                // 提示随即关闭：焦点先移到画布上，新图片的画布随后自己获得焦点。
                focusCanvas(event.currentTarget)
                discardAndOpen(imageId, target)
              },
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
  /** 执行命令；返回 false 时这次按键交给浏览器（不阻止默认行为）。 */
  const run = (command: Command): false | void => {
    const result = boxes.run(command)
    if (result !== UNHANDLED) {
      return result
    }
    switch (command.type) {
      case "image":
        void step(command.direction, command.unannotated)
        return
      case "goto":
        positionRef.current?.focus()
        return
      case "save":
        void save()
        return
      case "negative":
        void markNegative()
        return
      case "help":
        setHelpOpen(true)
        return
    }
  }

  useEditorKeys(run, { mac, hasSelection: activeObject !== undefined })

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
      <ObjectPanel
        objects={doc.objects}
        selected={doc.selected}
        options={classOptions}
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
        projects={dataset.projects}
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
        canUndo={editable && doc.past.length > 0}
        canRedo={editable && doc.future.length > 0}
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
        canCopyPrevious={editable && size !== null}
        onCopyPrevious={() => void copyPrevious()}
        canMarkNegative={editable && doc.objects.length === 0}
        onMarkNegative={() => void markNegative()}
        canClearFile={
          !readOnly && latest !== null && latest.annotation_version !== ""
        }
        onClearFile={() => setConfirmClear(true)}
        onReload={() => void reload()}
        onHelp={() => setHelpOpen(true)}
        onOpenImages={() => setImagesOpen(true)}
        onOpenPanel={() => setPanelOpen(true)}
        mod={mod}
      />

      <div className="flex min-h-0 flex-1">
        {imagesDocked && (
          <aside className="flex w-64 shrink-0 flex-col border-r">
            {imagesPanel}
          </aside>
        )}

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
              image={current}
              fileUrl={fileUrl(datasetId, current)}
              thumbnailUrl={thumbnailUrl(datasetId, current, "lg")}
              size={size}
              objects={doc.objects}
              selected={doc.selected}
              tool={tool}
              readOnly={readOnly}
              colorFor={colorFor}
              showBoxes={view.showBoxes}
              showLabels={view.showLabels}
              highlight={highlight}
              pendingBox={picker?.mode === "new" ? picker.box : null}
              adjustments={view}
              initialViewport={initialViewport}
              onViewportChange={onViewportChange}
              onSizeKnown={onSizeKnown}
              onSelect={select}
              onCreate={boxes.onCreate}
              onRejectBox={() =>
                toast.info("框太小（宽或高不足 1 像素），已忽略", {
                  duration: 1500,
                })
              }
              onCommitBox={(uid, box) =>
                change(updateObject(doc.objects, uid, box))
              }
              overlay={boxes.overlay}
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

        {panelDocked && (
          <aside className="w-72 shrink-0 overflow-y-auto border-l">
            {sidePanel}
          </aside>
        )}
      </div>

      <Sheet open={imagesOpen && !imagesDocked} onOpenChange={setImagesOpen}>
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
      <Sheet open={panelOpen && !panelDocked} onOpenChange={setPanelOpen}>
        <SheetContent side="right" className="w-80 gap-0 overflow-y-auto p-0">
          <SheetHeader className="border-b">
            <SheetTitle>目标与图片信息</SheetTitle>
            <SheetDescription className="sr-only">
              本图的框（类别、坐标与属性）与图片信息
            </SheetDescription>
          </SheetHeader>
          {sidePanel}
        </SheetContent>
      </Sheet>

      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} mac={mac} />
      <UnsavedChangesDialog
        blocker={blocker}
        open={leavePromptOpen}
        saveState={saveState}
        conflictMessage="这张图片的标注文件已被修改（可能有其他人同时在标注它），你的修改没有保存。留在这里可以载入最新版本或用你的版本覆盖；离开则放弃这些修改。"
        onDiscard={discardChanges}
      />
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent
          onCloseAutoFocus={(event) => focusAfterDialog(event)}
        >
          <AlertDialogHeader>
            <AlertDialogTitle>删除这张图片的标注文件？</AlertDialogTitle>
            <AlertDialogDescription>
              {latest?.annotation_path ?? "标注文件"}
              会从数据集文件夹中删除。还有重新扫描时会使用的其它标注文件时，图片改用它，否则恢复为“未标注”。这一步不能撤销。
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
