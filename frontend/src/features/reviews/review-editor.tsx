import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, ImageOff } from "lucide-react"
import { Link, Navigate, useSearchParams } from "react-router"
import { toast } from "sonner"

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
  type ReferenceBox,
} from "@/features/annotation/annotation-canvas"
import { openableDetail } from "@/features/annotation/api"
import {
  AnnotationSaver,
  type SaverState,
} from "@/features/annotation/autosave"
import { UNHANDLED, useBoxEditing } from "@/features/annotation/box-editing"
import {
  buildClassOptions,
  colorResolver,
  unlistedNames,
} from "@/features/annotation/classes"
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
import type { Size, Viewport } from "@/features/annotation/geometry"
import {
  UnsavedChangesDialog,
  useSaveGuard,
} from "@/features/annotation/save-guard"
import {
  focusCanvas,
  isMac,
  type Command,
  type Tool,
} from "@/features/annotation/shortcuts"
import { ShortcutsDialog } from "@/features/annotation/shortcuts-dialog"
import {
  NoticeBox,
  ObjectPanel,
  type Notice,
} from "@/features/annotation/side-panel"
import { useEditorKeys } from "@/features/annotation/use-editor-keys"
import {
  ALL_REVIEW_IMAGES,
  fetchReviewImageAt,
  reviewClassesQuery,
  reviewImageQuery,
  reviewLabelsQuery,
  reviewNeighborsQuery,
  reviewImageFileUrl,
  reviewThumbnailUrl,
  type ReviewClass,
  type ReviewImage,
  type ReviewImageDetail,
  type ReviewImageFilters,
  type ReviewImageStatus,
  type ReviewLabel,
  type ReviewPrediction,
  type ReviewSource,
  type ReviewSummary,
} from "@/features/reviews/api"
import {
  markReviewImage,
  persistReviewAnnotation,
  saveReviewEditorAnnotation,
  savedReviewDetail,
  type ReviewSaveContext,
} from "@/features/reviews/editor-api"
import {
  hasReviewFilters,
  parseId,
  parseReviewFilters,
  updateReviewParams,
} from "@/features/reviews/params"
import { ReviewImageList } from "@/features/reviews/review-image-list"
import {
  PredictionList,
  ReviewImageInfo,
  ReviewMarkButtons,
  ReviewStatusCard,
} from "@/features/reviews/review-side-panel"
import { ReviewImageStatusBadge } from "@/features/reviews/review-status-badge"
import {
  formatConfidence,
  isInferring,
  matchConfidences,
  predictionsToObjects,
  reviewImageStatusLabels,
  sameBoxes,
  unmatchedPredictions,
} from "@/features/reviews/review-status"
import { ReviewToolbar } from "@/features/reviews/review-toolbar"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatNumber } from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"

const AUTO_ADVANCE_KEY = "trainmaster.review.autoAdvance"
const PREDICTIONS_KEY = "trainmaster.review.predictions"
const classKey = (source: ReviewSource) =>
  `trainmaster.review.class.${JSON.stringify(source.key)}`

const EMPTY_CLASSES: ReviewClass[] = []
const EMPTY_LABELS: ReviewLabel[] = []
const EMPTY_PREDICTIONS: ReviewPrediction[] = []

/** 正在编辑的图片：载入时的详情；`token` 在重新读取时递增。 */
type Loaded = { detail: ReviewImageDetail; token: number }

/** 文档的 key：哪张图片、第几次载入（异步操作完成时据此判断是否还是同一份）。 */
const docKey = (loaded: Loaded) => `${loaded.detail.id}:${loaded.token}`

/** 交给编辑器之外的操作（{@link ReviewEditorProps.actions}）。 */
export type ReviewEditorControls = {
  /** 立即保存这张图片的修改；保存失败、有冲突时返回 false（并已提示）。 */
  flush: () => Promise<boolean>
}

export type ReviewEditorProps = {
  /** 读写哪个审图任务（平台或本地审图程序）。 */
  source: ReviewSource
  /** 审图任务的信息：各状态的图片数、数据集、模型的类别等。 */
  review: ReviewSummary
  /** 返回按钮的地址与说明。 */
  backTo: string
  backLabel: string
  /** 本地审图程序：不显示平台上的链接（如项目的类别列表）。 */
  local?: boolean
  /** 工具栏中编辑器之外的操作（如本地审图程序的“导出审图结果”）。 */
  actions?: (controls: ReviewEditorControls) => ReactNode
}

