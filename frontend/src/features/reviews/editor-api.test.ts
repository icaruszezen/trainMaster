import { QueryClient } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  reviewSourceKeys,
  saveReviewAnnotation,
  setReviewImageStatus,
  ALL_REVIEW_IMAGES,
  type ReviewCounts,
  type ReviewImage,
  type ReviewImageDetail,
  type ReviewSource,
  type ReviewSummary,
} from "@/features/reviews/api"
import {
  applyReviewImage,
  markReviewImage,
  persistReviewAnnotation,
  saveReviewEditorAnnotation,
  savedReviewDetail,
} from "@/features/reviews/editor-api"
import { toReviewImage } from "@/features/reviews/review-status"
import { ApiError, type ListResponse } from "@/lib/api"

vi.mock("@/features/reviews/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/features/reviews/api")>()),
  saveReviewAnnotation: vi.fn(),
  setReviewImageStatus: vi.fn(),
}))

const source: ReviewSource = { key: ["reviews", "detail", 1], base: "/r/1" }

const counts = (changes: Partial<ReviewCounts> = {}): ReviewCounts => ({
  total: 2,
  inferred: 2,
  inference_failed: 0,
  pending: 2,
  approved: 0,
  needs_sample: 0,
  edited: 0,
  merged: 0,
  ...changes,
})

function detail(changes: Partial<ReviewImageDetail> = {}): ReviewImageDetail {
  return {
    id: 7,
    review_id: 1,
    dataset_id: 3,
    image_id: 70,
    position: 0,
    path: "a.png",
    file_name: "a.png",
    package_path: "A/a.png",
    annotation_path: "A/a.xml",
    width: 100,
    height: 100,
    image_version: "v",
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
    annotation_version: "v1",
    objects: [],
    predictions: [],
    dataset_name: "A",
    annotation_error: "",
    ...changes,
  }
}

const summary: ReviewSummary = {
  id: 1,
  name: "审图",
  description: "",
  model: {
    id: 1,
    project_id: 1,
    project_name: "P",
    version: 1,
    yolo_version: "yolo11",
    task: "detect",
    file_name: "best.pt",
    class_names: ["dent"],
  },
  options: { conf: 0.25, iou: 0.7, imgsz: [640, 640], max_det: 300 },
  status: "ready",
  counts: counts(),
  datasets: [{ id: 3, name: "A", folder: "A", counts: counts() }],
}

let client: QueryClient
const pageKey = reviewSourceKeys.page(source, ALL_REVIEW_IMAGES, 0)

function seed(initial = detail()) {
  client.setQueryData(reviewSourceKeys.image(source, 7), initial)
  client.setQueryData(reviewSourceKeys.summary(source), summary)
  client.setQueryData<ListResponse<ReviewImage>>(pageKey, {
    items: [toReviewImage(initial)],
    total: 1,
  })
}

beforeEach(() => {
  client = new QueryClient()
  vi.mocked(saveReviewAnnotation).mockReset()
  vi.mocked(setReviewImageStatus).mockReset()
})

describe("persistReviewAnnotation", () => {
  it("writes the boxes to the review image", async () => {
    vi.mocked(saveReviewAnnotation).mockResolvedValue(detail())
    const box = {
      name: "dent",
      xmin: 0,
      ymin: 0,
      xmax: 1,
      ymax: 1,
      difficult: false,
      truncated: false,
    }
    await persistReviewAnnotation(
      [box],
      "v1",
      true,
      { source, imageId: 7 },
      {
        keepalive: true,
      }
    )
    expect(saveReviewAnnotation).toHaveBeenCalledWith(
      source,
      7,
      { objects: [box], base_version: "v1", force: true },
      { keepalive: true }
    )
  })
})

