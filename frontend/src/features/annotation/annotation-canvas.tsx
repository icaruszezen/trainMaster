import {
  memo,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type Ref,
} from "react"
import { ImageOff } from "lucide-react"

import { Spinner } from "@/components/ui/spinner"
import type { EditorObject } from "@/features/annotation/document"
import {
  boxFromPoints,
  boxHeight,
  boxToScreen,
  boxWidth,
  clamp,
  fitBox,
  fitViewport,
  handleAt,
  handlePoint,
  hitCandidates,
  HANDLE_CURSORS,
  moveBox,
  pinchState,
  resizeBox,
  resizeGrabOffset,
  revealBox,
  sameBox,
  scaleLimits,
  screenAnchor,
  squarePoint,
  toImage,
  visibleHandles,
  wheelZoomFactor,
  zoomAt,
  zoomTo,
  type Box,
  type Handle,
  type OverlayAnchor,
  type Point,
  type Size,
  type Viewport,
} from "@/features/annotation/geometry"
import { isTypingTarget, type Tool } from "@/features/annotation/shortcuts"
import {
  fileUrl,
  thumbnailUrl,
  type DatasetImage,
} from "@/features/datasets/api"
import { isBrowserNativeImage } from "@/features/datasets/files"
import { readableTextColor } from "@/features/datasets/label-colors"
import { useElementSize } from "@/hooks/use-element-size"
import { cn } from "@/lib/utils"

/** 拖动超过这个距离（屏幕像素）才算拖动，否则是单击（两次单击画框）。 */
const DRAG_THRESHOLD = 4
/** 点中框的容差（屏幕像素），方便点中很细的框。 */
const HIT_TOLERANCE = 4
/** 目标太多时不画类别名称，避免遮挡图片、拖慢页面。 */
const MAX_LABELED_OBJECTS = 300
/** 放大到这个倍数以上时按像素显示（不做平滑），便于对齐缺陷边缘。 */
const PIXELATED_SCALE = 4
const ZOOM_STEP = 1.25

/** 事件是否发生在叠加的浮层（`data-canvas-overlay`）里：其中的滚轮、手势不缩放画布。 */
const inOverlay = (target: EventTarget | null) =>
  target instanceof Element && target.closest("[data-canvas-overlay]") !== null

/**
 * Safari 的触控板捏合事件（`gesturestart` / `gesturechange` / `gestureend`），
 * TypeScript 的 DOM 类型中没有，这里只声明用到的字段。
 */
type SafariGestureEvent = UIEvent & {
  /** 相对手势开始时的缩放倍数。 */
  scale: number
  clientX: number
  clientY: number
}

export type ImageAdjustments = { brightness: number; contrast: number }

export type CanvasHandle = {
  zoom: (direction: 1 | -1) => void
  fit: () => void
  actualSize: () => void
  zoomToBox: (box: Box) => void
  /** 框不在视口内时平移过去（Tab 选中时）。 */
  reveal: (box: Box) => void
  /** 取消正在画的框；没有在画时返回 false。 */
  cancelDrawing: () => boolean
}

type Drag =
  | { kind: "pan"; pointerId: number; start: Point; origin: Viewport }
  | {
      kind: "pinch"
      ids: [number, number]
      startDistance: number
      startCenter: Point
      origin: Viewport
    }
  | {
      kind: "draw"
      /** 按下的指针；两次单击模式中松开后为 null。 */
      pointerId: number | null
      anchor: Point
      current: Point
      startScreen: Point
      /** 两次单击模式：第一次单击定下一个角，第二次单击完成。 */
      click: boolean
    }
  | {
      kind: "move"
      pointerId: number
      uid: string
      start: Point
      startScreen: Point
      original: Box
      box: Box
      moved: boolean
      /** 按下前它就是选中的框：没有拖动时再次单击依次选中重叠的框。 */
      wasSelected: boolean
      candidates: string[]
    }
  | {
      kind: "resize"
      pointerId: number
      uid: string
      handle: Handle
      /** 按下时手柄控制的边与指针的偏移（屏幕像素），见 `resizeGrabOffset`。 */
      offset: Point
      original: Box
      box: Box
    }

