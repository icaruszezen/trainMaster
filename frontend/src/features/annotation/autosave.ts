/**
 * 当前图片标注的保存：停止编辑片刻后自动保存，切换图片前立即保存（flush），
 * 并处理“他人同时修改了这张图片”的冲突。与界面无关，便于测试。
 */
import type { AnnotationObject } from "@/features/datasets/api"
import { serializeObjects } from "@/features/annotation/document"
import { ApiError } from "@/lib/api"

/** 停止编辑多久后自动保存（毫秒）。 */
export const AUTOSAVE_DELAY = 800

export type SaveStatus =
  /** 与文件一致，没有修改。 */
  | "clean"
  /** 有修改，等待保存。 */
  | "dirty"
  | "saving"
  /** 刚刚保存成功（之后没有新的修改）。 */
  | "saved"
  /** 保存失败（网络、校验等），下次修改或手动保存时重试。 */
  | "error"
  /** 文件已被他人修改：需要用户选择重新加载或覆盖，暂停自动保存。 */
  | "conflict"

export type SaverState = {
  status: SaveStatus
  /** `error` 时的说明。 */
  error: string | null
  /** `error` 时接口返回的错误码（如 `annotation_unpatchable`），其它错误为空。 */
  code?: string
}

export type SaveRequest<C> = {
  objects: AnnotationObject[]
  baseVersion: string
  /** 不检查版本（冲突时用户选择了“覆盖”）。 */
  force: boolean
  /**
   * 文件无法在保留其它内容的情况下修改时，允许按平台的格式重新生成（用户选择了
   * “仍然保存”，只在这时带上）。与 force 相互独立：不带 force 时仍然检查版本。
   */
  regenerate?: boolean
  /** reset() 时给出的上下文（如哪张图片），保存进行中切换了图片也不会写错。 */
  context: C
}

/** 保存函数：成功时返回文件的新版本；失败时抛出错误（冲突为 `annotation_conflict`）。 */
export type SaveFunction<C> = (request: SaveRequest<C>) => Promise<string>

export const ANNOTATION_CONFLICT = "annotation_conflict"

/** 已有的标注文件无法在保留其它内容的情况下修改，只能选择重新生成（regenerate）。 */
export const ANNOTATION_UNPATCHABLE = "annotation_unpatchable"

export function isConflictError(error: unknown) {
  return error instanceof ApiError && error.code === ANNOTATION_CONFLICT
}

type SaverOptions<C> = {
  save: SaveFunction<C>
  onChange: (state: SaverState) => void
  delay?: number
  autosave?: boolean
}

export class AnnotationSaver<C> {
  private readonly saveFn: SaveFunction<C>
  private readonly onChange: (state: SaverState) => void
  private readonly delay: number
  private autosave: boolean

  /** 文件当前内容（已保存的框列表）的规范文本与版本。 */
  private baseline = "[]"
  private version = ""
  /** 编辑器中当前的框列表。 */
  private current: AnnotationObject[] = []
  private currentText = "[]"
  private context: C | null = null
  /**
   * 要求即使没有修改也写入一次文件（{@link saveNow}，如“标为空标注”），写入
   * 成功前算作未保存的修改。
   */
  private forced = false
  /**
   * 最近一次因 `annotation_unpatchable` 被拒绝的保存是否带了 force（冲突后选择
   * 了“覆盖”），{@link regenerate} 用同样的 force 重新发送。
   */
  private unpatchableForce = false

  private timer: ReturnType<typeof setTimeout> | null = null
  private inflight: Promise<boolean> | null = null
  private state: SaverState = { status: "clean", error: null }
  private disposed = false
  /** reset() 之后递增，丢弃上一张图片还在进行中的保存的结果。 */
  private generation = 0

  constructor(options: SaverOptions<C>) {
    this.saveFn = options.save
    this.onChange = options.onChange
    this.delay = options.delay ?? AUTOSAVE_DELAY
    this.autosave = options.autosave ?? true
  }

  getState() {
    return this.state
  }

  getVersion() {
    return this.version
  }

  /** 有尚未写入文件的修改（包括正在保存的）。 */
  isDirty() {
    return this.forced || this.currentText !== this.baseline
  }

  getContext() {
    return this.context
  }

  /**
   * 离开这张图片前需要先 flush()：有未保存的修改、正在保存，或保存失败 /
   * 冲突还没有处理。
   */
  hasPendingWork() {
    const { status } = this.state
    return (
      this.isDirty() ||
      status === "saving" ||
      status === "error" ||
      status === "conflict"
    )
  }

  /**
   * 开始编辑一张图片（或重新加载）：`objects` 是文件中的框，`version` 是其
   * 版本。上一张图片还在进行中的保存照常完成，但结果不再影响状态。
   */
  reset(objects: readonly AnnotationObject[], version: string, context: C) {
    this.generation += 1
    this.clearTimer()
    this.inflight = null
    this.forced = false
    this.unpatchableForce = false
    this.baseline = serializeObjects(objects)
    this.currentText = this.baseline
    this.current = [...objects]
    this.version = version
    this.context = context
    this.setState({ status: "clean", error: null })
  }

  /**
   * 放弃未保存的修改（用户选择“放弃修改并离开”）：之后的 flush() 不再保存
   * 它们；进行中的保存照常完成，但结果不再影响状态。
   */
  discard() {
    this.generation += 1
    this.clearTimer()
    this.inflight = null
    this.forced = false
    this.baseline = this.currentText
    this.setState({ status: "clean", error: null })
  }

  /** 尚未保存的修改（页面关闭时用 keepalive 请求补发），没有时为 null。 */
  pending(): SaveRequest<C> | null {
    if (!this.isDirty() || this.context === null) {
      return null
    }
    if (this.state.status === "conflict") {
      return null
    }
    return {
      objects: this.current,
      baseVersion: this.version,
      force: false,
      context: this.context,
    }
  }

