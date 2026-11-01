import { QueryClient, QueryObserver } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  annotationKeys,
  applySavedDetail,
  cancelImageReads,
  editorImageQuery,
  markImageOutdated,
  openableDetail,
  persistAnnotation,
  saveEditorAnnotation,
  savedDetail,
  type SaveContext,
} from "@/features/annotation/api"
import {
  datasetKeys,
  deleteAnnotation,
  saveAnnotation,
  type AnnotationObject,
  type DatasetImageDetail,
} from "@/features/datasets/api"
import { ApiError } from "@/lib/api"

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

  it("updates both image caches and refreshes counts only when labels change", async () => {
    const client = new QueryClient()
    const invalidate = vi.spyOn(client, "invalidateQueries")
    client.setQueryData(annotationKeys.image(1, 2), detail("annotated", ["a"]))

    await applySavedDetail(client, 1, detail("annotated", ["a"]))
    expect(invalidate).not.toHaveBeenCalled()
    expect(client.getQueryData(datasetKeys.image(1, 2))).toEqual(
      detail("annotated", ["a"])
    )

    await applySavedDetail(client, 1, detail("annotated", ["a", "b"]))
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: datasetKeys.detail(1),
    })
  })

  it("is not replaced by a read that started before the request", async () => {
    const client = new QueryClient()
    const key = annotationKeys.image(1, 2)
    client.setQueryData(key, detail("annotated", ["a"]))
    let resolve: (value: DatasetImageDetail) => void = () => undefined
    const read = client
      .fetchQuery({
        queryKey: key,
        queryFn: () =>
          new Promise<DatasetImageDetail>((done) => {
            resolve = done
          }),
      })
      .catch(() => undefined)

    // 删除标注文件（clearFile）：先取消之前开始的读取，再发出请求。读取晚于
    // 删除返回，读到的是删除前的文件。
    await cancelImageReads(client, 1, 2)
    await applySavedDetail(client, 1, detail("none", []))
    resolve(detail("annotated", ["a"]))
    await read
    await new Promise((done) => setTimeout(done, 0))
    expect(client.getQueryData(key)).toEqual(detail("none", []))
  })
})

describe("markImageOutdated", () => {
  it("invalidates only that image's editor detail", () => {
    const client = new QueryClient()
    client.setQueryData(annotationKeys.image(1, 2), { id: 2 })
    client.setQueryData(annotationKeys.image(1, 3), { id: 3 })
    markImageOutdated(client, 1, 2)
    expect(
      client.getQueryState(annotationKeys.image(1, 2))?.isInvalidated
    ).toBe(true)
    expect(
      client.getQueryState(annotationKeys.image(1, 3))?.isInvalidated
    ).toBe(false)
  })
})

describe("savedDetail", () => {
  const detail = (version: string, names: string[]) =>
    ({
      id: 2,
      annotation_version: version,
      objects: names.map((name) => ({ ...box, name })),
    }) as unknown as DatasetImageDetail

  it("returns what the last successful save wrote, not the loaded detail", async () => {
    const client = new QueryClient()
    const loaded = detail("", [])
    client.setQueryData(annotationKeys.image(1, 2), loaded)
    expect(savedDetail(client, 1, loaded)).toBe(loaded)

    const saved = detail("v1", ["dent"])
    await applySavedDetail(client, 1, saved)
    expect(savedDetail(client, 1, loaded)).toEqual(saved)
  })

  it("falls back to the loaded detail when the cache has none", () => {
    const loaded = detail("v1", ["dent"])
    expect(savedDetail(new QueryClient(), 1, loaded)).toBe(loaded)
  })
})

