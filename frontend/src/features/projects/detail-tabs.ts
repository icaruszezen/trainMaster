/**
 * 项目详情页的标签页（地址栏中的 `?tab=`，见 detail-tabs.test.ts）。默认的
 * “数据集”不写进地址栏；其它页面可以直接链接到某个标签页，如
 * `/projects/3?tab=data-versions`。
 */
export type DetailTab =
  "datasets" | "classes" | "data-versions" | "training-jobs" | "models"

/** 某个标签页的链接。 */
export function projectTabPath(projectId: number, tab: DetailTab) {
  return tab === "datasets"
    ? `/projects/${projectId}`
    : `/projects/${projectId}?tab=${tab}`
}

/** 地址栏中的 `tab` 参数对应的标签页；没有或不认识时为“数据集”。 */
export function parseDetailTab(value: string | null): DetailTab {
  // labels 是以前“标签分布”标签页的链接，标签分布现在显示在“类别”中。
  switch (value) {
    case "classes":
    case "labels":
      return "classes"
    case "data-versions":
    case "training-jobs":
    case "models":
      return value
    default:
      return "datasets"
  }
}
