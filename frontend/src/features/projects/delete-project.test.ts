import { describe, expect, it } from "vitest"

import {
  dataVersionsCheckboxText,
  deletedProjectDescription,
  deleteProjectLabel,
  dependantsNotice,
  type ProjectDependants,
} from "@/features/projects/delete-project"

const none: ProjectDependants = {
  models: 0,
  dataVersions: 0,
  readyDataVersions: 0,
}

describe("deleteProjectLabel", () => {
  it("names what is deleted with the project", () => {
    expect(deleteProjectLabel(none)).toBe("删除项目")
    expect(deleteProjectLabel({ ...none, models: 2 })).toBe("删除项目与模型")
    expect(
      deleteProjectLabel({ ...none, dataVersions: 1, readyDataVersions: null })
    ).toBe("删除项目与数据版本")
    expect(
      deleteProjectLabel({ models: 1, dataVersions: 3, readyDataVersions: 3 })
    ).toBe("删除项目、模型与数据版本")
    expect(deleteProjectLabel({ ...none, trainingJobs: 2 })).toBe(
      "删除项目与训练任务"
    )
    expect(
      deleteProjectLabel({
        models: 1,
        dataVersions: 2,
        readyDataVersions: 2,
        trainingJobs: 3,
      })
    ).toBe("删除项目、模型、数据版本与训练任务")
  })
})

describe("dependantsNotice", () => {
  it("explains why models, data versions, and training jobs go too", () => {
    expect(dependantsNotice(none)).toBeNull()
    expect(dependantsNotice({ ...none, models: 2 })).toBe(
      "项目中有 2 个模型；模型只属于这个项目，需要一起删除。"
    )
    expect(dependantsNotice({ ...none, dataVersions: 1200 })).toBe(
      "项目中有 1,200 个数据版本；数据版本只属于这个项目，需要一起删除。"
    )
    expect(dependantsNotice({ ...none, trainingJobs: 3 })).toBe(
      "项目中有 3 个训练任务；训练任务只属于这个项目，需要一起删除。"
    )
    expect(dependantsNotice({ ...none, models: 1, dataVersions: 4 })).toBe(
      "项目中有 1 个模型、4 个数据版本；它们只属于这个项目，需要一起删除。"
    )
  })
})

describe("dataVersionsCheckboxText", () => {
  it("says whether the versions go to the bin or are removed", () => {
    expect(
      dataVersionsCheckboxText(
        { ...none, dataVersions: 3, readyDataVersions: 3 },
        7
      )
    ).toEqual({
      label: "同时删除 3 个数据版本（移入回收站）",
      detail:
        "数据版本移入回收站，保留 7 天，期间可以恢复（项目随之恢复）；到期后永久删除。",
    })
    expect(
      dataVersionsCheckboxText(
        { ...none, dataVersions: 2, readyDataVersions: 0 },
        7
      ).label
    ).toBe("同时删除 2 个数据版本（直接删除）")
    expect(
      dataVersionsCheckboxText(
        { ...none, dataVersions: 4, readyDataVersions: 3 },
        7
      )
    ).toEqual({
      label: "同时删除 4 个数据版本（已生成的移入回收站，其余直接删除）",
      detail:
        "已生成的 3 个移入回收站，保留 7 天，期间可以恢复（项目随之恢复）；其余 1 个（生成失败、排队中或正在生成）直接删除，正在生成的会停止。",
    })
  })

  it("stays general while the list is not loaded", () => {
    const text = dataVersionsCheckboxText(
      { ...none, dataVersions: 2, readyDataVersions: null },
      7
    )
    expect(text.label).toBe(
      "同时删除 2 个数据版本（已生成的移入回收站，其余直接删除）"
    )
    expect(text.detail).not.toMatch(/\d 个/)
  })
})

describe("deletedProjectDescription", () => {
  it("is empty when nothing else was deleted", () => {
    expect(deletedProjectDescription(none, 7)).toBeUndefined()
  })

  it("keeps the models-only wording", () => {
    expect(deletedProjectDescription({ ...none, models: 2 }, 7)).toBe(
      "2 个模型已移入回收站，7 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。"
    )
  })

  it("separates versions moved to the bin from removed ones", () => {
    expect(
      deletedProjectDescription(
        { models: 1, dataVersions: 3, readyDataVersions: 2 },
        7
      )
    ).toBe(
      "1 个模型与 2 个数据版本已移入回收站，7 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。其余 1 个数据版本（生成失败或未生成完成）已直接删除。"
    )
    expect(
      deletedProjectDescription(
        { models: 1, dataVersions: 1, readyDataVersions: 0 },
        7
      )
    ).toBe(
      "1 个模型已移入回收站，7 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。1 个数据版本（生成失败或未生成完成）已直接删除。"
    )
    expect(
      deletedProjectDescription(
        { models: 0, dataVersions: 2, readyDataVersions: 0 },
        7
      )
    ).toBe("2 个数据版本（生成失败或未生成完成）已直接删除。")
  })

  it("stays general while the list is not loaded", () => {
    expect(
      deletedProjectDescription(
        { models: 0, dataVersions: 2, readyDataVersions: null },
        7
      )
    ).toBe(
      "2 个数据版本已删除，其中已生成的移入回收站。7 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。"
    )
  })
})
