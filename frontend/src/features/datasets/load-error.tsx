import { RefreshCw, ServerCrash } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { cn } from "@/lib/utils"

/** 查询失败时的提示与重试按钮。 */
export function LoadError({
  title,
  error,
  retrying,
  onRetry,
  className,
}: {
  title: string
  error: Error
  retrying: boolean
  onRetry: () => void
  className?: string
}) {
  return (
    <Empty className={cn("border", className)}>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <ServerCrash />
        </EmptyMedia>
        <EmptyTitle className="text-base">{title}</EmptyTitle>
        <EmptyDescription>{error.message}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button
          variant="outline"
          size="sm"
          onClick={onRetry}
          disabled={retrying}
        >
          <RefreshCw className={cn(retrying && "animate-spin")} />
          {retrying ? "正在重试…" : "重试"}
        </Button>
      </EmptyContent>
    </Empty>
  )
}
