import { describe, expect, it } from "vitest"

import {
  addedBytes,
  augmentationText,
  classColor,
  classSplitWarnings,
  compareText,
  customScopeCount,
  DATA_VERSION_STATUS_LABELS,
  dataVersionFullTitle,
  dataVersionTitle,
  emptyClasses,
  findRatioPreset,
  negativesText,
  noteFirstLine,
  parseCustomRatio,
  parseSeed,
  previewSourceText,
  progressPercent,
  progressText,
  queueText,
  randomSeed,
  selectionHint,
  RATIO_PRESETS,
  SKIPPED_LABELS,
  skippedEntries,
  skippedSummary,
  skippedTotal,
  splitRatioText,
  splitSummary,
  statusDetailText,
  storageText,
  TRAINING_SCOPE_LABELS,
  TRAINING_SCOPES,
} from "@/features/data-versions/format"
import {
  dataVersion,
  EMPTY_STATS,
  NO_SKIPPED,
} from "@/features/data-versions/test-fixtures"
import type { DataVersionLabelStats } from "@/features/data-versions/api"

describe("titles", () => {
  it("never writes a bare version number", () => {
    expect(dataVersionTitle({ version: 4 })).toBe("数据版本 v4")
    expect(dataVersionFullTitle({ project_name: "外观检测", version: 4 })).toBe(
      "外观检测 · 数据版本 v4"
    )
  })

  it("labels every status", () => {
    expect(DATA_VERSION_STATUS_LABELS).toEqual({
      queued: "排队中",
      building: "生成中",
      ready: "已生成",
      failed: "生成失败",
    })
  })

  it("takes the first line of the note", () => {
    expect(noteFirstLine("第一行\n第二行")).toBe("第一行")
    expect(noteFirstLine("")).toBe("")
  })
})

describe("progress and queue", () => {
  it("shows the stage and the counts", () => {
    expect(progressText({ stage: "linking", done: 120, total: 400 })).toBe(
      "链接与复制图片 120 / 400"
    )
    expect(progressText({ stage: "preparing", done: 0, total: 0 })).toBe(
      "读取图片与标注"
    )
    expect(progressPercent({ stage: "linking", done: 100, total: 400 })).toBe(
      25
    )
    expect(progressPercent({ stage: "preparing", done: 0, total: 0 })).toBe(
      null
    )
    expect(progressPercent(null)).toBe(null)
  })

  it("shows the queue position", () => {
    expect(queueText(1)).toBe("下一个生成")
    expect(queueText(3)).toBe("前面还有 2 个")
    expect(
      statusDetailText({ status: "queued", queue_position: 2, progress: null })
    ).toBe("排队中 · 前面还有 1 个")
    expect(
      statusDetailText({
        status: "building",
        queue_position: null,
        progress: { stage: "augmenting", done: 5, total: 10 },
      })
    ).toBe("生成增强图片 5 / 10")
    expect(
      statusDetailText({
        status: "ready",
        queue_position: null,
        progress: null,
      })
    ).toBe("已生成")
  })
})

describe("splits", () => {
  it("summarizes the counts", () => {
    expect(splitSummary({ train: 800, val: 200, test: 0 })).toBe(
      "训练 800 · 验证 200 · 测试 0"
    )
    expect(splitSummary({ train: 12345, val: 1, test: 2 })).toBe(
      "训练 12,345 · 验证 1 · 测试 2"
    )
    expect(splitRatioText({ train: 70, val: 20, test: 10 })).toBe(
      "训练 70% · 验证 20% · 测试 10%"
    )
  })

  it("has the three presets", () => {
    expect(RATIO_PRESETS.map((p) => [p.train, p.val, p.test])).toEqual([
      [80, 20, 0],
      [70, 20, 10],
      [90, 10, 0],
    ])
    expect(findRatioPreset({ train: 70, val: 20, test: 10 })?.id).toBe(
      "70/20/10"
    )
    expect(findRatioPreset({ train: 60, val: 20, test: 20 })).toBe(null)
  })

  it("derives the test percentage of a custom ratio", () => {
    expect(parseCustomRatio("60", "25")).toEqual({
      ok: true,
      train: 60,
      val: 25,
      test: 15,
    })
    expect(parseCustomRatio(" 99 ", "1")).toEqual({
      ok: true,
      train: 99,
      val: 1,
      test: 0,
    })
  })

  it.each([
    ["0", "20", "训练集至少 1%"],
    ["80", "0", "验证集至少 1%"],
    ["90", "20", "训练与验证合计不能超过 100%"],
    ["", "20", "请填写 0–100 的整数百分比"],
    ["80.5", "19.5", "请填写 0–100 的整数百分比"],
    ["-1", "20", "请填写 0–100 的整数百分比"],
  ])("refuses train %s val %s", (train, val, error) => {
    expect(parseCustomRatio(train, val)).toEqual({ ok: false, error })
  })
})

