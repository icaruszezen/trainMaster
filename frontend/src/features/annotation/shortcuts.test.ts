import { afterEach, describe, expect, it, vi } from "vitest"

import {
  allowsRepeat,
  classHotkey,
  commandForKey,
  copiesSelectedText,
  focusAfterDialog,
  focusCanvas,
  leaveOverlay,
  preventTypeahead,
  shortcutHelp,
  shouldIgnoreKey,
  type KeyInput,
  type ShortcutContext,
} from "@/features/annotation/shortcuts"

/**
 * A stand-in for a DOM element (the tests run without a DOM): `closest()`
 * matches when one of the selectors in the list is one the element is in.
 */
class FakeElement {
  readonly isContentEditable = false
  readonly parentElement = null
  private readonly within: string[]
  constructor(within: string[]) {
    this.within = within
  }
  closest(selectors: string) {
    const list = selectors.split(",").map((part) => part.trim())
    return this.within.some((selector) => list.includes(selector)) ? this : null
  }
}

/** Runs `test` with `Element` / `HTMLElement` / `Node` set to {@link FakeElement}. */
function withDom(test: () => void) {
  vi.stubGlobal("Element", FakeElement)
  vi.stubGlobal("HTMLElement", FakeElement)
  vi.stubGlobal("Node", FakeElement)
  test()
}

afterEach(() => {
  vi.unstubAllGlobals()
})

/** An element inside the given selectors, typed as the event target it stands for. */
const element = (...within: string[]) =>
  new FakeElement(within) as unknown as EventTarget
const canvas = () => element('[role="application"]')
const body = () => element()
const combobox = () => element('[role="combobox"]')

const pc: ShortcutContext = { mac: false, hasSelection: false }
const mac: ShortcutContext = { mac: true, hasSelection: false }
const selected: ShortcutContext = { mac: false, hasSelection: true }

function key(code: string, modifiers: Partial<KeyInput> = {}): KeyInput {
  const letter = /^Key([A-Z])$/.exec(code)?.[1]
  return {
    key: letter ? letter.toLowerCase() : code,
    code,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...modifiers,
  }
}

