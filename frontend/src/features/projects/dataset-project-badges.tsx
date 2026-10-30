import { FolderKanban } from "lucide-react"
import { Link } from "react-router"

import { Badge } from "@/components/ui/badge"
import type { DatasetProjectRef } from "@/features/datasets/api"
import { cn } from "@/lib/utils"

/**
 * 数据集所属项目的标记，点击进入项目；超过 `max` 个时其余的显示为“+N”。
 * 链接浮在卡片的伸展链接之上（relative z-[1]），可以单独点击。
 */
export function DatasetProjectBadges({
  projects,
  max = 2,
  className,
}: {
  projects: DatasetProjectRef[]
  max?: number
  className?: string
}) {
  if (projects.length === 0) {
    return null
  }
  const shown = projects.slice(0, max)
  const rest = projects.slice(max)

  return (
    <div className={cn("flex min-w-0 flex-wrap gap-1", className)}>
      {shown.map((project) => (
        <Badge
          key={project.id}
          variant="secondary"
          asChild
          className="relative z-[1] max-w-40 font-normal"
        >
          <Link to={`/projects/${project.id}`} title={`项目：${project.name}`}>
            <FolderKanban />
            <span className="truncate">{project.name}</span>
          </Link>
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge
          variant="outline"
          className="font-normal tabular-nums"
          title={rest.map((project) => project.name).join("、")}
        >
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
