import { environmentManager } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { DataVersion } from "@/features/data-versions/api"
import {
  resetBuildWatch,
  setBuildWatchNavigate,
  watchDataVersionBuild,
  watchedBuildCount,
} from "@/features/data-versions/build-watch"
import { dataVersion } from "@/features/data-versions/test-fixtures"
import { queryClient } from "@/lib/query-client"

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast }))

/** fetch 依次返回这些版本（之后一直返回最后一个）。 */
function serve(...responses: (DataVersion | number)[]) {
  const fetchMock = vi.fn(async () => {
    const next = responses.length > 1 ? responses.shift() : responses[0]
    return typeof next === "number"
      ? Response.json(
          { error: { code: "not_found", message: "数据版本不存在" } },
          { status: next }
        )
      : Response.json(next)
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

beforeEach(() => {
  vi.useFakeTimers()
  // node 中没有 window，TanStack Query 默认不在“服务端”设置轮询的定时器。
  environmentManager.setIsServer(() => false)
})

afterEach(() => {
  resetBuildWatch()
  queryClient.clear()
  environmentManager.setIsServer(() => typeof window === "undefined")
  vi.useRealTimers()
  vi.unstubAllGlobals()
  toast.success.mockReset()
  toast.error.mockReset()
})

const queued = dataVersion({
  status: "queued",
  queue_position: 1,
  train_count: 0,
  val_count: 0,
})
const building = dataVersion({
  status: "building",
  progress: { stage: "linking", done: 10, total: 100 },
})
const ready = dataVersion({ status: "ready" })

describe("watchDataVersionBuild", () => {
  it("polls until ready, then toasts once with a link", async () => {
    const fetchMock = serve(building, ready)
    const navigate = vi.fn()
    setBuildWatchNavigate(navigate)

    watchDataVersionBuild(queued)
    // 同一个版本只观察一次。
    watchDataVersionBuild(queued)
    expect(watchedBuildCount()).toBe(1)

    await vi.advanceTimersByTimeAsync(1_600)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1_600)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(toast.success).toHaveBeenCalledTimes(1)
    const [title, options] = toast.success.mock.calls[0]
    expect(title).toBe("外观检测 · 数据版本 v4 已生成")
    expect(options.duration).toBe(10_000)
    expect(options.description).toBe("训练 800 · 验证 200 · 测试 0")
    options.action.onClick()
    expect(navigate).toHaveBeenCalledWith("/datasets/versions/12")

    // 不再轮询。
    expect(watchedBuildCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(5_000)
    const calls = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(5_000)
    expect(fetchMock.mock.calls.length).toBe(calls)

    // 再次告诉它同一个版本也不会重复提示。
    watchDataVersionBuild(queued)
    await vi.advanceTimersByTimeAsync(1_600)
    expect(toast.success).toHaveBeenCalledTimes(1)
  })

  it("shows the error of a failed build", async () => {
    serve(dataVersion({ status: "failed", error: "数据集“A”的文件夹不存在" }))
    watchDataVersionBuild(building)
    await vi.advanceTimersByTimeAsync(1_600)
    expect(toast.error).toHaveBeenCalledTimes(1)
    const [title, options] = toast.error.mock.calls[0]
    expect(title).toBe("外观检测 · 数据版本 v4 生成失败")
    expect(options.description).toBe("数据集“A”的文件夹不存在")
  })

  it("toasts again after a retry of the same version", async () => {
    serve(
      dataVersion({
        status: "failed",
        error: "磁盘空间不足",
        finished_at: "2026-11-01T08:00:00Z",
      })
    )
    watchDataVersionBuild(building)
    await vi.advanceTimersByTimeAsync(1_600)
    expect(toast.error).toHaveBeenCalledTimes(1)

    // 重新生成：同一个 id 再次排队，这次生成成功。
    serve(dataVersion({ status: "ready", finished_at: "2026-11-01T09:00:00Z" }))
    watchDataVersionBuild(queued)
    await vi.advanceTimersByTimeAsync(1_600)
    expect(toast.success).toHaveBeenCalledTimes(1)
    expect(watchedBuildCount()).toBe(0)
  })

  it("stops quietly when the version is deleted", async () => {
    const fetchMock = serve(404)
    watchDataVersionBuild(building)
    await vi.advanceTimersByTimeAsync(1_600)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(watchedBuildCount()).toBe(0)
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("ignores versions that are not building", () => {
    const fetchMock = serve(ready)
    watchDataVersionBuild(ready)
    expect(watchedBuildCount()).toBe(0)
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
