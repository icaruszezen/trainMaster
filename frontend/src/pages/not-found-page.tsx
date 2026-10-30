import { FileQuestion, House } from "lucide-react"
import { Link, useLocation } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"

export function NotFoundPage() {
  const { pathname } = useLocation()

  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FileQuestion />
        </EmptyMedia>
        <EmptyTitle>页面不存在</EmptyTitle>
        <EmptyDescription>
          找不到地址{" "}
          <code className="font-mono break-all text-foreground">
            {pathname}
          </code>
          ，它可能已被移动，或者对应的功能尚未开发。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to="/">
            <House />
            返回工作台
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