  /** 编辑器中的框列表变了。 */
  update(objects: readonly AnnotationObject[]) {
    this.current = [...objects]
    this.currentText = serializeObjects(objects)
    if (this.state.status === "conflict") {
      return // 等待用户处理冲突
    }
    if (!this.isDirty()) {
      this.clearTimer()
      if (this.state.status !== "saving") {
        this.setState({
          status: this.state.status === "saved" ? "saved" : "clean",
          error: null,
        })
      }
      return
    }
    if (this.state.status !== "saving") {
      this.setState({ status: "dirty", error: null })
    }
    this.schedule()
  }

  setAutosave(enabled: boolean) {
    this.autosave = enabled
    if (enabled) {
      this.schedule()
    } else {
      this.clearTimer()
    }
  }

  /**
   * 立即保存未保存的修改。修改都已写入文件时返回 true；保存失败、存在冲突时
   * 返回 false（状态中有原因）。
   */
  async flush(): Promise<boolean> {
    this.clearTimer()
    const generation = this.generation
    // 保存期间可能又有修改，最多再保存几轮。
    for (let round = 0; round < 5; round += 1) {
      // 同时有多个 flush() 时，等待期间别的 flush() 可能已开始下一次保存。
      while (this.inflight) {
        await this.inflight
      }
      if (generation !== this.generation) {
        return false // 已经切换到别的图片
      }
      if (this.state.status === "conflict") {
        return false
      }
      if (!this.isDirty()) {
        return true
      }
      const ok = await this.run(false)
      if (ok) {
        return true
      }
      if (this.state.status !== "dirty") {
        return false // 保存失败或冲突
      }
    }
    return !this.isDirty()
  }

  /**
   * 即使没有修改也立即写入编辑器中的框（如“标为空标注”：调用前在上下文中
   * 标明，再写入没有目标的文件）。先等待进行中的保存；与普通修改一样，写入
   * 成功前算作未保存（flush() 会等待并重试它，关闭页面时补发）。存在冲突时
   * 不写入，返回 false。
   */
  async saveNow(): Promise<boolean> {
    if (this.context === null || this.state.status === "conflict") {
      return false
    }
    this.forced = true
    if (this.state.status !== "saving") {
      this.setState({ status: "dirty", error: null })
    }
    return this.flush()
  }

  /**
   * 冲突时选择“覆盖”：不再检查版本，用编辑器中的框覆盖文件。覆盖不包括重新
   * 生成文件：文件无法在保留其它内容的情况下修改时同样失败（`annotation_unpatchable`），
   * 由用户再选择 {@link regenerate}。
   */
  async overwrite(): Promise<boolean> {
    this.clearTimer()
    if (this.inflight) {
      await this.inflight
    }
    return this.run(true)
  }

  /**
   * 文件无法在保留其它内容的情况下修改（`annotation_unpatchable`）时选择“仍然
   * 保存”：允许按平台的格式重新生成整个文件。force 与被拒绝的那次保存相同：
   * 普通保存仍然检查版本，提示期间文件被他人修改时进入冲突，不会覆盖别人的修改；
   * 冲突后选择的覆盖则继续覆盖。
   */
  async regenerate(): Promise<boolean> {
    this.clearTimer()
    if (this.inflight) {
      await this.inflight
    }
    return this.run(this.unpatchableForce, true)
  }

  dispose() {
    this.disposed = true
    this.clearTimer()
  }

  private schedule() {
    this.clearTimer()
    if (!this.autosave || this.disposed || !this.isDirty()) {
      return
    }
    this.timer = setTimeout(() => {
      this.timer = null
      if (
        this.inflight ||
        this.state.status === "conflict" ||
        !this.isDirty() // 已由 flush() 保存
      ) {
        return // 正在保存：保存完成后会再检查一次
      }
      void this.run(false)
    }, this.delay)
  }

  private clearTimer() {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
  }

  private run(force: boolean, regenerate = false): Promise<boolean> {
    const generation = this.generation
    const objects = this.current
    const text = this.currentText
    const forced = this.forced
    const context = this.context
    if (context === null) {
      return Promise.resolve(false) // 还没有 reset()
    }
    this.setState({ status: "saving", error: null })
    const promise = this.saveFn({
      objects,
      baseVersion: this.version,
      force,
      ...(regenerate ? { regenerate } : {}),
      context,
    }).then(
      (version) => {
        if (generation !== this.generation) {
          return false
        }
        this.inflight = null
        this.version = version
        this.baseline = text
        if (forced) {
          this.forced = false
        }
        if (this.isDirty()) {
          // 保存期间又有修改：继续保存。
          this.setState({ status: "dirty", error: null })
          this.schedule()
          return false
        }
        this.setState({ status: "saved", error: null })
        return true
      },
      (error: unknown) => {
        if (generation !== this.generation) {
          return false
        }
        this.inflight = null
        if (isConflictError(error)) {
          this.setState({ status: "conflict", error: null })
        } else {
          if (
            error instanceof ApiError &&
            error.code === ANNOTATION_UNPATCHABLE
          ) {
            this.unpatchableForce = force
          }
          this.setState({
            status: "error",
            error: error instanceof Error ? error.message : "保存失败",
            code: error instanceof ApiError ? error.code : undefined,
          })
        }
        return false
      }
    )
    this.inflight = promise
    return promise
  }

  private setState(state: SaverState) {
    if (
      state.status === this.state.status &&
      state.error === this.state.error &&
      state.code === this.state.code
    ) {
      return
    }
    this.state = state
    if (!this.disposed) {
      this.onChange(state)
    }
  }
}
