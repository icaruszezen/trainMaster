import { Outlet, ScrollRestoration } from "react-router"

import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { useActiveNav, useIsNotFound } from "@/hooks/use-active-nav"

// SidebarProvider 会把折叠状态写入该 cookie，这里读回来以便刷新后保持。
function isSidebarOpenByDefault() {
  return !document.cookie.split("; ").includes("sidebar_state=false")
}

export function AppLayout() {
  const active = useActiveNav()
  const notFound = useIsNotFound()
  const title = notFound || !active ? "页面不存在" : active.item.title

  return (
    <SidebarProvider defaultOpen={isSidebarOpenByDefault()}>
      <title>{`${title} · trainMaster`}</title>
      <AppSidebar />
      <SidebarInset className="min-w-0">
        <SiteHeader />
        <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
          <Outlet />
        </div>
      </SidebarInset>
      <ScrollRestoration />
    </SidebarProvider>
  )
}
