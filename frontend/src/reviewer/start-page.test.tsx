// 开始页上正在审的审图包：正在打开另一个审图包时不能继续审它（打开完成时程序
// 关闭它，之后的保存、标记会写进新的审图包）。
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderToStaticMarkup } from "react-dom/server"
import { describe, expect, it } from "vitest"

import type { PackageInfo } from "@/reviewer/api"
import { CurrentPackage } from "@/reviewer/start-page"

const pkg: PackageInfo = {
  path: "C:\\Users\\a\\Downloads\\审图-12-外观",
  zip_path: "C:\\Users\\a\\Downloads\\审图-12-外观.zip",
  results_path: "C:\\Users\\a\\Downloads\\审图-12-外观-审图结果.zip",
  package_id: "p-a",
  review: { id: 12, name: "外观", description: "" },
  model: {
    id: 3,
    project_id: 1,
    project_name: "A 线",
    version: 2,
    yolo_version: "yolo11",
    task: "detect",
    file_name: "best.pt",
    class_names: ["dent"],
  },
  datasets: ["A 线 1 月"],
  platform: "http://trainmaster.local",
  downloaded_at: "2026-11-01T08:00:00Z",
  counts: {
    total: 40,
    inferred: 40,
    inference_failed: 0,
    pending: 28,
    approved: 9,
    needs_sample: 3,
    edited: 2,
    merged: 0,
  },
  missing_images: 0,
  resumed: true,
  opened_at: "2026-11-04T08:00:00Z",
  last_export: null,
}

function render(opening: boolean) {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <CurrentPackage
        pkg={pkg}
        opening={opening}
        closing={false}
        onContinue={() => {}}
        onClose={() => {}}
      />
    </QueryClientProvider>
  )
}

/** “继续审图”按钮的开始标签。 */
function continueButton(html: string) {
  const match = /<button([^>]*)>继续审图/.exec(html)
  expect(match).not.toBeNull()
  return match?.[1] ?? ""
}

describe("CurrentPackage", () => {
  it("continues reviewing when nothing else is being opened", () => {
    const html = render(false)
    expect(continueButton(html)).not.toMatch(/\sdisabled=""/)
    expect(html).not.toContain("正在打开另一个审图包")
  })

  it("cannot be continued while another package is being opened", () => {
    const html = render(true)
    expect(continueButton(html)).toMatch(/\sdisabled=""/)
    expect(html).toContain("正在打开另一个审图包")
  })
})