type AnnotationCanvasProps = {
  ref?: Ref<CanvasHandle>
  datasetId: number
  image: DatasetImage
  /** 原图尺寸（来自索引）；未知时加载原图读取。 */
  size: Size | null
  objects: EditorObject[]
  selected: string | null
  tool: Tool
  readOnly: boolean
  colorFor: (name: string) => string
  showBoxes: boolean
  showLabels: boolean
  /** 隐藏的类别：不显示、不能点中。 */
  hiddenClasses: ReadonlySet<string>
  /** 正在悬停的框（如在目标列表中），突出显示。 */
  highlight: string | null
  adjustments: ImageAdjustments
  /** 切换图片时沿用的视口（同尺寸的图片保持缩放与位置）。 */
  initialViewport: Viewport | null
  onViewportChange: (viewport: Viewport) => void
  onSizeKnown: (size: Size) => void
  onSelect: (uid: string | null) => void
  onCreate: (box: Box) => void
  /** 画的框太小（宽或高不足 1 像素）被丢弃。 */
  onRejectBox: () => void
  onCommitBox: (uid: string, box: Box) => void
  /**
   * 叠加在画布上的内容（如类别选择框），相对画布定位；`anchor` 给出框在画布上
   * 的位置与画布尺寸。浮层内的滚轮、手势不缩放画布。
   */
  overlay?: (anchor: (box: Box) => OverlayAnchor | null) => ReactNode
}

type ImageLoad = "loading" | "ok" | "error"

/**
 * 标注画布：显示图片与标注框，画框、选中、拖动、缩放框，滚轮 / 双指缩放，
 * 拖动平移。坐标换算见 `geometry.ts`；框列表的修改通过回调交给页面（记入
 * 撤销历史）。按图片 id 挂载（`key`），切换图片时状态重置。
 */
