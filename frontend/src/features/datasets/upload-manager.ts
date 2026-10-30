/**
 * 全局上传管理器：模块级的状态 + useSyncExternalStore，上传不随页面切换中断。
 *
 * 一个任务对应服务端的一个导入任务（DatasetImport）：
 *   uploading（分块 / 分批上传）→ committing（提交）→ processing（服务端解压、导入，
 *   轮询任务状态）→ done / error / canceled。
 *
 * - 压缩包：按 8 MiB 分块顺序 PUT，分块失败自动重试；服务端返回
 *   `upload_offset_mismatch` 时重新获取任务，从 `received_bytes` 续传。
 * - 文件夹：按 64 个文件 / 32 MiB 分批，3 个请求并发 POST multipart。
 * - 上传中关闭或刷新页面会弹出浏览器的离开确认。
 */
import { useSyncExternalStore } from "react"

import {
  cancelImport,
  commitImport,
  datasetKeys,
  importPath,
  isActiveImport,
  type Dataset,
  type DatasetImport,
} from "@/features/datasets/api"
import { baseName } from "@/features/datasets/files"
import type { PickedFile } from "@/features/datasets/folder-input"
import {
  refreshStartedImport,
  watchImport,
} from "@/features/datasets/import-watch"
import {
  FOLDER_CONCURRENCY,
  MAX_RETRIES,
  nextChunk,
  planBatches,
  retryDelayMs,
  smoothSpeed,
} from "@/features/datasets/upload-plan"
import { ApiError, apiGet, apiUpload } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type UploadKind = "archive" | "folder"
export type UploadStatus =
  "uploading" | "committing" | "processing" | "done" | "error" | "canceled"

export type UploadTask = {
  /** `${datasetId}:${importId}` */
  id: string
  datasetId: number
  datasetName: string
  importId: number
  kind: UploadKind
  /** 压缩包文件名 / 文件夹名。 */
  fileName: string
  totalBytes: number
  totalFiles: number
  uploadedBytes: number
  uploadedFiles: number
  /** 平滑后的上传速度（字节/秒），0 表示未知。 */
  speed: number
  status: UploadStatus
  error: string
  /** 上传或提交阶段失败、可以从断点继续时为 true。 */
  retryable: boolean
  /** 服务端任务的最新状态（处理阶段的进度、最终结果）。 */
  job: DatasetImport
  createdAt: number
}

type Runtime = {
  controller: AbortController
  canceled: boolean
  /** 上传完成或任务结束后释放，不再持有 File 引用。 */
  archive: File | null
  files: PickedFile[] | null
  /** 文件夹上传中已成功的批次序号：重试时跳过。 */
  completedBatches: Set<number>
  stopObserving: (() => void) | null
  sample: { time: number; bytes: number }
  lastEmit: number
}

const RUNNING: ReadonlySet<UploadStatus> = new Set([
  "uploading",
  "committing",
  "processing",
])

export function isUploadRunning(task: Pick<UploadTask, "status">) {
  return RUNNING.has(task.status)
}

let tasks: readonly UploadTask[] = []
const listeners = new Set<() => void>()
const runtimes = new Map<string, Runtime>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function getSnapshot() {
  return tasks
}

function setTasks(next: readonly UploadTask[]) {
  tasks = next
  syncUnloadGuard()
  for (const listener of listeners) {
    listener()
  }
}

function getTask(id: string) {
  return tasks.find((task) => task.id === id)
}

function patchTask(id: string, changes: Partial<UploadTask>) {
  setTasks(
    tasks.map((task) => (task.id === id ? { ...task, ...changes } : task))
  )
}

// ---- 离开页面提醒 ---------------------------------------------------------

let unloadGuardInstalled = false

function onBeforeUnload(event: BeforeUnloadEvent) {
  event.preventDefault()
  // 旧版浏览器需要设置 returnValue 才会弹出确认。
  event.returnValue = ""
}

