/**
 * 本地审图程序（trainmaster-reviewer）的接口：会话、浏览文件夹、打开 / 关闭审图包、
 * 导出审图结果、退出。审图编辑器读写审图包用的是与平台相同的接口（`/api/review`，
 * 见 {@link localReviewSource}），由 `features/reviews` 的编辑器直接使用。
 *
 * 程序只在本机（127.0.0.1）上提供服务，页面由程序打开的第一个地址登录（令牌写入
 * Cookie），之后的请求都带着它。
 */
import { queryOptions } from "@tanstack/react-query"

import type {
  ReviewCounts,
  ReviewModelRef,
  ReviewSource,
} from "@/features/reviews/api"
import { apiGet, apiSend } from "@/lib/api"

/** 正在打开的审图包：检查、解压（字节）或读取图片（张）。 */
export type OpeningStage = "checking" | "extracting" | "loading"

export type Opening = {
  path: string
  stage: OpeningStage
  done: number
  total: number
}

export type OpenFailure = { path: string; message: string }

export type ExportInfo = {
  path: string
  file_name: string
  size_bytes: number
  exported_at: string
}

/** 打开的审图包。 */
export type PackageInfo = {
  /** 审图包文件夹（zip 解压到它旁边的同名文件夹）。 */
  path: string
  /** 从 zip 打开时是 zip 的路径，打开文件夹时为空。 */
  zip_path: string
  /** 审图结果导出到这里（文件夹旁边的“<文件夹名>-审图结果.zip”）。 */
  results_path: string
  package_id: string
  review: { id: number; name: string; description: string }
  model: ReviewModelRef
  /** 数据集名称。 */
  datasets: string[]
  platform: string
  downloaded_at: string
  counts: ReviewCounts
  /** 审图包中缺少文件的图片数。 */
  missing_images: number
  /** 文件夹之前打开过：继续上次的进度。 */
  resumed: boolean
  opened_at: string
  last_export: ExportInfo | null
}

export type RecentPackage = {
  path: string
  zip_path: string
  package_id: string
  review_id: number
  review_name: string
  counts: ReviewCounts
  opened_at: string
  /** 文件夹还在。 */
  exists: boolean
}

export type Session = {
  version: string
  os: string
  /** 页面显示在程序自己的窗口中（而不是浏览器中）。 */
  window: boolean
  package: PackageInfo | null
  opening: Opening | null
  open_error: OpenFailure | null
  recent: RecentPackage[]
}

export type PlaceKind =
  "home" | "downloads" | "desktop" | "documents" | "drive" | "root"

export type Place = { name: string; path: string; kind: PlaceKind }

/** `results` 是导出的审图结果（要上传到平台，不能在这里打开）。 */
export type BrowseEntryKind =
  "folder" | "package" | "zip" | "results" | PlaceKind

export type BrowseEntry = {
  name: string
  path: string
  kind: BrowseEntryKind
  size_bytes: number
  modified_at: string | null
}

export type Browse = {
  /** 列出的文件夹；为空时列出的是常用位置。 */
  path: string
  /** 上一级；在磁盘的根目录时为空（回到常用位置）。 */
  parent: string
  entries: BrowseEntry[]
  places: Place[]
  /** 文件夹中的项目太多，只列出了一部分。 */
  truncated: boolean
}

export type ExportResult = ExportInfo & {
  /** review.json 中的图片数。 */
  images: number
  /** 审核状态与审图包中不同的图片。 */
  status_changes: number
  /** 导出的标注文件（标注改过的图片）。 */
  annotation_changes: number
  /** 改过但无法读取、没有导出的标注文件（它们的审核状态照常导出）。 */
  skipped: number
  skipped_paths: string[]
  counts: ReviewCounts
  missing_images: number
  /** 什么也没有改的图片。 */
  unchanged: number
}

export const sessionKey = ["local-session"] as const

/**
 * 会话：打开的审图包、打开进度、最近打开的审图包。正在打开时每 0.4 秒刷新一次
 * 进度。
 */
export function sessionQuery() {
  return queryOptions({
    queryKey: sessionKey,
    queryFn: ({ signal }) => apiGet<Session>("/api/session", { signal }),
    staleTime: 0,
    refetchInterval: (query) => (query.state.data?.opening ? 400 : false),
  })
}

export function browseQuery(path: string) {
  return queryOptions({
    queryKey: ["local-browse", path],
    queryFn: ({ signal }) =>
      apiGet<Browse>(`/api/browse?${new URLSearchParams({ path })}`, {
        signal,
      }),
    staleTime: 0,
  })
}

/** 审图编辑器读写打开的审图包：查询键带着 package_id，换了审图包时缓存不混用。 */
export function localReviewSource(packageId: string): ReviewSource {
  return { key: ["local-review", packageId], base: "/api/review" }
}

/** 开始打开审图包（zip 或文件夹）；进度见会话的 `opening`。 */
export function openPackage(path: string) {
  return apiSend<{ opening: Opening }>("POST", "/api/open", { path })
}

export function cancelOpening() {
  return apiSend("POST", "/api/open/cancel")
}

export function closePackage() {
  return apiSend("POST", "/api/close")
}

/** 导出审图结果（覆盖之前导出的）。 */
export function exportResults() {
  return apiSend<ExportResult>("POST", "/api/export")
}

/** 在资源管理器中显示导出的审图结果或审图包文件夹。 */
export function revealInFolder(target: "results" | "folder") {
  return apiSend("POST", "/api/reveal", { target })
}

export function removeRecent(path: string) {
  return apiSend("POST", "/api/recent/remove", { path })
}

/** 告诉程序：页面接手了关闭窗口（先保存最后的修改，再退出）。 */
export function acknowledgeClosing() {
  return apiSend("POST", "/api/closing")
}

/** 退出程序：窗口关闭；在浏览器中时程序停止运行。 */
export function quitProgram() {
  return apiSend("POST", "/api/quit")
}