export function AnnotationCanvas({
  ref,
  datasetId,
  image,
  size,
  objects,
  selected,
  tool,
  readOnly,
  colorFor,
  showBoxes,
  showLabels,
  hiddenClasses,
  highlight,
  adjustments,
  initialViewport,
  onViewportChange,
  onSizeKnown,
  onSelect,
  onCreate,
  onRejectBox,
  onCommitBox,
  overlay,
}: AnnotationCanvasProps) {
  const [containerRef, container] = useElementSize<HTMLDivElement>()
  const native = isBrowserNativeImage(image.path)
  const [thumb, setThumb] = useState<ImageLoad>("loading")
  const [original, setOriginal] = useState<ImageLoad>(
    native ? "loading" : "error"
  )
  const [natural, setNatural] = useState<Size | null>(null)
  const imageSize = size ?? natural
  const imageWidth = imageSize?.width ?? 0
  const imageHeight = imageSize?.height ?? 0

  // 用户缩放 / 平移之前跟随容器尺寸自动适应窗口。
  const [userViewport, setUserViewport] = useState<Viewport | null>(
    initialViewport
  )
  const ready = imageSize !== null && container.width > 0
  const fitted = useMemo(
    () =>
      imageWidth > 0 && container.width > 0
        ? fitViewport({ width: imageWidth, height: imageHeight }, container)
        : null,
    [imageWidth, imageHeight, container]
  )
  const viewport: Viewport | null = ready ? (userViewport ?? fitted) : null
  const limits = useMemo(
    () =>
      imageWidth > 0 && container.width > 0
        ? scaleLimits({ width: imageWidth, height: imageHeight }, container)
        : { min: 0.01, max: 32 },
    [imageWidth, imageHeight, container]
  )

  const [drag, setDrag] = useState<Drag | null>(null)
  const [hover, setHover] = useState<Point | null>(null)
  const [spaceHeld, setSpaceHeld] = useState(false)
  const pointers = useRef(new Map<number, Point>())

  // 换成其它工具或变为只读时取消正在画的框（包括两次单击画框中等待第二次
  // 单击的框），否则之后的单击会把它画完。
  const canDraw = tool === "rect" && !readOnly
  const [couldDraw, setCouldDraw] = useState(canDraw)
  if (couldDraw !== canDraw) {
    setCouldDraw(canDraw)
    if (!canDraw && drag?.kind === "draw") {
      setDrag(null)
    }
  }

  // 给非 React 事件（滚轮）读取的最新值。
  const latest = useRef({ viewport, limits })
  useEffect(() => {
    latest.current = { viewport, limits }
  })

  useEffect(() => {
    if (viewport) {
      onViewportChange(viewport)
    }
  }, [viewport, onViewportChange])

  // 滚轮缩放：需要非被动监听才能阻止浏览器缩放整个页面。
  useEffect(() => {
    const element = containerRef.current
    if (!element) {
      return
    }
    const onWheel = (event: WheelEvent) => {
      if (inOverlay(event.target)) {
        // 浮层（如类别列表）照常滚动；捏合（Ctrl+滚轮）仍不缩放整个页面。
        if (event.ctrlKey) {
          event.preventDefault()
        }
        return
      }
      event.preventDefault()
      const { viewport: current, limits: currentLimits } = latest.current
      if (!current) {
        return
      }
      const rect = element.getBoundingClientRect()
      const anchor = {
        x: event.clientX - rect.left,
        y: event.clientY - rect.top,
      }
      if (event.shiftKey && !event.ctrlKey) {
        // Shift+滚轮：横向平移。
        const delta = event.deltaY || event.deltaX
        setUserViewport({ ...current, x: current.x - delta })
        return
      }
      setUserViewport(
        zoomAt(
          current,
          wheelZoomFactor(event.deltaY, event.deltaMode),
          anchor,
          currentLimits
        )
      )
    }

    // Safari（macOS）的触控板捏合不产生 Ctrl+滚轮，而是 gesture* 事件：阻止
    // 页面缩放，以手势开始的位置为中心按手势的倍数缩放图片。触屏上的双指缩放
    // 由指针事件处理（此时已有两个按下的指针），这里不重复处理。
    let gesture: { origin: Viewport; anchor: Point } | null = null
    const onGestureStart = (event: Event) => {
      event.preventDefault()
      gesture = null
      const current = latest.current.viewport
      if (!current || inOverlay(event.target) || pointers.current.size >= 2) {
        return
      }
      const { clientX, clientY } = event as SafariGestureEvent
      const rect = element.getBoundingClientRect()
      gesture = {
        origin: current,
        anchor: { x: clientX - rect.left, y: clientY - rect.top },
      }
    }
    const onGestureChange = (event: Event) => {
      event.preventDefault()
      const { scale } = event as SafariGestureEvent
      if (gesture && Number.isFinite(scale) && scale > 0) {
        setUserViewport(
          zoomAt(gesture.origin, scale, gesture.anchor, latest.current.limits)
        )
      }
    }
    const onGestureEnd = (event: Event) => {
      event.preventDefault()
      gesture = null
    }
    const gestures = "ongesturestart" in window
    const options = { passive: false }
    element.addEventListener("wheel", onWheel, options)
    if (gestures) {
      element.addEventListener("gesturestart", onGestureStart, options)
      element.addEventListener("gesturechange", onGestureChange, options)
      element.addEventListener("gestureend", onGestureEnd, options)
    }
    return () => {
      element.removeEventListener("wheel", onWheel)
      if (gestures) {
        element.removeEventListener("gesturestart", onGestureStart)
        element.removeEventListener("gesturechange", onGestureChange)
        element.removeEventListener("gestureend", onGestureEnd)
      }
    }
  }, [containerRef])

  // 按住空格平移。
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.code !== "Space" ||
        isTypingTarget(event.target) ||
        // 焦点在按钮等控件上时空格是“按下”它。
        (event.target instanceof Element &&
          event.target.closest(
            'button, a, [role="button"], [role="checkbox"], [role="switch"], [role="tab"], [role="radio"]'
          ))
      ) {
        return
      }
      event.preventDefault() // 不滚动页面、不触发焦点所在的按钮
      if (!event.repeat) {
        setSpaceHeld(true)
      }
    }
    const onKeyUp = (event: KeyboardEvent) => {
      if (event.code === "Space") {
        setSpaceHeld(false)
      }
    }
    const onBlur = () => setSpaceHeld(false)
    window.addEventListener("keydown", onKeyDown)
    window.addEventListener("keyup", onKeyUp)
    window.addEventListener("blur", onBlur)
    return () => {
      window.removeEventListener("keydown", onKeyDown)
      window.removeEventListener("keyup", onKeyUp)
      window.removeEventListener("blur", onBlur)
    }
  }, [])

  const center = () => ({ x: container.width / 2, y: container.height / 2 })

  useImperativeHandle(ref, () => ({
    zoom: (direction) => {
      if (viewport) {
        setUserViewport(
          zoomAt(
            viewport,
            direction === 1 ? ZOOM_STEP : 1 / ZOOM_STEP,
            center(),
            limits
          )
        )
      }
    },
    fit: () => setUserViewport(null),
    actualSize: () => {
      if (viewport) {
        setUserViewport(zoomTo(viewport, 1, center(), limits))
      }
    },
    zoomToBox: (box) => {
      if (ready) {
        setUserViewport(fitBox(box, container, limits))
      }
    },
    reveal: (box) => {
      if (viewport) {
        const next = revealBox(viewport, box, container, limits)
        if (next !== viewport) {
          setUserViewport(next)
        }
      }
    },
    cancelDrawing: () => {
      if (drag?.kind === "draw") {
        setDrag(null)
        return true
      }
      return false
    },
  }))

  const visible = (object: EditorObject) =>
    showBoxes && !hiddenClasses.has(object.name)

  const selectedObject =
    selected === null
      ? undefined
      : objects.find((object) => object.uid === selected)
  const editableSelection =
    !readOnly && selectedObject && visible(selectedObject)
      ? selectedObject
      : undefined

  const local = (event: { clientX: number; clientY: number }) => {
    const rect = containerRef.current?.getBoundingClientRect()
    return rect
      ? { x: event.clientX - rect.left, y: event.clientY - rect.top }
      : { x: event.clientX, y: event.clientY }
  }

  const finishDraw = (anchor: Point, current: Point) => {
    if (!imageSize) {
      return
    }
    const box = boxFromPoints(anchor, current, imageSize)
    setDrag(null)
    if (box) {
      onCreate(box)
    } else {
      onRejectBox()
    }
  }

  const drawPoint = (anchor: Point, point: Point, square: boolean) =>
    square ? squarePoint(anchor, point) : point

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!viewport || !imageSize) {
      return
    }
    const element = event.currentTarget
    // 让输入框等失去焦点，快捷键随即生效。
    element.focus({ preventScroll: true })
    const screen = local(event)
    pointers.current.set(event.pointerId, screen)

    if (event.pointerType === "touch" && pointers.current.size === 2) {
      // 第二根手指：改为双指缩放，放弃第一根手指开始的操作。
      const [a, b] = [...pointers.current.entries()]
      const pinch = pinchState(a[1], b[1])
      element.setPointerCapture(event.pointerId)
      setDrag({
        kind: "pinch",
        ids: [a[0], b[0]],
        startDistance: Math.max(1, pinch.distance),
        startCenter: pinch.center,
        origin: viewport,
      })
      return
    }
    if (event.pointerType === "touch" && pointers.current.size > 2) {
      return
    }

    if (event.button === 2) {
      // 右键：取消正在画的框。
      if (drag?.kind === "draw") {
        setDrag(null)
      }
      return
    }
    const startPan = () => {
      element.setPointerCapture(event.pointerId)
      setDrag({
        kind: "pan",
        pointerId: event.pointerId,
        start: screen,
        origin: viewport,
      })
    }
    if (event.button === 1 || spaceHeld || tool === "pan") {
      event.preventDefault()
      startPan()
      return
    }
    if (event.button !== 0) {
      return
    }

    const point = toImage(viewport, screen)

    // 两次单击画框的第二次单击。
    if (drag?.kind === "draw" && drag.click && canDraw) {
      finishDraw(drag.anchor, drawPoint(drag.anchor, point, event.shiftKey))
      return
    }

    // 选中框的手柄优先（两种工具都可以直接缩放刚画的框）。
    if (editableSelection) {
      const handle = handleAt(viewport, editableSelection, screen)
      if (handle) {
        element.setPointerCapture(event.pointerId)
        setDrag({
          kind: "resize",
          pointerId: event.pointerId,
          uid: editableSelection.uid,
          handle,
          offset: resizeGrabOffset(viewport, editableSelection, handle, screen),
          original: editableSelection,
          box: editableSelection,
        })
        return
      }
    }

    if (canDraw) {
      element.setPointerCapture(event.pointerId)
      setDrag({
        kind: "draw",
        pointerId: event.pointerId,
        anchor: point,
        current: point,
        startScreen: screen,
        click: false,
      })
      return
    }

    const boxes = objects.map((object) => (visible(object) ? object : null))
    const candidates = hitCandidates(
      boxes,
      point,
      HIT_TOLERANCE / viewport.scale
    ).map((index) => objects[index].uid)
    if (candidates.length === 0) {
      onSelect(null)
      startPan()
      return
    }
    // 选中的框在光标下时继续拖动它（大框里套着小框时也能拖动大框）。
    const target =
      selected !== null && candidates.includes(selected)
        ? selected
        : candidates[0]
    if (target !== selected) {
      onSelect(target)
    }
    const object = objects.find((o) => o.uid === target)
    if (!object) {
      return
    }
    element.setPointerCapture(event.pointerId)
    setDrag({
      kind: "move",
      pointerId: event.pointerId,
      uid: target,
      start: point,
      startScreen: screen,
      original: object,
      box: object,
      moved: false,
      wasSelected: target === selected,
      candidates,
    })
  }

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const screen = local(event)
    if (pointers.current.has(event.pointerId)) {
      pointers.current.set(event.pointerId, screen)
    }
    if (event.pointerType !== "touch") {
      setHover(screen)
    }
    if (!drag || !viewport || !imageSize) {
      return
    }
    const point = toImage(viewport, screen)
    switch (drag.kind) {
      case "pan":
        if (drag.pointerId === event.pointerId) {
          setUserViewport({
            ...drag.origin,
            x: drag.origin.x + screen.x - drag.start.x,
            y: drag.origin.y + screen.y - drag.start.y,
          })
        }
        return
      case "pinch": {
        const a = pointers.current.get(drag.ids[0])
        const b = pointers.current.get(drag.ids[1])
        if (!a || !b) {
          return
        }
        const pinch = pinchState(a, b)
        const scale = clamp(
          drag.origin.scale * (pinch.distance / drag.startDistance),
          limits.min,
          limits.max
        )
        const anchor = toImage(drag.origin, drag.startCenter)
        setUserViewport({
          scale,
          x: pinch.center.x - anchor.x * scale,
          y: pinch.center.y - anchor.y * scale,
        })
        return
      }
      case "draw":
        if (drag.pointerId === null || drag.pointerId === event.pointerId) {
          setDrag({
            ...drag,
            current: drawPoint(drag.anchor, point, event.shiftKey),
          })
        }
        return
      case "move": {
        if (drag.pointerId !== event.pointerId) {
          return
        }
        const moved =
          drag.moved ||
          Math.hypot(
            screen.x - drag.startScreen.x,
            screen.y - drag.startScreen.y
          ) > 2
        if (!moved) {
          return
        }
        setDrag({
          ...drag,
          moved,
          box: readOnly
            ? drag.original
            : moveBox(
                drag.original,
                point.x - drag.start.x,
                point.y - drag.start.y,
                imageSize
              ),
        })
        return
      }
      case "resize":
        if (drag.pointerId === event.pointerId) {
          // 边随指针移动的距离移动（不跳到指针处）。
          const grabbed = toImage(viewport, {
            x: screen.x + drag.offset.x,
            y: screen.y + drag.offset.y,
          })
          setDrag({
            ...drag,
            box: resizeBox(drag.original, drag.handle, grabbed, imageSize),
          })
        }
        return
    }
  }

  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.delete(event.pointerId)
    if (!drag) {
      return
    }
    switch (drag.kind) {
      case "pan":
        if (drag.pointerId === event.pointerId) {
          setDrag(null)
        }
        return
      case "pinch":
        if (drag.ids.includes(event.pointerId)) {
          setDrag(null)
        }
        return
      case "draw": {
        if (drag.pointerId !== event.pointerId) {
          return
        }
        const screen = local(event)
        const dragged =
          Math.hypot(
            screen.x - drag.startScreen.x,
            screen.y - drag.startScreen.y
          ) > DRAG_THRESHOLD
        if (dragged) {
          finishDraw(drag.anchor, drag.current)
        } else if (event.pointerType === "touch") {
          setDrag(null) // 触屏上轻点不开始画框，避免误触
        } else {
          setDrag({ ...drag, pointerId: null, click: true })
        }
        return
      }
      case "move":
        if (drag.pointerId !== event.pointerId) {
          return
        }
        setDrag(null)
        if (drag.moved) {
          if (!sameBox(drag.box, drag.original)) {
            onCommitBox(drag.uid, drag.box)
          }
        } else if (drag.wasSelected && drag.candidates.length > 1) {
          // 再次单击同一处：依次选中下面重叠的框。
          const index = drag.candidates.indexOf(drag.uid)
          onSelect(drag.candidates[(index + 1) % drag.candidates.length])
        }
        return
      case "resize":
        if (drag.pointerId !== event.pointerId) {
          return
        }
        setDrag(null)
        if (!sameBox(drag.box, drag.original)) {
          onCommitBox(drag.uid, drag.box)
        }
        return
    }
  }

  const onPointerCancel = (event: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.delete(event.pointerId)
    if (drag && drag.kind !== "draw") {
      setDrag(null)
    } else if (drag?.kind === "draw" && drag.pointerId === event.pointerId) {
      setDrag(null)
    }
  }

  const onDoubleClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (!viewport || !ready || tool === "rect") {
      return
    }
    const point = toImage(viewport, local(event))
    const boxes = objects.map((object) => (visible(object) ? object : null))
    const [index] = hitCandidates(boxes, point, HIT_TOLERANCE / viewport.scale)
    if (index === undefined) {
      setUserViewport(null)
    } else {
      setUserViewport(fitBox(objects[index], container, limits))
    }
  }

  // 画面中显示的框：拖动中的框显示拖动后的位置。
  const draftBox =
    drag?.kind === "move" || drag?.kind === "resize" ? drag : null
  const shownObjects = useMemo(
    () =>
      draftBox
        ? objects.map((object) =>
            draftBox.uid === object.uid
              ? { ...object, ...draftBox.box }
              : object
          )
        : objects,
    [objects, draftBox]
  )
  const drawBox =
    drag?.kind === "draw" && imageSize
      ? boxFromPoints(drag.anchor, drag.current, imageSize)
      : null

  const hoverImage = viewport && hover ? toImage(viewport, hover) : null
  const hoverHandle =
    viewport && hover && editableSelection && !drag
      ? handleAt(viewport, editableSelection, hover)
      : null
  const hoverUid =
    viewport && hoverImage && !drag && tool !== "rect"
      ? (() => {
          const boxes = shownObjects.map((object) =>
            visible(object) ? object : null
          )
          const [index] = hitCandidates(
            boxes,
            hoverImage,
            HIT_TOLERANCE / viewport.scale
          )
          return index === undefined ? null : shownObjects[index].uid
        })()
      : null

  const cursor =
    drag?.kind === "pan" || drag?.kind === "pinch"
      ? "grabbing"
      : spaceHeld || tool === "pan"
        ? "grab"
        : drag?.kind === "resize"
          ? HANDLE_CURSORS[drag.handle]
          : drag?.kind === "move"
            ? "move"
            : hoverHandle
              ? HANDLE_CURSORS[hoverHandle]
              : tool === "rect" && !readOnly
                ? "crosshair"
                : hoverUid
                  ? "move"
                  : "default"

  const anchor = (box: Box) =>
    viewport ? screenAnchor(viewport, box, container) : null

  // 尺寸未知时不加载缩略图，只看原图。
  const failed = original === "error" && (thumb === "error" || !imageSize)
  const loaded = thumb === "ok" || original === "ok"
  const filter =
    adjustments.brightness !== 1 || adjustments.contrast !== 1
      ? `brightness(${adjustments.brightness}) contrast(${adjustments.contrast})`
      : undefined
  // 按文件中存储的像素方向显示（不按 EXIF 方向旋转）：原图尺寸、缩略图和 VOC
  // 坐标都以它为准。
  const imageStyle = (s: Size, v: Viewport) => ({
    imageOrientation: "none" as const,
    width: s.width,
    height: s.height,
    transform: `translate(${v.x}px, ${v.y}px) scale(${v.scale})`,
    transformOrigin: "0 0",
    imageRendering:
      v.scale >= PIXELATED_SCALE ? ("pixelated" as const) : undefined,
    filter,
  })

  const showCrosshair =
    tool === "rect" &&
    !readOnly &&
    hover !== null &&
    !spaceHeld &&
    drag?.kind !== "pan" &&
    drag?.kind !== "pinch"

  const readout = (() => {
    if (drawBox) {
      return `${boxWidth(drawBox)} × ${boxHeight(drawBox)}`
    }
    if (draftBox) {
      return `${boxWidth(draftBox.box)} × ${boxHeight(draftBox.box)}`
    }
    if (hoverImage && imageSize) {
      const x = Math.floor(hoverImage.x)
      const y = Math.floor(hoverImage.y)
      if (x >= 0 && y >= 0 && x < imageSize.width && y < imageSize.height) {
        return `${x}, ${y}`
      }
    }
    return null
  })()

  return (
    <div
      ref={containerRef}
      tabIndex={-1}
      role="application"
      aria-label={`标注画布：${image.file_name}`}
      // overflow-clip：不是滚动容器，浮层里的输入框获得焦点时不会把画布滚开
      // （那样指针位置与图片的对应关系就错了）。
      className="relative min-h-0 min-w-0 flex-1 touch-none overflow-clip bg-muted/60 outline-none select-none"
      style={{ cursor }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerCancel}
      onPointerLeave={() => setHover(null)}
      onDoubleClick={onDoubleClick}
      onContextMenu={(event) => event.preventDefault()}
    >
      {/* 原图尺寸未知时先加载原图读出尺寸（不显示）：按存储的像素方向、原尺寸
          排版后读排版尺寸，naturalWidth / naturalHeight 总是按 EXIF 方向旋转后的尺寸。 */}
      {!imageSize && native && (
        <img
          src={fileUrl(datasetId, image)}
          alt=""
          className="invisible absolute top-0 left-0 max-w-none"
          style={{ imageOrientation: "none" }}
          onLoad={(event) => {
            const { offsetWidth, offsetHeight } = event.currentTarget
            if (offsetWidth > 0 && offsetHeight > 0) {
              const known = { width: offsetWidth, height: offsetHeight }
              setNatural(known)
              onSizeKnown(known)
            }
          }}
          onError={() => setOriginal("error")}
        />
      )}
      {imageSize && viewport && (
        <>
          <img
            src={thumbnailUrl(datasetId, image, "lg")}
            alt=""
            draggable={false}
            decoding="async"
            onLoad={() => setThumb("ok")}
            onError={() => setThumb("error")}
            className={cn(
              "pointer-events-none absolute top-0 left-0 max-w-none",
              (thumb !== "ok" || original === "ok") && "invisible"
            )}
            style={imageStyle(imageSize, viewport)}
          />
          {native && original !== "error" && (
            <img
              src={fileUrl(datasetId, image)}
              alt={image.file_name}
              draggable={false}
              decoding="async"
              onLoad={() => setOriginal("ok")}
              onError={() => setOriginal("error")}
              className={cn(
                "pointer-events-none absolute top-0 left-0 max-w-none",
                original !== "ok" && "invisible"
              )}
              style={imageStyle(imageSize, viewport)}
            />
          )}
        </>
      )}

      {viewport && imageSize && loaded && (
        <svg
          className="pointer-events-none absolute inset-0 size-full overflow-visible"
          aria-hidden
        >
          <ImageFrame viewport={viewport} size={imageSize} />
          <BoxLayer
            objects={shownObjects}
            viewport={viewport}
            colorFor={colorFor}
            hiddenClasses={hiddenClasses}
            showBoxes={showBoxes}
            selected={selected}
            emphasized={highlight ?? hoverUid}
          />
          {editableSelection && (
            <Handles
              viewport={viewport}
              box={
                draftBox?.uid === editableSelection.uid
                  ? draftBox.box
                  : editableSelection
              }
              color={colorFor(editableSelection.name)}
            />
          )}
          {drawBox && (
            <rect
              {...screenRect(viewport, drawBox)}
              fill="none"
              stroke="white"
              strokeWidth={1.5}
              strokeDasharray="5 3"
              className="mix-blend-difference"
            />
          )}
          {showCrosshair && hover && (
            <g className="mix-blend-difference" stroke="white" strokeWidth={1}>
              <line x1={0} x2={container.width} y1={hover.y} y2={hover.y} />
              <line x1={hover.x} x2={hover.x} y1={0} y2={container.height} />
            </g>
          )}
        </svg>
      )}

      {viewport && loaded && showLabels && showBoxes && (
        <LabelLayer
          objects={shownObjects}
          viewport={viewport}
          colorFor={colorFor}
          hiddenClasses={hiddenClasses}
          selected={selected}
        />
      )}

      {!loaded && !failed && (
        <div className="absolute inset-0 flex items-center justify-center">
          <Spinner className="size-6 text-muted-foreground" />
        </div>
      )}
      {(failed || (!imageSize && !native)) && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground">
          <ImageOff className="size-8" />
          {imageSize
            ? "无法在浏览器中显示这张图片"
            : "无法读取图片尺寸，不能在线标注这张图片"}
        </div>
      )}

      {readout && (
        <div className="pointer-events-none absolute bottom-2 left-2 rounded-md bg-background/85 px-2 py-0.5 font-mono text-xs text-foreground tabular-nums shadow-sm backdrop-blur">
          {readout}
        </div>
      )}

      {overlay && (
        <div data-canvas-overlay="" className="contents">
          {overlay(anchor)}
        </div>
      )}
    </div>
  )
}