/**
 * 审图编辑器：逐张查看模型的识别结果，修改审图标注（只写在审图任务中），把图片
 * 标为已审核 / 需加样 / 待审核。当前图片与筛选条件在地址栏（`?image=12&status=pending`），
 * 没有指定图片时打开第一张符合筛选条件的图片。只通过 `source` 读写，平台与本地
 * 审图程序共用。
 */
export function ReviewEditor(props: ReviewEditorProps) {
  const [params] = useSearchParams()
  const imageId = parseId(params.get("image"))
  const sourceKey = JSON.stringify(props.source.key)
  if (imageId === null) {
    return <StartImage key={sourceKey} {...props} />
  }
  return <Editor key={sourceKey} {...props} />
}

/**
 * 没有指定图片时打开第一张符合筛选条件的图片（“网页审图”带着 `status=pending`
 * 进来时是第一张待审核的）；没有符合条件的图片时打开第一张，筛选条件不变。
 */
function StartImage({ source, review, backTo, backLabel }: ReviewEditorProps) {
  const [params] = useSearchParams()
  const filters = useMemo(() => parseReviewFilters(params), [params])
  const start = useQuery({
    queryKey: ["review-start", ...source.key, filters],
    queryFn: async () => {
      const first = await fetchReviewImageAt(source, filters, 0)
      if (first || !hasReviewFilters(filters)) {
        return first?.id ?? null
      }
      const any = await fetchReviewImageAt(source, ALL_REVIEW_IMAGES, 0)
      if (any) {
        toast.info(
          filters.status === "pending"
            ? "没有待审核的图片了，已打开第一张"
            : "没有符合筛选条件的图片，已打开第一张"
        )
      }
      return any?.id ?? null
    },
    staleTime: 0,
    gcTime: 0,
  })

  if (start.data === null || review.counts.total === 0) {
    return (
      <Empty className="min-h-[60svh]">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <ImageOff />
          </EmptyMedia>
          <EmptyTitle>“{review.name}”中没有图片</EmptyTitle>
          <EmptyDescription>
            创建审图任务时选择的数据集中没有图片。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button asChild>
            <Link to={backTo}>
              <ArrowLeft />
              {backLabel}
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  }
  if (start.error) {
    return (
      <Empty className="min-h-[60svh]">
        <EmptyHeader>
          <EmptyTitle>无法获取图片列表</EmptyTitle>
          <EmptyDescription>{start.error.message}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            onClick={() => void start.refetch()}
            disabled={start.isFetching}
          >
            重试
          </Button>
        </EmptyContent>
      </Empty>
    )
  }
  if (start.data === undefined) {
    return <ReviewEditorSkeleton />
  }
  return (
    <Navigate
      replace
      to={{ search: `?${updateReviewParams(params, { image: start.data })}` }}
    />
  )
}

/** 编辑器载入前的占位（工具栏与画布）。 */
export function ReviewEditorSkeleton() {
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

/**
 * 保存这张图片的修改；失败或有冲突时提示原因（`action` 是因此没有进行的操作，
 * 如“标记”）。保存期间切换了图片时不提示。
 */
async function flushWithNotice(
  saver: AnnotationSaver<ReviewSaveContext>,
  action: string | null
) {
  const context = saver.getContext()
  if (await saver.flush()) {
    return true
  }
  if (saver.getContext() !== context || !saver.isDirty()) {
    return false
  }
  const state = saver.getState()
  if (state.status === "conflict") {
    toast.error(
      action
        ? `请先处理这张图片的保存冲突，再${action}`
        : "请先处理这张图片的保存冲突"
    )
  } else {
    toast.error(
      action ? `保存失败，没有${action}` : "这张图片的修改没有保存成功",
      { description: state.error ?? undefined }
    )
  }
  return false
}

function Editor({
  source: sourceProp,
  review,
  backTo,
  backLabel,
  local = false,
  actions,
}: ReviewEditorProps) {
  // 编辑器按 `source.key` 挂载（见 ReviewEditor）：调用方每次渲染给出的新对象
  // 不会让保存器重新开始（那样会丢掉进行中的编辑、用旧的版本保存）。
  const [source] = useState(sourceProp)
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()
  const imageId = Number(params.get("image"))
  const filters = useMemo(() => parseReviewFilters(params), [params])
  const [mac] = useState(isMac)
  const mod = mac ? "⌘" : "Ctrl"

  // 候选类别：模型所属项目的类别列表（没有时是模型的类别）、审图标注中已有的
  // 类别与模型的类别。读取中、读取失败时照样可以画框、输入类别名称。
  const classesQ = useQuery(reviewClassesQuery(source))
  const labelsQ = useQuery(reviewLabelsQuery(source))
  const projectClasses = classesQ.data?.items ?? EMPTY_CLASSES
  const reviewLabels = labelsQ.data?.items ?? EMPTY_LABELS
  const modelNames = review.model.class_names
  const candidateStatus = candidateStatusOf([classesQ, labelsQ])
  const detailQ = useQuery(reviewImageQuery(source, imageId))
  const neighborsQ = useQuery(reviewNeighborsQuery(source, imageId, filters))

  // 跨图片保持的设置。
  const [tool, setToolState] = useState<Tool>(readTool)
  const [activeClass, setActiveClassState] = useState<string | null>(() =>
    readPreference(classKey(source))
  )
  const [view, setViewState] = useState<ViewOptions>(readView)
  const [autoAdvance, setAutoAdvanceState] = useState(
    () => readPreference(AUTO_ADVANCE_KEY) !== "false"
  )
  const [showPredictions, setShowPredictionsState] = useState(
    () => readPreference(PREDICTIONS_KEY) !== "false"
  )
  // 这次审图中输入的新类别名称与给出过的候选顺序，见标注工作台。
  const [typedNames, setTypedNames] = useState<readonly string[]>([])
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
  const [highlightPrediction, setHighlightPrediction] = useState<number | null>(
    null
  )
  const [picker, setPicker] = useState<Picker | null>(null)
  const [saveState, setSaveState] = useState<SaverState>({
    status: "clean",
    error: null,
  })
  const [savedAt, setSavedAt] = useState<Date | null>(null)
  const [navigating, setNavigating] = useState(false)
  const [marking, setMarking] = useState<ReviewImageStatus | null>(null)

  // 对话框与面板。
  const [helpOpen, setHelpOpen] = useState(false)
  const [imagesOpen, setImagesOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)
  const panelDocked = useMediaQuery(PANEL_QUERY)
  const imagesDocked = useMediaQuery(IMAGES_QUERY)

  const canvasRef = useRef<CanvasHandle>(null)
  const positionRef = useRef<HTMLInputElement>(null)
  const lastViewport = useRef<{ size: Size; viewport: Viewport } | null>(null)
  const markingRef = useRef(false)

  const [saver] = useState(
    () =>
      new AnnotationSaver<ReviewSaveContext>({
        save: (request) => saveReviewEditorAnnotation(queryClient, request),
        onChange: (state) => {
          setSaveState(state)
          if (state.status === "saved") {
            setSavedAt(new Date())
          }
        },
      })
  )
  const [controls] = useState<ReviewEditorControls>(() => ({
    flush: () => flushWithNotice(saver, null),
  }))

  // 换了图片：载入它的框（在渲染中调整状态，避免先渲染一次旧图片的框）。
  const detail = openableDetail(detailQ, imageId)
  if (detail && loaded?.detail.id !== detail.id) {
    const next = { detail, token: (loaded?.token ?? 0) + 1 }
    setLoaded(next)
    dispatch({ type: "load", objects: detail.objects, key: docKey(next) })
    setHighlight(null)
    setHighlightPrediction(null)
    setPicker(null)
  }
  const current = loaded && loaded.detail.id === imageId ? loaded.detail : null

  // 异步操作完成时判断是否还在同一次编辑中（没有切换图片、没有重新读取）。
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
    saver.reset(d.objects, d.annotation_version, { source, imageId: d.id })
  }, [loaded, saver, source])

  useEffect(() => {
    if (loaded) {
      saver.update(doc.objects)
    }
  }, [doc.objects, loaded, saver])

  // 离开这张图片、离开编辑器、关闭页面前先保存（见 useSaveGuard）。
  const { blocker, leavePromptOpen } = useSaveGuard(saver, {
    leaveError: "离开审图页面前，最后的修改没有保存成功",
    persist: (pending, init) =>
      persistReviewAnnotation(
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
        void queryClient.prefetchQuery(reviewImageQuery(source, image.id))
        new Image().src = reviewThumbnailUrl(source, image, "lg")
      }
    }
  }, [source, next, prev, queryClient])

  const colorFor = useMemo(
    () => colorResolver(projectClasses),
    [projectClasses]
  )
  // 审图标注中已有的类别，加上模型的类别（目标数为 0，排在后面）。
  const labelCounts = useMemo(
    () => [
      ...reviewLabels,
      ...modelNames.map((name) => ({ name, object_count: 0 })),
    ],
    [reviewLabels, modelNames]
  )
  const classOptions = useMemo(
    () =>
      buildClassOptions({
        projectClasses,
        datasetLabels: labelCounts,
        extraNames: [
          ...typedNames,
          ...(activeClass === null ? [] : [activeClass]),
          ...doc.objects.map((object) => object.name),
        ],
        shown: shownNames,
      }),
    [
      projectClasses,
      labelCounts,
      typedNames,
      activeClass,
      doc.objects,
      shownNames,
    ]
  )
  if (classesQ.data && labelsQ.data) {
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

  // 缓存中的最新详情（保存、标记后的状态等）；`current` 是开始编辑时的。
  const latest = detailQ.data?.id === imageId ? detailQ.data : current
  const predictions = current?.predictions ?? EMPTY_PREDICTIONS

  // 与模型识别结果的对照：框的置信度（类别名称后面），已经没有的识别框（虚线）。
  const confidences = useMemo(() => {
    const matched = matchConfidences(doc.objects, predictions)
    return new Map(
      [...matched].map(([uid, confidence]) => [
        uid,
        formatConfidence(confidence),
      ])
    )
  }, [doc.objects, predictions])
  const unmatched = useMemo(
    () => new Set(unmatchedPredictions(doc.objects, predictions)),
    [doc.objects, predictions]
  )
  const references = useMemo<ReferenceBox[]>(
    () =>
      showPredictions && view.showBoxes
        ? predictions.flatMap((prediction, index) =>
            unmatched.has(index)
              ? [
                  {
                    key: `p${index}`,
                    name: prediction.name,
                    label: `模型：${prediction.name} ${formatConfidence(prediction.confidence)}`,
                    xmin: prediction.xmin,
                    ymin: prediction.ymin,
                    xmax: prediction.xmax,
                    ymax: prediction.ymax,
                  },
                ]
              : []
          )
        : [],
    [showPredictions, view.showBoxes, predictions, unmatched]
  )
  const highlightReference =
    highlightPrediction !== null &&
    references.some((r) => r.key === `p${highlightPrediction}`)
      ? `p${highlightPrediction}`
      : null

  /** 提示中的按钮：先把焦点移到画布上，再执行操作（见标注工作台）。 */
  const fromNotice = (event: MouseEvent<HTMLElement>) =>
    focusCanvas(event.currentTarget)

  // ---- 只读与不能标记的原因 ----
  const notices: Notice[] = []
  let readOnly = false
  let markBlocked: string | null = current ? null : "图片还在加载中"
  if (current && latest) {
    if (!latest.inferred) {
      readOnly = true
      markBlocked = isInferring(review)
        ? "这张图片还在识别中，识别完成后才能标记"
        : "这张图片还没有识别，识别完成后才能标记"
    }
    if (latest.annotation_error) {
      readOnly = true
      notices.push({
        tone: "error",
        message: `审图标注文件无法使用：${latest.annotation_error}。只能查看，仍然可以标记审核状态。`,
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
    writePreference(classKey(source), name)
  }
  const setAutoAdvance = (value: boolean) => {
    setAutoAdvanceState(value)
    writePreference(AUTO_ADVANCE_KEY, String(value))
    toast(value ? "标记后自动跳到下一张" : "标记后停在这张图片", {
      duration: 1200,
    })
  }
  const setShowPredictions = (value: boolean) => {
    setShowPredictionsState(value)
    writePreference(PREDICTIONS_KEY, String(value))
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
    // 用的是模型所属项目的类别列表时（而不是模型的类别）标出列表之外的候选。
    flagUnlisted: classesQ.data?.version !== "" && projectClasses.length > 0,
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

  /** 审图标注换成模型识别出的框（可以撤销）。 */
  const resetToPredictions = () => {
    if (!current || !editable) {
      return
    }
    if (sameBoxes(doc.objects, predictions)) {
      toast.info("审图标注与模型的识别结果相同")
      return
    }
    const start = loadedRef.current
    change(
      predictionsToObjects(predictions).map((object) => ({
        ...object,
        uid: boxes.newUid(),
      })),
      null
    )
    toast.success(
      predictions.length > 0
        ? `已恢复模型识别出的 ${formatNumber(predictions.length)} 个框`
        : "已恢复模型的识别结果（没有框）",
      {
        action: {
          label: "撤销",
          onClick: (event) => {
            focusCanvas(event.currentTarget)
            if (isCurrent(start)) {
              dispatch({ type: "undo" }) // 还在这张图片上
            }
          },
        },
      }
    )
  }

  // ---- 保存 ----
  const reload = async () => {
    const start = loadedRef.current
    try {
      const fresh = await queryClient.fetchQuery({
        ...reviewImageQuery(source, imageId),
        staleTime: 0,
      })
      if (!isCurrent(start)) {
        return // 已切换到别的图片
      }
      const next = { detail: fresh, token: start.token + 1 }
      setLoaded(next)
      dispatch({ type: "load", objects: fresh.objects, key: docKey(next) })
      toast.success("已重新读取审图标注")
    } catch (error) {
      if (isCurrent(start)) {
        toast.error("无法读取这张图片", {
          description: error instanceof Error ? error.message : undefined,
        })
      }
    }
  }

  const overwrite = async () => {
    if (await saver.overwrite()) {
      toast.success("已用你的版本覆盖审图标注")
    }
  }

  const save = async () => {
    const ok = await saver.flush()
    if (ok && saver.getState().status !== "clean") {
      toast.success("已保存", { duration: 1200 })
    }
  }

  if (latest && !latest.inferred) {
    notices.unshift({
      tone: "warning",
      message: isInferring(review)
        ? "这张图片还在识别中，识别完成后才能修改框、标记审核状态。"
        : "这张图片还没有识别（识别没有完成），重新识别后才能修改框、标记审核状态。",
      action: (
        <Button
          size="sm"
          variant="outline"
          onClick={(event) => {
            fromNotice(event)
            void reload()
          }}
        >
          重新读取
        </Button>
      ),
    })
  }
  if (saveState.status === "conflict") {
    notices.unshift({
      tone: "warning",
      message: "这张图片的审图标注已被修改（可能有其他人同时在审这张图片）。",
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
      action: (
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
  const urgentNotices = notices.filter((notice) => notice.tone !== "info")

  // ---- 切换图片 ----
  const openImage = (id: number) => {
    setCarry(view.keepViewport ? lastViewport.current : null)
    setParams((previous) => updateReviewParams(previous, { image: id }))
    setImagesOpen(false)
  }

  /** 放弃这张图片上没有保存成功的修改（见标注工作台）。 */
  const discardChanges = () => {
    saver.discard()
    const start = loadedRef.current
    if (!start) {
      return
    }
    const saved = savedReviewDetail(queryClient, source, start.detail)
    const next = { detail: saved, token: start.token + 1 }
    setLoaded(next)
    dispatch({ type: "load", objects: saved.objects, key: docKey(next) })
    setHighlight(null)
    setPicker(null)
  }

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
      target === imageIdRef.current ||
      navigating ||
      blocker.state !== "unblocked"
    ) {
      return
    }
    setNavigating(true)
    try {
      const from = imageIdRef.current
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
                focusCanvas(event.currentTarget)
                discardAndOpen(from, target)
              },
            },
          })
        }
        return
      }
      if (imageIdRef.current !== from) {
        return // 保存期间已切换到别的图片
      }
      openImage(target)
    } finally {
      setNavigating(false)
    }
  }

  /** 只看待审核的图片（Shift+A / Shift+D），数据集的范围不变。 */
  const pendingFilters: ReviewImageFilters = {
    ...ALL_REVIEW_IMAGES,
    datasetId: filters.datasetId,
    status: "pending",
  }

  /** 上一张 / 下一张（`pending` 时是待审核的）；没有时返回 false 并提示。 */
  const step = async (direction: 1 | -1, pending: boolean, from = imageId) => {
    try {
      const neighbors = await queryClient.fetchQuery({
        ...reviewNeighborsQuery(
          source,
          from,
          pending ? pendingFilters : filters
        ),
        staleTime: 0,
      })
      if (imageIdRef.current !== from) {
        return false // 已切换到别的图片
      }
      const target = direction === 1 ? neighbors.next : neighbors.prev
      if (!target) {
        toast.info(
          pending
            ? direction === 1
              ? "后面没有待审核的图片了"
              : "前面没有待审核的图片了"
            : direction === -1
              ? "已经是第一张"
              : hasReviewFilters(filters)
                ? "筛选结果中没有更多图片"
                : "已经是最后一张"
        )
        return false
      }
      await navigateTo(target.id)
      return true
    } catch (error) {
      toast.error("无法获取相邻的图片", {
        description: error instanceof Error ? error.message : undefined,
      })
      return false
    }
  }

  const gotoIndex = async (index: number) => {
    try {
      const image = await fetchReviewImageAt(source, filters, index)
      if (image) {
        await navigateTo(image.id)
      }
    } catch (error) {
      toast.error("无法跳转", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  const setFilters = (changes: Partial<ReviewImageFilters>) =>
    setParams((previous) => updateReviewParams(previous, changes), {
      replace: true,
    })

  // ---- 标记 ----
  /**
   * 标记审核状态：先保存这张图片的修改（失败或有冲突时不标记），再修改状态、
   * 更新缓存；标为已审核 / 需加样且打开了“自动跳到下一张”时跳到下一张符合
   * 筛选条件的图片。状态没有变化时不再请求，只跳转。
   */
  const mark = async (status: ReviewImageStatus) => {
    const start = loadedRef.current
    if (!current || !start || markingRef.current || navigating) {
      return
    }
    if (markBlocked) {
      toast.info(markBlocked)
      return
    }
    markingRef.current = true
    setMarking(status)
    try {
      if (!(await flushWithNotice(saver, "标记"))) {
        return
      }
      if (!isCurrent(start)) {
        return
      }
      const before = latest?.status ?? current.status
      if (before !== status) {
        await markReviewImage(queryClient, source, start.detail.id, status)
        if (!isCurrent(start)) {
          return
        }
      }
      const label = reviewImageStatusLabels[status]
      if (status === "pending" || !autoAdvance) {
        toast(before === status ? `已经是${label}` : `已标为${label}`, {
          duration: 1200,
        })
        return
      }
      if (before !== status) {
        toast(`已标为${label}`, { duration: 1000 })
      }
      await step(1, false, start.detail.id)
    } catch (error) {
      if (isCurrent(start)) {
        toast.error("标记失败", {
          description: error instanceof Error ? error.message : undefined,
        })
      }
    } finally {
      markingRef.current = false
      setMarking(null)
    }
  }

  // ---- 快捷键 ----
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
      case "review":
        void mark(command.status)
        return
      case "help":
        setHelpOpen(true)
        return
      // N（空标注）在审图时没有对应的按键。
    }
  }

  useEditorKeys(run, {
    mac,
    hasSelection: activeObject !== undefined,
    review: true,
  })

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
  const status = latest?.status ?? "pending"
  const canReset =
    editable &&
    latest !== null &&
    latest.inferred &&
    !sameBoxes(doc.objects, predictions)
  const classesPath =
    !local && review.project
      ? `/projects/${review.project.id}?tab=classes`
      : null

  const imagesPanel = (
    <ReviewImageList
      source={source}
      review={review}
      filters={filters}
      onFiltersChange={setFilters}
      currentId={imageId}
      position={neighbors ? neighbors.position : null}
      onOpen={(image: ReviewImage) => void navigateTo(image.id)}
    />
  )

  const sidePanel =
    current && latest ? (
      <div className="grid content-start gap-5 p-3">
        {urgentNotices.length > 0 && (
          <div className="grid gap-2">
            {urgentNotices.map((notice, index) => (
              <NoticeBox key={index} notice={notice} />
            ))}
          </div>
        )}
        <ReviewStatusCard
          detail={{
            status,
            reviewed_at: latest.reviewed_at,
            edited: latest.inferred && !sameBoxes(doc.objects, predictions),
          }}
          marking={marking}
          disabledReason={markBlocked}
          onMark={(value) => void mark(value)}
          autoAdvance={autoAdvance}
          onAutoAdvanceChange={setAutoAdvance}
        />
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
          onChange={(uid, patch) =>
            change(updateObject(doc.objects, uid, patch))
          }
          onDelete={(uid) => change(removeObject(doc.objects, uid), null)}
        />
        <PredictionList
          predictions={predictions}
          unmatched={unmatched}
          colorFor={colorFor}
          inferred={latest.inferred}
          inferenceError={latest.inference_error}
          canReset={canReset}
          onReset={resetToPredictions}
          onHighlight={setHighlightPrediction}
        />
        <ReviewImageInfo
          detail={latest}
          size={size}
          notices={notices.filter((notice) => notice.tone === "info")}
        />
      </div>
    ) : null

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ReviewToolbar
        backTo={backTo}
        backLabel={backLabel}
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
        onNextPending={() => void step(1, true)}
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
        showPredictions={showPredictions}
        onShowPredictionsChange={setShowPredictions}
        autoAdvance={autoAdvance}
        onAutoAdvanceChange={setAutoAdvance}
        saveState={saveState}
        savedAt={savedAt}
        onRetrySave={() => void saver.flush()}
        canReset={canReset}
        onReset={resetToPredictions}
        onReload={() => void reload()}
        classesPath={classesPath}
        onHelp={() => setHelpOpen(true)}
        onOpenImages={() => setImagesOpen(true)}
        onOpenPanel={() => setPanelOpen(true)}
        actions={actions?.(controls)}
        mod={mod}
      />

      <div className="flex min-h-0 flex-1">
        {imagesDocked && (
          <aside className="flex w-72 shrink-0 flex-col border-r">
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
              fileUrl={reviewImageFileUrl(source, current)}
              thumbnailUrl={reviewThumbnailUrl(source, current, "lg")}
              size={size}
              objects={doc.objects}
              selected={doc.selected}
              tool={tool}
              readOnly={readOnly}
              colorFor={colorFor}
              showBoxes={view.showBoxes}
              showLabels={view.showLabels}
              highlight={highlight}
              labelDetails={confidences}
              references={references}
              highlightReference={highlightReference}
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

          {/* 窄屏时审核状态面板收起：画布下方的状态栏用于标记。 */}
          {!panelDocked && current && (
            <div className="flex shrink-0 items-center gap-2 border-t bg-background px-2 py-1.5">
              <ReviewImageStatusBadge
                status={status}
                className="hidden shrink-0 sm:inline-flex"
              />
              <div className="min-w-0 flex-1">
                <ReviewMarkButtons
                  status={status}
                  marking={marking}
                  disabledReason={markBlocked}
                  onMark={(value) => void mark(value)}
                  compact
                />
              </div>
            </div>
          )}
        </div>

        {panelDocked && (
          <aside className="w-80 shrink-0 overflow-y-auto border-l">
            {sidePanel}
          </aside>
        )}
      </div>

      <Sheet open={imagesOpen && !imagesDocked} onOpenChange={setImagesOpen}>
        <SheetContent side="left" className="flex w-80 flex-col gap-0 p-0">
          <SheetHeader className="border-b">
            <SheetTitle>图片列表</SheetTitle>
            <SheetDescription className="sr-only">
              审图任务中的图片，可按审核状态等筛选，单击打开
            </SheetDescription>
          </SheetHeader>
          {imagesPanel}
        </SheetContent>
      </Sheet>
      <Sheet open={panelOpen && !panelDocked} onOpenChange={setPanelOpen}>
        <SheetContent side="right" className="w-80 gap-0 overflow-y-auto p-0">
          <SheetHeader className="border-b">
            <SheetTitle>审核状态与目标</SheetTitle>
            <SheetDescription className="sr-only">
              这张图片的审核状态、审图标注的框、模型的识别结果与图片信息
            </SheetDescription>
          </SheetHeader>
          {sidePanel}
        </SheetContent>
      </Sheet>

      <ShortcutsDialog
        open={helpOpen}
        onOpenChange={setHelpOpen}
        mac={mac}
        review
      />
      <UnsavedChangesDialog
        blocker={blocker}
        open={leavePromptOpen}
        saveState={saveState}
        conflictMessage="这张图片的审图标注已被修改（可能有其他人同时在审这张图片），你的修改没有保存。留在这里可以载入最新版本或用你的版本覆盖；离开则放弃这些修改。"
        onDiscard={discardChanges}
      />
    </div>
  )
}
