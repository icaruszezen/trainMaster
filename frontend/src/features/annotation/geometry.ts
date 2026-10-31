/**
 * 标注画布的几何计算（纯函数，便于测试）。
 *
 * 坐标系：图片坐标以原图像素为单位，框的四条边落在整数像素边界上，取值
 * 范围 [0, 宽] × [0, 高]（与 VOC 文件中的 xmin / ymin / xmax / ymax 一致）；
 * 屏幕坐标是相对于画布容器左上角的 CSS 像素。视口 `{ scale, x, y }` 表示
 * 屏幕坐标 = 图片坐标 × scale + (x, y)。
 */

export type Point = { x: number; y: number }
export type Size = { width: number; height: number }
export type Box = { xmin: number; ymin: number; xmax: number; ymax: number }
export type Viewport = { scale: number; x: number; y: number }

/** 八个缩放手柄：四个角与四条边的中点。 */
export type Handle = "nw" | "n" | "ne" | "e" | "se" | "s" | "sw" | "w"

export const HANDLES: readonly Handle[] = [
  "nw",
  "n",
  "ne",
  "e",
  "se",
  "s",
  "sw",
  "w",
]

/** 最大放大倍数：每个原图像素 32 个屏幕像素，足以逐像素对齐边缘。 */
export const MAX_SCALE = 32

export const clamp = (value: number, min: number, max: number) =>
  Math.min(max, Math.max(min, value))

/** 把图片等比缩放并居中放进容器（四周留 padding）；不会超过原尺寸的 `maxFit` 倍。 */
export function fitViewport(
  image: Size,
  container: Size,
  padding = 24,
  maxFit = 4
): Viewport {
  const availableWidth = Math.max(1, container.width - padding * 2)
  const availableHeight = Math.max(1, container.height - padding * 2)
  const scale = Math.min(
    availableWidth / image.width,
    availableHeight / image.height,
    maxFit
  )
  return {
    scale,
    x: (container.width - image.width * scale) / 2,
    y: (container.height - image.height * scale) / 2,
  }
}

/** 缩放倍数的允许范围：最小为适应窗口时的 1/4（但不超过 1），最大 {@link MAX_SCALE}。 */
export function scaleLimits(image: Size, container: Size) {
  const fit = fitViewport(image, container).scale
  return { min: Math.min(1, fit / 4), max: MAX_SCALE }
}

/** 以屏幕上的 `anchor` 为中心缩放 `factor` 倍，该点下的图片内容保持不动。 */
export function zoomAt(
  viewport: Viewport,
  factor: number,
  anchor: Point,
  limits: { min: number; max: number }
): Viewport {
  const scale = clamp(viewport.scale * factor, limits.min, limits.max)
  const ratio = scale / viewport.scale
  return {
    scale,
    x: anchor.x - (anchor.x - viewport.x) * ratio,
    y: anchor.y - (anchor.y - viewport.y) * ratio,
  }
}

/** 缩放到指定倍数（如 1 = 原图 100%），以 `anchor` 为中心。 */
export function zoomTo(
  viewport: Viewport,
  scale: number,
  anchor: Point,
  limits: { min: number; max: number }
): Viewport {
  return zoomAt(viewport, scale / viewport.scale, anchor, limits)
}

export function toImage(viewport: Viewport, point: Point): Point {
  return {
    x: (point.x - viewport.x) / viewport.scale,
    y: (point.y - viewport.y) / viewport.scale,
  }
}

export function toScreen(viewport: Viewport, point: Point): Point {
  return {
    x: point.x * viewport.scale + viewport.x,
    y: point.y * viewport.scale + viewport.y,
  }
}

/** 框在屏幕上的位置与尺寸。 */
export function boxToScreen(viewport: Viewport, box: Box) {
  return {
    x: box.xmin * viewport.scale + viewport.x,
    y: box.ymin * viewport.scale + viewport.y,
    width: (box.xmax - box.xmin) * viewport.scale,
    height: (box.ymax - box.ymin) * viewport.scale,
  }
}

export const boxWidth = (box: Box) => box.xmax - box.xmin
export const boxHeight = (box: Box) => box.ymax - box.ymin
export const boxArea = (box: Box) => boxWidth(box) * boxHeight(box)

export function sameBox(a: Box, b: Box) {
  return (
    a.xmin === b.xmin &&
    a.ymin === b.ymin &&
    a.xmax === b.xmax &&
    a.ymax === b.ymax
  )
}

/** 坐标取整到像素边界并限制在图片内。 */
export function snap(value: number, limit: number) {
  return clamp(Math.round(value), 0, limit)
}

/**
 * 由拖动的两个角点（图片坐标，可以超出图片）得到框：取整、限制在图片内；
 * 宽或高不足 1 像素时返回 null。
 */
