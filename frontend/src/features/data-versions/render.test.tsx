// 渲染冒烟测试：列表卡片、菜单项与详情的概览在各状态下的内容，以及长文本
// （失败原因、数据集名）不会撑宽手机上的卡片。
import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui"
import { renderToStaticMarkup } from "react-dom/server"
import { MemoryRouter } from "react-router"
import { describe, expect, it } from "vitest"

import type { DataVersion } from "@/features/data-versions/api"
import {
  dataVersionMenuItems,
  DataVersionMenuItems,
  deleteLabel,
  type DataVersionActions,
} from "@/features/data-versions/data-version-actions"
import { DataVersionOverview } from "@/features/data-versions/data-version-detail-page"
import { DataVersionCards } from "@/features/data-versions/data-version-list"
import {
  latestDataVersion,
  projectDataVersionsSummary,
} from "@/features/data-versions/project-data-versions"
import {
  dataVersion,
  EMPTY_STATS,
} from "@/features/data-versions/test-fixtures"
import { autoColumnGrids, enclosingClasses } from "@/lib/test-markup"

const noop = () => {}
const actions: DataVersionActions = {
  download: noop,
  editNote: noop,
  retry: noop,
  remove: noop,
}

const longError =
  "数据集“产线A-外观”的文件夹不存在：/data/datasets/line_a_night_shift_2026_11_camera_left_and_right_combined"

const versions: DataVersion[] = [
  dataVersion({ id: 1, version: 4, status: "ready" }),
  dataVersion({
    id: 2,
    version: 5,
    status: "queued",
    queue_position: 2,
    finished_at: null,
  }),
  dataVersion({
    id: 3,
    version: 6,
    status: "building",
    progress: { stage: "linking", done: 120, total: 400 },
    finished_at: null,
  }),
  dataVersion({ id: 4, version: 7, status: "failed", error: longError }),
]

function render(element: React.ReactNode) {
  return renderToStaticMarkup(<MemoryRouter>{element}</MemoryRouter>)
}

describe("DataVersionCards", () => {
  const html = render(
    <DataVersionCards versions={versions} actions={actions} />
  )

  it("shows every status with its details", () => {
    for (const text of [
      "数据版本 v4",
      "已生成",
      "排队中",
      "前面还有 1 个",
      "生成中",
      "链接与复制图片 120 / 400",
      "生成失败",
      "训练 800 · 验证 200 · 测试 0",
    ]) {
      expect(html).toContain(text)
    }
  })

  it("wraps a long failure reason inside the card", () => {
    const classes = enclosingClasses(html, longError)
    expect(classes).not.toBeNull()
    expect(autoColumnGrids(classes ?? [])).toEqual([])
    expect(classes?.at(-1)).toContain("break-words")
  })

  it("links each card to its details", () => {
    expect(html).toContain('href="/datasets/versions/1"')
    expect(html).toContain('href="/datasets/versions/4"')
  })
})

describe("menu items", () => {
  it("depend on the status", () => {
    expect(dataVersionMenuItems("ready")).toEqual([
      "download",
      "details",
      "edit-note",
      "template",
      "delete",
    ])
    expect(dataVersionMenuItems("failed", { showDetails: false })).toEqual([
      "retry",
      "edit-note",
      "delete",
    ])
    expect(dataVersionMenuItems("building")).toEqual([
      "details",
      "edit-note",
      "delete",
    ])
    expect(dataVersionMenuItems("queued")).toEqual(
      dataVersionMenuItems("building")
    )
    expect(deleteLabel("ready")).toBe("移入回收站")
    expect(deleteLabel("failed")).toBe("删除")
    expect(deleteLabel("queued")).toBe("停止并删除")
  })

  const renderItems = (version: DataVersion) =>
    render(
      <DropdownMenuPrimitive.Root open>
        <DropdownMenuPrimitive.Content>
          <DataVersionMenuItems version={version} actions={actions} />
        </DropdownMenuPrimitive.Content>
      </DropdownMenuPrimitive.Root>
    )

  it("renders the labels of a ready version", () => {
    const html = renderItems(versions[0])
    for (const text of [
      "下载…",
      "查看详情",
      "修改备注",
      "以此为模板新建",
      "移入回收站",
    ]) {
      expect(html).toContain(text)
    }
    expect(html).toContain('href="/datasets/versions/new?project=3&amp;from=1"')
    expect(html).not.toContain("重新生成")
  })

  it("renders the labels of a failed and a building version", () => {
    const failed = renderItems(versions[3])
    expect(failed).toContain("重新生成")
    expect(failed).toContain("删除")
    expect(failed).not.toContain("下载")
    expect(failed).not.toContain("以此为模板新建")
    const building = renderItems(versions[2])
    expect(building).toContain("停止并删除")
    expect(building).not.toContain("重新生成")
  })
})

