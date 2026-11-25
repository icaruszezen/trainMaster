import type { ModuleId } from "@/config/navigation"
import type { GuideChapterId } from "@/features/guide/types"

export const GUIDE_PATH = "/system/guide"
export const DEFAULT_GUIDE_CHAPTER: GuideChapterId = "quickstart"

const VALID_CHAPTER_IDS = new Set<string>([
  "quickstart",
  "projects",
  "datasets",
  "annotation",
  "reviews",
  "data-versions",
  "training-jobs",
  "training-templates",
  "compute",
  "evaluations",
  "models",
  "storage",
  "recycle-bin",
])

export function isGuideChapterId(
  value: string | undefined
): value is GuideChapterId {
  return value !== undefined && VALID_CHAPTER_IDS.has(value)
}

export function guideChapterPath(chapterId: GuideChapterId) {
  return chapterId === DEFAULT_GUIDE_CHAPTER
    ? GUIDE_PATH
    : `${GUIDE_PATH}/${chapterId}`
}

/**
 * 顶栏上下文“?”帮助按钮映射：按当前所在模块跳转到最相关的操作指南章节。
 * 规划中的占位模块统一落到“快速上手与工作台”。
 */
const MODULE_TO_CHAPTER: Partial<Record<ModuleId, GuideChapterId>> = {
  dashboard: "quickstart",
  projects: "projects",
  datasets: "datasets",
  "dataset-versions": "data-versions",
  "annotation-workspace": "annotation",
  reviews: "reviews",
  "training-jobs": "training-jobs",
  "training-templates": "training-templates",
  compute: "compute",
  evaluations: "evaluations",
  models: "models",
  storage: "storage",
  "recycle-bin": "recycle-bin",
  guide: "quickstart",
}

export function guideChapterForModule(
  moduleId: ModuleId | undefined
): GuideChapterId {
  if (!moduleId) {
    return DEFAULT_GUIDE_CHAPTER
  }
  return MODULE_TO_CHAPTER[moduleId] ?? DEFAULT_GUIDE_CHAPTER
}