describe("seed", () => {
  it("accepts 0–2147483647", () => {
    expect(parseSeed("0")).toBe(0)
    expect(parseSeed(" 42 ")).toBe(42)
    expect(parseSeed("2147483647")).toBe(2147483647)
    expect(parseSeed("2147483648")).toBe(null)
    expect(parseSeed("-1")).toBe(null)
    expect(parseSeed("1e3")).toBe(null)
    expect(parseSeed("")).toBe(null)
  })

  it("draws another seed", () => {
    const values = [0.5, 0.5, 0.25]
    const random = () => values.shift() ?? 0
    const first = randomSeed(null, () => 0.5)
    expect(randomSeed(first, random)).toBe(Math.floor(0.25 * 2147483648))
    expect(randomSeed(null, () => 0.999999999)).toBeLessThanOrEqual(2147483647)
  })
})

describe("skipped", () => {
  it("has a Chinese label for every key", () => {
    expect(Object.keys(SKIPPED_LABELS).sort()).toEqual(
      Object.keys(NO_SKIPPED).sort()
    )
    for (const label of Object.values(SKIPPED_LABELS)) {
      expect(label).toMatch(/[一-鿿]/)
    }
  })

  it("summarizes the non-zero reasons", () => {
    const skipped = { ...NO_SKIPPED, unannotated: 3, invalid: 1, changed: 2 }
    expect(skippedSummary(skipped)).toBe(
      "没有标注 3 张 · 标注文件无法读取 1 张 · 文件在上次扫描后被修改 2 张"
    )
    expect(skippedEntries(skipped).map((e) => e.key)).toEqual([
      "unannotated",
      "invalid",
      "changed",
    ])
    expect(skippedSummary(NO_SKIPPED)).toBe("")
  })

  it("lists images with objects after the empty ones", () => {
    const skipped = { ...NO_SKIPPED, with_objects: 12, empty: 1, missing: 2 }
    expect(skippedEntries(skipped).map((e) => e.key)).toEqual([
      "empty",
      "with_objects",
      "missing",
    ])
    expect(skippedTotal(skipped)).toBe(15)
  })

  it("does not count images that were only not augmented", () => {
    expect(
      skippedTotal({ ...NO_SKIPPED, unlisted: 2, augment_skipped: 5 })
    ).toBe(2)
  })
})

describe("training scopes", () => {
  const version = dataVersion()
  const source = version.sources[0]

  it("names every scope", () => {
    expect(
      TRAINING_SCOPES.map((scope) => TRAINING_SCOPE_LABELS[scope])
    ).toEqual(["仅有标注的图片", "仅无标注的背景图", "整个数据集"])
  })

  it("counts the datasets with a scope of their own", () => {
    expect(customScopeCount(version)).toBe(0)
    expect(
      customScopeCount({
        config: version.config,
        sources: [source, { ...source, scope: "background" }],
      })
    ).toBe(1)
  })

  it("says the empty switch only applies to annotated datasets", () => {
    const config = { ...version.config, include_empty: false }
    expect(negativesText({ config, sources: [source] })).toBe(
      "不纳入空标注图片"
    )
    expect(
      negativesText({
        config,
        sources: [source, { ...source, scope: "all" }],
      })
    ).toBe(
      "不纳入空标注图片（仅有标注的图片的数据集），其它数据集纳入没有目标的图片"
    )
    expect(
      negativesText({ config, sources: [{ ...source, scope: "background" }] })
    ).toBe("纳入没有目标的图片")
  })
})

