import { describe, expect, it } from "vitest"

import {
  blockingWarning,
  buildPreviewInput,
  CUSTOM_RATIO,
  defaultSelection,
  defaultUnlistedChoice,
  effectiveCopies,
  effectiveScope,
  effectiveSelection,
  fieldOfServerError,
  ignoreLabels,
  initialDraft,
  isClassListConflict,
  isNewReviewVersion,
  mergeKnownUnlisted,
  missingTemplateReviewVersions,
  missingTemplateSources,
  NO_SOURCE,
  notInTemplate,
  parseAugmentation,
  parseRatio,
  previewWarnings,
  ratioDraftOf,
  readyReviewVersions,
  scopeOverride,
  sourceDisabledReason,
  sourceInputs,
  templateScope,
  toggleVersion,
  type FormDraft,
} from "@/features/data-versions/form-logic"
import {
  dataVersion,
  dataset,
  NO_SKIPPED,
  preview,
  reviewVersion,
} from "@/features/data-versions/test-fixtures"

const v5 = reviewVersion({ id: 5, name: "审图1.2" })
const v6 = reviewVersion({
  id: 6,
  name: "审图1.3",
  created_at: "2026-11-05T00:00:00Z",
  finished_at: "2026-11-05T00:01:00Z",
})

describe("default sources", () => {
  it("takes the base when it is annotated and every ready review version", () => {
    const versions = readyReviewVersions([
      v6,
      reviewVersion({ id: 7, status: "building", name: "" }),
      v5,
    ])
    expect(versions.map((v) => v.id)).toEqual([6, 5])
    expect(defaultSelection(dataset({ annotated_count: 3 }), versions)).toEqual(
      { include_base: true, version_ids: [6, 5] }
    )
    // 基础没有标注（只做了审图）：只用审图版本中需加样的图片。
    expect(defaultSelection(dataset({ annotated_count: 0 }), versions)).toEqual(
      { include_base: false, version_ids: [6, 5] }
    )
  })

  it("never selects a dataset whose folder is missing", () => {
    const missing = dataset({ directory_missing: true })
    expect(sourceDisabledReason(missing)).toContain("文件夹不存在")
    expect(sourceDisabledReason(dataset({ image_count: 0 }))).toContain(
      "没有图片"
    )
    expect(sourceDisabledReason(dataset())).toBe(null)
    expect(
      effectiveSelection({
        dataset: missing,
        versions: [v5],
        template: null,
        override: { include_base: true, version_ids: [5] },
      })
    ).toEqual({ include_base: false, version_ids: [] })
  })

  it("prefers the user's choice, then the template, then the default", () => {
    const template = dataVersion({
      sources: [
        {
          dataset_id: 1,
          dataset_name: "A",
          dir_name: "a",
          include_base: false,
          versions: [
            { id: 5, name: "审图1.2" },
            { id: 4, name: "审图1.1（已删除）" },
          ],
          scope: "annotated",
          image_count: 0,
          dataset_deleted: false,
        },
      ],
    })
    const base = { dataset: dataset({ id: 1 }), versions: [v6, v5] }
    expect(
      effectiveSelection({ ...base, template: null, override: undefined })
    ).toEqual({ include_base: true, version_ids: [6, 5] })
    // 模板中的审图版本 4 已不存在。
    expect(
      effectiveSelection({ ...base, template, override: undefined })
    ).toEqual({ include_base: false, version_ids: [5] })
    expect(
      effectiveSelection({
        ...base,
        template,
        override: { include_base: true, version_ids: [6, 99] },
      })
    ).toEqual({ include_base: true, version_ids: [6] })
    // 模板中没有的数据集默认不选。
    expect(
      effectiveSelection({
        dataset: dataset({ id: 2 }),
        versions: [],
        template,
        override: undefined,
      })
    ).toEqual({ include_base: false, version_ids: [] })
  })

  it("keeps the review versions newest first when toggling", () => {
    const selection = { include_base: true, version_ids: [5] }
    expect(toggleVersion(selection, 6, true, [v6, v5])).toEqual({
      include_base: true,
      version_ids: [6, 5],
    })
    expect(toggleVersion(selection, 5, false, [v6, v5])).toEqual({
      include_base: true,
      version_ids: [],
    })
  })

  it("sends only the selected datasets", () => {
    expect(
      sourceInputs([
        { datasetId: 1, selection: { include_base: true, version_ids: [] } },
        {
          datasetId: 2,
          selection: { include_base: false, version_ids: [] },
          scope: "all",
        },
        {
          datasetId: 3,
          selection: { include_base: false, version_ids: [8] },
          scope: "background",
        },
      ])
    ).toEqual([
      { dataset_id: 1, include_base: true, version_ids: [], scope: "" },
      {
        dataset_id: 3,
        include_base: false,
        version_ids: [8],
        scope: "background",
      },
    ])
  })

  it("takes the base of an unannotated dataset for background images", () => {
    const unannotated = dataset({ annotated_count: 0, image_count: 40 })
    expect(defaultSelection(unannotated, [], "annotated")).toEqual(NO_SOURCE)
    for (const scope of ["background", "all"] as const) {
      expect(defaultSelection(unannotated, [v5], scope)).toEqual({
        include_base: true,
        version_ids: [5],
      })
      expect(
        effectiveSelection({
          dataset: unannotated,
          versions: [],
          template: null,
          override: undefined,
          scope,
        })
      ).toEqual({ include_base: true, version_ids: [] })
    }
    // 没有图片的数据集不能选择。
    expect(
      defaultSelection(
        dataset({ annotated_count: 0, image_count: 0 }),
        [],
        "all"
      )
    ).toEqual(NO_SOURCE)
  })
})

