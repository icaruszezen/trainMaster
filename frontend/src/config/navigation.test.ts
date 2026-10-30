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
    expect(activeId("/models/inference")).toBe("inference")
    expect(activeId("/models/abc")).toBe("models")
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