function screenRect(viewport: Viewport, box: Box) {
  const rect = boxToScreen(viewport, box)
  return { x: rect.x, y: rect.y, width: rect.width, height: rect.height }
}

function ImageFrame({ viewport, size }: { viewport: Viewport; size: Size }) {
  return (
    <rect
      {...screenRect(viewport, {
        xmin: 0,
        ymin: 0,
        xmax: size.width,
        ymax: size.height,
      })}
      fill="none"
      className="stroke-border"
      strokeWidth={1}
    />
  )
}

type BoxLayerProps = {
  objects: EditorObject[]
  viewport: Viewport
  colorFor: (name: string) => string
  hiddenClasses: ReadonlySet<string>
  showBoxes: boolean
  selected: string | null
  emphasized: string | null
}

const BoxLayer = memo(function BoxLayer({
  objects,
  viewport,
  colorFor,
  hiddenClasses,
  showBoxes,
  selected,
  emphasized,
}: BoxLayerProps) {
  if (!showBoxes) {
    return null
  }
  return (
    <g>
      {objects.map((object) => {
        if (hiddenClasses.has(object.name)) {
          return null
        }
        const color = colorFor(object.name)
        const isSelected = object.uid === selected
        const isEmphasized = object.uid === emphasized
        return (
          <rect
            key={object.uid}
            {...screenRect(viewport, object)}
            fill={color}
            fillOpacity={isSelected ? 0.2 : isEmphasized ? 0.16 : 0.06}
            stroke={color}
            strokeWidth={isSelected || isEmphasized ? 2.5 : 1.75}
            strokeDasharray={object.difficult ? "6 4" : undefined}
          />
        )
      })}
    </g>
  )
})

