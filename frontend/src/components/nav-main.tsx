import { Link } from "react-router"

import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar"
import type { NavGroup } from "@/config/navigation"
import { useActiveNav } from "@/hooks/use-active-nav"

export function NavMain({ groups }: { groups: NavGroup[] }) {
  const active = useActiveNav()

  return groups.map((group) => (
    <SidebarGroup key={group.id}>
      <SidebarGroupLabel>{group.title}</SidebarGroupLabel>
      <SidebarMenu>
        {group.items.map((item) => (
          <SidebarMenuItem key={item.id}>
            <SidebarMenuButton
              asChild
              tooltip={item.title}
              isActive={active?.item.id === item.id}
            >
              <Link to={item.path}>
                <item.icon />
                <span>{item.title}</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        ))}
      </SidebarMenu>
    </SidebarGroup>
  ))
}
