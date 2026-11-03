/**
 * 模型上传管理器：模块级的状态 + useSyncExternalStore，上传不随页面切换中断
 * （做法与数据集的 features/datasets/upload-manager.ts 相同，进度显示在同一个
 * 上传面板中）。
 *
 * 一个任务对应服务端的一个上传（ModelUpload）：
 *   uploading（8 MiB 分块顺序 PUT）→ committing（提交：服务端计算 sha256、解析
 *   模型文件、分配版本号）→ done / mismatch / error / canceled。
 *
 * - 分块失败自动重试；服务端返回 `upload_offset_mismatch` 时重新获取上传，从
 *   `received_bytes` 续传。上传或提交失败后可以从断点继续。
 * - mismatch：文件中的 YOLO 版本与所选的不同。服务端保留已上传的文件，改选版本
 *   后再次提交即可，不需要重新上传。
 * - invalid_model：文件不是平台支持的 YOLO 模型，服务端已删除临时文件，不能重试。
 * - 上传、提交或等待改选版本时关闭或刷新页面会弹出浏览器的离开确认。
 */
import { useEffect, useSyncExternalStore } from "react"
import { toast } from "sonner"

import {
  cancelModelUpload,
  commitModelUpload,
  getModelUpload,
  modelKeys,
  modelPath,
  refreshModels,
  uploadModelChunk,
  type Model,
  type ModelUpload,
  type YoloVersion,
} from "@/features/models/api"
import { yoloVersionLabel } from "@/features/models/yolo-versions"
import {
  MAX_RETRIES,
  nextChunk,
  smoothSpeed,
} from "@/features/datasets/upload-plan"
import { withRetry } from "@/features/datasets/upload-retry"
import { ApiError, apiGet } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type ModelUploadStatus =
  "uploading" | "committing" | "mismatch" | "done" | "error" | "canceled"

export type ModelUploadTask = {
  /** `model:${uploadId}`，与数据集上传任务的 id 不会重复。 */
  id: string
  uploadId: number
  projectId: number
  projectName: string
  fileName: string
  totalBytes: number
  uploadedBytes: number
  /** 平滑后的上传速度（字节/秒），0 表示未知。 */
  speed: number
  status: ModelUploadStatus
  /** 提交时使用的版本（版本不一致后改选时随之更新）。 */
  yoloVersion: YoloVersion
  /** mismatch：文件中识别出的版本；没能读取到时为 null。 */
  detectedVersion: YoloVersion | null
  /** 失败或版本不一致的说明（中文），否则为 `""`。 */
  error: string
  /** 失败时服务端的错误码（如 `invalid_model`），否则为 `""`。 */
  errorCode: string
  /** 上传或提交失败、可以从断点继续时为 true。 */
  retryable: boolean
  /** done：创建的模型。 */
  model: Model | null
  createdAt: number
}

type Runtime = {
  controller: AbortController
  canceled: boolean
  /** 任务结束后释放，不再持有 File 引用。 */
  file: File | null
  sample: { time: number; bytes: number }
  lastEmit: number
}

const RUNNING: ReadonlySet<ModelUploadStatus> = new Set([
  "uploading",
  "committing",
])

/** 正在上传或提交。 */
export function isModelUploadRunning(task: Pick<ModelUploadTask, "status">) {
  return RUNNING.has(task.status)
}

/** 还没有结束：正在上传、提交，或等待改选版本（服务端仍保留着文件）。 */
export function isModelUploadActive(task: Pick<ModelUploadTask, "status">) {
  return isModelUploadRunning(task) || task.status === "mismatch"
}

let tasks: readonly ModelUploadTask[] = []
const listeners = new Set<() => void>()
const runtimes = new Map<string, Runtime>()
/** 正在被界面（上传对话框）显示的任务：结果由界面展示，不再弹出提示。 */
const watched = new Map<string, number>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function getSnapshot() {
  return tasks
}

function setTasks(next: readonly ModelUploadTask[]) {
  tasks = next
  syncUnloadGuard()
  for (const listener of listeners) {
    listener()
  }
}

function getTask(id: string) {
  return tasks.find((task) => task.id === id)
}