export function boxFromPoints(a: Point, b: Point, size: Size): Box | null {
  const xmin = snap(Math.min(a.x, b.x), size.width)
  const xmax = snap(Math.max(a.x, b.x), size.width)
  const ymin = snap(Math.min(a.y, b.y), size.height)
  const ymax = snap(Math.max(a.y, b.y), size.height)
  if (xmax - xmin < 1 || ymax - ymin < 1) {
    return null
  }
  return { xmin, ymin, xmax, ymax }
}

/**
 * 把框限制在图片内（例如来自其它工具、略超出图片的框，或粘贴到更小的图片上）。
 * 宽或高变为 0 时返回 null。
 */
export function clampBox(box: Box, size: Size): Box | null {
  const clamped = {
    xmin: snap(box.xmin, size.width),
    ymin: snap(box.ymin, size.height),
    xmax: snap(box.xmax, size.width),
    ymax: snap(box.ymax, size.height),
  }
  if (clamped.xmax - clamped.xmin < 1 || clamped.ymax - clamped.ymin < 1) {
    return null
  }
  return clamped
}

/** 平移框（保持尺寸），不移出图片。 */
export function moveBox(box: Box, dx: number, dy: number, size: Size): Box {
  const width = boxWidth(box)
  const height = boxHeight(box)
  const xmin = clamp(
    Math.round(box.xmin + dx),
    0,
    Math.max(0, size.width - width)
  )
  const ymin = clamp(
    Math.round(box.ymin + dy),
    0,
    Math.max(0, size.height - height)
  )
  return { xmin, ymin, xmax: xmin + width, ymax: ymin + height }
}

/**
 * 拖动手柄缩放框：手柄对应的边跟随 `point`（图片坐标），对边不动。边越过
 * 对边时框翻转（与常见工具一致），并始终至少保留 1 像素。
 */
export function resizeBox(
  box: Box,
  handle: Handle,
  point: Point,
  size: Size
): Box {
  let { xmin, ymin, xmax, ymax } = box
  const x = snap(point.x, size.width)
  const y = snap(point.y, size.height)
  if (handle.includes("w")) {
    xmin = x
  } else if (handle.includes("e")) {
    xmax = x
  }
  if (handle.startsWith("n")) {
    ymin = y
  } else if (handle.startsWith("s")) {
    ymax = y
  }
  return normalizeBox({ xmin, ymin, xmax, ymax }, size)
}

/**
 * 用键盘调整框：右边移动 `dx`、下边移动 `dy`（图片像素），左上角不动，宽、高
 * 至少 1 像素，不超出图片。
 */
export function resizeEdges(box: Box, dx: number, dy: number, size: Size): Box {
  return {
    xmin: box.xmin,
    ymin: box.ymin,
    xmax: clamp(
      box.xmax + dx,
      box.xmin + 1,
      Math.max(box.xmin + 1, size.width)
    ),
    ymax: clamp(
      box.ymax + dy,
      box.ymin + 1,
      Math.max(box.ymin + 1, size.height)
    ),
  }
}

/** 画正方形（按住 Shift）：把 `point` 调整到以 `anchor` 为对角、边长取较大一边。 */
export function squarePoint(anchor: Point, point: Point): Point {
  const dx = point.x - anchor.x
  const dy = point.y - anchor.y
  const side = Math.max(Math.abs(dx), Math.abs(dy))
  return {
    x: anchor.x + (dx < 0 ? -side : side),
    y: anchor.y + (dy < 0 ? -side : side),
  }
}

/** 交换颠倒的边，并保证宽、高至少 1 像素（贴着图片边缘时向内扩展）。 */
export function normalizeBox(box: Box, size: Size): Box {
  let xmin = Math.min(box.xmin, box.xmax)
  let xmax = Math.max(box.xmin, box.xmax)
  let ymin = Math.min(box.ymin, box.ymax)
  let ymax = Math.max(box.ymin, box.ymax)
  if (xmax - xmin < 1) {
    if (xmin + 1 <= size.width) {
      xmax = xmin + 1
    } else {
      xmin = xmax - 1
    }
  }
  if (ymax - ymin < 1) {
    if (ymin + 1 <= size.height) {
      ymax = ymin + 1
    } else {
      ymin = ymax - 1
    }
  }
  return { xmin, ymin, xmax, ymax }
}

/** 框在屏幕上的宽或高小于它时视为“小框”：只有四个角的手柄，且放在框外。 */
export const SMALL_BOX = 24

/** 小框的角手柄向外偏移的距离（屏幕像素），框内仍可以拖动整个框。 */
const SMALL_BOX_OUTSET = 6

export function isSmallOnScreen(viewport: Viewport, box: Box) {
  const rect = boxToScreen(viewport, box)
  return rect.width < SMALL_BOX || rect.height < SMALL_BOX
}

