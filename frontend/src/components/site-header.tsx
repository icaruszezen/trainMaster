import { Link } from "react-router"

import { ModeToggle } from "@/components/mode-toggle"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Separator } from "@/components/ui/separator"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { useActiveNav, useIsNotFound } from "@/hooks/use-active-nav"

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b bg-background px-4">
      <SidebarTrigger className="-ml-1" aria-label="折叠 / 展开侧边栏" />
      <Separator
        orientation="vertical"
        className="mr-2 data-[orientation=vertical]:h-4"
      />
      <AppBreadcrumb />
      <div className="ml-auto flex items-center gap-1">
        <ModeToggle />
      </div>
    </header>
  )
}

/**
 * 面包屑：分组 › 页面；工作台只显示“工作台”。
 * 模块路径下的 404（如 `/models/typo`）显示为：分组 › 模块 › 页面不存在。
 */
function AppBreadcrumb() {
  const active = useActiveNav()
  const notFound = useIsNotFound() || !active
  const isCurrentPage = active?.exact && !notFound

  return (
    <Breadcrumb aria-label="面包屑导航" className="min-w-0">
      <BreadcrumbList className="flex-nowrap">
        {active && (
          <>
            {active.item.path !== "/" && (
              <>
                <BreadcrumbItem className="hidden md:inline-flex">
                  {active.group.title}
                </BreadcrumbItem>
                <BreadcrumbSeparator className="hidden md:block" />
              </>
            )}
            <BreadcrumbItem className="min-w-0">
              {isCurrentPage ? (
                <BreadcrumbPage className="truncate">
                  {active.item.title}
                </BreadcrumbPage>
              ) : (
                <BreadcrumbLink asChild className="truncate">
                  <Link to={active.item.path}>{active.item.title}</Link>
                </BreadcrumbLink>
              )}
            </BreadcrumbItem>
          </>
        )}
        {notFound && (
          <>
            {active && <BreadcrumbSeparator />}
            <BreadcrumbItem className="min-w-0">
              <BreadcrumbPage className="truncate">页面不存在</BreadcrumbPage>
            </BreadcrumbItem>
          </>
        )}
      </BreadcrumbList>
    </Breadcrumb>
  )
}
