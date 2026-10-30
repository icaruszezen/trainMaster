import type { ReactNode } from "react"
import type { LucideIcon } from "lucide-react"

import {
  Card,
  CardAction,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

/**
 * 页面顶部的统计卡片：标题、大号数值、右上角图标，底部是补充说明（可放进度条）。
 * 手机上两列排布，内边距相应缩小。
 */
export function StatCard({
  title,
  value,
  icon: Icon,
  children,
}: {
  title: string
  value: ReactNode
  icon: LucideIcon
  children?: ReactNode
}) {
  return (
    <Card className="gap-3 py-4 sm:py-6">
      <CardHeader className="px-4 sm:px-6">
        <CardDescription>{title}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
        <CardAction>
          <Icon className="size-4 text-muted-foreground" />
        </CardAction>
      </CardHeader>
      {children && (
        <CardFooter className="mt-auto flex-col items-stretch gap-2 px-4 text-xs text-muted-foreground sm:px-6">
          {children}
        </CardFooter>
      )}
    </Card>
  )
}