describe("applyReviewImage", () => {
  it("updates the detail, the list pages and the counts", () => {
    seed()
    const updated = detail({ status: "approved", edited: true, revision: 1 })
    applyReviewImage(client, source, updated)
    expect(client.getQueryData(reviewSourceKeys.image(source, 7))).toEqual(
      updated
    )
    const page = client.getQueryData<ListResponse<ReviewImage>>(pageKey)
    expect(page?.items[0]).toMatchObject({ status: "approved", revision: 1 })
    expect(page?.items[0]).not.toHaveProperty("objects")
    const review = client.getQueryData<ReviewSummary>(
      reviewSourceKeys.summary(source)
    )
    expect(review?.counts).toMatchObject({ pending: 1, approved: 1, edited: 1 })
    expect(review?.datasets[0].counts).toMatchObject({ approved: 1 })
    // The summary, the pages and the neighbors are read again.
    expect(
      client.getQueryState(reviewSourceKeys.summary(source))?.isInvalidated
    ).toBe(true)
    expect(client.getQueryState(pageKey)?.isInvalidated).toBe(true)
  })

  it("re-reads the labels only when they changed", () => {
    seed()
    client.setQueryData(reviewSourceKeys.labels(source), {
      items: [],
      total: 0,
    })
    applyReviewImage(client, source, detail({ status: "approved" }))
    expect(
      client.getQueryState(reviewSourceKeys.labels(source))?.isInvalidated
    ).toBe(false)
    applyReviewImage(client, source, detail({ labels: [] }))
    expect(
      client.getQueryState(reviewSourceKeys.labels(source))?.isInvalidated
    ).toBe(true)
  })

  it("leaves the counts to the server without a previous detail", () => {
    client.setQueryData(reviewSourceKeys.summary(source), summary)
    applyReviewImage(client, source, detail({ status: "approved" }))
    expect(
      client.getQueryData<ReviewSummary>(reviewSourceKeys.summary(source))
        ?.counts.approved
    ).toBe(0)
  })
})

describe("saveReviewEditorAnnotation", () => {
  const request = {
    objects: [],
    baseVersion: "v1",
    force: false,
    context: { source, imageId: 7 },
  }

  it("returns the new version and caches the saved detail", async () => {
    seed()
    const saved = detail({ annotation_version: "v2", edited: true })
    vi.mocked(saveReviewAnnotation).mockResolvedValue(saved)
    await expect(saveReviewEditorAnnotation(client, request)).resolves.toBe(
      "v2"
    )
    expect(savedReviewDetail(client, source, detail())).toEqual(saved)
  })

  it("marks the cached detail outdated on a conflict", async () => {
    seed()
    vi.mocked(saveReviewAnnotation).mockRejectedValue(
      new ApiError(409, "annotation_conflict", "已被修改")
    )
    await expect(saveReviewEditorAnnotation(client, request)).rejects.toThrow(
      "已被修改"
    )
    expect(
      client.getQueryState(reviewSourceKeys.image(source, 7))?.isInvalidated
    ).toBe(true)
    expect(
      client.getQueryState(reviewSourceKeys.summary(source))?.isInvalidated
    ).toBe(false)
  })
})

describe("markReviewImage", () => {
  it("sets the status and updates the caches", async () => {
    seed()
    const marked = detail({ status: "needs_sample", revision: 1 })
    vi.mocked(setReviewImageStatus).mockResolvedValue(marked)
    await expect(
      markReviewImage(client, source, 7, "needs_sample")
    ).resolves.toBe(marked)
    expect(setReviewImageStatus).toHaveBeenCalledWith(source, 7, "needs_sample")
    expect(
      client.getQueryData<ReviewSummary>(reviewSourceKeys.summary(source))
        ?.counts
    ).toMatchObject({ pending: 1, needs_sample: 1 })
  })

  it("changes nothing when the request fails", async () => {
    seed()
    vi.mocked(setReviewImageStatus).mockRejectedValue(
      new ApiError(409, "conflict", "这张图片还没有识别完成")
    )
    await expect(
      markReviewImage(client, source, 7, "approved")
    ).rejects.toThrow("还没有识别完成")
    expect(
      client.getQueryData<ReviewSummary>(reviewSourceKeys.summary(source))
        ?.counts.approved
    ).toBe(0)
  })
})