function patchTask(id: string, changes: Partial<ModelUploadTask>) {
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
  const active = tasks.some(isModelUploadActive)
  if (active && !unloadGuardInstalled) {
    window.addEventListener("beforeunload", onBeforeUnload)
    unloadGuardInstalled = true
  } else if (!active && unloadGuardInstalled) {
    window.removeEventListener("beforeunload", onBeforeUnload)
    unloadGuardInstalled = false
  }
}

// ---- 请求辅助 -------------------------------------------------------------

/** 上传已经不能继续（被取消、失败或已不存在），重试也没有用。 */
class UploadStateError extends Error {}

/** 重新获取上传，确认它还能继续（可能已被取消、超时清理，或已经提交成功）。 */
async function fetchResumableUpload(
  task: ModelUploadTask,
  signal: AbortSignal
) {
  const upload = await withRetry(
    () => getModelUpload(task.uploadId, { signal }),
    signal
  )
  if (upload.status === "canceled") {
    throw new UploadStateError(upload.error || "上传已被取消，请重新上传")
  }
  if (upload.status === "failed") {
    throw new UploadStateError(upload.error || "上传已失败，请重新上传")
  }
  return upload
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
  patchTask(id, { uploadedBytes, speed })
}

// ---- 上传流程 -------------------------------------------------------------

/** 从 `offset` 开始顺序上传剩余的分块。 */
async function uploadChunks(id: string, runtime: Runtime, from: number) {
  const task = getTask(id)
  if (!task) {
    return
  }
  const { signal } = runtime.controller
  const size = task.totalBytes
  let offset = from
  let mismatches = 0

  if (offset < size && !runtime.file) {
    throw new UploadStateError("文件已释放，请重新上传")
  }
  if (offset < size) {
    patchTask(id, { status: "uploading", uploadedBytes: offset })
  }

  while (offset < size) {
    const file = runtime.file
    if (!file) {
      throw new UploadStateError("文件已释放，请重新上传")
    }
    const { start, end } = nextChunk(offset, size)
    let upload: ModelUpload
    try {
      upload = await withRetry(
        () =>
          uploadModelChunk(task.uploadId, start, file.slice(start, end), {
            signal,
            onProgress: (loaded) =>
              reportProgress(
                id,
                runtime,
                start + Math.min(loaded, end - start),
                false
              ),
          }),
        signal
      )
    } catch (error) {
      // 服务端已接收的字节数与本地不一致（例如上一个分块的响应丢失）：
      // 以服务端为准继续。
      if (
        error instanceof ApiError &&
        error.code === "upload_offset_mismatch" &&
        mismatches < MAX_RETRIES
      ) {
        mismatches += 1
        const current = await fetchResumableUpload(task, signal)
        if (current.status === "completed") {
          return
        }
        offset = current.received_bytes
        continue
      }
      throw error
    }

    if (upload.received_bytes <= start || upload.received_bytes > size) {
      throw new UploadStateError("服务器接收的数据与文件大小不一致，请重新上传")
    }
    offset = upload.received_bytes
    mismatches = 0
    reportProgress(id, runtime, offset, true)
  }
}

function releaseFile(runtime: Runtime) {
  runtime.file = null
}

function isWatched(id: string) {
  return (watched.get(id) ?? 0) > 0
}

function complete(id: string, runtime: Runtime, model: Model) {
  const task = getTask(id)
  if (!task) {
    return
  }
  releaseFile(runtime)
  queryClient.setQueryData(modelKeys.detail(model.id), model)
  refreshModels()
  patchTask(id, {
    status: "done",
    model,
    uploadedBytes: task.totalBytes,
    speed: 0,
    error: "",
    errorCode: "",
    retryable: false,
  })
  if (!isWatched(id)) {
    toast.success(`已上传为 v${model.version}`, {
      description: `${model.project.name} · ${task.fileName}`,
    })
  }
}