function syncUnloadGuard() {
  const uploading = tasks.some(
    (task) => task.status === "uploading" || task.status === "committing"
  )
  if (uploading && !unloadGuardInstalled) {
    window.addEventListener("beforeunload", onBeforeUnload)
    unloadGuardInstalled = true
  } else if (!uploading && unloadGuardInstalled) {
    window.removeEventListener("beforeunload", onBeforeUnload)
    unloadGuardInstalled = false
  }
}

// ---- 请求辅助 -------------------------------------------------------------

/** 值得重试的错误：网络中断、超时、限流与网关 / 服务端的暂时性错误。 */
function isTransientError(error: unknown) {
  if (!(error instanceof ApiError)) {
    return false
  }
  const { status } = error
  return (
    status === 0 ||
    status === 408 ||
    status === 429 ||
    (status >= 500 && status !== 501 && status !== 507)
  )
}

function sleep(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("上传已取消", "AbortError"))
      return
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(new DOMException("上传已取消", "AbortError"))
    }
    signal.addEventListener("abort", onAbort, { once: true })
  })
}

async function withRetry<T>(
  request: () => Promise<T>,
  signal: AbortSignal
): Promise<T> {
  for (let attempt = 1; ; attempt += 1) {
    try {
      return await request()
    } catch (error) {
      if (signal.aborted || attempt > MAX_RETRIES || !isTransientError(error)) {
        throw error
      }
      await sleep(retryDelayMs(attempt), signal)
    }
  }
}

class UploadStateError extends Error {}

/** 重新获取任务，并确认它仍在等待上传（可能已被取消或超时清理）。 */
async function fetchUploadingJob(task: UploadTask, signal: AbortSignal) {
  const job = await withRetry(
    () =>
      apiGet<DatasetImport>(importPath(task.datasetId, task.importId), {
        signal,
      }),
    signal
  )
  if (job.status !== "uploading") {
    throw new UploadStateError(
      job.status === "canceled"
        ? job.error || "导入任务已被取消"
        : "导入任务已不在上传状态"
    )
  }
  return job
}

function describeError(error: unknown) {
  if (error instanceof ApiError || error instanceof UploadStateError) {
    return error.message
  }
  return "上传失败，请重试"
}

// ---- 进度 -----------------------------------------------------------------

const EMIT_INTERVAL_MS = 150
const SPEED_SAMPLE_MS = 500

function reportProgress(
  id: string,
  runtime: Runtime,
  uploadedBytes: number,
  uploadedFiles: number | null,
  force: boolean
) {
  const task = getTask(id)
  if (!task || task.status !== "uploading") {
    return
  }

  const now = performance.now()
  let { speed } = task
  const elapsed = now - runtime.sample.time
  if (elapsed >= SPEED_SAMPLE_MS) {
    speed = smoothSpeed(speed, uploadedBytes - runtime.sample.bytes, elapsed)
    runtime.sample = { time: now, bytes: uploadedBytes }
  }
  if (!force && now - runtime.lastEmit < EMIT_INTERVAL_MS) {
    return
  }
  runtime.lastEmit = now
  patchTask(id, {
    uploadedBytes,
    uploadedFiles: uploadedFiles ?? task.uploadedFiles,
    speed,
  })
}

// ---- 上传流程 -------------------------------------------------------------

