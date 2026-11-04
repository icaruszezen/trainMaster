import { toast } from "sonner"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  AnnotationSaver,
  type SaveRequest,
} from "@/features/annotation/autosave"
import type { AnnotationObject } from "@/features/datasets/api"
import { flushWithNotice } from "@/features/reviews/editor-flush"
import { ApiError } from "@/lib/api"

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))

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

function setup() {
  const calls: Deferred[] = []
  const saver = new AnnotationSaver<string>({
    save: (request) =>
      new Promise<string>((resolve, reject) => {
        calls.push({ request, resolve, reject })
      }),
    onChange: () => {},
    autosave: false,
  })
  return { saver, calls }
}

const conflict = () =>
  new ApiError(409, "annotation_conflict", "标注文件已被修改")

beforeEach(() => {
  vi.mocked(toast.error).mockClear()
})

describe("flushWithNotice", () => {
  it("saves the edits and reports success", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    const done = flushWithNotice(saver, "标记")
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    calls[0].resolve("v2")
    expect(await done).toBe(true)
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("explains a failed save", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    const done = flushWithNotice(saver, "标记")
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    calls[0].reject(new Error("网络错误"))
    expect(await done).toBe(false)
    expect(toast.error).toHaveBeenCalledWith("保存失败，没有标记", {
      description: "网络错误",
    })
  })

  it("asks to resolve a pending conflict even after the edits were undone", async () => {
    const { saver, calls } = setup()
    saver.reset([box("a")], "v1", "image-1")
    saver.update([box("b")]) // moved a box
    const first = saver.flush()
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    calls[0].reject(conflict()) // someone else saved the image
    expect(await first).toBe(false)
    expect(saver.getState().status).toBe("conflict")

    saver.update([box("a")]) // Ctrl+Z back to the file the edit started from
    expect(saver.isDirty()).toBe(false)

    expect(await flushWithNotice(saver, "标记")).toBe(false)
    expect(toast.error).toHaveBeenCalledWith(
      "请先处理这张图片的保存冲突，再标记"
    )
    expect(calls).toHaveLength(1) // nothing was overwritten
  })

  it("stays quiet when the image changed or the edits were discarded meanwhile", async () => {
    const { saver, calls } = setup()
    saver.reset([], "v1", "image-1")
    saver.update([box("a")])
    const switched = flushWithNotice(saver, "标记")
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    saver.reset([], "v9", "image-2")
    calls[0].reject(new Error("网络错误"))
    expect(await switched).toBe(false)

    saver.update([box("b")])
    const discarded = flushWithNotice(saver, null)
    await vi.waitFor(() => expect(calls).toHaveLength(2))
    saver.discard()
    calls[1].resolve("v10")
    expect(await discarded).toBe(false)
    expect(toast.error).not.toHaveBeenCalled()
  })
})
