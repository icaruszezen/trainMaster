// 排队的审图任务什么时候不会开始识别：模型已删除（在回收站中）时等待恢复模
// 型，否则没有在线的训练节点时等待节点。
import { describe, expect, it } from "vitest"

import {
  waitingForModel,
  waitingForWorkers,
} from "@/features/reviews/review-parts"
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
    queue_position: null,
    error: "",
    counts: {} as Review["counts"],
  }
}

describe("waitingForModel / waitingForWorkers", () => {
  it("waits for the model before the workers", () => {
    expect(waitingForModel(review("queued", false, 0))).toBe(true)
    expect(waitingForWorkers(review("queued", false, 0))).toBe(false)
    expect(waitingForModel(review("queued", true, 0))).toBe(false)
    expect(waitingForWorkers(review("queued", true, 0))).toBe(true)
    expect(waitingForWorkers(review("queued", true, 1))).toBe(false)
  })

  it("only applies to queued reviews", () => {
    for (const status of ["running", "ready", "failed"] as const) {
      expect(waitingForModel(review(status, false, 0))).toBe(false)
      expect(waitingForWorkers(review(status, true, 0))).toBe(false)
    }
  })
})
