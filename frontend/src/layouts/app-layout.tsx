import { useState } from "react"
import { Outlet, ScrollRestoration } from "react-router"

import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { UploadTray } from "@/features/datasets/upload-tray"
import {
  useActiveNav,
  useIsFullBleed,
  useIsNotFound,
} from "@/hooks/use-active-nav"
import { PageTitleProvider, useCurrentPageTitle } from "@/hooks/use-page-title"
import { cn } from "@/lib/utils"

// SidebarProvider 会把折叠状态写入该 cookie，这里读回来以便刷新后保持。
function isSidebarOpenByDefault() {
  return !document.cookie.split("; ").includes("sidebar_state=false")
}

export function AppLayout() {
  const fullBleed = useIsFullBleed()
  const [sidebarOpen, setSidebarOpen] = useState(isSidebarOpenByDefault)
  // 标注工作台默认收起侧边栏，把宽度留给图片；离开后恢复原来的状态。
  const [fullBleedOpen, setFullBleedOpen] = useState(false)

  return (
    <PageTitleProvider>
      <SidebarProvider
        open={fullBleed ? fullBleedOpen : sidebarOpen}
        onOpenChange={fullBleed ? setFullBleedOpen : setSidebarOpen}
      >
        <DocumentTitle />
        <AppSidebar />
        <SidebarInset
          className={cn("min-w-0", fullBleed && "h-svh overflow-hidden")}
        >
          <SiteHeader />
          <div
            className={cn(
              "flex flex-1 flex-col",
              fullBleed ? "min-h-0" : "gap-6 p-4 md:p-6"
            )}
          >
            <Outlet />
          </div>
        </SidebarInset>
        {/* 上传在切换页面时继续进行，进度面板因此挂在布局上。 */}
        <UploadTray />
        <ScrollRestoration />
      </SidebarProvider>
    </PageTitleProvider>
  )
}

function DocumentTitle() {
  const active = useActiveNav()
  const notFound = useIsNotFound()
  const pageTitle = useCurrentPageTitle()
  const title =
    notFound || !active ? "页面不存在" : (pageTitle ?? active.item.title)

  return <title>{`${title} · trainMaster`}</title>
}
