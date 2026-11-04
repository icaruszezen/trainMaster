import { describe, expect, it } from "vitest"

import {
  cycleSelection,
  documentReducer,
  duplicateBox,
  emptyDocument,
  HISTORY_LIMIT,
  pasteObjects,
  removeObject,
  serializeObjects,
  updateObject,
  type DocumentState,
  type EditorObject,
} from "@/features/annotation/document"
import type { AnnotationObject } from "@/features/datasets/api"

const box = (
  name: string,
  xmin = 0,
  ymin = 0,
  xmax = 10,
  ymax = 10
): AnnotationObject => ({
  name,
  xmin,
  ymin,
  xmax,
  ymax,
  difficult: false,
  truncated: false,
})

function loaded(...objects: AnnotationObject[]): DocumentState {
  return documentReducer(emptyDocument, { type: "load", objects, key: "img" })
}

describe("documentReducer", () => {
  it("loads objects with stable uids and no history", () => {
    const extra = { ...box("dent"), extra: true } as AnnotationObject
    const state = loaded(extra, box("scratch"))
    expect(state.objects.map((o) => o.uid)).toEqual(["img:0", "img:1"])
    expect(state.objects[0]).not.toHaveProperty("extra")
    expect(state.past).toEqual([])
    expect(state.selected).toBeNull()
  })

  it("records changes and undoes / redoes them", () => {
    const start = loaded(box("dent"))
    const added: EditorObject[] = [
      ...start.objects,
      { ...box("scratch"), uid: "new" },
    ]
    const changed = documentReducer(start, {
      type: "change",
      objects: added,
      select: "new",
    })
    expect(changed.selected).toBe("new")
    expect(changed.past).toEqual([start.objects])

    const undone = documentReducer(changed, { type: "undo" })
    expect(undone.objects).toBe(start.objects)
    expect(undone.selected).toBeNull() // "new" no longer exists
    expect(undone.future).toEqual([added])

    const redone = documentReducer(undone, { type: "redo" })
    expect(redone.objects).toBe(added)
    expect(redone.future).toEqual([])
  })

  it("ignores no-op changes and empty history", () => {
    const start = loaded(box("dent"))
    expect(
      documentReducer(start, { type: "change", objects: start.objects })
    ).toBe(start)
    expect(documentReducer(start, { type: "undo" })).toBe(start)
    expect(documentReducer(start, { type: "redo" })).toBe(start)
  })

  it("drops the redo history on a new change", () => {
    let state = loaded(box("a"))
    state = documentReducer(state, { type: "change", objects: [] })
    state = documentReducer(state, { type: "undo" })
    state = documentReducer(state, {
      type: "change",
      objects: [{ ...box("b"), uid: "b" }],
    })
    expect(state.future).toEqual([])
  })

  it("keeps at most HISTORY_LIMIT steps", () => {
    let state = loaded()
    for (let i = 0; i < HISTORY_LIMIT + 5; i += 1) {
      state = documentReducer(state, {
        type: "change",
        objects: [{ ...box(`c${i}`), uid: `u${i}` }],
      })
    }
    expect(state.past).toHaveLength(HISTORY_LIMIT)
  })

  it("appends to the current objects, only for the same document", () => {
    const start = loaded(box("a"))
    // An edit made while the async operation was running is kept.
    const edited = documentReducer(start, {
      type: "change",
      objects: [...start.objects, { ...box("b"), uid: "b" }],
    })
    const pasted = { ...box("p"), uid: "p" }
    const appended = documentReducer(edited, {
      type: "append",
      key: "img",
      objects: [pasted],
      select: "p",
    })
    expect(appended.objects.map((o) => o.uid)).toEqual(["img:0", "b", "p"])
    expect(appended.selected).toBe("p")
    expect(appended.past.at(-1)).toBe(edited.objects)

    // Another image (or a reload) was loaded meanwhile: ignored.
    const other = documentReducer(edited, {
      type: "load",
      objects: [box("x")],
      key: "other",
    })
    expect(
      documentReducer(other, { type: "append", key: "img", objects: [pasted] })
    ).toBe(other)
    expect(
      documentReducer(edited, { type: "append", key: "img", objects: [] })
    ).toBe(edited)
  })

  it("keeps the key across edits, undo and redo", () => {
    let state = loaded(box("a"))
    state = documentReducer(state, { type: "change", objects: [] })
    expect(state.key).toBe("img")
    state = documentReducer(state, { type: "undo" })
    expect(state.key).toBe("img")
    state = documentReducer(state, { type: "redo" })
    expect(state.key).toBe("img")
  })

  it("selects only existing objects", () => {
    const state = loaded(box("dent"))
    expect(
      documentReducer(state, { type: "select", uid: "img:0" }).selected
    ).toBe("img:0")
    expect(
      documentReducer(state, { type: "select", uid: "missing" }).selected
    ).toBeNull()
  })
})

describe("object edits", () => {
  const objects = loaded(box("dent"), box("scratch", 5, 5, 20, 20)).objects

  it("updateObject returns the same array when nothing changes", () => {
    expect(updateObject(objects, "img:0", { name: "dent" })).toBe(objects)
    const next = updateObject(objects, "img:0", { difficult: true })
    expect(next).not.toBe(objects)
    expect(next[0].difficult).toBe(true)
    expect(next[1]).toBe(objects[1])
  })

  it("removeObject", () => {
    expect(removeObject(objects, "missing")).toBe(objects)
    expect(removeObject(objects, "img:0").map((o) => o.name)).toEqual([
      "scratch",
    ])
  })

  it("serializeObjects ignores uids and field order", () => {
    const reordered = objects.map((o) => ({ ...o, uid: "other" }))
    expect(serializeObjects(reordered)).toBe(serializeObjects(objects))
  })
})

describe("duplicateBox and pasteObjects", () => {
  const size = { width: 100, height: 80 }

  it("offsets the copy and keeps it inside the image", () => {
    expect(
      duplicateBox({ xmin: 0, ymin: 0, xmax: 10, ymax: 10 }, size)
    ).toEqual({ xmin: 10, ymin: 10, xmax: 20, ymax: 20 })
    expect(
      duplicateBox({ xmin: 90, ymin: 70, xmax: 100, ymax: 80 }, size)
    ).toEqual({ xmin: 90, ymin: 70, xmax: 100, ymax: 80 })
  })

  it("clamps pasted boxes and drops those outside the image", () => {
    let n = 0
    const { objects, dropped } = pasteObjects(
      [box("a", 90, 70, 150, 120), box("b", 200, 200, 210, 210)],
      size,
      () => `p${(n += 1)}`
    )
    expect(dropped).toBe(1)
    expect(objects).toEqual([{ ...box("a", 90, 70, 100, 80), uid: "p1" }])
  })
})

describe("cycleSelection", () => {
  const objects = [{ uid: "a" }, { uid: "b" }, { uid: "c" }]

  it("starts at either end and moves in both directions", () => {
    expect(cycleSelection(objects, null, 1)).toBe("a")
    expect(cycleSelection(objects, null, -1)).toBe("c")
    expect(cycleSelection(objects, "missing", 1)).toBe("a")
    expect(cycleSelection(objects, "a", 1)).toBe("b")
    expect(cycleSelection(objects, "c", -1)).toBe("b")
  })

  it("ends after the last / before the first box instead of wrapping", () => {
    expect(cycleSelection(objects, "c", 1)).toBeNull()
    expect(cycleSelection(objects, "a", -1)).toBeNull()
    expect(cycleSelection([], null, 1)).toBeNull()
    expect(cycleSelection([], null, -1)).toBeNull()
  })
})