describe("saveEditorAnnotation", () => {
  const request = (changes: Partial<SaveContext> = {}) => ({
    objects: [box],
    baseVersion: "v1",
    force: false,
    context: context(changes),
  })
  const cached = (client: QueryClient) =>
    client.getQueryState(annotationKeys.image(1, 2))

  it("updates the cache and returns the new version", async () => {
    const client = new QueryClient()
    vi.mocked(saveAnnotation).mockResolvedValueOnce({
      id: 2,
      annotation_version: "v2",
    } as DatasetImageDetail)
    await expect(saveEditorAnnotation(client, request())).resolves.toBe("v2")
    expect(client.getQueryData(annotationKeys.image(1, 2))).toEqual({
      id: 2,
      annotation_version: "v2",
    })
  })

  it("keeps the version when nothing was written", async () => {
    const client = new QueryClient()
    await expect(
      saveEditorAnnotation(client, {
        ...request(),
        objects: [],
        baseVersion: "",
      })
    ).resolves.toBe("")
  })

  it("marks the cached detail outdated when someone else changed the file", async () => {
    const client = new QueryClient()
    client.setQueryData(annotationKeys.image(1, 2), { id: 2 })
    client.setQueryData(annotationKeys.image(1, 3), { id: 3 })
    const conflict = new ApiError(409, "annotation_conflict", "已被修改")
    vi.mocked(saveAnnotation).mockRejectedValueOnce(conflict)
    await expect(saveEditorAnnotation(client, request())).rejects.toBe(conflict)
    // 放弃修改、之后再打开时重新读取，不再用缓存中过期的框。
    expect(cached(client)?.isInvalidated).toBe(true)
    expect(
      client.getQueryState(annotationKeys.image(1, 3))?.isInvalidated
    ).toBe(false)
  })

  it("leaves the cache alone for other errors", async () => {
    const client = new QueryClient()
    client.setQueryData(annotationKeys.image(1, 2), { id: 2 })
    const failure = new ApiError(500, "internal", "boom")
    vi.mocked(saveAnnotation).mockRejectedValueOnce(failure)
    await expect(saveEditorAnnotation(client, request())).rejects.toBe(failure)
    expect(cached(client)?.isInvalidated).toBe(false)
  })

  it("keeps the overwritten version when the conflict refetch returns later", async () => {
    const client = new QueryClient()
    const reads: ((value: DatasetImageDetail) => void)[] = []
    const options = {
      ...editorImageQuery(1, 2),
      queryFn: () =>
        new Promise<DatasetImageDetail>((done) => {
          reads.push(done)
        }),
    }
    const version = (annotation_version: string) =>
      ({ id: 2, annotation_version }) as DatasetImageDetail
    client.setQueryData(options.queryKey, version("v1"))
    // 编辑器正在查看这张图片：冲突后在后台重新读取。
    const observer = new QueryObserver(client, options)
    const unsubscribe = observer.subscribe(() => undefined)

    const conflict = new ApiError(409, "annotation_conflict", "已被修改")
    vi.mocked(saveAnnotation).mockRejectedValueOnce(conflict)
    await expect(saveEditorAnnotation(client, request())).rejects.toBe(conflict)
    expect(reads).toHaveLength(1)

    // 读取还没有返回时用户选择了“用我的版本覆盖”。
    vi.mocked(saveAnnotation).mockResolvedValueOnce(version("mine"))
    await expect(
      saveEditorAnnotation(client, { ...request(), force: true })
    ).resolves.toBe("mine")
    // 读取返回的是覆盖之前的文件，不能替换缓存中保存的结果。
    reads[0](version("theirs"))
    await vi.waitFor(() =>
      expect(observer.getCurrentResult().isFetching).toBe(false)
    )
    await new Promise((done) => setTimeout(done, 0))
    expect(client.getQueryData(options.queryKey)).toEqual(version("mine"))
    expect(cached(client)?.isInvalidated).toBe(false)
    unsubscribe()
  })

  it("lets a read started after the request was sent finish (re-reading the file during a save)", async () => {
    const client = new QueryClient()
    const version = (annotation_version: string) =>
      ({ id: 2, annotation_version }) as DatasetImageDetail
    const key = annotationKeys.image(1, 2)
    client.setQueryData(key, version("v1"))

    let finishSave: (value: DatasetImageDetail) => void = () => undefined
    vi.mocked(saveAnnotation).mockImplementationOnce(
      () =>
        new Promise<DatasetImageDetail>((done) => {
          finishSave = done
        })
    )
    const save = saveEditorAnnotation(client, request())
    await vi.waitFor(() => expect(saveAnnotation).toHaveBeenCalled())

    // 保存的请求已发出，用户选择“重新读取标注文件”。
    let finishRead: (value: DatasetImageDetail) => void = () => undefined
    const reload = client.fetchQuery({
      ...editorImageQuery(1, 2),
      staleTime: 0,
      queryFn: () =>
        new Promise<DatasetImageDetail>((done) => {
          finishRead = done
        }),
    })
    finishSave(version("v2"))
    await expect(save).resolves.toBe("v2")
    // 读取晚于保存返回，读到的是保存后的文件。
    finishRead(version("v2"))
    await expect(reload).resolves.toEqual(version("v2"))
    expect(client.getQueryData(key)).toEqual(version("v2"))
  })
})

describe("openableDetail", () => {
  const detail = (id: number, version: string) =>
    ({ id, annotation_version: version }) as DatasetImageDetail

  it("uses fresh cached details, including after a failed refetch", () => {
    const data = detail(2, "v1")
    expect(openableDetail({ data, isStale: false, isFetching: false }, 2)).toBe(
      data
    )
    expect(openableDetail({ data, isStale: false, isFetching: true }, 2)).toBe(
      data
    )
    expect(openableDetail({ data, isStale: true, isFetching: false }, 2)).toBe(
      data
    )
  })

  it("does not use another image's detail or a stale one being refetched", () => {
    const data = detail(2, "v1")
    expect(
      openableDetail({ data, isStale: false, isFetching: false }, 3)
    ).toBeUndefined()
    expect(
      openableDetail({ data: undefined, isStale: true, isFetching: true }, 2)
    ).toBeUndefined()
    expect(
      openableDetail({ data, isStale: true, isFetching: true }, 2)
    ).toBeUndefined()
  })

  it("waits for the file when an image is opened from an outdated cache", async () => {
    const client = new QueryClient()
    let resolve: (value: DatasetImageDetail) => void = () => undefined
    const options = {
      ...editorImageQuery(1, 2),
      queryFn: () =>
        new Promise<DatasetImageDetail>((done) => {
          resolve = done
        }),
    }
    // 31 秒前预加载的详情：文件可能已被他人修改。
    client.setQueryData(options.queryKey, detail(2, "old"), {
      updatedAt: Date.now() - 31_000,
    })
    const observer = new QueryObserver(client, options)
    const unsubscribe = observer.subscribe(() => undefined)
    expect(openableDetail(observer.getCurrentResult(), 2)).toBeUndefined()

    resolve(detail(2, "new"))
    await vi.waitFor(() =>
      expect(openableDetail(observer.getCurrentResult(), 2)).toEqual(
        detail(2, "new")
      )
    )
    unsubscribe()
  })

  it("opens a recently loaded image at once", () => {
    const client = new QueryClient()
    const options = {
      ...editorImageQuery(1, 2),
      queryFn: () => new Promise<DatasetImageDetail>(() => undefined),
    }
    client.setQueryData(options.queryKey, detail(2, "v1"))
    const observer = new QueryObserver(client, options)
    const unsubscribe = observer.subscribe(() => undefined)
    expect(openableDetail(observer.getCurrentResult(), 2)).toEqual(
      detail(2, "v1")
    )
    unsubscribe()
  })
})
