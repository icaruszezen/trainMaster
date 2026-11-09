import { QueryClientProvider } from "@tanstack/react-query"
import { renderToStaticMarkup } from "react-dom/server"
import { afterEach, describe, expect, it, vi } from "vitest"

import { datasetKeys, type DatasetStatus } from "@/features/datasets/api"
import {
  countByStatus,
  DATASET_STATUSES,
  datasetStatusLabels,
  datasetStatusStyles,
  filterByStatus,
  parseStatusFilter,
  statusFilterParam,
} from "@/features/datasets/dataset-status"
import {
  DatasetStatusBadge,
  DatasetStatusPicker,
  setDatasetStatus,
} from "@/features/datasets/dataset-status-picker"
import { dataset } from "@/features/data-versions/test-fixtures"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
  queryClient.clear()
})

describe("status filter", () => {
  it("round-trips every value through the address bar", () => {
    for (const filter of [null, "", ...DATASET_STATUSES] as const) {
      expect(parseStatusFilter(statusFilterParam(filter))).toBe(filter)
    }
    expect(statusFilterParam("")).toBe("none")
    expect(statusFilterParam("trainable")).toBe("trainable")
  })

  it("ignores unknown values", () => {
    for (const value of ["", "all", "Trainable", "done", "可训练"]) {
      expect(parseStatusFilter(value)).toBeNull()
    }
  })

  it("filters and counts, unset included", () => {
    const items = [
      { id: 1, status: "trainable" },
      { id: 2, status: "" },
      { id: 3, status: "trainable" },
      { id: 4, status: "reviewing" },
    ] satisfies { id: number; status: DatasetStatus }[]
    expect(filterByStatus(items, null)).toBe(items)
    expect(filterByStatus(items, "trainable").map((d) => d.id)).toEqual([1, 3])
    expect(filterByStatus(items, "").map((d) => d.id)).toEqual([2])
    expect(filterByStatus(items, "annotating")).toEqual([])
    expect(countByStatus(items)).toEqual({
      "": 1,
      pending_annotation: 0,
      annotating: 0,
      pending_review: 0,
      reviewing: 1,
      trainable: 2,
    })
  })

  it("names and colours every status", () => {
    const statuses: DatasetStatus[] = ["", ...DATASET_STATUSES]
    expect(statuses.map((s) => datasetStatusLabels[s])).toEqual([
      "未设置",
      "待标注",
      "标注中",
      "待审图",
      "审图中",
      "可训练",
    ])
    const badges = new Set(statuses.map((s) => datasetStatusStyles[s].badge))
    expect(badges.size).toBe(statuses.length)
  })
})

describe("status badges", () => {
  function render(element: React.ReactNode) {
    return renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
    )
  }

  it("hides an unset status unless asked", () => {
    expect(render(<DatasetStatusBadge status="" />)).toBe("")
    expect(render(<DatasetStatusBadge status="" showUnset />)).toContain(
      "未设置"
    )
    expect(render(<DatasetStatusBadge status="pending_review" />)).toContain(
      "待审图"
    )
  })

  it("offers to set the status of an unmarked dataset", () => {
    const html = render(<DatasetStatusPicker dataset={dataset()} />)
    expect(html).toContain("设置状态")
    expect(html).toContain("数据集状态：未设置，点击修改")
    const marked = render(
      <DatasetStatusPicker dataset={dataset({ status: "annotating" })} />
    )
    expect(marked).toContain("标注中")
    expect(marked).not.toContain("设置状态")
  })
})

describe("setDatasetStatus", () => {
  it("sends only the status and updates the cached dataset", async () => {
    const before = dataset({ id: 7 })
    const other = dataset({ id: 8, name: "其它" })
    const after = {
      ...before,
      status: "trainable",
      status_updated_at: "2026-11-09T08:00:00Z",
    } as const
    queryClient.setQueryData(datasetKeys.detail(7), before)
    queryClient.setQueryData(datasetKeys.list("", null), {
      items: [before, other],
      total: 2,
    })
    const fetchMock = vi.fn(async () => Response.json(after))
    vi.stubGlobal("fetch", fetchMock)

    await expect(setDatasetStatus(before, "trainable")).resolves.toEqual(after)

    const [path, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(path).toBe("/api/v1/datasets/7")
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(String(init.body))).toEqual({ status: "trainable" })
    expect(queryClient.getQueryData(datasetKeys.detail(7))).toEqual(after)
    expect(
      queryClient.getQueryData<{ items: unknown[] }>(datasetKeys.list("", null))
        ?.items
    ).toEqual([after, other])
  })
})
