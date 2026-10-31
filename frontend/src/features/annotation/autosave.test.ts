import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  AnnotationSaver,
  AUTOSAVE_DELAY,
  type SaveRequest,
  type SaveStatus,
} from "@/features/annotation/autosave"
import type { AnnotationObject } from "@/features/datasets/api"
import { ApiError } from "@/lib/api"

const box = (name: string): AnnotationObject => ({
  name,
  xmin: 0,
  ymin: 0,
  xmax: 10,
  ymax: 10,
  difficult: false,
  truncated: false,
})

type Deferred = {
  request: SaveRequest<string>
  resolve: (version: string) => void
  reject: (error: unknown) => void
}

/** A saver whose saves wait until the test resolves them. */
function setup(options: { autosave?: boolean } = {}) {
  const calls: Deferred[] = []
  const statuses: SaveStatus[] = []
  const saver = new AnnotationSaver<string>({
    save: (request) =>
      new Promise<string>((resolve, reject) => {
        calls.push({ request, resolve, reject })
      }),
    onChange: (state) => statuses.push(state.status),
    autosave: options.autosave,
  })
  return { saver, calls, statuses }
}

const conflict = () =>
  new ApiError(409, "annotation_conflict", "标注文件已被修改")

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe("AnnotationSaver", () => {
  it("saves after the edits stop, with the context of the image", async () => {
    const { saver, calls, statuses } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    expect(saver.isDirty()).toBe(true)
    vi.advanceTimersByTime(AUTOSAVE_DELAY - 1)
    saver.update([box("a"), box("b")]) // restarts the timer
    vi.advanceTimersByTime(AUTOSAVE_DELAY - 1)
    expect(calls).toHaveLength(0)
    vi.advanceTimersByTime(1)
    expect(calls).toHaveLength(1)
    expect(calls[0].request).toEqual({
      objects: [box("a"), box("b")],
      baseVersion: "v1",
      force: false,
      context: "image-1",
    })
    calls[0].resolve("v2")
    await vi.runAllTimersAsync()
    expect(saver.getVersion()).toBe("v2")
    expect(saver.isDirty()).toBe(false)
    expect(statuses).toEqual(["dirty", "saving", "saved"])
  })

  it("is clean again when the edits are undone", () => {
    const { saver, calls } = setup()
    saver.reset([box("a")], "v1", "image-1")
    saver.update([])
    saver.update([box("a")])
    vi.advanceTimersByTime(AUTOSAVE_DELAY * 2)
    expect(calls).toHaveLength(0)
    expect(saver.getState().status).toBe("clean")
  })

  it("saves edits made during a save afterwards, from the new version", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    const flushed = saver.flush()
    saver.update([box("a"), box("b")])
    calls[0].resolve("v2")
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(2)
    expect(calls[1].request.baseVersion).toBe("v2")
    expect(calls[1].request.objects).toHaveLength(2)
    calls[1].resolve("v3")
    await expect(flushed).resolves.toBe(true)
    expect(saver.getVersion()).toBe("v3")
  })

  it("flush resolves at once when there is nothing to save", async () => {
    const { saver, calls } = setup()
    saver.reset([box("a")], "v1", "image-1")
    await expect(saver.flush()).resolves.toBe(true)
    expect(calls).toHaveLength(0)
  })

  it("reports errors and retries on the next save", async () => {
    const { saver, calls } = setup()
    saver.reset([], "", "image-1")
    saver.update([box("a")])
    const flushed = saver.flush()
    calls[0].reject(new Error("网络错误"))
    await expect(flushed).resolves.toBe(false)
    expect(saver.getState()).toEqual({ status: "error", error: "网络错误" })
    const retry = saver.flush()
    calls[1].resolve("v1")
    await expect(retry).resolves.toBe(true)
  })

  it("stops on a conflict until the user overwrites", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    const flushed = saver.flush()
    calls[0].reject(conflict())
    await expect(flushed).resolves.toBe(false)
    expect(saver.getState().status).toBe("conflict")

    // Further edits do not save over the other version.
    saver.update([box("b")])
    vi.advanceTimersByTime(AUTOSAVE_DELAY * 2)
    expect(calls).toHaveLength(1)
    expect(saver.pending()).toBeNull()
    await expect(saver.flush()).resolves.toBe(false)

    const overwritten = saver.overwrite()
    expect(calls[1].request).toMatchObject({
      force: true,
      objects: [box("b")],
    })
    calls[1].resolve("v9")
    await expect(overwritten).resolves.toBe(true)
    expect(saver.getState().status).toBe("saved")
  })

  it("ignores the result of a save for the previous image", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    void saver.flush()
    saver.reset([box("x")], "w1", "image-2")
    calls[0].resolve("v2")
    await vi.runAllTimersAsync()
    expect(saver.getVersion()).toBe("w1")
    expect(saver.getContext()).toBe("image-2")
    expect(saver.getState().status).toBe("clean")
  })

  it("does not autosave when turned off, but still flushes", async () => {
    const { saver, calls } = setup({ autosave: false })
    saver.reset([], "", "image-1")
    saver.update([box("a")])
    vi.advanceTimersByTime(AUTOSAVE_DELAY * 2)
    expect(calls).toHaveLength(0)
    const flushed = saver.flush()
    calls[0].resolve("v1")
    await expect(flushed).resolves.toBe(true)
  })

  it("pending returns the unsaved edits for a keepalive save", () => {
    const { saver } = setup()
    expect(saver.pending()).toBeNull() // no image yet
    saver.reset([], "v1", "image-1")
    expect(saver.pending()).toBeNull()
    saver.update([box("a")])
    expect(saver.pending()).toEqual({
      objects: [box("a")],
      baseVersion: "v1",
      force: false,
      context: "image-1",
    })
  })

  it("rebase takes a version written elsewhere", async () => {
    const { saver, calls } = setup()
    saver.reset([box("a")], "v1", "image-1")
    saver.update([])
    saver.rebase([], "v2") // e.g. "标为无目标" wrote an empty file
    expect(saver.isDirty()).toBe(false)
    expect(saver.getState().status).toBe("saved")
    vi.advanceTimersByTime(AUTOSAVE_DELAY * 2)
    expect(calls).toHaveLength(0)

    saver.update([box("b")])
    saver.rebase([], "v3")
    expect(saver.getState().status).toBe("dirty")
    await vi.advanceTimersByTimeAsync(AUTOSAVE_DELAY)
    expect(calls[0].request.baseVersion).toBe("v3")
  })

  it("does nothing before reset", async () => {
    const { saver, calls } = setup()
    saver.update([box("a")])
    await expect(saver.flush()).resolves.toBe(false)
    expect(calls).toHaveLength(0)
  })
})
