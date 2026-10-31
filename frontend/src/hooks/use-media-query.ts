import { useCallback, useSyncExternalStore } from "react"

/**
 * 媒体查询当前是否匹配（如 `(min-width: 64rem)`，与 Tailwind 的 `lg:` 一致），
 * 窗口大小变化时更新。用于只在某个宽度下渲染的部分（而不是渲染后用 CSS 隐藏）。
 */
export function useMediaQuery(query: string) {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const list = window.matchMedia(query)
      list.addEventListener("change", onChange)
      return () => list.removeEventListener("change", onChange)
    },
    [query]
  )
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => false
  )
}