/** 手柄在屏幕上的位置（小框的角手柄在框外）。 */
export function handlePoint(
  viewport: Viewport,
  box: Box,
  handle: Handle
): Point {
  return handlePosition(
    viewport,
    box,
    handle,
    isSmallOnScreen(viewport, box) ? SMALL_BOX_OUTSET : 0
  )
}

/**
 * 开始缩放时，手柄控制的边（角）与指针之间的偏移（屏幕像素）。拖动时先把它
 * 加到指针上再换算成图片坐标，边就随指针移动的距离移动，而不会跳到指针处
 * （小框的角手柄画在框外，按下的位置也不一定正好在手柄中心）。
 */
export function resizeGrabOffset(
  viewport: Viewport,
  box: Box,
  handle: Handle,
  pointer: Point
): Point {
  const edge = handlePosition(viewport, box, handle, 0)
  return { x: edge.x - pointer.x, y: edge.y - pointer.y }
}

/** 手柄对应的边（角）在屏幕上的位置，角手柄向外偏移 `outset`。 */
function handlePosition(
  viewport: Viewport,
  box: Box,
  handle: Handle,
  outset: number
): Point {
  const rect = boxToScreen(viewport, box)
  const x = handle.includes("w")
    ? rect.x - outset
    : handle.includes("e")
      ? rect.x + rect.width + outset
      : rect.x + rect.width / 2
  const y = handle.startsWith("n")
    ? rect.y - outset
    : handle.startsWith("s")
      ? rect.y + rect.height + outset
      : rect.y + rect.height / 2
  return { x, y }
}

/** 框在屏幕上显示的手柄：小框只有四个角。 */
export function visibleHandles(viewport: Viewport, box: Box) {
  return isSmallOnScreen(viewport, box)
    ? HANDLES.filter((handle) => handle.length === 2)
    : HANDLES
}

/**
 * 屏幕上 `point` 处的手柄（容差 `radius` 像素），没有时为 null。
 */
export function handleAt(
  viewport: Viewport,
  box: Box,
  point: Point,
  radius = 8
): Handle | null {
  let best: Handle | null = null
  let bestDistance = Infinity
  for (const handle of visibleHandles(viewport, box)) {
    const p = handlePoint(viewport, box, handle)
    const distance = Math.max(Math.abs(p.x - point.x), Math.abs(p.y - point.y))
    if (distance <= radius && distance < bestDistance) {
      best = handle
      bestDistance = distance
    }
  }
  return best
}

export const HANDLE_CURSORS: Record<Handle, string> = {
  nw: "nwse-resize",
  se: "nwse-resize",
  ne: "nesw-resize",
  sw: "nesw-resize",
  n: "ns-resize",
  s: "ns-resize",
  e: "ew-resize",
  w: "ew-resize",
}

export function containsPoint(box: Box, point: Point) {
  return (
    point.x >= box.xmin &&
    point.x <= box.xmax &&
    point.y >= box.ymin &&
    point.y <= box.ymax
  )
}

/**
 * 图片坐标 `point` 处的所有框（下标），按面积从小到大排列（套在大框里的小框
 * 也能点中），面积相同时后画的（显示在上层）在前。隐藏的框传 null。
 * `tolerance` 是框外仍算点中的距离（图片像素），方便点中很细的框。
 */
export function hitCandidates(
  boxes: readonly (Box | null)[],
  point: Point,
  tolerance = 0
): number[] {
  const hits: { index: number; area: number }[] = []
  boxes.forEach((box, index) => {
    if (!box) {
      return
    }
    const grown = {
      xmin: box.xmin - tolerance,
      ymin: box.ymin - tolerance,
      xmax: box.xmax + tolerance,
      ymax: box.ymax + tolerance,
    }
    if (containsPoint(grown, point)) {
      hits.push({ index, area: boxArea(box) })
    }
  })
  return hits
    .sort((a, b) => a.area - b.area || b.index - a.index)
    .map((hit) => hit.index)
}

/** `point` 处最上层的框（见 {@link hitCandidates}），没有时为 -1。 */
export function hitTest(
  boxes: readonly (Box | null)[],
  point: Point,
  tolerance = 0
): number {
  return hitCandidates(boxes, point, tolerance)[0] ?? -1
}

