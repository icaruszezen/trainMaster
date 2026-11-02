import { useQuery } from "@tanstack/react-query"
import { ArrowRight, CircleAlert, Server } from "lucide-react"
import { Link } from "react-router"

import { getNavItem } from "@/config/navigation"
import { computeNodesQuery } from "@/features/training/compute-api"
import {
  dashboardComputeText,
  summarizeNodes,
} from "@/features/training/compute-status"
import { cn } from "@/lib/utils"

/** 工作台中“计算资源”的一行摘要，链接到计算资源页。 */
export function ComputeSummaryLink({ className }: { className?: string }) {
  // 工作台不需要计算资源页那样频繁刷新。
  const { data, isPending } = useQuery({
    ...computeNodesQuery,
    refetchInterval: 30_000,
  })
  const summary = data ? summarizeNodes(data.items) : null
  const text = summary
    ? dashboardComputeText(summary)
    : isPending
      ? "正在加载…"
      : "无法获取节点信息"
  const problem = summary !== null && summary.problemCount > 0

  return (
    <Link
      to={getNavItem("compute").path}
      className={cn(
        "group flex min-w-0 items-center gap-3 rounded-xl border bg-card px-4 py-3 text-card-foreground shadow-sm transition-colors hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none sm:px-6",
        className
      )}
    >
      <div className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground group-hover:bg-background group-hover:text-foreground">
        <Server className="size-4" />
      </div>
      <div className="grid min-w-0 flex-1 gap-0.5">
        <span className="text-sm font-medium">计算资源</span>
        <span className="flex min-w-0 items-start gap-1 text-xs text-muted-foreground tabular-nums">
          {problem && (
            <CircleAlert
              className="mt-px size-3 shrink-0 text-destructive"
              aria-hidden
            />
          )}
          <span className="min-w-0 break-words">{text}</span>
        </span>
      </div>
      <ArrowRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-foreground motion-reduce:transition-none" />
    </Link>
  )
}
