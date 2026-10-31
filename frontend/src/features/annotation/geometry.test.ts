import { describe, expect, it } from "vitest"

import {
  boxFromPoints,
  clampBox,
  fitBox,
  fitViewport,
  handleAt,
  handlePoint,
  hitCandidates,
  hitTest,
  moveBox,
  nearestScrollTop,
  normalizeBox,
  placeOverlay,
  resizeBox,
  resizeEdges,
  resizeGrabOffset,
  revealBox,
  scaleLimits,
  screenAnchor,
  squarePoint,
  toImage,
  toScreen,
  visibleHandles,
  wheelZoomFactor,
  zoomAt,
  zoomTo,
  type Box,
} from "@/features/annotation/geometry"

const size = { width: 100, height: 80 }
const limits = { min: 0.1, max: 32 }

describe("fitViewport", () => {
  it("fits and centers the image with padding", () => {
    expect(
      fitViewport({ width: 200, height: 100 }, { width: 448, height: 448 })
    ).toEqual({ scale: 2, x: 24, y: 124 })
  })

  it("does not enlarge small images beyond maxFit", () => {
    const viewport = fitViewport(
      { width: 10, height: 10 },
      { width: 1000, height: 1000 }
    )
    expect(viewport.scale).toBe(4)
    expect(viewport.x).toBe(480)
  })

  it("survives a container smaller than the padding", () => {
    const viewport = fitViewport(
      { width: 100, height: 100 },
      { width: 10, height: 10 }
    )
    expect(viewport.scale).toBeGreaterThan(0)
  })
})

describe("scaleLimits", () => {
  it("allows zooming out to a quarter of the fitted scale, at most 1", () => {
    expect(
      scaleLimits({ width: 4000, height: 4000 }, { width: 448, height: 448 })
    ).toEqual({ min: 0.025, max: 32 })
    expect(
      scaleLimits({ width: 10, height: 10 }, { width: 448, height: 448 }).min
    ).toBe(1)
  })
})

describe("zoom", () => {
  it("keeps the image point under the anchor in place", () => {
    const viewport = { scale: 2, x: 10, y: 20 }
    const anchor = { x: 110, y: 70 }
    const before = toImage(viewport, anchor)
    const zoomed = zoomAt(viewport, 1.5, anchor, limits)
    expect(zoomed.scale).toBe(3)
    expect(toImage(zoomed, anchor)).toEqual(before)
  })

  it("clamps to the limits", () => {
    const viewport = { scale: 20, x: 0, y: 0 }
    expect(zoomAt(viewport, 4, { x: 0, y: 0 }, limits).scale).toBe(32)
    expect(zoomTo(viewport, 1, { x: 0, y: 0 }, limits).scale).toBeCloseTo(1)
    expect(zoomTo(viewport, 0.01, { x: 0, y: 0 }, limits).scale).toBe(0.1)
  })

  it("converts between screen and image coordinates", () => {
    const viewport = { scale: 2.5, x: -30, y: 12 }
    const point = { x: 13, y: 7 }
    expect(toImage(viewport, toScreen(viewport, point))).toEqual(point)
  })
})

describe("boxFromPoints", () => {
  it("orders, rounds and clamps the corners", () => {
    expect(boxFromPoints({ x: 50.6, y: 90 }, { x: -5, y: 10.2 }, size)).toEqual(
      { xmin: 0, ymin: 10, xmax: 51, ymax: 80 }
    )
  })

  it("returns null below one pixel", () => {
    expect(boxFromPoints({ x: 10, y: 10 }, { x: 10.3, y: 40 }, size)).toBeNull()
    expect(boxFromPoints({ x: 120, y: 10 }, { x: 130, y: 40 }, size)).toBeNull()
  })
})

describe("clampBox", () => {
  it("clamps boxes partly outside the image", () => {
    expect(
      clampBox({ xmin: -10, ymin: 70, xmax: 20, ymax: 200 }, size)
    ).toEqual({ xmin: 0, ymin: 70, xmax: 20, ymax: 80 })
  })

  it("drops boxes entirely outside", () => {
    expect(
      clampBox({ xmin: 120, ymin: 0, xmax: 150, ymax: 10 }, size)
    ).toBeNull()
  })
})

