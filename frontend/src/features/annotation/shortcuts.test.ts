import { describe, expect, it } from "vitest"

import {
  allowsRepeat,
  classHotkey,
  commandForKey,
  shortcutHelp,
  shouldIgnoreKey,
  type KeyInput,
  type ShortcutContext,
} from "@/features/annotation/shortcuts"

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
