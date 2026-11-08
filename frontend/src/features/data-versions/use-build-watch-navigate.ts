import { useEffect } from "react"
import { useNavigate } from "react-router"

import { setBuildWatchNavigate } from "@/features/data-versions/build-watch"

/**
 * 数据版本的页面挂载时登记路由的跳转：生成完成的提示中的“查看”不必整页刷新
 * （离开这些页面后仍然有效）。
 */
export function useBuildWatchNavigate() {
  const navigate = useNavigate()
  useEffect(() => {
    setBuildWatchNavigate((path) => void navigate(path))
  }, [navigate])
}
