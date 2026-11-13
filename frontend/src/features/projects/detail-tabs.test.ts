import { describe, expect, it } from "vitest"

import {
  parseDetailTab,
  projectTabPath,
  type DetailTab,
} from "@/features/projects/detail-tabs"

describe("parseDetailTab", () => {
  it("reads the tab from the address bar", () => {
    expect(parseDetailTab("classes")).toBe("classes")
    expect(parseDetailTab("data-versions")).toBe("data-versions")
    expect(parseDetailTab("training-jobs")).toBe("training-jobs")
    expect(parseDetailTab("models")).toBe("models")
  })

  it("keeps old links to the label distribution working", () => {
    expect(parseDetailTab("labels")).toBe("classes")
  })

  it("falls back to the datasets", () => {
    for (const value of [null, "", "datasets", "versions", "Models"]) {
      expect(parseDetailTab(value), String(value)).toBe("datasets")
    }
  })

  it("round-trips the links it builds", () => {
    const tabs: DetailTab[] = [
      "datasets",
      "classes",
      "data-versions",
      "training-jobs",
      "models",
    ]
    for (const tab of tabs) {
      const url = new URL(projectTabPath(3, tab), "http://localhost")
      expect(url.pathname).toBe("/projects/3")
      expect(parseDetailTab(url.searchParams.get("tab"))).toBe(tab)
    }
    expect(projectTabPath(3, "datasets")).toBe("/projects/3")
    expect(projectTabPath(3, "data-versions")).toBe(
      "/projects/3?tab=data-versions"
    )
    expect(projectTabPath(3, "training-jobs")).toBe(
      "/projects/3?tab=training-jobs"
    )
  })
})