describe("commandForKey", () => {
  it("maps LabelImg-style keys", () => {
    expect(commandForKey(key("KeyW"), pc)).toEqual({
      type: "tool",
      tool: "rect",
    })
    expect(commandForKey(key("KeyV"), pc)).toEqual({
      type: "tool",
      tool: "select",
    })
    expect(commandForKey(key("KeyD"), pc)).toEqual({
      type: "image",
      direction: 1,
      unannotated: false,
    })
    expect(commandForKey(key("KeyA", { shiftKey: true }), pc)).toEqual({
      type: "image",
      direction: -1,
      unannotated: true,
    })
    expect(commandForKey(key("Delete"), pc)).toEqual({ type: "delete" })
    expect(commandForKey(key("Backspace"), pc)).toEqual({ type: "delete" })
  })

  it("maps digits to classes, 0 to the tenth", () => {
    expect(commandForKey(key("Digit1"), pc)).toEqual({
      type: "class",
      index: 0,
    })
    expect(commandForKey(key("Numpad9"), pc)).toEqual({
      type: "class",
      index: 8,
    })
    expect(commandForKey(key("Digit0"), pc)).toEqual({
      type: "class",
      index: 9,
    })
    expect(commandForKey(key("Digit1", { shiftKey: true }), pc)).toBeNull()
  })

  it("uses the physical key, whatever the layout or caps lock", () => {
    expect(
      commandForKey({ ...key("KeyW"), key: "Z" }, pc) // AZERTY / caps lock
    ).toEqual({ type: "tool", tool: "rect" })
  })

  it("moves the selected box with the arrows, else changes the image", () => {
    expect(commandForKey(key("ArrowLeft"), selected)).toEqual({
      type: "nudge",
      dx: -1,
      dy: 0,
    })
    expect(
      commandForKey(key("ArrowDown", { shiftKey: true }), selected)
    ).toEqual({ type: "nudge", dx: 0, dy: 10 })
    expect(commandForKey(key("ArrowRight"), pc)).toEqual({
      type: "image",
      direction: 1,
      unannotated: false,
    })
    expect(commandForKey(key("ArrowUp"), pc)).toBeNull()
  })

  it("uses Ctrl on Windows / Linux and ⌘ on macOS", () => {
    expect(commandForKey(key("KeyS", { ctrlKey: true }), pc)).toEqual({
      type: "save",
    })
    expect(commandForKey(key("KeyS", { metaKey: true }), mac)).toEqual({
      type: "save",
    })
    expect(commandForKey(key("KeyS", { metaKey: true }), pc)).toBeNull()
    expect(commandForKey(key("KeyS", { ctrlKey: true }), mac)).toBeNull()
  })

  it("maps undo, redo, copy and paste", () => {
    const ctrl = { ctrlKey: true }
    expect(commandForKey(key("KeyZ", ctrl), pc)).toEqual({ type: "undo" })
    expect(commandForKey(key("KeyZ", { ...ctrl, shiftKey: true }), pc)).toEqual(
      { type: "redo" }
    )
    expect(commandForKey(key("KeyY", ctrl), pc)).toEqual({ type: "redo" })
    expect(commandForKey(key("KeyY", { metaKey: true }), mac)).toBeNull()
    expect(commandForKey(key("KeyC", ctrl), pc)).toEqual({ type: "copy" })
    expect(commandForKey(key("KeyV", ctrl), pc)).toEqual({ type: "paste" })
    expect(commandForKey(key("KeyD", ctrl), pc)).toEqual({ type: "duplicate" })
    expect(commandForKey(key("Digit0", ctrl), pc)).toEqual({
      type: "actualSize",
    })
    expect(
      commandForKey(key("ArrowRight", { ...ctrl, shiftKey: true }), pc)
    ).toEqual({ type: "resize", dx: 10, dy: 0 })
  })

  it("leaves Alt combinations to the browser", () => {
    expect(commandForKey(key("ArrowLeft", { altKey: true }), pc)).toBeNull()
    expect(commandForKey(key("KeyW", { altKey: true }), pc)).toBeNull()
  })

  it("opens the help with ? on any layout and F1", () => {
    expect(commandForKey(key("Slash", { shiftKey: true }), pc)).toEqual({
      type: "help",
    })
    expect(
      commandForKey({ ...key("Minus"), key: "?", shiftKey: true }, pc)
    ).toEqual({ type: "zoom", direction: -1 }) // the physical key wins
    expect(commandForKey({ ...key("Comma"), key: "?" }, pc)).toEqual({
      type: "help",
    })
    expect(commandForKey(key("F1"), pc)).toEqual({ type: "help" })
  })

  it("cycles through boxes with Tab", () => {
    expect(commandForKey(key("Tab"), pc)).toEqual({
      type: "cycle",
      direction: 1,
    })
    expect(commandForKey(key("Tab", { shiftKey: true }), pc)).toEqual({
      type: "cycle",
      direction: -1,
    })
  })
})

describe("allowsRepeat", () => {
  it("repeats moves and zooms, not image changes", () => {
    expect(allowsRepeat({ type: "nudge", dx: 1, dy: 0 })).toBe(true)
    expect(allowsRepeat({ type: "zoom", direction: 1 })).toBe(true)
    expect(
      allowsRepeat({ type: "image", direction: 1, unannotated: false })
    ).toBe(false)
    expect(allowsRepeat({ type: "negative" })).toBe(false)
  })
})

describe("classHotkey", () => {
  it("numbers the first ten classes", () => {
    expect(classHotkey(0)).toBe("1")
    expect(classHotkey(9)).toBe("0")
    expect(classHotkey(10)).toBeNull()
    expect(classHotkey(-1)).toBeNull()
  })
})

