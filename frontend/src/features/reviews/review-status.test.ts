import { describe, expect, it } from "vitest"

import type {
  ReviewCounts,
  ReviewImage,
  ReviewImageDetail,
  ReviewSummary,
} from "@/features/reviews/api"
import {
  adjustReviewCounts,
  formatConfidence,
  isInferring,
  matchConfidences,
  predictionsToObjects,
  replaceReviewImage,
  reviewImageStatusKeys,
  sameBoxes,
  scopedCounts,
  statusFilterOptions,
  toReviewImage,
  unmatchedPredictions,
} from "@/features/reviews/review-status"

const counts = (changes: Partial<ReviewCounts> = {}): ReviewCounts => ({
  total: 10,
  inferred: 10,
  inference_failed: 0,
  pending: 6,
  approved: 3,
  needs_sample: 1,
  edited: 2,
  merged: 0,
  ...changes,
})

const summary: Pick<ReviewSummary, "counts" | "datasets"> = {
  counts: counts(),
  datasets: [
    {
      id: 1,
      name: "A",
      folder: "A",
      next_version: "审图1.0",
      counts: counts({
        total: 4,
        pending: 4,
        approved: 0,
        needs_sample: 0,
        edited: 0,
      }),
    },
    {
      id: 2,
      name: "B",
      folder: "B",
      next_version: "审图1.0",
      counts: counts({ total: 6, pending: 2 }),
    },
  ],
}

function image(changes: Partial<ReviewImage> = {}): ReviewImage {
  return {
    id: 7,
    review_id: 1,
    dataset_id: 2,
    image_id: 70,
    position: 3,
    path: "夜班/a.png",
    file_name: "a.png",
    package_path: "B/夜班/a.png",
    annotation_path: "B/夜班/a.xml",
    width: 640,
    height: 480,
    image_version: "v1",
    inferred: true,
    inference_error: "",
    object_count: 1,
    labels: [{ name: "dent", count: 1 }],
    prediction_count: 1,
    edited: false,
    status: "pending",
    revision: 0,
    reviewed_at: null,
    merged_at: null,
    annotation_version: "abc",
    ...changes,
  }
}

const box = (name: string, x = 0) => ({
  name,
  xmin: x,
  ymin: 0,
  xmax: x + 10,
  ymax: 10,
})

describe("counts", () => {
  it("scopes the counts to a dataset", () => {
    expect(scopedCounts(summary, null)).toBe(summary.counts)
    expect(scopedCounts(summary, 1).total).toBe(4)
    // A dataset that is not in the review: the review's counts.
    expect(scopedCounts(summary, 9)).toBe(summary.counts)
  })

  it("lists the status filter options with their counts", () => {
    expect(statusFilterOptions(counts())).toEqual([
      { value: "all", label: "全部", count: 10 },
      { value: "pending", label: "待审核", count: 6 },
      { value: "approved", label: "已审核", count: 3 },
      { value: "needs_sample", label: "需加样", count: 1 },
    ])
  })

  it("moves an image between statuses in the review and its dataset", () => {
    const next = adjustReviewCounts(
      summary,
      image(),
      image({ status: "approved" })
    )
    expect(next.counts).toMatchObject({ pending: 5, approved: 4, edited: 2 })
    expect(next.datasets[1].counts).toMatchObject({ pending: 1, approved: 4 })
    expect(next.datasets[0]).toBe(summary.datasets[0])
    expect(summary.counts.pending).toBe(6) // unchanged
  })

  it("counts edited images", () => {
    const next = adjustReviewCounts(
      summary,
      image(),
      image({ edited: true, status: "needs_sample" })
    )
    expect(next.counts).toMatchObject({
      pending: 5,
      needs_sample: 2,
      edited: 3,
    })
    const back = adjustReviewCounts(next, image({ edited: true }), image())
    expect(back.counts.edited).toBe(2)
  })

  it("returns the same object without a change and never goes below 0", () => {
    expect(adjustReviewCounts(summary, image(), image())).toBe(summary)
    const zero = adjustReviewCounts(
      { counts: counts({ approved: 0 }), datasets: [] },
      image({ status: "approved" }),
      image()
    )
    expect(zero.counts).toMatchObject({ approved: 0, pending: 7 })
  })

  it("knows when inference is still going on", () => {
    expect(isInferring({ status: "queued" })).toBe(true)
    expect(isInferring({ status: "running" })).toBe(true)
    expect(isInferring({ status: "ready" })).toBe(false)
    expect(isInferring({ status: "failed" })).toBe(false)
  })
})