describe("DataVersionOverview", () => {
  const longName = "产线A-外观-夜班-左右相机合并-2026年11月第二批补充数据集"

  it("shows the distribution, sources, settings and storage of a ready version", () => {
    const html = render(
      <DataVersionOverview
        version={dataVersion({
          sources: [
            {
              dataset_id: 1,
              dataset_name: longName,
              dir_name: "line_a",
              include_base: true,
              versions: [{ id: 5, name: "审图1.2" }],
              image_count: 120,
              dataset_deleted: true,
            },
          ],
          config: {
            ...dataVersion().config,
            ignore_labels: ["dirt"],
            augmentation: {
              methods: ["hflip"],
              copies: 1,
              only_with_objects: true,
            },
          },
          stats: {
            ...EMPTY_STATS,
            labels: [
              {
                name: "scratch",
                color: "#ef4444",
                train: { objects: 10, images: 8 },
                val: { objects: 0, images: 0 },
                test: { objects: 0, images: 0 },
              },
            ],
            skipped: { ...EMPTY_STATS.skipped, unannotated: 3 },
            storage: {
              ...EMPTY_STATS.storage,
              linked: 900,
              copied: 2,
              copied_bytes: 2048,
            },
            compare: {
              id: 11,
              version: 3,
              added: 1,
              removed: 2,
              split_changed: 0,
              annotation_changed: 4,
            },
          },
        })}
      />
    )
    for (const text of [
      "类别（目标数）",
      "“scratch”在验证集中没有目标",
      "数据集已删除",
      "基础标注",
      "审图1.2",
      "训练 80% · 验证 20% · 测试 0%",
      "按文件名分组",
      "dirt",
      "水平翻转",
      "没有标注",
      "新增占用 2 KB（其余文件与数据集共用）",
      "相比 数据版本 v3：新增 1 张，移除 2 张，划分变化 0 张，标注变化 4 张",
      'href="/datasets/versions/11"',
    ]) {
      expect(html).toContain(text)
    }
    // 已删除的数据集不链接到数据集页。
    expect(html).not.toContain('href="/datasets/1"')
    const classes = enclosingClasses(html, longName)
    expect(classes?.at(-1)).toContain("truncate")
  })

  it("shows the frozen class list before the version is ready", () => {
    const html = render(
      <DataVersionOverview
        version={dataVersion({ status: "building", image_count: 0 })}
      />
    )
    expect(html).toContain("创建时冻结的类别列表")
    expect(html).toContain("scratch")
    expect(html).not.toContain("未纳入")
    expect(html).not.toContain("新增占用")
  })
})

describe("project tab", () => {
  it("summarizes the project's versions", () => {
    expect(latestDataVersion([])).toBe(null)
    expect(latestDataVersion(versions)?.version).toBe(7)
    expect(projectDataVersionsSummary(versions)).toBe(
      "共 4 个 · 最新 数据版本 v7"
    )
    expect(projectDataVersionsSummary([])).toBe("还没有数据版本")
  })
})
