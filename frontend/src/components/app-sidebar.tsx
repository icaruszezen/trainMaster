import * as React from "react"
import { Aperture } from "lucide-react"
import { Link, useLocation } from "react-router"

import { NavMain } from "@/components/nav-main"
import { NavUser } from "@/components/nav-user"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar"
import { navGroups } from "@/config/navigation"

export function AppSidebar(props: React.ComponentProps<typeof Sidebar>) {
  // 移动端侧边栏是抽屉：每次导航（菜单、品牌、以后新增的链接，包括点击
  // 当前页面）之后都收起。location.key 在每次导航时都会变化。
  const { key: locationKey } = useLocation()
  const { setOpenMobile } = useSidebar()
  React.useEffect(() => {
    setOpenMobile(false)
  }, [locationKey, setOpenMobile])

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild>
              <Link to="/">
                <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                  <Aperture className="size-4" />
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">trainMaster</span>
                  <span className="truncate text-xs text-muted-foreground">
                    工业视觉平台
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavMain groups={navGroups} />
      </SidebarContent>
      <SidebarFooter>
        <NavUser />
      </SidebarFooter>
      <SidebarRail title="折叠 / 展开侧边栏" aria-label="折叠 / 展开侧边栏" />
    </Sidebar>
  )
}