describe("shouldIgnoreKey", () => {
  const event = (code: string, extra: object = {}) => ({
    ...key(code),
    isComposing: false,
    keyCode: 0,
    target: null,
    ...extra,
  })

  it("ignores keys while an input method is composing", () => {
    expect(
      shouldIgnoreKey(event("KeyW", { isComposing: true }), {
        type: "tool",
        tool: "rect",
      })
    ).toBe(true)
    expect(
      shouldIgnoreKey(event("KeyW", { keyCode: 229 }), {
        type: "tool",
        tool: "rect",
      })
    ).toBe(true)
  })

  it("handles keys outside inputs", () => {
    expect(shouldIgnoreKey(event("KeyW"), { type: "tool", tool: "rect" })).toBe(
      false
    )
    expect(shouldIgnoreKey(event("KeyS"), { type: "save" })).toBe(false)
  })

  it("lets Tab move the focus unless the canvas has it", () => {
    const cycle = { type: "cycle", direction: 1 } as const
    expect(shouldIgnoreKey(event("Tab"), cycle)).toBe(true) // no element
    withDom(() => {
      expect(shouldIgnoreKey(event("Tab", { target: body() }), cycle)).toBe(
        true
      )
      expect(
        shouldIgnoreKey(event("Tab", { target: element("button") }), cycle)
      ).toBe(true)
      expect(shouldIgnoreKey(event("Tab", { target: canvas() }), cycle)).toBe(
        false
      )
    })
  })

  it("leaves typed characters to a focused drop-down (type-ahead)", () => {
    withDom(() => {
      const next = { type: "image", direction: 1, unannotated: false } as const
      expect(shouldIgnoreKey(event("KeyD", { target: combobox() }), next)).toBe(
        true
      )
      expect(
        shouldIgnoreKey(event("Digit1", { key: "1", target: combobox() }), {
          type: "class",
          index: 0,
        })
      ).toBe(true)
      // Ctrl+S still saves; keys that type nothing still work.
      expect(
        shouldIgnoreKey(event("KeyS", { ctrlKey: true, target: combobox() }), {
          type: "save",
        })
      ).toBe(false)
      expect(
        shouldIgnoreKey(event("KeyZ", { ctrlKey: true, target: combobox() }), {
          type: "undo",
        })
      ).toBe(false)
      expect(
        shouldIgnoreKey(event("F1", { target: combobox() }), { type: "help" })
      ).toBe(false)
      expect(shouldIgnoreKey(event("KeyD", { target: body() }), next)).toBe(
        false
      )
    })
  })

  it("ignores keys in inputs and dialogs", () => {
    withDom(() => {
      expect(
        shouldIgnoreKey(event("KeyW", { target: element("input") }), {
          type: "tool",
          tool: "rect",
        })
      ).toBe(true)
      expect(
        shouldIgnoreKey(event("KeyW", { target: element('[role="dialog"]') }), {
          type: "tool",
          tool: "rect",
        })
      ).toBe(true)
    })
  })
})

describe("preventTypeahead", () => {
  /** Whether the drop-down button would swallow the key. */
  const swallows = (code: string, modifiers: Partial<KeyInput> = {}) => {
    const preventDefault = vi.fn()
    preventTypeahead({ ...key(code, modifiers), preventDefault })
    return preventDefault.mock.calls.length > 0
  }

  it("stops typed characters from picking an option on a closed drop-down", () => {
    expect(swallows("KeyD")).toBe(true)
    expect(swallows("KeyS", { key: "S", shiftKey: true })).toBe(true)
    expect(swallows("Digit1", { key: "1" })).toBe(true)
  })

  it("keeps the keys that open the drop-down and keys with modifiers", () => {
    expect(swallows("Space", { key: " " })).toBe(false)
    expect(swallows("Enter")).toBe(false)
    expect(swallows("ArrowDown")).toBe(false)
    expect(swallows("KeyS", { ctrlKey: true })).toBe(false)
    expect(swallows("KeyS", { metaKey: true })).toBe(false)
    expect(swallows("KeyS", { altKey: true })).toBe(false)
  })
})

describe("focusCanvas", () => {
  /** A drop-down button inside `within`, on a page with or without the canvas. */
  const trigger = (within: string[], canvas: { focus: () => void } | null) =>
    ({
      closest: (selector: string) => (within.includes(selector) ? {} : null),
      ownerDocument: {
        querySelector: (selector: string) =>
          selector === '[role="application"]' ? canvas : null,
      },
    }) as unknown as Element

  it("moves the focus to the canvas", () => {
    const canvas = { focus: vi.fn() }
    expect(focusCanvas(trigger([], canvas))).toBe(true)
    expect(canvas.focus).toHaveBeenCalledWith({ preventScroll: true })
  })

  it("leaves the focus alone in a sheet or dialog, or without a canvas", () => {
    const canvas = { focus: vi.fn() }
    expect(focusCanvas(trigger(['[role="dialog"]'], canvas))).toBe(false)
    expect(focusCanvas(trigger([], null))).toBe(false)
    expect(focusCanvas(null)).toBe(false)
    expect(canvas.focus).not.toHaveBeenCalled()
  })
})