async function uploadArchive(
  task: UploadTask,
  runtime: Runtime,
  resume: boolean
) {
  const file = runtime.archive
  if (!file) {
    throw new UploadStateError("文件已释放，请重新导入")
  }
  const { signal } = runtime.controller
  const path = importPath(task.datasetId, task.importId)

  let offset = resume
    ? (await fetchUploadingJob(task, signal)).received_bytes
    : task.job.received_bytes
  let mismatches = 0

  while (offset < file.size) {
    const { start, end } = nextChunk(offset, file.size)
    let job: DatasetImport
    try {
      job = await withRetry(
        () =>
          apiUpload<DatasetImport>(
            "PUT",
            `${path}/archive?offset=${start}`,
            file.slice(start, end),
            {
              signal,
              contentType: "application/octet-stream",
              onProgress: (loaded) =>
                reportProgress(
                  task.id,
                  runtime,
                  start + Math.min(loaded, end - start),
                  null,
                  false
                ),
            }
          ),
        signal
      )
    } catch (error) {
      // 服务端已接收的字节数与本地不一致（例如上一次分块的响应丢失）：
      // 以服务端为准继续。
      if (
        error instanceof ApiError &&
        error.code === "upload_offset_mismatch" &&
        mismatches < MAX_RETRIES
      ) {
        mismatches += 1
        offset = (await fetchUploadingJob(task, signal)).received_bytes
        continue
      }
      throw error
    }

    if (job.received_bytes <= start || job.received_bytes > file.size) {
      throw new UploadStateError("服务器接收的数据与文件大小不一致，请重新导入")
    }
    offset = job.received_bytes
    mismatches = 0
    reportProgress(task.id, runtime, offset, null, true)
  }
}

async function uploadFolder(
  task: UploadTask,
  runtime: Runtime,
  resume: boolean
) {
  const files = runtime.files
  if (!files) {
    throw new UploadStateError("文件已释放，请重新导入")
  }
  const { signal } = runtime.controller
  const path = `${importPath(task.datasetId, task.importId)}/files`

  if (resume) {
    await fetchUploadingJob(task, signal)
  }

  // 分批结果是确定的：重试时同一序号对应同一批文件。
  const batches = planBatches(files)
  const batchBytes = batches.map((batch) =>
    batch.reduce((sum, file) => sum + file.size, 0)
  )
  const pending: number[] = []
  let doneBytes = 0
  let doneFiles = 0
  batches.forEach((batch, index) => {
    if (runtime.completedBatches.has(index)) {
      doneBytes += batchBytes[index]
      doneFiles += batch.length
    } else {
      pending.push(index)
    }
  })

  // 进行中的批次已上传的字节数（按请求体进度折算到文件字节）。
  const inFlight = new Map<number, number>()
  const report = (force: boolean) => {
    let bytes = doneBytes
    for (const value of inFlight.values()) {
      bytes += value
    }
    reportProgress(task.id, runtime, bytes, doneFiles, force)
  }

  let cursor = 0
  const worker = async () => {
    while (cursor < pending.length && !signal.aborted) {
      const index = pending[cursor]
      cursor += 1
      const batch = batches[index]
      await withRetry(() => {
        const form = new FormData()
        for (const entry of batch) {
          // 服务端按 path 字段确定位置：每个文件前紧跟它的相对路径。
          form.append("path", entry.path)
          form.append("file", entry.file, baseName(entry.path))
        }
        inFlight.set(index, 0)
        return apiUpload<DatasetImport>("POST", path, form, {
          signal,
          onProgress: (loaded, total) => {
            const ratio = total > 0 ? Math.min(1, loaded / total) : 0
            inFlight.set(index, ratio * batchBytes[index])
            report(false)
          },
        })
      }, signal)
      inFlight.delete(index)
      runtime.completedBatches.add(index)
      doneBytes += batchBytes[index]
      doneFiles += batch.length
      report(true)
    }
  }

  try {
    await Promise.all(
      Array.from(
        { length: Math.min(FOLDER_CONCURRENCY, pending.length) },
        worker
      )
    )
  } catch (error) {
    // 一个批次彻底失败：停止其它并发的批次。
    runtime.controller.abort()
    throw error
  }
}

/** 提交上传；提交请求的响应丢失后重试会得到 409，此时以服务端任务状态为准。 */
async function commit(task: UploadTask, signal: AbortSignal) {
  try {
    return await withRetry(
      () => commitImport(task.datasetId, task.importId),
      signal
    )
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      const job = await apiGet<DatasetImport>(
        importPath(task.datasetId, task.importId),
        { signal }
      )
      if (job.status !== "uploading" && job.status !== "canceled") {
        return job
      }
    }
    throw error
  }
}

