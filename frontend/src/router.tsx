import type { ComponentType } from "react"
import type { RouteObject } from "react-router"

import { navItems, type ModuleId } from "@/config/navigation"
import { AnnotationTasksPage } from "@/features/annotation/annotation-tasks-page"
import { AnnotationWorkspacePage } from "@/features/annotation/annotation-workspace-page"
import { LabelsPage } from "@/features/annotation/labels-page"
import { PrelabelPage } from "@/features/annotation/prelabel-page"
import { DashboardPage } from "@/features/dashboard/dashboard-page"
import { DatasetDetailPage } from "@/features/datasets/dataset-detail-page"
import { DatasetVersionsPage } from "@/features/datasets/dataset-versions-page"
import { DatasetsPage } from "@/features/datasets/datasets-page"
import { EvaluationsPage } from "@/features/evaluation/evaluations-page"
import { InferencePage } from "@/features/models/inference-page"
import { ModelsPage } from "@/features/models/models-page"
import { MembersPage } from "@/features/system/members-page"
import { SettingsPage } from "@/features/system/settings-page"
import { StoragePage } from "@/features/system/storage-page"
import { ComputePage } from "@/features/training/compute-page"
import { TrainingJobsPage } from "@/features/training/training-jobs-page"
import { AppLayout } from "@/layouts/app-layout"
import { NotFoundPage } from "@/pages/not-found-page"
import { RouteErrorPage } from "@/pages/route-error-page"

/** 路由的 handle：404 路由带 notFound，标题与面包屑据此显示“页面不存在”。 */
export type RouteHandle = { notFound?: boolean }

/** 每个导航模块对应的页面组件；类型保证不会漏登记。 */
export const pages: Record<ModuleId, ComponentType> = {
  dashboard: DashboardPage,
  datasets: DatasetsPage,
  "dataset-versions": DatasetVersionsPage,
  "annotation-tasks": AnnotationTasksPage,
  "annotation-workspace": AnnotationWorkspacePage,
  labels: LabelsPage,
  prelabel: PrelabelPage,
  "training-jobs": TrainingJobsPage,
  compute: ComputePage,
  evaluations: EvaluationsPage,
  models: ModelsPage,
  inference: InferencePage,
  members: MembersPage,
  storage: StoragePage,
  settings: SettingsPage,
}

/** 路由表：由导航配置生成，外加 404 与错误边界。 */
export const routes: RouteObject[] = [
  {
    path: "/",
    Component: AppLayout,
    // 布局本身出错时整页兜底。
    ErrorBoundary: RouteErrorPage,
    children: [
      {
        // 页面出错时保留侧边栏与顶栏。
        ErrorBoundary: RouteErrorPage,
        children: [
          ...navItems.map((item): RouteObject => ({
            path: item.path,
            Component: pages[item.id],
          })),
          // 模块的子页面。静态路径优先于动态参数，/datasets/versions 仍是数据版本页。
          { path: "/datasets/:datasetId", Component: DatasetDetailPage },
          {
            path: "*",
            Component: NotFoundPage,
            handle: { notFound: true } satisfies RouteHandle,
          },
        ],
      },
    ],
  },
]