describe("moveBox", () => {
  it("moves within the image, keeping the size", () => {
    const box = { xmin: 10, ymin: 10, xmax: 30, ymax: 20 }
    expect(moveBox(box, 5, -3, size)).toEqual({
      xmin: 15,
      ymin: 7,
      xmax: 35,
      ymax: 17,
    })
    expect(moveBox(box, 500, -500, size)).toEqual({
      xmin: 80,
      ymin: 0,
      xmax: 100,
      ymax: 10,
    })
  })
})

describe("resizeBox", () => {
  const box = { xmin: 10, ymin: 10, xmax: 30, ymax: 30 }

  it("moves the dragged edges only", () => {
    expect(resizeBox(box, "se", { x: 40.4, y: 50.6 }, size)).toEqual({
      xmin: 10,
      ymin: 10,
      xmax: 40,
      ymax: 51,
    })
    expect(resizeBox(box, "n", { x: 99, y: 5 }, size)).toEqual({
      xmin: 10,
      ymin: 5,
      xmax: 30,
      ymax: 30,
    })
  })

  it("flips when an edge crosses the opposite one", () => {
    expect(resizeBox(box, "w", { x: 50, y: 0 }, size)).toEqual({
      xmin: 30,
      ymin: 10,
      xmax: 50,
      ymax: 30,
    })
  })

  it("keeps at least one pixel", () => {
    expect(resizeBox(box, "e", { x: 10, y: 0 }, size)).toEqual({
      xmin: 10,
      ymin: 10,
      xmax: 11,
      ymax: 30,
    })
  })
})

describe("resizeEdges", () => {
  it("moves the right and bottom edges within the image", () => {
    const box = { xmin: 10, ymin: 10, xmax: 30, ymax: 30 }
    expect(resizeEdges(box, 10, -5, size)).toEqual({
      xmin: 10,
      ymin: 10,
      xmax: 40,
      ymax: 25,
    })
    expect(resizeEdges(box, -100, 100, size)).toEqual({
      xmin: 10,
      ymin: 10,
      xmax: 11,
      ymax: 80,
    })
  })
})

describe("squarePoint", () => {
  it("uses the longer side in the drag direction", () => {
    expect(squarePoint({ x: 10, y: 10 }, { x: 15, y: -20 })).toEqual({
      x: 40,
      y: -20,
    })
  })
})

describe("normalizeBox", () => {
  it("grows inward at the image edge", () => {
    expect(
      normalizeBox({ xmin: 100, ymin: 80, xmax: 100, ymax: 80 }, size)
    ).toEqual({ xmin: 99, ymin: 79, xmax: 100, ymax: 80 })
  })
})

describe("handles", () => {
  const viewport = { scale: 2, x: 0, y: 0 }

  it("shows eight handles on large boxes", () => {
    const box = { xmin: 10, ymin: 10, xmax: 40, ymax: 40 }
    expect(visibleHandles(viewport, box)).toHaveLength(8)
    expect(handleAt(viewport, box, { x: 51, y: 19 })).toBe("n")
    expect(handleAt(viewport, box, { x: 81, y: 81 })).toBe("se")
    expect(handleAt(viewport, box, { x: 50, y: 50 })).toBeNull()
  })

  it("puts the corner handles of small boxes outside the box", () => {
    const box = { xmin: 10, ymin: 10, xmax: 15, ymax: 15 } // 10×10 on screen
    expect(visibleHandles(viewport, box)).toEqual(["nw", "ne", "se", "sw"])
    expect(handlePoint(viewport, box, "nw")).toEqual({ x: 14, y: 14 })
    // The middle of a small box is for moving it, not for a handle.
    expect(handleAt(viewport, box, { x: 25, y: 25 }, 4)).toBeNull()
  })
})

