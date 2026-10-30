import { House, RotateCw, TriangleAlert } from "lucide-react"
import { isRouteErrorResponse, useRouteError } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"

function describeError(error: unknown) {
  if (isRouteErrorResponse(error)) {
    return `${error.status} ${error.statusText}`.trim()
  }
  if (error instanceof Error) {
    return error.message
  }
  return "未知错误"
}

/** 路由渲染出错时的兜底页面，同时用于布局内与整页两种场景。 */
export function RouteErrorPage() {
  const error = useRouteError()

  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <TriangleAlert />
        </EmptyMedia>
        <EmptyTitle>页面出错了</EmptyTitle>
        <EmptyDescription>
          页面加载时发生了意外错误，可以刷新重试或返回工作台。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <code className="max-w-full rounded-md bg-muted px-2 py-1 font-mono text-xs break-all text-muted-foreground">
          {describeError(error)}
        </code>
        <div className="flex flex-wrap justify-center gap-2">
          <Button variant="outline" onClick={() => window.location.reload()}>
            <RotateCw />
            刷新页面
          </Button>
          {/* 使用整页跳转，避免出错的路由状态残留。 */}
          <Button asChild>
            <a href="/">
              <House />
              返回工作台
            </a>
          </Button>
        </div>
      </EmptyContent>
    </Empty>
  )
}
