import { describe, expect, it } from "vitest"

import type {
  RecycleBin,
  TrashedDataset,
  TrashedDataVersion,
  TrashedModel,
  TrashedVersion,
} from "@/features/recycle-bin/api"
import {
  dataVersionAddedBytes,
  filterRecycleItems,
  purgeCountdown,
  recycleItems,
  remainingFraction,
  restoreBlockedReason,
  suggestRestoreName,
  type RecycleKind,
} from "@/features/recycle-bin/recycle-format"

const HOUR = 3_600_000
const DAY = 24 * HOUR
const now = Date.parse("2026-11-06T12:00:00Z")
const at = (offset: number) => new Date(now + offset).toISOString()

function dataset(id: number, name: string, deletedAgo: number): TrashedDataset {
  return {
    id,
    name,
    description: "",
    dir_name: name,
    deleted_at: at(-deletedAgo),
    purge_at: at(7 * DAY - deletedAgo),
  } as TrashedDataset
}

function version(
  id: number,
  name: string,
  deletedAgo: number,
  datasetDeleted = false
): TrashedVersion {
  return {
    id,
    name,
    note: "第二轮",
    dataset_name: "划痕",
    dataset_deleted: datasetDeleted,
    deleted_at: at(-deletedAgo),
    purge_at: at(7 * DAY - deletedAgo),
  } as TrashedVersion
}

function model(id: number, deletedAgo: number): TrashedModel {
  return {
    id,
    version: 3,
    project: { id: 1, name: "A 线外观" },
    description: "",
    file_name: "best.pt",
    deleted_at: at(-deletedAgo),
    purge_at: at(7 * DAY - deletedAgo),
  } as TrashedModel
}

function dataVersion(
  id: number,
  deletedAgo: number,
  projectDeleted = false
): TrashedDataVersion {
  return {
    id,
    project_id: 1,
    project_name: "A 线外观",
    version: 4,
    note: "加入 3 号线的样本",
    sources: [{ dataset_id: 3, dataset_name: "Line3-NG" }],
    stats: {
      storage: { copied_bytes: 1_000, generated_bytes: 500 },
    },
    project_deleted: projectDeleted,
    deleted_at: at(-deletedAgo),
    purge_at: at(7 * DAY - deletedAgo),
  } as TrashedDataVersion
}

const bin: RecycleBin = {
  retention_days: 7,
  datasets: [dataset(1, "划痕", 2 * DAY), dataset(2, "Dent", HOUR)],
  versions: [version(9, "审图1.3", 3 * HOUR, true)],
  data_versions: [dataVersion(12, 3 * DAY, true)],
  models: [model(5, 5 * DAY)],
}

describe("recycleItems", () => {
  it("merges the lists, most recently deleted first", () => {
    const items = recycleItems(bin)
    expect(items.map((item) => [item.key, item.title])).toEqual([
      ["dataset-2", "Dent"],
      ["version-9", "审图1.3"],
      ["dataset-1", "划痕"],
      ["data_version-12", "A 线外观 · 数据版本 v4"],
      ["model-5", "A 线外观 v3"],
    ])
  })

  it("titles a data version with its project", () => {
    const [item] = recycleItems({
      ...bin,
      datasets: [],
      versions: [],
      models: [],
    })
    expect(item).toMatchObject({
      kind: "data_version",
      title: "A 线外观 · 数据版本 v4",
      deletedAt: at(-3 * DAY),
      purgeAt: at(4 * DAY),
      dataVersion: { id: 12, project_deleted: true },
    })
  })

  it("filters by kind and keyword", () => {
    const items = recycleItems(bin)
    expect(
      filterRecycleItems(items, { kind: "dataset", q: "" }).map((i) => i.key)
    ).toEqual(["dataset-2", "dataset-1"])
    // 名称不区分大小写；版本也按数据集名称与备注匹配，模型按文件名。
    expect(
      filterRecycleItems(items, { kind: null, q: " dent " }).map((i) => i.key)
    ).toEqual(["dataset-2"])
    expect(
      filterRecycleItems(items, { kind: null, q: "划痕" }).map((i) => i.key)
    ).toEqual(["version-9", "dataset-1"])
    expect(
      filterRecycleItems(items, { kind: null, q: "BEST" }).map((i) => i.key)
    ).toEqual(["model-5"])
    expect(filterRecycleItems(items, { kind: "model", q: "划痕" })).toEqual([])
  })

  it("finds data versions by project, note and source dataset", () => {
    const items = recycleItems(bin)
    const keys = (kind: RecycleKind | null, q: string) =>
      filterRecycleItems(items, { kind, q }).map((i) => i.key)
    expect(keys("data_version", "")).toEqual(["data_version-12"])
    expect(keys(null, "数据版本 V4")).toEqual(["data_version-12"])
    expect(keys(null, "3 号线")).toEqual(["data_version-12"])
    expect(keys(null, "line3")).toEqual(["data_version-12"])
    // 项目名称在标题中：同一项目的模型也匹配。
    expect(keys(null, "A 线外观")).toEqual(["data_version-12", "model-5"])
    expect(keys("data_version", "划痕")).toEqual([])
  })
})