/** 版本不一致：服务端保留了文件并记下了识别出的版本，读取它以便直接改选。 */
async function enterMismatch(id: string, runtime: Runtime, error: ApiError) {
  const task = getTask(id)
  if (!task) {
    return
  }
  let detected: YoloVersion | null = null
  const { signal } = runtime.controller
  try {
    const upload = await withRetry(
      () => getModelUpload(task.uploadId, { signal }),
      signal
    )
    detected =
      upload.detected_yolo_version !== upload.yolo_version
        ? upload.detected_yolo_version
        : null
  } catch {
    // 没能读取到时由用户自己选择版本。
  }
  if (runtime.canceled) {
    return
  }
  patchTask(id, {
    status: "mismatch",
    detectedVersion: detected,
    error: error.message,
    errorCode: error.code,
    retryable: false,
    speed: 0,
  })
  if (!isWatched(id)) {
    toast.warning(`“${task.fileName}”的 YOLO 版本与所选的不同`, {
      description: detected
        ? `文件中的模型是 ${yoloVersionLabel(detected)}，可在上传面板中改选后保存。`
        : error.message,
      duration: 15_000,
    })
  }
}

function fail(id: string, runtime: Runtime, error: unknown) {
  const task = getTask(id)
  if (!task) {
    return
  }
  const code = error instanceof ApiError ? error.code : ""
  // 文件不是可用的模型（服务端已删除临时文件），或上传已经不存在 / 不能继续。
  const permanent =
    error instanceof UploadStateError ||
    code === "invalid_model" ||
    (error instanceof ApiError && error.status === 404)
  if (permanent) {
    releaseFile(runtime)
  }
  patchTask(id, {
    status: "error",
    error: describeError(error),
    errorCode: code,
    retryable: !permanent && runtime.file !== null,
    speed: 0,
  })
  if (!isWatched(id)) {
    toast.error(`“${task.fileName}”上传失败`, {
      description: describeError(error),
      duration: 10_000,
    })
  }
}

async function run(id: string, resume: boolean) {
  const runtime = runtimes.get(id)
  const initial = getTask(id)
  if (!runtime || !initial) {
    return
  }
  const { signal } = runtime.controller

  try {
    // 继续时以服务端的进度为准；提交的响应丢失时上传可能已经完成。
    const upload = resume ? await fetchResumableUpload(initial, signal) : null
    if (upload?.status !== "completed") {
      await uploadChunks(id, runtime, upload?.received_bytes ?? 0)
    }
    if (runtime.canceled) {
      return
    }

    patchTask(id, {
      status: "committing",
      uploadedBytes: initial.totalBytes,
      speed: 0,
    })
    // 重复提交已完成的上传返回同一个模型，所以提交可以放心重试。
    const yoloVersion = getTask(id)?.yoloVersion ?? initial.yoloVersion
    const model = await withRetry(
      () =>
        commitModelUpload(
          initial.uploadId,
          { yolo_version: yoloVersion },
          { signal }
        ),
      signal
    )
    if (runtime.canceled) {
      return
    }
    complete(id, runtime, model)
  } catch (error) {
    // 用户取消时请求以 AbortError 结束，状态已由 cancelModelUploadTask 设置。
    if (runtime.canceled) {
      return
    }
    if (error instanceof ApiError && error.code === "yolo_version_mismatch") {
      await enterMismatch(id, runtime, error)
      return
    }
    fail(id, runtime, error)
  }
}

type StartOptions = {
  project: { id: number; name: string }
  /** `POST /api/v1/models/uploads` 刚创建的上传（status `uploading`）。 */
  upload: ModelUpload
  file: File
}

/** 开始上传，返回任务 id（用于在对话框中跟随进度）。 */
export function startModelUpload({ project, upload, file }: StartOptions) {
  const task: ModelUploadTask = {
    id: `model:${upload.id}`,
    uploadId: upload.id,
    projectId: project.id,
    projectName: project.name,
    fileName: upload.file_name || file.name,
    totalBytes: file.size,
    uploadedBytes: upload.received_bytes,
    speed: 0,
    status: "uploading",
    yoloVersion: upload.yolo_version,
    detectedVersion: null,
    error: "",
    errorCode: "",
    retryable: false,
    model: null,
    createdAt: Date.now(),
  }
  runtimes.set(task.id, {
    controller: new AbortController(),
    canceled: false,
    file,
    sample: { time: performance.now(), bytes: upload.received_bytes },
    lastEmit: 0,
  })
  setTasks([task, ...tasks.filter((existing) => existing.id !== task.id)])
  void run(task.id, upload.received_bytes > 0)
  return task.id
}

/**
 * 取消：中止进行中的请求，并通知服务端取消上传（删除已上传的临时文件）。
 * 提交恰好在取消之前完成时，模型已经创建，任务改为完成。
 */
