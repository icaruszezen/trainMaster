import { QueryClient } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  annotationKeys,
  applySavedDetail,
  persistAnnotation,
  type SaveContext,
} from "@/features/annotation/api"
import {
  datasetKeys,
  deleteAnnotation,
  saveAnnotation,
  type AnnotationObject,
  type DatasetImageDetail,
} from "@/features/datasets/api"

vi.mock("@/features/datasets/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/features/datasets/api")>()),
  saveAnnotation: vi.fn(() => Promise.resolve({ id: 2 })),
  deleteAnnotation: vi.fn(() => Promise.resolve({ id: 2 })),
}))

const box: AnnotationObject = {
  name: "dent",
  xmin: 0,
  ymin: 0,
  xmax: 1,
  ymax: 1,
  difficult: false,
  truncated: false,
}
const context = (changes: Partial<SaveContext> = {}): SaveContext => ({
  datasetId: 1,
  imageId: 2,
  hadFile: false,
  negative: false,
  ...changes,
})

beforeEach(() => {
  vi.mocked(saveAnnotation).mockClear()
  vi.mocked(deleteAnnotation).mockClear()
})

describe("persistAnnotation", () => {
  it("writes the boxes", async () => {
    await persistAnnotation([box], "v1", false, context())
    expect(saveAnnotation).toHaveBeenCalledWith(
      1,
      2,
      { objects: [box], base_version: "v1", force: false },
      undefined
    )
  })

  it("asks to regenerate the file only when told to", async () => {
    await persistAnnotation([box], "v1", true, context(), undefined, true)
    expect(saveAnnotation).toHaveBeenCalledWith(
      1,
      2,
      { objects: [box], base_version: "v1", force: true, regenerate: true },
      undefined
    )
  })

  it("writes nothing for an image that had no file and still has no box", async () => {
    await expect(
      persistAnnotation([], "", false, context())
    ).resolves.toBeNull()
    expect(saveAnnotation).not.toHaveBeenCalled()
    expect(deleteAnnotation).not.toHaveBeenCalled()
  })

  it("removes the file created in this session when all boxes are deleted", async () => {
    await persistAnnotation([], "v2", false, context())
    expect(deleteAnnotation).toHaveBeenCalledWith(1, 2, "v2", false, undefined)
    expect(saveAnnotation).not.toHaveBeenCalled()
  })

  it("passes the request options through, also when deleting (keepalive)", async () => {
    const init = { keepalive: true }
    await persistAnnotation([], "v2", false, context(), init)
    expect(deleteAnnotation).toHaveBeenCalledWith(1, 2, "v2", false, init)
    await persistAnnotation([box], "v2", false, context(), init)
    expect(saveAnnotation).toHaveBeenCalledWith(
      1,
      2,
      { objects: [box], base_version: "v2", force: false },
      init
    )
    await expect(
      persistAnnotation([], "", false, context(), init)
    ).resolves.toBeNull()
    expect(deleteAnnotation).toHaveBeenCalledTimes(1)
  })

  it("writes an empty annotation when the image had a file or is marked negative", async () => {
    await persistAnnotation([], "v1", false, context({ hadFile: true }))
    await persistAnnotation([], "", false, context({ negative: true }))
    expect(saveAnnotation).toHaveBeenCalledTimes(2)
    expect(deleteAnnotation).not.toHaveBeenCalled()
  })
})

describe("applySavedDetail", () => {
  const detail = (state: string, labels: string[]) =>
    ({
      id: 2,
      annotation_state: state,
      annotation: { labels },
    }) as unknown as DatasetImageDetail

  it("updates both image caches and refreshes counts only when labels change", () => {
    const client = new QueryClient()
    const invalidate = vi.spyOn(client, "invalidateQueries")
    client.setQueryData(annotationKeys.image(1, 2), detail("annotated", ["a"]))

    applySavedDetail(client, 1, detail("annotated", ["a"]))
    expect(invalidate).not.toHaveBeenCalled()
    expect(client.getQueryData(datasetKeys.image(1, 2))).toEqual(
      detail("annotated", ["a"])
    )

    applySavedDetail(client, 1, detail("annotated", ["a", "b"]))
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: datasetKeys.detail(1),
    })
  })
})