describe("leaveOverlay", () => {
  /**
   * A picker in a canvas. `focused`: the focus is inside it when it unmounts.
   * `remove()` takes it off the page, as React does after the cleanup.
   */
  const picker = (focused: boolean, canvas: { focus: () => void } | null) => {
    const body = {}
    const input = {}
    const doc = { body, activeElement: focused ? input : body }
    const overlay = {
      isConnected: true,
      contains: (node: unknown) => node === input,
      ownerDocument: doc,
      closest: (selector: string) =>
        selector === '[role="application"]' ? canvas : null,
    }
    const remove = () => {
      overlay.isConnected = false
      doc.activeElement = body
    }
    return { overlay: overlay as unknown as Element, remove }
  }
  const canvas = () => ({ focus: vi.fn(), isConnected: true })
  const settle = () => new Promise<void>((done) => queueMicrotask(done))

  it("moves the focus back to the canvas once the closed picker is gone", async () => {
    const target = canvas()
    const { overlay, remove } = picker(true, target)
    leaveOverlay(overlay)
    remove()
    await settle()
    expect(target.focus).toHaveBeenCalledWith({ preventScroll: true })
  })

  it("leaves the focus where the user moved it", async () => {
    const target = canvas()
    const { overlay, remove } = picker(false, target)
    leaveOverlay(overlay)
    remove()
    leaveOverlay(null)
    await settle()
    expect(target.focus).not.toHaveBeenCalled()
  })

  it("keeps the focus in a picker that stays on the page (StrictMode)", async () => {
    const target = canvas()
    leaveOverlay(picker(true, target).overlay)
    await settle()
    expect(target.focus).not.toHaveBeenCalled()
  })

  it("leaves a canvas that was removed with the picker to the next one", async () => {
    const target = { ...canvas(), isConnected: false }
    const { overlay, remove } = picker(true, target)
    leaveOverlay(overlay)
    remove()
    await settle()
    expect(target.focus).not.toHaveBeenCalled()
  })
})

describe("focusAfterDialog", () => {
  /** A page with the given open dialogs (in DOM order) and maybe a canvas. */
  const page = (
    dialogs: { focus: () => void }[],
    canvas: { focus: () => void } | null
  ) =>
    ({
      querySelectorAll: () => dialogs,
      querySelector: (selector: string) =>
        selector === '[role="application"]' ? canvas : null,
    }) as unknown as Document
  const event = () => ({ preventDefault: vi.fn() })

  it("moves the focus to the canvas instead of leaving it on the body", () => {
    const canvas = { focus: vi.fn() }
    const closed = event()
    focusAfterDialog(closed, page([], canvas))
    expect(closed.preventDefault).toHaveBeenCalled()
    expect(canvas.focus).toHaveBeenCalledWith({ preventScroll: true })
  })

  it("keeps the focus in a sheet that is still open", () => {
    const canvas = { focus: vi.fn() }
    const sheet = { focus: vi.fn() }
    const closed = event()
    focusAfterDialog(closed, page([sheet], canvas))
    expect(closed.preventDefault).toHaveBeenCalled()
    expect(sheet.focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(canvas.focus).not.toHaveBeenCalled()
  })

  it("keeps the default while the canvas is not on the page", () => {
    const closed = event()
    focusAfterDialog(closed, page([], null))
    expect(closed.preventDefault).not.toHaveBeenCalled()
  })
})

describe("copiesSelectedText", () => {
  const selection = (anchorNode: unknown, isCollapsed = false) =>
    ({ isCollapsed, rangeCount: 1, anchorNode }) as unknown as Parameters<
      typeof copiesSelectedText
    >[1]

  it("leaves Ctrl+C to the browser when text outside the canvas is selected", () => {
    withDom(() => {
      expect(copiesSelectedText(body(), selection(body()))).toBe(true)
    })
  })

  it("copies boxes otherwise", () => {
    withDom(() => {
      expect(copiesSelectedText(body(), null)).toBe(false)
      expect(copiesSelectedText(body(), selection(body(), true))).toBe(false)
      // The canvas has the focus: the user means the boxes.
      expect(copiesSelectedText(canvas(), selection(body()))).toBe(false)
      expect(copiesSelectedText(body(), selection(canvas()))).toBe(false)
    })
  })
})

describe("shortcutHelp", () => {
  it("shows the platform's modifier", () => {
    const keys = (isMac: boolean) =>
      shortcutHelp(isMac).flatMap((group) =>
        group.items.flatMap((item) => item.keys)
      )
    expect(keys(true)).toContain("⌘+S")
    expect(keys(true)).not.toContain("⌘+Y")
    expect(keys(false)).toContain("Ctrl+Y")
  })
})
