import { describe, expect, it, vi } from "vitest"

import {
  discardEditor,
  flushEditor,
  registerEditorControls,
} from "@/reviewer/editor-controls"

describe("editor controls", () => {
  it("flushes and discards through the registered editor", async () => {
    const controls = {
      flush: vi.fn(async () => false),
      discard: vi.fn(() => true),
    }
    const unregister = registerEditorControls(controls)
    expect(await flushEditor()).toBe(false)
    expect(discardEditor()).toBe(true)
    expect(controls.discard).toHaveBeenCalledTimes(1)
    unregister()

    expect(await flushEditor()).toBe(true)
    expect(discardEditor()).toBe(false)
    expect(controls.discard).toHaveBeenCalledTimes(1)
  })

  it("keeps a newer editor registered when an older one unregisters", () => {
    const older = { flush: async () => true, discard: vi.fn(() => false) }
    const newer = { flush: async () => true, discard: vi.fn(() => true) }
    const unregisterOlder = registerEditorControls(older)
    const unregisterNewer = registerEditorControls(newer)
    unregisterOlder()
    expect(discardEditor()).toBe(true)
    expect(newer.discard).toHaveBeenCalledTimes(1)
    expect(older.discard).not.toHaveBeenCalled()
    unregisterNewer()
  })
})
