import { useEffect, useRef, type ReactNode } from "react"

import { TabsList } from "@/components/ui/tabs"

/**
 * 标签列表：手机宽度下放不下一行时横向滚动（不撑宽页面），并把选中的标签滚到
 * 可见处（如打开 ?tab=versions 的链接时）。`value` 是当前选中的标签，放在
 * `<Tabs>` 中代替 `<TabsList>`。
 */
export function ScrollingTabsList({
  value,
  children,
}: {
  value: string
  children: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const scroller = ref.current
    const tab = scroller?.querySelector<HTMLElement>(
      '[role="tab"][data-state="active"]'
    )
    if (!scroller || !tab) {
      return
    }
    // 只横向滚动标签列表；scrollIntoView 可能连页面一起滚动。
    const box = scroller.getBoundingClientRect()
    const rect = tab.getBoundingClientRect()
    if (rect.left < box.left) {
      scroller.scrollLeft -= box.left - rect.left
    } else if (rect.right > box.right) {
      scroller.scrollLeft += rect.right - box.right
    }
  }, [value])
  return (
    <div ref={ref} className="scrollbar-auto-hide overflow-x-auto">
      <TabsList className="w-max min-w-full sm:w-fit sm:min-w-0">
        {children}
      </TabsList>
    </div>
  )
}
