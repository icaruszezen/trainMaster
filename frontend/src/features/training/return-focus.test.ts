import { describe, expect, it, vi } from "vitest"

import { returnFocus } from "@/features/training/return-focus"

describe("returnFocus", () => {
  const event = () => ({ preventDefault: vi.fn() })

  it("gives the focus back to the element that opened the dialog", () => {
    const trigger = { isConnected: true, focus: vi.fn() }
    const closed = event()
    returnFocus(closed, trigger)
    expect(closed.preventDefault).toHaveBeenCalled()
    expect(trigger.focus).toHaveBeenCalled()
  })

  it("keeps the default when the element left the page or is unknown", () => {
    // E.g. the node card is gone after deleting the node.
    const removed = { isConnected: false, focus: vi.fn() }
    const closed = event()
    returnFocus(closed, removed)
    expect(closed.preventDefault).not.toHaveBeenCalled()
    expect(removed.focus).not.toHaveBeenCalled()

    const unknown = event()
    returnFocus(unknown, null)
    expect(unknown.preventDefault).not.toHaveBeenCalled()
  })
})
