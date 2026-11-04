/**
 * 标注工作台与审图编辑器共用的小工具：跨图片保持的视图与工具设置（记在浏览器
 * 中，两个编辑器共用）、图片尺寸、类别选择框的状态、候选类别的读取状态，以及
 * 面板固定显示的宽度。
 */
import type { ImageAdjustments } from "@/features/annotation/annotation-canvas"
import type { Box, Size, Viewport } from "@/features/annotation/geometry"
import type { CandidateStatus } from "@/features/annotation/label-picker"
import type { Tool } from "@/features/annotation/shortcuts"
import { readPreference, writePreference } from "@/lib/storage"

export type ViewOptions = {
  showLabels: boolean
  showBoxes: boolean
  /** 切换到同尺寸的图片时保持缩放与位置（固定相机拍摄的产线图片）。 */
  keepViewport: boolean
} & ImageAdjustments

const VIEW_KEY = "trainmaster.annotation.view"
const TOOL_KEY = "trainmaster.annotation.tool"

export const DEFAULT_VIEW: ViewOptions = {
  showLabels: true,
  showBoxes: true,
  keepViewport: true,
  brightness: 1,
  contrast: 1,
}

/** 记住的视图设置；没有或无法解析时为默认值。每次打开都显示框。 */
export function readView(): ViewOptions {
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

export function writeView(view: ViewOptions) {
  writePreference(VIEW_KEY, JSON.stringify(view))
}

/** 记住的工具；没有时为选择工具。 */
export function readTool(): Tool {
  const value = readPreference(TOOL_KEY)
  return value === "rect" || value === "pan" ? value : "select"
}

export function writeTool(tool: Tool) {
  writePreference(TOOL_KEY, tool)
}

/** 与 Tailwind 的 `lg:`、`xl:` 一致：侧边面板、图片列表固定显示的宽度。 */
export const PANEL_QUERY = "(min-width: 64rem)"
export const IMAGES_QUERY = "(min-width: 80rem)"

/** 图片尺寸（来自索引），未知时为 null。 */
export function imageSizeOf(image: {
  width: number | null
  height: number | null
}): Size | null {
  return image.width && image.height
    ? { width: image.width, height: image.height }
    : null
}

export type Picker =
  /** 刚画好的框：选择类别后才加入，关闭选择框时放弃。 */
  | { mode: "new"; box: Box }
  /** 修改选中框的类别。 */
  | { mode: "relabel"; uid: string }
  /** 切换当前类别。 */
  | { mode: "active" }

/** 切换图片时沿用的视口：同尺寸的图片保持缩放与位置。 */
export type Carry = { size: Size; viewport: Viewport } | null

/** 候选类别的一个查询（如项目的类别列表、已有的标签），只用到这些字段。 */
type CandidateQuery = {
  data: unknown
  error: Error | null
  isPending: boolean
  isFetching: boolean
  refetch: () => Promise<unknown>
}

/**
 * 候选类别的读取状态（类别选择框中的提示）：还没有读到的查询失败时为
 * `error`（重试只重新读取失败的那些），还在读取时为 `loading`，否则为 null。
 * 读取中、读取失败时照样可以选择已有的候选、输入新的名称。
 */
export function candidateStatusOf(
  queries: readonly CandidateQuery[]
): CandidateStatus | null {
  const failed = queries.filter((query) => !query.data && query.error)
  if (failed.length > 0) {
    return {
      state: "error",
      message: failed[0].error?.message ?? "",
      retrying: queries.some((query) => query.isFetching),
      onRetry: () => {
        for (const query of queries) {
          if (!query.data) {
            void query.refetch()
          }
        }
      },
    }
  }
  return queries.some((query) => query.isPending) ? { state: "loading" } : null
}