/** 让框完整出现在视口中：已经可见时不动，否则平移（必要时缩小）到居中。 */
export function revealBox(
  viewport: Viewport,
  box: Box,
  container: Size,
  limits: { min: number; max: number },
  margin = 48
): Viewport {
  const rect = boxToScreen(viewport, box)
  const visible =
    rect.x >= 0 &&
    rect.y >= 0 &&
    rect.x + rect.width <= container.width &&
    rect.y + rect.height <= container.height
  if (visible) {
    return viewport
  }
  const fitScale = Math.min(
    (container.width - margin * 2) / Math.max(1, boxWidth(box)),
    (container.height - margin * 2) / Math.max(1, boxHeight(box))
  )
  const scale = clamp(
    Math.min(viewport.scale, fitScale),
    limits.min,
    limits.max
  )
  const cx = (box.xmin + box.xmax) / 2
  const cy = (box.ymin + box.ymax) / 2
  return {
    scale,
    x: container.width / 2 - cx * scale,
    y: container.height / 2 - cy * scale,
  }
}

/** 缩放并平移到让框居中、占满视口（四周留 `margin`）。 */
export function fitBox(
  box: Box,
  container: Size,
  limits: { min: number; max: number },
  margin = 48
): Viewport {
  const scale = clamp(
    Math.min(
      Math.max(1, container.width - margin * 2) / Math.max(1, boxWidth(box)),
      Math.max(1, container.height - margin * 2) / Math.max(1, boxHeight(box))
    ),
    limits.min,
    limits.max
  )
  const cx = (box.xmin + box.xmax) / 2
  const cy = (box.ymin + box.ymax) / 2
  return {
    scale,
    x: container.width / 2 - cx * scale,
    y: container.height / 2 - cy * scale,
  }
}

/** 两个触点之间的距离与中点（双指缩放）。 */
export function pinchState(a: Point, b: Point) {
  return {
    distance: Math.hypot(a.x - b.x, a.y - b.y),
    center: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 },
  }
}

/**
 * 滚轮事件 → 缩放倍数：鼠标滚轮每格（约 100 像素）约 15%，单次最多 30%，
 * 避免无阻尼滚轮一下跳过好几级；触控板的小幅滚动对应平滑缩放。
 * `deltaMode` 为 1（行）、2（页）时换算成像素。
 */
export function wheelZoomFactor(deltaY: number, deltaMode: number) {
  const pixels =
    deltaMode === 1 ? deltaY * 16 : deltaMode === 2 ? deltaY * 400 : deltaY
  return clamp(Math.exp(-pixels * 0.0014), 1 / 1.3, 1.3)
}

/** 画布上一个框的屏幕位置（限制在画布内），供在框旁边显示浮层。 */
export type OverlayAnchor = {
  /** 框的左边。 */
  x: number
  /** 框的上边。 */
  top: number
  /** 框的下边。 */
  bottom: number
  /** 画布尺寸：浮层放在这个范围内。 */
  bounds: Size
}

/** 框在画布上的位置，供在框旁边显示浮层（如类别选择框），见 {@link placeOverlay}。 */
export function screenAnchor(
  viewport: Viewport,
  box: Box,
  container: Size
): OverlayAnchor {
  const rect = boxToScreen(viewport, box)
  const height = Math.max(0, container.height)
  return {
    x: clamp(rect.x, 0, Math.max(0, container.width)),
    top: clamp(rect.y, 0, height),
    bottom: clamp(rect.y + rect.height, 0, height),
    bounds: { width: container.width, height: container.height },
  }
}

/**
 * 浮层（尺寸 `size`）的左上角位置：与框左边对齐、放在框下方；超出画布右边时
 * 左移，下方放不下时翻到框上方，上下都放不下时放在空间较大的一边并限制在画布
 * 内（浮层比画布还高时顶部对齐，保证上方的输入框可见）。四周留 `margin`。
 */
export function placeOverlay(
  anchor: OverlayAnchor,
  size: Size,
  gap = 6,
  margin = 8
): Point {
  const { bounds } = anchor
  const x = Math.max(
    margin,
    Math.min(anchor.x, bounds.width - margin - size.width)
  )
  const maxY = bounds.height - margin - size.height
  const below = anchor.bottom + gap
  const above = anchor.top - gap - size.height
  if (below <= maxY) {
    return { x, y: below }
  }
  if (above >= margin) {
    return { x, y: above }
  }
  const preferred = bounds.height - anchor.bottom >= anchor.top ? below : above
  return { x, y: Math.max(margin, Math.min(preferred, maxY)) }
}

/**
 * 让列表中的一项完整可见的滚动位置（同 `scrollIntoView({ block: "nearest" })`，
 * 但只滚动这个列表）：已经可见时不变，比列表还高时与顶部对齐。`itemTop` 是这一
 * 项相对列表内容顶部的位置。
 */
export function nearestScrollTop(
  scrollTop: number,
  viewHeight: number,
  itemTop: number,
  itemHeight: number
) {
  if (itemTop < scrollTop || itemHeight > viewHeight) {
    return itemTop
  }
  if (itemTop + itemHeight > scrollTop + viewHeight) {
    return itemTop + itemHeight - viewHeight
  }
  return scrollTop
}