export async function cancelModelUploadTask(id: string) {
  const task = getTask(id)
  const runtime = runtimes.get(id)
  if (!task || !runtime || !isModelUploadActive(task)) {
    return
  }
  runtime.canceled = true
  runtime.controller.abort()
  releaseFile(runtime)
  patchTask(id, { status: "canceled", speed: 0, retryable: false })

  try {
    const upload = await cancelModelUpload(task.uploadId)
    if (upload.status === "completed" && upload.model_id !== null) {
      const model = await apiGet<Model>(modelPath(upload.model_id))
      queryClient.setQueryData(modelKeys.detail(model.id), model)
      refreshModels()
      patchTask(id, { status: "done", model })
      toast.info(`取消前已经保存为 v${model.version}`, {
        description: "如果不需要这个模型，可以在模型仓库中删除。",
      })
    }
  } catch {
    // 网络不可用、上传已被删除等：服务端会在 24 小时后自动清理未完成的上传。
  }
}

/** 上传 / 提交失败后从断点继续。 */
export function retryModelUpload(id: string) {
  const task = getTask(id)
  const runtime = runtimes.get(id)
  if (!task || !runtime || task.status !== "error" || !task.retryable) {
    return
  }
  runtime.controller = new AbortController()
  runtime.canceled = false
  runtime.sample = { time: performance.now(), bytes: task.uploadedBytes }
  patchTask(id, {
    status: "uploading",
    error: "",
    errorCode: "",
    retryable: false,
  })
  void run(id, true)
}

/** 版本不一致时改选版本，再次提交（不需要重新上传文件）。 */
export function commitWithYoloVersion(id: string, yoloVersion: YoloVersion) {
  const task = getTask(id)
  const runtime = runtimes.get(id)
  if (!task || !runtime || task.status !== "mismatch") {
    return
  }
  runtime.controller = new AbortController()
  runtime.canceled = false
  patchTask(id, {
    status: "committing",
    yoloVersion,
    detectedVersion: null,
    error: "",
    errorCode: "",
  })
  void run(id, true)
}

/**
 * 放弃失败的上传：服务端的上传如果仍在等待数据，就取消它，释放已上传的临时
 * 文件。尽力而为：失败时服务端会在 24 小时后自动清理。
 */
async function abandonUpload(task: ModelUploadTask) {
  try {
    const upload = await getModelUpload(task.uploadId)
    if (upload.status === "uploading") {
      await cancelModelUpload(task.uploadId)
    } else if (upload.status === "completed") {
      // 提交其实成功了，只是响应丢失：模型已在仓库中。
      refreshModels()
    }
  } catch {
    // 网络不可用、上传或项目已被删除等。
  }
}

/** 失败后还可以继续上传的任务：从面板中移除即放弃该上传。 */
export function isAbandonable(
  task: Pick<ModelUploadTask, "status" | "retryable">
) {
  return task.status === "error" && task.retryable
}

function removeTasks(shouldRemove: (task: ModelUploadTask) => boolean) {
  const removed = tasks.filter(
    (task) => !isModelUploadActive(task) && shouldRemove(task)
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

/** 从面板中移除已结束的任务（失败的上传同时取消服务端的上传）。 */
export function dismissModelUpload(id: string) {
  removeTasks((task) => task.id === id)
}

export function dismissFinishedModelUploads() {
  removeTasks(() => true)
}

/** 当前全部上传任务（在 React 之外读取，如测试）。 */
export function getModelUploadTasks() {
  return tasks
}

/** 全部模型上传任务，新的在前。 */
export function useModelUploadTasks() {
  return useSyncExternalStore(subscribe, getSnapshot)
}

/**
 * 跟随一个任务（上传对话框使用）。显示期间任务的结果由界面展示，管理器不再
 * 弹出提示；对话框关闭后上传在后台继续，结果照常提示。
 */
export function useWatchedModelUpload(id: string | null) {
  useEffect(() => {
    if (id === null) {
      return undefined
    }
    watched.set(id, (watched.get(id) ?? 0) + 1)
    return () => {
      const count = (watched.get(id) ?? 1) - 1
      if (count > 0) {
        watched.set(id, count)
      } else {
        watched.delete(id)
      }
    }
  }, [id])
  return useSyncExternalStore(subscribe, () =>
    id === null ? undefined : getTask(id)
  )
}
