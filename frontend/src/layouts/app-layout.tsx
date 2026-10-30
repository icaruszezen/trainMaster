import { Outlet, ScrollRestoration } from "react-router"

import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { UploadTray } from "@/features/datasets/upload-tray"
import { useActiveNav, useIsNotFound } from "@/hooks/use-active-nav"
import { PageTitleProvider, useCurrentPageTitle } from "@/hooks/use-page-title"

// SidebarProvider 会把折叠状态写入该 cookie，这里读回来以便刷新后保持。
function isSidebarOpenByDefault() {
  return !document.cookie.split("; ").includes("sidebar_state=false")
}

export function AppLayout() {
  return (
    <PageTitleProvider>
      <SidebarProvider defaultOpen={isSidebarOpenByDefault()}>
        <DocumentTitle />
        <AppSidebar />
        <SidebarInset className="min-w-0">
          <SiteHeader />
          <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
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
