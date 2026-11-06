// 排队的审图任务什么时候在等训练节点：模型在回收站中（移出了队列，等待恢复
// 模型）的不算。
import { describe, expect, it } from "vitest"

import { waitingForWorkers } from "@/features/reviews/review-parts"
import type { Review } from "@/features/reviews/api"

function review(
  status: Review["status"],
  modelExists: boolean,
  workersOnline: number
) {
  return {
    status,
    model_exists: modelExists,
    workers_online: workersOnline,
    // 平台把模型在回收站中的排队任务移出队列。
    queue_position: status === "queued" && !modelExists ? null : 1,
    error: "",
    counts: {} as Review["counts"],
  }
}

describe("waitingForWorkers", () => {
  it("waits for the model before the workers", () => {
    expect(waitingForWorkers(review("queued", false, 0))).toBe(false)
    expect(waitingForWorkers(review("queued", true, 0))).toBe(true)
    expect(waitingForWorkers(review("queued", true, 1))).toBe(false)
    // 模型已被永久删除：仍在队列中，领取时才失败，没有节点时照常提示。
    expect(
      waitingForWorkers({ ...review("queued", false, 0), queue_position: 1 })
    ).toBe(true)
  })

  it("only applies to queued reviews", () => {
    for (const status of ["running", "ready", "failed"] as const) {
      expect(waitingForWorkers(review(status, true, 0))).toBe(false)
    }
  })
})