describe("resizeGrabOffset", () => {
  const viewport = { scale: 2, x: 0, y: 0 }
  const resizeTo = (box: Box, pointer: { x: number; y: number }) => {
    const offset = resizeGrabOffset(viewport, box, "nw", { x: 14, y: 14 })
    return resizeBox(
      box,
      "nw",
      toImage(viewport, { x: pointer.x + offset.x, y: pointer.y + offset.y }),
      size
    )
  }

  it("is the distance from the pointer to the edge, not to the drawn handle", () => {
    const small = { xmin: 10, ymin: 10, xmax: 15, ymax: 15 }
    // The corner handle of a small box is drawn 6px outside the corner.
    expect(resizeGrabOffset(viewport, small, "nw", { x: 14, y: 14 })).toEqual({
      x: 6,
      y: 6,
    })
    const large = { xmin: 10, ymin: 10, xmax: 40, ymax: 40 }
    expect(resizeGrabOffset(viewport, large, "e", { x: 83, y: 30 })).toEqual({
      x: -3,
      y: 20,
    })
  })

  it("moves the edge by the pointer delta instead of jumping to the pointer", () => {
    const small = { xmin: 10, ymin: 10, xmax: 15, ymax: 15 }
    expect(resizeTo(small, { x: 14, y: 14 })).toEqual(small)
    expect(resizeTo(small, { x: 15, y: 15 })).toEqual({
      xmin: 11,
      ymin: 11,
      xmax: 15,
      ymax: 15,
    })
    expect(resizeTo(small, { x: 10, y: 12 })).toEqual({
      xmin: 8,
      ymin: 9,
      xmax: 15,
      ymax: 15,
    })
  })
})

describe("hitCandidates", () => {
  const boxes: (Box | null)[] = [
    { xmin: 0, ymin: 0, xmax: 50, ymax: 50 },
    { xmin: 10, ymin: 10, xmax: 20, ymax: 20 },
    null, // hidden
    { xmin: 0, ymin: 0, xmax: 50, ymax: 50 },
  ]

  it("lists smaller boxes first, later boxes first on ties", () => {
    expect(hitCandidates(boxes, { x: 15, y: 15 })).toEqual([1, 3, 0])
    expect(hitTest(boxes, { x: 40, y: 40 })).toBe(3)
  })

  it("skips hidden boxes and honors the tolerance", () => {
    expect(hitTest([null], { x: 0, y: 0 })).toBe(-1)
    expect(hitTest(boxes.slice(1, 2), { x: 22, y: 15 })).toBe(-1)
    expect(hitTest(boxes.slice(1, 2), { x: 22, y: 15 }, 3)).toBe(0)
  })
})

describe("revealBox and fitBox", () => {
  const container = { width: 400, height: 300 }

  it("leaves visible boxes alone", () => {
    const viewport = { scale: 1, x: 0, y: 0 }
    expect(
      revealBox(
        viewport,
        { xmin: 10, ymin: 10, xmax: 50, ymax: 50 },
        container,
        limits
      )
    ).toBe(viewport)
  })

  it("centers boxes outside the view", () => {
    const viewport = revealBox(
      { scale: 1, x: 0, y: 0 },
      { xmin: 1000, ymin: 1000, xmax: 1100, ymax: 1100 },
      container,
      limits
    )
    expect(viewport).toEqual({ scale: 1, x: -850, y: -900 })
  })

  it("zooms to fill the view", () => {
    const viewport = fitBox(
      { xmin: 0, ymin: 0, xmax: 10, ymax: 10 },
      container,
      limits
    )
    expect(viewport.scale).toBeCloseTo(20.4)
    expect(viewport.x).toBeCloseTo(98)
    expect(viewport.y).toBeCloseTo(48)
  })
})

describe("wheelZoomFactor", () => {
  it("zooms in for negative deltas and caps each step", () => {
    expect(wheelZoomFactor(-100, 0)).toBeCloseTo(Math.exp(0.14))
    expect(wheelZoomFactor(100, 0)).toBeLessThan(1)
    expect(wheelZoomFactor(-10000, 0)).toBe(1.3)
    expect(wheelZoomFactor(10000, 0)).toBeCloseTo(1 / 1.3)
  })

  it("converts lines and pages to pixels", () => {
    expect(wheelZoomFactor(-3, 1)).toBeCloseTo(wheelZoomFactor(-48, 0))
    expect(wheelZoomFactor(1, 2)).toBeCloseTo(1 / 1.3)
  })
})

