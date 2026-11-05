// 合并在后台进行：开始合并后页面上的审图任务列表（包括数据集的“审核记录”）
// 要知道它，结束时据此刷新数据集，关闭了合并对话框也是。
import { afterEach, describe, expect, it } from "vitest"

import { datasetKeys } from "@/features/datasets/api"
import {
  ALL_REVIEWS,
  refreshMergedDatasets,
  refreshReviewMerges,
  reviewKeys,
  type ReviewMerge,
} from "@/features/reviews/api"
import { endedMerges } from "@/features/reviews/merge-dialog"
import { queryClient } from "@/lib/query-client"

function merge(changes: Partial<ReviewMerge> = {}): ReviewMerge {
  return {
    id: 3,
    review_id: 1,
    dataset_ids: [5],
    statuses: ["needs_sample"],
    on_existing: null,
    note: "第一轮",
    versions: [],
    status: "running",
    total: 2000,
    done: 10,
    summary: {
      merged: 0,
      skipped_existing: 0,
      skipped_empty: 0,
      skipped_changed: 0,
      conflicts: 0,
      failed: 0,
    },
    problems: [],
    error: "",
    created_at: "2026-11-04T08:00:00Z",
    finished_at: null,
    ...changes,
  }
}

const invalidated = (queryKey: readonly unknown[]) =>
  queryClient.getQueryState(queryKey)?.isInvalidated ?? false

afterEach(() => {
  queryClient.clear()
})

describe("refreshReviewMerges", () => {
  it("lets every list of the review see the merge it just started", () => {
    const keys = [
      reviewKeys.detail(1),
      reviewKeys.merges(1),
      reviewKeys.list(ALL_REVIEWS),
      reviewKeys.dataset(5),
      reviewKeys.dataset(6),
    ]
    for (const key of keys) {
      queryClient.setQueryData(key, { items: [] })
    }
    const images = [...reviewKeys.detail(1), "images"]
    queryClient.setQueryData(images, { items: [] })

    refreshReviewMerges(1)

    for (const key of keys) {
      expect(invalidated(key), JSON.stringify(key)).toBe(true)
    }
    // 只刷新任务本身，不重新读取它的图片列表。
    expect(invalidated(images)).toBe(false)
  })
})

describe("endedMerges", () => {
  const running = merge()
  const finished = merge({
    status: "succeeded",
    done: 2000,
    finished_at: "2026-11-04T08:05:00Z",
  })

  it("tracks merges that are running", () => {
    const first = endedMerges(new Map(), [
      { id: 1, active_merge: running, last_merge: null },
      { id: 2, active_merge: null, last_merge: null },
    ])
    expect([...first.active]).toEqual([[1, running]])
    expect(first.ended).toEqual([])
  })

  it("reports a merge that finished since the list was last seen", () => {
    const previous = new Map([[1, running]])
    const { active, ended } = endedMerges(previous, [
      { id: 1, active_merge: null, last_merge: finished },
    ])
    expect(active.size).toBe(0)
    expect(ended).toEqual([{ reviewId: 1, running, merge: finished }])

    // 结束后又有别的合并结束了：不知道这次的结果。
    const other = endedMerges(previous, [
      { id: 1, active_merge: null, last_merge: merge({ id: 4 }) },
    ])
    expect(other.ended).toEqual([{ reviewId: 1, running, merge: null }])
  })

  it("does not report a merge the list never saw running", () => {
    // 开始合并后没有刷新列表时就是这样：合并结束时不会刷新数据集。
    expect(
      endedMerges(new Map(), [
        { id: 1, active_merge: null, last_merge: finished },
      ]).ended
    ).toEqual([])
  })
})

describe("refreshMergedDatasets", () => {
  it("refreshes the merged datasets, their versions and the review lists", () => {
    queryClient.setQueryData(datasetKeys.detail(5), {})
    queryClient.setQueryData(datasetKeys.detail(6), {})
    queryClient.setQueryData(datasetKeys.versions(5), { items: [] })
    const preview = datasetKeys.exportPreview(5, {
      include_base: true,
      version_ids: [],
    })
    queryClient.setQueryData(preview, {})
    queryClient.setQueryData(reviewKeys.dataset(5), { items: [] })
    refreshMergedDatasets(1, [5])
    expect(invalidated(datasetKeys.detail(5))).toBe(true)
    expect(invalidated(datasetKeys.versions(5))).toBe(true)
    expect(invalidated(preview)).toBe(true)
    expect(invalidated(datasetKeys.detail(6))).toBe(false)
    expect(invalidated(reviewKeys.dataset(5))).toBe(true)
  })
})