describe("training scopes", () => {
  const template = dataVersion({
    config: { ...dataVersion().config, scope: "all" },
    sources: [
      { ...dataVersion().sources[0], dataset_id: 1, scope: "all" },
      { ...dataVersion().sources[0], dataset_id: 2, scope: "background" },
    ],
  })

  it("starts from the template's default and its datasets' own settings", () => {
    expect(initialDraft(null).scope).toBe("annotated")
    const draft = initialDraft(template)
    expect(draft.scope).toBe("all")
    // 与模板默认相同的数据集使用默认；不同的单独设置；不在模板中的使用默认。
    expect(templateScope(template, 1)).toBe("")
    expect(templateScope(template, 2)).toBe("background")
    expect(templateScope(template, 3)).toBe("")
    expect(templateScope(null, 2)).toBe("")
    expect([1, 2, 3].map((id) => effectiveScope(draft, template, id))).toEqual([
      "all",
      "background",
      "all",
    ])
  })

  it("lets the user change a dataset or the default", () => {
    const draft = {
      ...initialDraft(template),
      scope: "annotated" as const,
      scopes: { 1: "background" as const, 2: "" as const },
    }
    expect(scopeOverride(draft, template, 1)).toBe("background")
    // 改回“使用默认”覆盖模板中的设置。
    expect(scopeOverride(draft, template, 2)).toBe("")
    expect([1, 2, 3].map((id) => effectiveScope(draft, template, id))).toEqual([
      "background",
      "annotated",
      "annotated",
    ])
  })
})

describe("template highlights", () => {
  const template = dataVersion({ created_at: "2026-11-01T08:00:00Z" })

  it("marks review versions made after the template that it does not use", () => {
    expect(isNewReviewVersion(v6, 1, template)).toBe(true)
    // 模板用了它。
    expect(isNewReviewVersion(v5, 1, template)).toBe(false)
    // 模板之前就有、模板没有选：有意不选的，不算新的。
    expect(isNewReviewVersion(reviewVersion({ id: 3 }), 1, template)).toBe(
      false
    )
    expect(isNewReviewVersion(v6, 1, null)).toBe(false)
  })

  it("marks datasets the template does not use", () => {
    expect(notInTemplate(1, template)).toBe(false)
    expect(notInTemplate(2, template)).toBe(true)
    expect(notInTemplate(2, null)).toBe(false)
  })

  it("lists template datasets that cannot be used any more", () => {
    expect(missingTemplateSources(template, [dataset({ id: 2 })])).toEqual([
      "产线A-外观",
    ])
    expect(
      missingTemplateSources(template, [
        dataset({ id: 1, directory_missing: true }),
      ])
    ).toEqual(["产线A-外观"])
    expect(missingTemplateSources(template, [dataset({ id: 1 })])).toEqual([])
  })

  it("lists template review versions that cannot be selected any more", () => {
    expect(missingTemplateReviewVersions(template, 1, [v6])).toEqual([
      "审图1.2",
    ])
    expect(missingTemplateReviewVersions(template, 1, [v5, v6])).toEqual([])
    expect(missingTemplateReviewVersions(template, 2, [])).toEqual([])
    expect(missingTemplateReviewVersions(null, 1, [])).toEqual([])
  })
})

