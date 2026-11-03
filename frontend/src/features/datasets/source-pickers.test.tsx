import { FileArchive } from "lucide-react"
import { renderToStaticMarkup } from "react-dom/server"
import { describe, expect, it } from "vitest"

import { SelectedSource } from "@/features/datasets/source-pickers"
import { autoColumnGrids, enclosingClasses } from "@/lib/test-markup"

describe("SelectedSource", () => {
  it("truncates a long file name instead of widening the card", () => {
    const name = "helmet_detection_yolo11s_best_20261103.pt"
    const html = renderToStaticMarkup(
      <SelectedSource
        icon={FileArchive}
        title={name}
        meta="18.4 MB"
        onRemove={() => {}}
        removeLabel="移除文件"
      >
        <p>上传后在服务器上校验</p>
      </SelectedSource>
    )

    const classes = enclosingClasses(html, name)
    expect(classes).not.toBeNull()
    expect(autoColumnGrids(classes ?? [])).toEqual([])
    expect(classes?.at(-1)).toContain("truncate")
  })
})
