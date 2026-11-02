import { afterEach, describe, expect, it, vi } from "vitest"

import { copyText } from "@/lib/clipboard"

type CopyEvent = {
  clipboardData: { setData: (type: string, data: string) => void } | null
  preventDefault: () => void
}

/**
 * A stand-in for `document` (the tests run without a DOM). `execCommand`
 * decides what happens when the page runs `document.execCommand("copy")`:
 * `copy()` fires a copy event at the listeners, like a browser does.
 */
function fakePage(
  execCommand: (page: { copy: (withData?: boolean) => CopyEvent }) => boolean,
  { selection = "" }: { selection?: string } = {}
) {
  const listeners = new Set<(event: CopyEvent) => void>()
  const clipboard: Record<string, string> = {}
  const button = { focus: vi.fn() }
  const textarea = {
    value: "",
    style: {},
    setAttribute: vi.fn(),
    select: vi.fn(),
    remove: vi.fn(),
  }
  const page = {
    copy(withData = true) {
      const event: CopyEvent = {
        clipboardData: withData
          ? {
              setData: (type: string, data: string) => {
                clipboard[type] = data
              },
            }
          : null,
        preventDefault: vi.fn(),
      }
      for (const listener of listeners) {
        listener(event)
      }
      return event
    },
  }
  const doc = {
    // The copy button had the focus; a dialog's focus trap keeps it there.
    activeElement: button as unknown,
    body: { appendChild: vi.fn() },
    createElement: vi.fn(() => textarea),
    addEventListener: vi.fn(
      (_type: string, listener: (event: CopyEvent) => void) =>
        listeners.add(listener)
    ),
    removeEventListener: vi.fn(
      (_type: string, listener: (event: CopyEvent) => void) =>
        listeners.delete(listener)
    ),
    getSelection: () => ({ toString: () => selection }),
    execCommand: vi.fn(() => execCommand(page)),
  }
  vi.stubGlobal("document", doc)
  vi.stubGlobal("window", { isSecureContext: false })
  vi.stubGlobal("navigator", {})
  return { doc, clipboard, listeners, textarea, button }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("copyText", () => {
  it("uses the Clipboard API in a secure context", async () => {
    const writeText = vi.fn(() => Promise.resolve())
    const { doc } = fakePage(() => true)
    vi.stubGlobal("window", { isSecureContext: true })
    vi.stubGlobal("navigator", { clipboard: { writeText } })

    expect(await copyText("make up")).toBe(true)
    expect(writeText).toHaveBeenCalledWith("make up")
    expect(doc.execCommand).not.toHaveBeenCalled()
  })

  it("writes the text in the copy event even when the selection is empty", async () => {
    // In a dialog the focus trap pulls the focus back from the textarea: the
    // selection is empty and execCommand still returns true.
    let event: CopyEvent | undefined
    const { doc, clipboard, listeners, textarea } = fakePage((page) => {
      event = page.copy()
      return true
    })

    expect(await copyText("docker compose up -d --build trainer")).toBe(true)
    expect(clipboard["text/plain"]).toBe("docker compose up -d --build trainer")
    expect(event?.preventDefault).toHaveBeenCalled()
    expect(doc.execCommand).toHaveBeenCalledWith("copy")
    expect(listeners.size).toBe(0)
    expect(textarea.remove).toHaveBeenCalled()
  })

  it("falls back to execCommand when the Clipboard API is refused", async () => {
    const { clipboard } = fakePage((page) => {
      page.copy()
      return true
    })
    vi.stubGlobal("window", { isSecureContext: true })
    vi.stubGlobal("navigator", {
      clipboard: { writeText: () => Promise.reject(new Error("denied")) },
    })

    expect(await copyText("make up")).toBe(true)
    expect(clipboard["text/plain"]).toBe("make up")
  })

  it("reports a failure when no copy event wrote the text", async () => {
    const { listeners } = fakePage(() => true)
    expect(await copyText("make up")).toBe(false)
    expect(listeners.size).toBe(0)
  })

  it("reports a failure when execCommand fails", async () => {
    fakePage((page) => {
      page.copy()
      return false
    })
    expect(await copyText("make up")).toBe(false)

    const { listeners, textarea } = fakePage(() => {
      throw new Error("not supported")
    })
    expect(await copyText("make up")).toBe(false)
    expect(listeners.size).toBe(0)
    expect(textarea.remove).toHaveBeenCalled()
  })

  it("trusts the selection when the event carries no clipboard data", async () => {
    fakePage(
      (page) => {
        page.copy(false)
        return true
      },
      { selection: "make up" }
    )
    expect(await copyText("make up")).toBe(true)

    fakePage(
      (page) => {
        page.copy(false)
        return true
      },
      { selection: "" }
    )
    expect(await copyText("make up")).toBe(false)
  })

  it("gives the focus back to the copy button", async () => {
    const { doc, button } = fakePage((page) => {
      // Outside a dialog the textarea takes the focus and leaves with it.
      doc.activeElement = null
      page.copy()
      return true
    })
    expect(await copyText("make up")).toBe(true)
    expect(button.focus).toHaveBeenCalledWith({ preventScroll: true })
  })

  it("leaves the focus alone when it stayed on the button", async () => {
    const { button } = fakePage((page) => {
      page.copy()
      return true
    })
    expect(await copyText("make up")).toBe(true)
    expect(button.focus).not.toHaveBeenCalled()
  })
})
