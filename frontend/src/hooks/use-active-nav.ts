import { useLocation, useMatches } from "react-router"

import { findActiveNavItem } from "@/config/navigation"
import type { RouteHandle } from "@/router"

/** 当前路由对应的导航分组与导航项（最长前缀匹配）。 */
export function useActiveNav() {
  const { pathname } = useLocation()
  return findActiveNavItem(pathname)
}

/**
 * 当前地址是否落到了 404 页。
 * 例如 `/models/typo` 按前缀属于“模型仓库”（侧边栏仍高亮它），但页面是 404。
 */
export function useIsNotFound() {
  const handle = useMatches().at(-1)?.handle as RouteHandle | undefined
  return handle?.notFound === true
}