describe("screenAnchor", () => {
  it("is the left, top and bottom edge on screen, inside the container", () => {
    const viewport = { scale: 2, x: 10, y: 10 }
    const bounds = { width: 400, height: 300 }
    expect(
      screenAnchor(viewport, { xmin: 5, ymin: 5, xmax: 10, ymax: 10 }, bounds)
    ).toEqual({ x: 20, top: 20, bottom: 30, bounds })
    expect(
      screenAnchor(
        viewport,
        { xmin: -50, ymin: 100, xmax: 0, ymax: 200 },
        bounds
      )
    ).toEqual({ x: 0, top: 210, bottom: 300, bounds })
    expect(
      screenAnchor(viewport, { xmin: 0, ymin: -50, xmax: 5, ymax: -20 }, bounds)
    ).toEqual({ x: 10, top: 0, bottom: 0, bounds })
  })
})

describe("placeOverlay", () => {
  const bounds = { width: 600, height: 400 }
  const size = { width: 256, height: 200 }

  it("goes below the box, aligned with its left edge", () => {
    expect(
      placeOverlay({ x: 100, top: 50, bottom: 120, bounds }, size)
    ).toEqual({ x: 100, y: 126 })
  })

  it("shifts left at the right edge and keeps a margin on the left", () => {
    expect(
      placeOverlay({ x: 500, top: 50, bottom: 120, bounds }, size)
    ).toEqual({ x: 336, y: 126 })
    expect(
      placeOverlay(
        { x: 0, top: 50, bottom: 120, bounds: { width: 200, height: 400 } },
        size
      )
    ).toEqual({ x: 8, y: 126 })
  })

  it("flips above the box when it would overflow the bottom", () => {
    expect(
      placeOverlay({ x: 100, top: 250, bottom: 300, bounds }, size)
    ).toEqual({ x: 100, y: 44 })
    // Boxes below the visible area are anchored at the bottom edge.
    expect(
      placeOverlay({ x: 100, top: 400, bottom: 400, bounds }, size)
    ).toEqual({ x: 100, y: 194 })
  })

  it("stays inside the canvas when it fits on neither side", () => {
    // More room below: as low as fits.
    expect(
      placeOverlay({ x: 100, top: 180, bottom: 220, bounds }, size)
    ).toEqual({ x: 100, y: 192 })
    // More room above: as high as fits.
    expect(
      placeOverlay({ x: 100, top: 210, bottom: 260, bounds }, size)
    ).toEqual({ x: 100, y: 8 })
    // Taller than the canvas: top-aligned so the input stays visible.
    expect(
      placeOverlay(
        { x: 100, top: 80, bottom: 120, bounds: { width: 600, height: 150 } },
        size
      )
    ).toEqual({ x: 100, y: 8 })
  })

  it("fits exactly when its height is capped to the canvas minus the margins", () => {
    // The label picker caps its height at 100% - 2 * margin on short canvases.
    const short = { width: 667, height: 175 }
    const capped = { width: 256, height: 175 - 16 }
    for (const [top, bottom] of [
      [0, 20],
      [70, 100],
      [150, 175],
    ]) {
      const { y } = placeOverlay({ x: 100, top, bottom, bounds: short }, capped)
      expect(y).toBe(8)
      expect(y + capped.height).toBe(short.height - 8)
    }
  })
})

describe("nearestScrollTop", () => {
  it("scrolls only as far as needed to show the item", () => {
    expect(nearestScrollTop(0, 240, 64, 32)).toBe(0)
    expect(nearestScrollTop(0, 240, 288, 32)).toBe(80)
    expect(nearestScrollTop(200, 240, 96, 32)).toBe(96)
    expect(nearestScrollTop(0, 20, 64, 32)).toBe(64)
  })
})