describe("list caches", () => {
  it("strips the detail fields", () => {
    const detail: ReviewImageDetail = {
      ...image(),
      objects: [],
      predictions: [],
      dataset_name: "B",
      annotation_error: "",
    }
    expect(toReviewImage(detail)).toEqual(image())
  })

  it("replaces the image in a page that has it", () => {
    const page = { items: [image({ id: 1 }), image()], total: 2 }
    const updated = image({ status: "approved", revision: 1 })
    const next = replaceReviewImage(page, updated)
    expect(next.items[1]).toBe(updated)
    expect(next.items[0]).toBe(page.items[0])
    expect(page.items[1].status).toBe("pending")
    expect(replaceReviewImage(page, image({ id: 99 }))).toBe(page)
  })
})

describe("sameBoxes", () => {
  it("compares boxes as multisets of class and coordinates", () => {
    expect(sameBoxes([], [])).toBe(true)
    expect(sameBoxes([box("a"), box("b", 5)], [box("b", 5), box("a")])).toBe(
      true
    )
    expect(sameBoxes([box("a")], [box("a"), box("a")])).toBe(false)
    expect(sameBoxes([box("a"), box("a")], [box("a"), box("b")])).toBe(false)
    expect(sameBoxes([box("a", 1)], [box("a")])).toBe(false)
    expect(sameBoxes([box("b")], [box("a")])).toBe(false)
  })

  it("ignores flags and confidence", () => {
    expect(
      sameBoxes(
        [{ ...box("a"), difficult: true, truncated: true } as never],
        [{ ...box("a"), confidence: 0.3 } as never]
      )
    ).toBe(true)
  })
})

describe("matchConfidences", () => {
  it("gives unchanged boxes the confidence of their prediction", () => {
    const predictions = [
      { ...box("a"), confidence: 0.4 },
      { ...box("a"), confidence: 0.9 },
      { ...box("b", 5), confidence: 0.75 },
    ]
    const matched = matchConfidences(
      [
        { ...box("a"), uid: "x" },
        { ...box("a"), uid: "y" },
        { ...box("a"), uid: "z" }, // a third identical box: no prediction left
        { ...box("b", 6), uid: "moved" },
      ],
      predictions
    )
    expect([...matched]).toEqual([
      ["x", 0.9],
      ["y", 0.4],
    ])
  })

  it("formats confidences", () => {
    expect(formatConfidence(0.8749)).toBe("0.87")
    expect(formatConfidence(1)).toBe("1.00")
  })
})

describe("reviewImageStatusKeys", () => {
  it("matches the editor shortcuts", () => {
    expect(reviewImageStatusKeys).toEqual({
      approved: "S",
      needs_sample: "X",
      pending: "U",
    })
  })
})

describe("unmatchedPredictions", () => {
  const box = (name: string, x: number, confidence = 0.5) => ({
    name,
    confidence,
    xmin: x,
    ymin: 0,
    xmax: x + 10,
    ymax: 10,
  })

  it("lists the predictions no longer in the annotation", () => {
    const predictions = [box("dent", 0), box("dent", 20), box("scratch", 40)]
    expect(unmatchedPredictions(predictions, predictions)).toEqual([])
    // Moved, relabeled and deleted boxes.
    expect(
      unmatchedPredictions([box("dent", 1), box("burr", 20)], predictions)
    ).toEqual([0, 1, 2])
    expect(unmatchedPredictions([box("dent", 20)], predictions)).toEqual([0, 2])
  })

  it("pairs duplicates with the most confident predictions first", () => {
    const predictions = [box("dent", 0, 0.3), box("dent", 0, 0.9)]
    expect(unmatchedPredictions([box("dent", 0)], predictions)).toEqual([0])
  })
})

describe("predictionsToObjects", () => {
  it("drops the confidence and clears the flags", () => {
    expect(
      predictionsToObjects([
        { name: "dent", confidence: 0.8, xmin: 1, ymin: 2, xmax: 3, ymax: 4 },
      ])
    ).toEqual([
      {
        name: "dent",
        xmin: 1,
        ymin: 2,
        xmax: 3,
        ymax: 4,
        difficult: false,
        truncated: false,
      },
    ])
  })
})
