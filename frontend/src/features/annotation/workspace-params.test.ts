import { describe, expect, it } from "vitest"

import {
  hasFilters,
  leavesImage,
  NO_FILTERS,
  parseFilters,
  parseId,
  updateParams,
  workspacePath,
} from "@/features/annotation/workspace-params"

describe("parseId", () => {
  it("accepts positive integers only", () => {
    expect(parseId("12")).toBe(12)
    for (const value of [
      null,
      undefined,
      "",
      "0",
      "-1",
      "1.5",
      "012",
      "1e3",
      "abc",
    ]) {
      expect(parseId(value)).toBeNull()
    }
    expect(parseId("99999999999999999")).toBeNull() // beyond safe integers
  })
})

describe("parseFilters", () => {
  it("reads the filters, falling back to the defaults", () => {
    expect(parseFilters(new URLSearchParams(""))).toEqual(NO_FILTERS)
    expect(
      parseFilters(new URLSearchParams("status=unannotated&label=划痕&q=NG"))
    ).toEqual({ status: "unannotated", label: "划痕", q: "NG" })
    expect(parseFilters(new URLSearchParams("status=bogus")).status).toBe("all")
  })
})

describe("updateParams", () => {
  it("sets values and drops defaults, keeping other parameters", () => {
    const params = new URLSearchParams("image=3&status=empty&q=a&other=1")
    expect(
      updateParams(params, { image: 4, status: "all", q: "" }).toString()
    ).toBe("image=4&other=1")
    expect(params.toString()).toBe("image=3&status=empty&q=a&other=1")
  })
})

describe("hasFilters and workspacePath", () => {
  it("work", () => {
    expect(hasFilters(NO_FILTERS)).toBe(false)
    expect(hasFilters({ ...NO_FILTERS, label: "a" })).toBe(true)
    expect(workspacePath(5)).toBe("/annotation/workspace/5")
    expect(workspacePath(5, 7)).toBe("/annotation/workspace/5?image=7")
  })
})

describe("leavesImage", () => {
  const at = (search: string, pathname = "/annotation/workspace/1") => ({
    pathname,
    search,
  })

  it("is true for another image or another page", () => {
    expect(leavesImage(at("?image=1"), at("?image=2"))).toBe(true)
    expect(leavesImage(at("?image=1&status=unannotated"), at("?image=2"))).toBe(
      true
    )
    expect(leavesImage(at("?image=1"), at("", "/datasets/1"))).toBe(true)
    expect(
      leavesImage(at("?image=1"), at("?image=1", "/annotation/workspace/2"))
    ).toBe(true)
  })

  it("is false when only the filters change", () => {
    expect(
      leavesImage(at("?image=1"), at("?image=1&status=unannotated&q=a"))
    ).toBe(false)
    expect(leavesImage(at("?image=1&q=a"), at("?q=b&image=1"))).toBe(false)
  })
})
