import { annotationAndReviewChapters } from "@/features/guide/chapters/annotation-and-review"
import { evaluationAndModelsChapters } from "@/features/guide/chapters/evaluation-and-models"
import { quickstartAndDataChapters } from "@/features/guide/chapters/quickstart-and-data"
import { systemChapters } from "@/features/guide/chapters/system"
import { trainingChapters } from "@/features/guide/chapters/training"
import type {
  GuideChapter,
  GuideChapterId,
  GuideGroup,
} from "@/features/guide/types"

export const guideGroups: GuideGroup[] = [
  { id: "quickstart", title: "快速入门" },
  { id: "data", title: "项目与数据" },
  { id: "annotation", title: "标注、审图与版本" },
  { id: "training", title: "模型训练与算力" },
  { id: "evaluation", title: "评估与发布" },
  { id: "system", title: "系统运维" },
]

export const guideChapters: GuideChapter[] = [
  ...quickstartAndDataChapters,
  ...annotationAndReviewChapters,
  ...trainingChapters,
  ...evaluationAndModelsChapters,
  ...systemChapters,
]

export function getGuideChapter(id: GuideChapterId): GuideChapter | undefined {
  return guideChapters.find((chapter) => chapter.id === id)
}
