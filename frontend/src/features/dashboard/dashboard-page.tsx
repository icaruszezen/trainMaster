import { useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  Boxes,
  Database,
  Images,
  ListChecks,
  Rocket,
  type LucideIcon,
} from "lucide-react"
import { Link } from "react-router"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { getNavItem, type ModuleId } from "@/config/navigation"
import { datasetsQuery } from "@/features/datasets/api"
import { SystemStatusCard } from "@/features/system/system-status-card"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"

/** 尚未开发的模块的统计项：对应模块完成后再接入真实数据。 */
const plannedStats: { title: string; icon: LucideIcon }[] = [
  { title: "标注任务", icon: ListChecks },
  { title: "训练任务", icon: Rocket },
  { title: "模型", icon: Boxes },
]

const workflow: { moduleId: ModuleId; title: string; description: string }[] = [
  {
    moduleId: "datasets",
    title: "数据导入",
    description: "导入产线采集的图片，建立数据集",
  },
  {
    moduleId: "annotation-tasks",
    title: "标注",
    description: "分配标注任务，标注缺陷与目标",
  },
  {
    moduleId: "dataset-versions",
    title: "数据版本",
    description: "划分训练 / 验证集并冻结快照",
  },
  {
    moduleId: "training-jobs",
    title: "模型训练",
    description: "选择数据版本与参数发起训练",
  },
  {
    moduleId: "evaluations",
    title: "模型评估",
    description: "评估 mAP 等指标，分析漏检误检",
  },
  {
    moduleId: "models",
    title: "模型发布",
    description: "管理模型版本，导出并发布",
  },
]

export function DashboardPage() {
  return (
    <>
      <PageHeader
        title="欢迎使用 trainMaster"
        description="面向团队内部的工业视觉平台：图片数据集管理、标注、模型训练与模型评估一体化。"
      />

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3 xl:grid-cols-5">
        <DatasetStats />
        {plannedStats.map((stat) => (
          <StatCard
            key={stat.title}
            title={stat.title}
            icon={stat.icon}
            value="—"
          >
            功能开发中
          </StatCard>
        ))}
      </div>

      <div className="grid gap-4 xl:grid-cols-3">
        <SystemStatusCard />
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>平台流程</CardTitle>
            <CardDescription>
              从数据到模型的完整链路，点击步骤进入对应模块
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ol className="grid gap-3 sm:grid-cols-2 md:grid-cols-3">
              {workflow.map((step, index) => {
                const item = getNavItem(step.moduleId)
                return (
                  <li key={step.moduleId}>
                    <Link
                      to={item.path}
                      className="group flex h-full items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
                    >
                      <div className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground group-hover:bg-background group-hover:text-foreground">
                        <item.icon className="size-4" />
                      </div>
                      <div className="min-w-0 flex-1 space-y-1">
                        <div className="flex items-center gap-1.5 text-sm font-medium">
                          <span className="text-xs text-muted-foreground tabular-nums">
                            {String(index + 1).padStart(2, "0")}
                          </span>
                          {step.title}
                          <ArrowRight className="ml-auto size-3.5 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
                        </div>
                        <p className="text-xs text-muted-foreground">
                          {step.description}
                        </p>
                      </div>
                    </Link>
                  </li>
                )
              })}
            </ol>
          </CardContent>
        </Card>
      </div>
    </>
  )
}

/** 数据集与图片两张统计卡片，数据来自数据集列表。 */
function DatasetStats() {
  const { data, isPending } = useQuery(datasetsQuery())

  // 后台刷新失败时保留已加载的数字。
  if (data === undefined) {
    const value = isPending ? <Skeleton className="h-8 w-16" /> : "—"
    const footer = isPending ? "正在加载…" : "无法获取数据"
    return (
      <>
        <StatCard title="数据集" icon={Database} value={value}>
          {footer}
        </StatCard>
        <StatCard title="图片" icon={Images} value={value}>
          {footer}
        </StatCard>
      </>
    )
  }

  const images = data.items.reduce((sum, d) => sum + d.image_count, 0)
  const annotated = data.items.reduce((sum, d) => sum + d.annotated_count, 0)
  return (
    <>
      <StatCard title="数据集" icon={Database} value={formatNumber(data.total)}>
        <Link
          to="/datasets"
          className="inline-flex items-center gap-1 self-start underline-offset-4 hover:text-foreground hover:underline"
        >
          {data.total > 0 ? "查看全部" : "创建数据集"}
          <ArrowRight className="size-3" />
        </Link>
      </StatCard>
      <StatCard title="图片" icon={Images} value={formatNumber(images)}>
        {images > 0
          ? `已标注 ${formatPercent(percentOf(annotated, images))}`
          : "暂无图片"}
      </StatCard>
    </>
  )
}