function releaseFiles(runtime: Runtime) {
  runtime.archive = null
  runtime.files = null
}

function finishProcessing(id: string, job: DatasetImport) {
  const task = getTask(id)
  if (!task || task.status !== "processing") {
    return
  }
  patchTask(id, {
    job,
    status:
      job.status === "succeeded"
        ? "done"
        : job.status === "failed"
          ? "error"
          : "canceled",
    error: job.status === "failed" ? job.error || "导入失败" : "",
    retryable: false,
  })
}

/** 跟随服务端处理进度直到任务结束（结果提示由 import-watch 统一弹出）。 */
function observeProcessing(id: string, runtime: Runtime) {
  const task = getTask(id)
  if (!task) {
    return
  }
  runtime.stopObserving = watchImport(task.job, task.datasetName, (event) => {
    if ("error" in event) {
      runtime.stopObserving?.()
      runtime.stopObserving = null
      patchTask(id, {
        status: "error",
        error: describeError(event.error),
        retryable: false,
      })
    } else if (isActiveImport(event.job)) {
      patchTask(id, { job: event.job })
    } else {
      runtime.stopObserving?.()
      runtime.stopObserving = null
      finishProcessing(id, event.job)
    }
  })
}

async function run(id: string, resume: boolean) {
  const runtime = runtimes.get(id)
  const task = getTask(id)
  if (!runtime || !task) {
    return
  }
  const { signal } = runtime.controller

  try {
    if (task.kind === "archive") {
      await uploadArchive(task, runtime, resume)
    } else {
      await uploadFolder(task, runtime, resume)
    }
    if (runtime.canceled) {
      return
    }

    patchTask(id, {
      status: "committing",
      uploadedBytes: task.totalBytes,
      uploadedFiles: task.totalFiles,
      speed: 0,
    })
    const job = await commit(task, signal)
    if (runtime.canceled) {
      return
    }

    releaseFiles(runtime)
    patchTask(id, { status: "processing", job })
    refreshStartedImport(task.datasetId)
    observeProcessing(id, runtime)
  } catch (error) {
    // 用户取消时请求以 AbortError 结束，状态已由 cancelUpload 设置。
    if (runtime.canceled) {
      return
    }
    patchTask(id, {
      status: "error",
      error: describeError(error),
      retryable: runtime.archive !== null || runtime.files !== null,
      speed: 0,
    })
  }
}

function createRuntime(
  archive: File | null,
  files: PickedFile[] | null
): Runtime {
  return {
    controller: new AbortController(),
    canceled: false,
    archive,
    files,
    completedBatches: new Set(),
    stopObserving: null,
    sample: { time: performance.now(), bytes: 0 },
    lastEmit: 0,
  }
}

function addTask(task: UploadTask, runtime: Runtime) {
  runtimes.set(task.id, runtime)
  setTasks([task, ...tasks.filter((existing) => existing.id !== task.id)])
  void run(task.id, false)
}

type StartOptions = {
  dataset: Pick<Dataset, "id" | "name">
  /** `POST .../imports` 刚创建的任务（status `uploading`）。 */
  job: DatasetImport
}

function baseTask(
  { dataset, job }: StartOptions,
  kind: UploadKind
): UploadTask {
  return {
    id: `${dataset.id}:${job.id}`,
    datasetId: dataset.id,
    datasetName: dataset.name,
    importId: job.id,
    kind,
    fileName: job.file_name,
    totalBytes: 0,
    totalFiles: 0,
    uploadedBytes: 0,
    uploadedFiles: 0,
    speed: 0,
    status: "uploading",
    error: "",
    retryable: false,
    job,
    createdAt: Date.now(),
  }
}

export function startArchiveUpload(options: StartOptions & { file: File }) {
  const task: UploadTask = {
    ...baseTask(options, "archive"),
    fileName: options.file.name,
    totalBytes: options.file.size,
    totalFiles: 1,
    uploadedBytes: options.job.received_bytes,
  }
  addTask(task, createRuntime(options.file, null))
  return task.id
}