describe("storage", () => {
  it("counts only the copied and generated bytes", () => {
    const storage = {
      ...EMPTY_STATS.storage,
      linked: 1000,
      copied: 2,
      generated: 600,
      copied_bytes: 512,
      generated_bytes: 1536,
    }
    expect(addedBytes(storage)).toBe(2048)
    expect(storageText(storage)).toBe("新增占用 2 KB（其余文件与数据集共用）")
  })

  it("does not mention shared files when nothing is linked", () => {
    expect(
      storageText({
        ...EMPTY_STATS.storage,
        copied: 3,
        copied_bytes: 1024 * 1024,
      })
    ).toBe("新增占用 1 MB")
  })
})

describe("compareText", () => {
  const compare = {
    id: 11,
    version: 3,
    added: 120,
    removed: 4,
    split_changed: 0,
    annotation_changed: 7,
  }

  it("compares with the earlier version", () => {
    expect(compareText(compare)).toBe(
      "相比 数据版本 v3：新增 120 张，移除 4 张，划分变化 0 张，标注变化 7 张"
    )
  })

  it("leaves out annotations in a preview", () => {
    expect(compareText(compare, { annotations: false })).toBe(
      "相比 数据版本 v3：新增 120 张，移除 4 张，划分变化 0 张"
    )
  })
})

describe("classSplitWarnings", () => {
  const label = (
    name: string,
    train: number,
    val: number,
    test: number
  ): DataVersionLabelStats => ({
    name,
    color: "",
    train: { objects: train, images: train },
    val: { objects: val, images: val },
    test: { objects: test, images: test },
  })

  it("warns about a class missing from val or train", () => {
    const labels = [
      label("scratch", 10, 3, 0),
      label("dent", 5, 0, 0),
      label("crack", 0, 2, 0),
      label("unused", 0, 0, 0),
    ]
    expect(classSplitWarnings(labels, 0)).toEqual([
      { name: "dent", split: "val", text: "“dent”在验证集中没有目标" },
      { name: "crack", split: "train", text: "“crack”在训练集中没有目标" },
    ])
    expect(emptyClasses(labels)).toEqual(["unused"])
  })

  it("checks test only when it has a percentage", () => {
    const labels = [label("scratch", 10, 3, 0)]
    expect(classSplitWarnings(labels, 0)).toEqual([])
    expect(classSplitWarnings(labels, 10)).toEqual([
      { name: "scratch", split: "test", text: "“scratch”在测试集中没有目标" },
    ])
  })
})

describe("misc", () => {
  it("uses the class color or the label color", () => {
    expect(classColor({ name: "a", color: "#123456" })).toBe("#123456")
    expect(classColor({ name: "a", color: "" })).toMatch(/^#[0-9a-f]{6}$/)
  })

  it("describes the augmentation", () => {
    expect(augmentationText(null)).toBe("不增强")
    expect(
      augmentationText({
        methods: ["hflip", "blur"],
        copies: 2,
        only_with_objects: true,
      })
    ).toBe("水平翻转、轻微模糊 · 每张训练集图片 2 份 · 只增强有目标的图片")
  })
})

describe("sources", () => {
  it("counts a source of the preview", () => {
    expect(
      previewSourceText({
        selected_count: 130,
        image_count: 120,
        from_versions: 20,
        by_folder: 15,
        negatives: 0,
      })
    ).toBe("选中 130 张 · 纳入 120 张（20 张用审图标注） · 15 张按文件夹划分")
    expect(
      previewSourceText({
        selected_count: 10,
        image_count: 10,
        from_versions: 0,
        by_folder: 0,
        negatives: 0,
      })
    ).toBe("选中 10 张 · 纳入 10 张")
    expect(
      previewSourceText({
        selected_count: 40,
        image_count: 25,
        from_versions: 0,
        by_folder: 0,
        negatives: 25,
      })
    ).toBe("选中 40 张 · 纳入 25 张 · 负样本 25 张")
  })

  it("explains the annotation sources", () => {
    expect(selectionHint({ include_base: true, version_ids: [] })).toBe(
      "使用数据集自己的标注。"
    )
    expect(selectionHint({ include_base: false, version_ids: [5] })).toContain(
      "需加样"
    )
    expect(selectionHint({ include_base: true, version_ids: [5] })).toContain(
      "以新的为准"
    )
    expect(selectionHint({ include_base: false, version_ids: [] })).toBe("")
  })
})
