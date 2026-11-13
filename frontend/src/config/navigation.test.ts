import { describe, expect, it } from "vitest"

import {
  findActiveNavItem,
  getNavItem,
  navGroups,
  navItems,
  type ModuleId,
} from "@/config/navigation"

describe("navigation config", () => {
  it("has unique ids and paths", () => {
    const ids = navItems.map((item) => item.id)
    const paths = navItems.map((item) => item.path)
    expect(new Set(ids).size).toBe(ids.length)
    expect(new Set(paths).size).toBe(paths.length)
    expect(new Set(navGroups.map((group) => group.id)).size).toBe(
      navGroups.length
    )
  })

  it("uses absolute paths without trailing slashes", () => {
    for (const item of navItems) {
      expect(item.path).toMatch(/^\/([a-z0-9-]+(\/[a-z0-9-]+)*)?$/)
    }
  })

  it("gives every group at least one item", () => {
    for (const group of navGroups) {
      expect(group.items.length).toBeGreaterThan(0)
    }
  })

  it("describes 3–5 planned features for every placeholder module", () => {
    for (const item of navItems.filter((i) => i.status === "planned")) {
      expect(item.features.length, item.id).toBeGreaterThanOrEqual(3)
      expect(item.features.length, item.id).toBeLessThanOrEqual(5)
    }
  })

  it("only reserves API paths under /api/v1/", () => {
    for (const item of navItems) {
      if (item.apiPath) {
        expect(item.apiPath).toMatch(/^\/api\/v1\//)
      }
    }
  })

  it("lists the available features of every implemented module", () => {
    for (const item of navItems.filter((i) => i.status === "ready")) {
      if (item.id !== "dashboard") {
        expect(item.features.length, item.id).toBeGreaterThan(0)
      }
    }
  })

  it("marks training jobs and compute modules as implemented with their APIs", () => {
    expect(getNavItem("training-jobs")).toMatchObject({
      status: "ready",
      path: "/training/jobs",
      apiPath: "/api/v1/training/jobs",
    })
    expect(getNavItem("compute")).toMatchObject({
      status: "ready",
      path: "/training/compute",
      apiPath: "/api/v1/compute",
    })
  })

  it("marks the model repository as implemented with its API", () => {
    expect(getNavItem("models")).toMatchObject({
      status: "ready",
      path: "/models",
      apiPath: "/api/v1/models",
    })
    // 推理测试仍是占位页，路径在模型仓库下面。
    expect(getNavItem("inference")).toMatchObject({
      status: "planned",
      path: "/models/inference",
    })
  })

  it("marks data versions as implemented with its API", () => {
    expect(getNavItem("dataset-versions")).toMatchObject({
      status: "ready",
      title: "数据版本",
      path: "/datasets/versions",
      apiPath: "/api/v1/data-versions",
    })
    // 审图版本（数据集详情与回收站）用 GitBranch，数据版本用另一个图标。
    const icon = getNavItem("dataset-versions").icon
    expect(
      navItems.filter((item) => item.icon === icon).map((item) => item.id)
    ).toEqual(["dataset-versions"])
  })

  it("lists model review under the data management group", () => {
    expect(getNavItem("reviews")).toMatchObject({
      status: "ready",
      title: "模型审图",
      path: "/datasets/reviews",
      apiPath: "/api/v1/reviews",
    })
    const data = navGroups.find((g) => g.id === "data")
    expect(data?.items.map((item) => item.id)).toEqual([
      "datasets",
      "reviews",
      "dataset-versions",
    ])
    const models = navGroups.find((g) => g.id === "models")
    expect(models?.items.map((item) => item.id)).toEqual([
      "models",
      "inference",
    ])
  })

  it("lists the recycle bin under system management", () => {
    expect(getNavItem("recycle-bin")).toMatchObject({
      status: "ready",
      title: "回收站",
      path: "/system/recycle-bin",
      apiPath: "/api/v1/recycle-bin",
    })
    expect(findActiveNavItem("/system/recycle-bin")).toMatchObject({
      group: { id: "system" },
      item: { id: "recycle-bin" },
      exact: true,
    })
  })

  it("looks up items by id", () => {
    expect(getNavItem("datasets").path).toBe("/datasets")
    expect(() => getNavItem("nope" as ModuleId)).toThrow()
  })
})

describe("findActiveNavItem", () => {
  const activeId = (pathname: string) => findActiveNavItem(pathname)?.item.id

  it("matches the dashboard only on the root path", () => {
    expect(activeId("/")).toBe("dashboard")
    expect(activeId("/nope")).toBeUndefined()
  })

  it("prefers the longest matching prefix", () => {
    expect(activeId("/datasets")).toBe("datasets")
    expect(activeId("/datasets/versions")).toBe("dataset-versions")
    expect(activeId("/datasets/versions/42")).toBe("dataset-versions")
    expect(activeId("/datasets/123")).toBe("datasets")
    expect(activeId("/projects/7")).toBe("projects")
    expect(activeId("/models/inference")).toBe("inference")
    expect(activeId("/models/abc")).toBe("models")
    expect(activeId("/models/12")).toBe("models")
    expect(activeId("/datasets/reviews")).toBe("reviews")
    expect(activeId("/datasets/reviews/3/workspace")).toBe("reviews")
  })

  it("matches whole path segments only", () => {
    expect(activeId("/datasets-archive")).toBeUndefined()
    expect(activeId("/evaluations")).toBeUndefined()
  })

  it("ignores trailing slashes and reports exact matches", () => {
    expect(findActiveNavItem("/datasets/")).toMatchObject({
      item: { id: "datasets" },
      exact: true,
    })
    expect(findActiveNavItem("/datasets/123")?.exact).toBe(false)
  })

  it("returns the group of the active item", () => {
    expect(findActiveNavItem("/annotation/labels")?.group.title).toBe("标注")
  })
})