function Handles({
  viewport,
  box,
  color,
}: {
  viewport: Viewport
  box: Box
  color: string
}) {
  return (
    <g>
      {visibleHandles(viewport, box).map((handle) => {
        const p = handlePoint(viewport, box, handle)
        return (
          <rect
            key={handle}
            x={p.x - 4}
            y={p.y - 4}
            width={8}
            height={8}
            rx={1.5}
            fill="white"
            stroke={color}
            strokeWidth={1.5}
          />
        )
      })}
    </g>
  )
}

type LabelLayerProps = {
  objects: EditorObject[]
  viewport: Viewport
  colorFor: (name: string) => string
  hiddenClasses: ReadonlySet<string>
  selected: string | null
}

const LabelLayer = memo(function LabelLayer({
  objects,
  viewport,
  colorFor,
  hiddenClasses,
  selected,
}: LabelLayerProps) {
  if (objects.length > MAX_LABELED_OBJECTS) {
    return null
  }
  return (
    <div className="pointer-events-none absolute inset-0 overflow-hidden">
      {objects.map((object) => {
        if (hiddenClasses.has(object.name)) {
          return null
        }
        const color = colorFor(object.name)
        const rect = boxToScreen(viewport, object)
        // 框贴着画布顶部时，名称放到框内。
        const inside = rect.y < 18
        return (
          <span
            key={object.uid}
            className={cn(
              "absolute max-w-48 truncate rounded-sm px-1 text-[11px] leading-4 font-medium whitespace-nowrap",
              object.uid === selected && "ring-1 ring-white/80"
            )}
            style={{
              left: rect.x,
              top: inside ? rect.y + 2 : rect.y - 17,
              backgroundColor: color,
              color: readableTextColor(color),
            }}
          >
            {object.name}
            {object.difficult && " · 难例"}
          </span>
        )
      })}
    </div>
  )
})
