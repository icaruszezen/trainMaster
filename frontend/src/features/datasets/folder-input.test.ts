import { describe, expect, it } from "vitest"

import { folderFromFileList } from "@/features/datasets/folder-input"

const file = (webkitRelativePath: string, size = 1) => ({
  name: webkitRelativePath.slice(webkitRelativePath.lastIndexOf("/") + 1),
  size,
  webkitRelativePath,
})

describe("folderFromFileList", () => {
  it("strips the selected folder's own name from the paths", () => {
    const folder = folderFromFileList([
      file("批次1/OK/b.jpg", 20),
      file("批次1/a.jpg", 10),
      file("批次1/OK/b.xml", 1),
    ])
    expect(folder.name).toBe("批次1")
    expect(folder.files.map((f) => [f.path, f.size])).toEqual([
      ["OK/b.jpg", 20],
      ["OK/b.xml", 1],
      ["a.jpg", 10],
    ])
  })

  it("falls back to the file name without a relative path", () => {
    const folder = folderFromFileList([
      { name: "loose.jpg", size: 5, webkitRelativePath: "" },
    ])
    expect(folder.name).toBe("")
    expect(folder.files.map((f) => f.path)).toEqual(["loose.jpg"])
  })
})