describe("prefill", () => {
  it("uses the UI defaults without a template", () => {
    const draft = initialDraft(null)
    expect(draft.ratio).toEqual({ preset: "80/20/0", train: "80", val: "20" })
    expect(draft.seed).toBe("42")
    expect(draft.use_folders).toBe(true)
    expect(draft.group_by).toBe("stem")
    expect(draft.include_empty).toBe(true)
    expect(draft.augment.enabled).toBe(false)
    expect(draft.augment.only_with_objects).toBe(true)
    expect(draft.note).toBe("")
  })

  it("copies the split, filters and augmentation of the template", () => {
    const template = dataVersion({
      note: "上一版",
      config: {
        split: {
          train: 60,
          val: 25,
          test: 15,
          seed: 7,
          use_folders: false,
          group_by: "folder",
        },
        scope: "background",
        include_empty: false,
        ignore_labels: ["dirt"],
        augmentation: {
          methods: ["noise", "hflip"],
          copies: 2,
          only_with_objects: false,
        },
      },
    })
    const draft = initialDraft(template)
    expect(draft.ratio).toEqual({
      preset: CUSTOM_RATIO,
      train: "60",
      val: "25",
    })
    expect(parseRatio(draft.ratio)).toEqual({
      ok: true,
      train: 60,
      val: 25,
      test: 15,
    })
    expect(draft.seed).toBe("7")
    expect(draft.use_folders).toBe(false)
    expect(draft.group_by).toBe("folder")
    expect(draft.include_empty).toBe(false)
    expect(draft.scope).toBe("background")
    expect(draft.scopes).toEqual({})
    expect(draft.augment).toEqual({
      enabled: true,
      methods: ["hflip", "noise"],
      copies: 2,
      only_with_objects: false,
    })
    expect(draft.note).toBe("")
  })

  it("recognizes a preset", () => {
    expect(ratioDraftOf({ train: 90, val: 10, test: 0 }).preset).toBe("90/10/0")
  })
})

describe("unlisted labels", () => {
  it("ignores other projects' labels and skips the others by default", () => {
    expect(
      defaultUnlistedChoice({ name: "dirt", other_project: true }, null)
    ).toBe("ignore")
    expect(
      defaultUnlistedChoice({ name: "crack", other_project: false }, null)
    ).toBe("skip")
    expect(
      defaultUnlistedChoice({ name: "crack", other_project: false }, ["crack"])
    ).toBe("ignore")
  })

  it("remembers labels across previews", () => {
    const known = mergeKnownUnlisted({}, [
      { name: "dirt", other_project: true },
    ])
    expect(
      mergeKnownUnlisted(known, [{ name: "dirt", other_project: true }])
    ).toBe(known)
    expect(Object.keys(mergeKnownUnlisted(known, []))).toEqual(["dirt"])
  })

  it("builds the sorted ignore list without class names", () => {
    const known = mergeKnownUnlisted({}, [
      { name: "zeta", other_project: true },
      { name: "dirt", other_project: true },
      { name: "crack", other_project: false },
      { name: "scratch", other_project: true },
    ])
    expect(
      ignoreLabels({
        known,
        choices: { zeta: "skip", crack: "ignore" },
        templateIgnore: ["old"],
        classNames: new Set(["scratch"]),
      })
    ).toEqual(["crack", "dirt", "old"])
  })
})

describe("augmentation", () => {
  it("is null when off", () => {
    expect(
      parseAugmentation({ ...initialDraft(null).augment, enabled: false })
    ).toEqual({ ok: true, value: null })
  })

  it("needs a method and clamps the copies", () => {
    expect(
      parseAugmentation({
        enabled: true,
        methods: [],
        copies: 1,
        only_with_objects: true,
      })
    ).toEqual({ ok: false, error: "请至少选择一种增强方式，或关闭增强" })
    expect(
      parseAugmentation({
        enabled: true,
        methods: ["vflip", "hflip"],
        copies: 5,
        only_with_objects: true,
      })
    ).toEqual({
      ok: true,
      value: {
        methods: ["hflip", "vflip"],
        copies: 3,
        only_with_objects: true,
      },
    })
    expect(effectiveCopies({ methods: ["hflip"], copies: 4 })).toBe(1)
    expect(effectiveCopies({ methods: [], copies: 4 })).toBe(1)
  })
})