export function startFolderUpload(
  options: StartOptions & { files: PickedFile[] }
) {
  const task: UploadTask = {
    ...baseTask(options, "folder"),
    totalBytes: options.files.reduce((sum, file) => sum + file.size, 0),
    totalFiles: options.files.length,
  }
  addTask(task, createRuntime(null, options.files))
  return task.id
}

/** 取消：中止进行中的请求，并通知服务端取消任务（清理已上传的临时文件）。 */
export async function cancelUpload(id: string) {
  const task = getTask(id)
  const runtime = runtimes.get(id)
  if (!task || !runtime || !isUploadRunning(task)) {
    return
  }
  runtime.canceled = true
  runtime.controller.abort()
  runtime.stopObserving?.()
  runtime.stopObserving = null
  releaseFiles(runtime)
  patchTask(id, { status: "canceled", speed: 0, retryable: false })

  try {
    const job = await cancelImport(task.datasetId, task.importId)
    patchTask(id, { job })
  } catch {
    // 任务可能恰好已经结束（409），或网络不可用：服务端会在超时后自动清理
    // 未完成的上传；已结束的任务以导入记录为准。
  }
  void queryClient.invalidateQueries({ queryKey: datasetKeys.all })
}

/** 上传 / 提交失败后从断点继续。 */
export function retryUpload(id: string) {
  const task = getTask(id)
  const runtime = runtimes.get(id)
  if (!task || !runtime || task.status !== "error" || !task.retryable) {
    return
  }
  runtime.controller = new AbortController()
  runtime.canceled = false
  runtime.sample = { time: performance.now(), bytes: task.uploadedBytes }
  patchTask(id, { status: "uploading", error: "", retryable: false })
  void run(id, true)
}

/**
 * 放弃上传或提交失败的任务：服务端任务如果仍在等待上传，就取消它，释放已上传
 * 的临时文件，数据集也不再显示“上传中”。先确认状态，因为提交请求的响应丢失时
 * 任务可能已经开始处理。尽力而为：失败时服务端会在 24 小时后自动取消。
 */
async function abandonUpload(task: UploadTask) {
  try {
    const job = await apiGet<DatasetImport>(
      importPath(task.datasetId, task.importId)
    )
    if (job.status === "uploading") {
      await cancelImport(task.datasetId, task.importId)
    }
  } catch {
    // 网络不可用、任务或数据集已被删除等。
  }
  void queryClient.invalidateQueries({ queryKey: datasetKeys.all })
}

/** 失败后还可以继续上传的任务：从面板中移除即放弃该导入。 */
export function isAbandonable(task: Pick<UploadTask, "status" | "retryable">) {
  return task.status === "error" && task.retryable
}

function removeTasks(shouldRemove: (task: UploadTask) => boolean) {
  const removed = tasks.filter(
    (task) => !isUploadRunning(task) && shouldRemove(task)
  )
  if (removed.length === 0) {
    return
  }
  for (const task of removed) {
    runtimes.delete(task.id)
    if (isAbandonable(task)) {
      void abandonUpload(task)
    }
  }
  setTasks(tasks.filter((task) => !removed.includes(task)))
}

/** 从面板中移除已结束的任务（上传失败的任务同时取消服务端的导入）。 */
export function dismissUpload(id: string) {
  removeTasks((task) => task.id === id)
}

export function dismissFinishedUploads() {
  removeTasks(() => true)
}

/** 当前全部上传任务（在 React 之外读取，如测试）。 */
export function getUploadTasks() {
  return tasks
}

/** 全部上传任务，新的在前。 */
export function useUploadTasks() {
  return useSyncExternalStore(subscribe, getSnapshot)
}

/** 该数据集最近一个仍在进行（上传、提交或服务端处理中）的任务。 */
export function useActiveUploadTask(datasetId: number) {
  return useSyncExternalStore(subscribe, () =>
    tasks.find((task) => task.datasetId === datasetId && isUploadRunning(task))
  )
}
