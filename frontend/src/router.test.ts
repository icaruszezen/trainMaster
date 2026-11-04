import { matchRoutes } from "react-router"
import { describe, expect, it } from "vitest"

import { navItems } from "@/config/navigation"
import { DatasetDetailPage } from "@/features/datasets/dataset-detail-page"
import { DatasetVersionsPage } from "@/features/datasets/dataset-versions-page"
import { InferencePage } from "@/features/models/inference-page"
import { ModelDetailPage } from "@/features/models/model-detail-page"
import { ModelsPage } from "@/features/models/models-page"
import { ProjectDetailPage } from "@/features/projects/project-detail-page"
import { ReviewDetailPage } from "@/features/reviews/review-detail-page"
import { ReviewWorkspacePage } from "@/features/reviews/review-workspace-page"
import { ReviewsPage } from "@/features/reviews/reviews-page"
import { NotFoundPage } from "@/pages/not-found-page"
import { pages, routes } from "@/router"

function leaf(pathname: string) {
  return matchRoutes(routes, pathname)?.at(-1)?.route
}

function leafComponent(pathname: string) {
  return leaf(pathname)?.Component
}

describe("routes", () => {
  it("renders the registered page for every navigation item", () => {
    for (const item of navItems) {
      expect(pages[item.id], item.id).toBeTypeOf("function")
      expect(leafComponent(item.path), item.path).toBe(pages[item.id])
    }
  })

  it("does not reuse a page component across modules", () => {
    const components = Object.values(pages)
    expect(new Set(components).size).toBe(components.length)
  })

  it("falls back to the 404 page for unknown paths", () => {
    expect(leafComponent("/nope")).toBe(NotFoundPage)
    expect(leafComponent("/datasets/123/extra")).toBe(NotFoundPage)
  })

  it("routes dataset ids to the detail page without shadowing versions", () => {
    expect(leafComponent("/datasets/123")).toBe(DatasetDetailPage)
    expect(leafComponent("/datasets/versions")).toBe(DatasetVersionsPage)
    expect(leaf("/datasets/123")?.handle).toBeUndefined()
  })

  it("routes project ids to the project detail page", () => {
    expect(leafComponent("/projects/7")).toBe(ProjectDetailPage)
    expect(leafComponent("/projects/7/extra")).toBe(NotFoundPage)
    expect(leaf("/projects/7")?.handle).toBeUndefined()
  })

  it("routes model ids to the model detail page without shadowing inference", () => {
    expect(leafComponent("/models")).toBe(ModelsPage)
    expect(leafComponent("/models/5")).toBe(ModelDetailPage)
    expect(leafComponent("/models/inference")).toBe(InferencePage)
    expect(leafComponent("/models/5/extra")).toBe(NotFoundPage)
    expect(leaf("/models/5")?.handle).toBeUndefined()
  })

  it("routes reviews under the model repository", () => {
    expect(leafComponent("/models/reviews")).toBe(ReviewsPage)
    expect(leafComponent("/models/reviews/3")).toBe(ReviewDetailPage)
    expect(leaf("/models/reviews/3")?.handle).toBeUndefined()
    expect(leafComponent("/models/reviews/3/workspace")).toBe(
      ReviewWorkspacePage
    )
    expect(leaf("/models/reviews/3/workspace")?.handle).toEqual({
      fullBleed: true,
    })
    expect(leafComponent("/models/reviews/3/extra")).toBe(NotFoundPage)
  })

  it("marks only the 404 route as not found", () => {
    expect(leaf("/models/5/extra")?.handle).toEqual({ notFound: true })
    expect(leaf("/annotation/labelz")?.handle).toEqual({ notFound: true })
    for (const item of navItems) {
      expect(leaf(item.path)?.handle, item.path).toBeUndefined()
    }
  })
})