describe("buildPreviewInput", () => {
  const draft: FormDraft = initialDraft(null)
  const sources = [
    { dataset_id: 1, include_base: true, version_ids: [5], scope: "" as const },
  ]

  it("builds the request", () => {
    expect(
      buildPreviewInput({ projectId: 3, draft, sources, ignore: ["dirt"] })
    ).toEqual({
      input: {
        project_id: 3,
        sources,
        split: {
          train: 80,
          val: 20,
          test: 0,
          seed: 42,
          use_folders: true,
          group_by: "stem",
        },
        scope: "annotated",
        include_empty: true,
        ignore_labels: ["dirt"],
        augmentation: null,
      },
      errors: {},
    })
  })

  it("sends nothing while the form is invalid", () => {
    const result = buildPreviewInput({
      projectId: 3,
      draft: {
        ...draft,
        ratio: { preset: CUSTOM_RATIO, train: "90", val: "20" },
        seed: "x",
        augment: { ...draft.augment, enabled: true, methods: [] },
      },
      sources: [],
      ignore: [],
    })
    expect(result.input).toBe(null)
    expect(Object.keys(result.errors).sort()).toEqual([
      "augment",
      "ratio",
      "seed",
      "sources",
    ])
  })
})

describe("previewWarnings", () => {
  it("is quiet for a good preview", () => {
    expect(previewWarnings(preview(), 0)).toEqual([])
  })

  it("refuses an empty val split and warns about classes", () => {
    const warnings = previewWarnings(
      preview({
        splits: {
          train: { images: 5, objects: 5, negatives: 0, by_folder: 0 },
          val: { images: 0, objects: 0, negatives: 0, by_folder: 0 },
          test: { images: 0, objects: 0, negatives: 0, by_folder: 0 },
        },
        labels: [
          {
            name: "scratch",
            color: "",
            train: { objects: 5, images: 5 },
            val: { objects: 0, images: 0 },
            test: { objects: 0, images: 0 },
          },
        ],
        skipped: { ...NO_SKIPPED, unlisted: 3 },
      }),
      10
    )
    expect(warnings.map((w) => [w.key, w.tone])).toEqual([
      ["val", "error"],
      ["test", "warning"],
      ["unlisted", "warning"],
      ["class:val:scratch", "warning"],
      ["class:test:scratch", "warning"],
    ])
    expect(blockingWarning(warnings)?.key).toBe("val")
  })

  it("refuses a version without images", () => {
    const warnings = previewWarnings(preview({ image_count: 0, labels: [] }), 0)
    expect(warnings.map((w) => w.key)).toEqual(["empty"])
  })

  it("warns about a selected dataset none of whose images are taken", () => {
    const source = {
      dataset_id: 4,
      dataset_name: "背景",
      scope: "background" as const,
      selected_count: 30,
      from_versions: 0,
      image_count: 0,
      negatives: 0,
      by_folder: 0,
    }
    const warnings = previewWarnings(
      preview({
        sources: [
          source,
          { ...source, dataset_id: 5, image_count: 30, negatives: 30 },
          { ...source, dataset_id: 6, selected_count: 0 },
        ],
      }),
      0
    )
    expect(warnings.map((w) => [w.key, w.tone])).toEqual([
      ["source:4", "warning"],
    ])
    expect(warnings[0]?.text).toContain(
      "数据集“背景”选中的 30 张图片都不会纳入"
    )
  })

  it("refuses too many augmented images", () => {
    expect(
      previewWarnings(preview({ augmented_count: 100_001 }), 0)[0]?.key
    ).toBe("augment")
  })
})

describe("server errors", () => {
  it("recognizes a class list change", () => {
    expect(
      isClassListConflict("项目的类别列表已经修改，请检查后重新生成")
    ).toBe(true)
    expect(isClassListConflict("划分后验证集没有图片")).toBe(false)
  })

  it("maps messages to fields", () => {
    expect(fieldOfServerError("版本备注不能超过 500 个字符")).toBe("note")
    expect(fieldOfServerError("所选增强方式最多能生成 3 份不同的副本")).toBe(
      "augment"
    )
    expect(fieldOfServerError("忽略的标签“scratch”在类别列表中")).toBe("labels")
    expect(fieldOfServerError("数据集“A”不在项目中")).toBe("sources")
    expect(
      fieldOfServerError(
        "参与训练的图片无效：应为 annotated（仅有标注的图片）、background（仅无标注的背景图）或 all（整个数据集）"
      )
    ).toBe("sources")
    // 名称中的字词不影响判断。
    expect(fieldOfServerError("数据集“产线A 副本”不在项目中")).toBe("sources")
    expect(fieldOfServerError("审图版本“划分备注”还没有生成完成")).toBe(
      "sources"
    )
    expect(fieldOfServerError("忽略的标签“增强”在类别列表中")).toBe("labels")
    expect(fieldOfServerError("项目不存在（可能已被删除）")).toBe(null)
  })
})