describe("dataVersionAddedBytes", () => {
  it("counts copied and generated files, not hard links", () => {
    expect(dataVersionAddedBytes(dataVersion(1, HOUR))).toBe(1_500)
  })
})

describe("purgeCountdown", () => {
  it("counts whole days, then hours", () => {
    expect(purgeCountdown(at(7 * DAY), now)).toEqual({
      text: "7 天后永久删除",
      urgent: false,
    })
    expect(purgeCountdown(at(DAY + 23 * HOUR), now)).toEqual({
      text: "1 天后永久删除",
      urgent: false,
    })
    expect(purgeCountdown(at(5 * HOUR + 59 * 60_000), now)).toEqual({
      text: "5 小时后永久删除",
      urgent: true,
    })
  })

  it("says it is about to go once less than an hour is left", () => {
    for (const purgeAt of [at(30 * 60_000), at(-HOUR), "garbage"]) {
      expect(purgeCountdown(purgeAt, now)).toEqual({
        text: "即将永久删除",
        urgent: true,
      })
    }
  })
})

describe("remainingFraction", () => {
  it("is the share of the retention left", () => {
    expect(remainingFraction(at(0), at(7 * DAY), now)).toBe(1)
    expect(remainingFraction(at(-7 * DAY), at(0), now)).toBe(0)
    expect(remainingFraction(at(-DAY), at(DAY), now)).toBe(0.5)
    expect(remainingFraction(at(-9 * DAY), at(-2 * DAY), now)).toBe(0)
    expect(remainingFraction("x", at(DAY), now)).toBe(0)
  })
})

describe("restoreBlockedReason", () => {
  it("asks to restore the dataset of a version first", () => {
    const [blocked] = recycleItems({ ...bin, datasets: [], models: [] })
    expect(restoreBlockedReason(blocked)).toBe(
      "数据集“划痕”也在回收站中，请先恢复数据集"
    )
    const [free] = recycleItems({
      ...bin,
      datasets: [],
      models: [],
      versions: [version(9, "审图1.3", HOUR)],
    })
    expect(restoreBlockedReason(free)).toBeNull()
    // 数据版本与模型的项目在回收站中时随它们一起恢复，不需要先恢复什么。
    for (const item of recycleItems({ ...bin, versions: [] })) {
      expect(restoreBlockedReason(item)).toBeNull()
    }
  })
})

describe("suggestRestoreName", () => {
  it("adds a suffix that is not taken", () => {
    expect(suggestRestoreName("划痕", [])).toBe("划痕（恢复）")
    expect(suggestRestoreName("Dent", ["dent（恢复）"])).toBe("Dent（恢复 2）")
    expect(suggestRestoreName("Dent", ["Dent（恢复）", "Dent（恢复 2）"])).toBe(
      "Dent（恢复 3）"
    )
  })

  it("shortens long names to fit", () => {
    const name = "长".repeat(64)
    const suggested = suggestRestoreName(name, [])
    expect(Array.from(suggested)).toHaveLength(64)
    expect(suggested.endsWith("（恢复）")).toBe(true)
  })
})
