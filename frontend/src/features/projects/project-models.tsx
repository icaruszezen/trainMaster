import { ArrowRight, Boxes, CircleDashed } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { getNavItem } from "@/config/navigation"

/** 项目将展示的模型信息（模型训练与模型仓库上线后接入）。 */
const plannedModelInfo = [
  "本项目训练任务产出的模型与版本",
  "每个模型使用的数据集与数据版本",
  "评估指标（mAP / Precision / Recall）与对比",
  "模型发布状态",
]

/** 模型情况：模型仓库尚未上线，先说明这里将显示什么。 */
export function ProjectModels() {
  const models = getNavItem("models")

  return (
    <Empty className="border">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <Boxes />
        </EmptyMedia>
        <EmptyTitle className="text-base">模型功能开发中</EmptyTitle>
        <EmptyDescription>
          模型训练与模型仓库上线后，这里会显示本项目的模型情况：
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="max-w-md">
        <ul className="grid gap-2 text-left text-sm">
          {plannedModelInfo.map((item) => (
            <li key={item} className="flex items-start gap-2.5">
              <CircleDashed className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <span>{item}</span>
            </li>
          ))}
        </ul>
        <Button variant="outline" size="sm" asChild>
          <Link to={models.path}>
            查看{models.title}规划
            <ArrowRight />
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
