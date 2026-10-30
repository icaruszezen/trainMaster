import type * as React from "react"
import { CircleDashed } from "lucide-react"

import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { getNavItem, type ModuleId } from "@/config/navigation"

type ModulePlaceholderProps = {
  moduleId: ModuleId
  /** 已经可用的真实内容，显示在规划信息之前。 */
  children?: React.ReactNode
}

/** 通用功能占位页：文案全部来自 config/navigation.ts。 */
export function ModulePlaceholder({
  moduleId,
  children,
}: ModulePlaceholderProps) {
  const item = getNavItem(moduleId)

  return (
    <>
      <PageHeader
        title={item.title}
        description={item.description}
        actions={<Badge variant="secondary">规划中</Badge>}
      />
      {children}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>规划功能</CardTitle>
            <CardDescription>
              该模块计划提供以下能力，具体以后续迭代为准。
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ul className="grid gap-3 text-sm">
              {item.features.map((feature) => (
                <li key={feature} className="flex items-start gap-2.5">
                  <CircleDashed className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span>{feature}</span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <item.icon />
            </EmptyMedia>
            <EmptyTitle>该模块正在开发中</EmptyTitle>
            <EmptyDescription>
              页面与路由已经就绪，功能将在后续迭代中逐步上线。
            </EmptyDescription>
          </EmptyHeader>
          {item.apiPath && (
            <EmptyContent>
              <p className="text-xs text-muted-foreground">
                预留接口{" "}
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">
                  {item.apiPath}
                </code>
              </p>
            </EmptyContent>
          )}
        </Empty>
      </div>
    </>
  )
}
