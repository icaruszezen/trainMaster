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

export type GuideWorkflowStage = {
  step: string
  title: string
  desc: string
  chapterId: GuideChapterId
}

export const guideWorkflowStages: GuideWorkflowStage[] = [
  {
    step: "01",
    title: "建项目与导数据",
    desc: "定义 YOLO 类别顺序，批量导入图片与 VOC/YOLO/X-AnyLabeling 标注",
    chapterId: "datasets",
  },
  {
    step: "02",
    title: "在线标注与审图",
    desc: "在标注工作台画框，或用已有模型跑审图筛查难例并合并审图版本",
    chapterId: "annotation",
  },
  {
    step: "03",
    title: "冻结数据版本",
    desc: "叠加基础标注与审图版本，稳定划分 train/val/test 并按需离线增强",
    chapterId: "data-versions",
  },
  {
    step: "04",
    title: "发起模型训练",
    desc: "加载参数模板启动 YOLO 训练，实时追踪 Loss/mAP 曲线与训练日志",
    chapterId: "training-jobs",
  },
  {
    step: "05",
    title: "模型评估与对比",
    desc: "实时拖动 Conf/IoU 阈值看漏杀过杀，下钻混淆矩阵与多模型逐图对比",
    chapterId: "evaluations",
  },
  {
    step: "06",
    title: "仓库发布与导出",
    desc: "管理项目版本迭代，下载原始 PT 或由训练节点自动转换缓存 ONNX",
    chapterId: "models",
  },
]

export function getGuideChapter(id: GuideChapterId): GuideChapter | undefined {
  return guideChapters.find((chapter) => chapter.id === id)
}
