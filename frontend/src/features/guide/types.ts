import type { ModuleId } from "@/config/navigation"

export type GuideGroupId =
  "quickstart" | "data" | "annotation" | "training" | "evaluation" | "system"

export type GuideChapterId =
  | "quickstart"
  | "projects"
  | "datasets"
  | "annotation"
  | "reviews"
  | "data-versions"
  | "training-jobs"
  | "training-templates"
  | "compute"
  | "evaluations"
  | "models"
  | "storage"
  | "recycle-bin"

export type GuideGroup = {
  id: GuideGroupId
  title: string
}

/** 截图上某一个交互式标注点（百分比坐标，基于原图宽高 0–100）。 */
export type GuideCallout = {
  /** 显示的序号（1, 2, 3…）。 */
  index: number
  /** 该界面区域或按钮的简短名称。 */
  label: string
  /** 面向用户的具体操作说明。 */
  description: string
  /** 序号圆点锚点横坐标（0–100%）。 */
  x: number
  /** 序号圆点锚点纵坐标（0–100%）。 */
  y: number
  /** 可选的高亮框区域（0–100%），用于悬停/选中时清晰框出目标控件或面板。 */
  box?: {
    x: number
    y: number
    width: number
    height: number
  }
}

export type GuideFigure = {
  id: string
  /** 通过 Vite 静态导入或路径引用的 WebP 截图地址。 */
  src: string
  alt: string
  caption: string
  width: number
  height: number
  callouts: GuideCallout[]
}

export type GuideStep = {
  title: string
  description: string
  tip?: string
}

export type GuideTip = {
  variant: "tip" | "warning" | "note"
  title: string
  content: string
}

export type GuideTable = {
  caption?: string
  headers: string[]
  rows: string[][]
}

export type GuideShortcutGroup = {
  title: string
  items: {
    keys: string[]
    description: string
  }[]
}

export type GuideSection = {
  id: string
  title: string
  summary?: string
  paragraphs?: string[]
  figures?: GuideFigure[]
  steps?: GuideStep[]
  table?: GuideTable
  shortcuts?: GuideShortcutGroup[]
  tips?: GuideTip[]
}

export type GuideChapter = {
  id: GuideChapterId
  groupId: GuideGroupId
  title: string
  shortTitle: string
  summary: string
  /** 对应功能模块在侧边栏中的 ID（用于图标与上下文帮助联动）。 */
  moduleId: ModuleId
  /** “前往该页面”快捷跳转路径。 */
  targetPath: string
  targetLabel: string
  /** 站内检索用的额外关键词。 */
  keywords: string[]
  sections: GuideSection[]
}
