import { CircleHelp } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { guideChapterForModule, guideChapterPath } from "@/features/guide/paths"
import { useActiveNav } from "@/hooks/use-active-nav"
import { cn } from "@/lib/utils"

/**
 * 顶栏上下文帮助按钮：点击后自动跳转到当前所在模块对应的操作指南章节；
 * 若已在操作指南页中则呈高亮提示态。
 */
export function HelpButton() {
  const active = useActiveNav()
  const isOnGuide = active?.item.id === "guide"
  const chapterId = guideChapterForModule(active?.item.id)
  const target = guideChapterPath(chapterId)
  const label = isOnGuide
    ? "当前正在查看操作指南"
    : active
      ? `查看「${active.item.title}」操作指南`
      : "查看操作指南"

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          asChild
          className={cn(
            "size-8",
            isOnGuide && "bg-accent text-accent-foreground"
          )}
        >
          <Link
            to={target}
            aria-label={label}
            aria-current={isOnGuide ? "page" : undefined}
          >
            <CircleHelp className="size-4" />
            <span className="sr-only">{label}</span>
          </Link>
        </Button>
      </TooltipTrigger>
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  )
}
