import * as React from "react"

/**
 * 子页面（如数据集详情 `/datasets/12`）的标题：显示为面包屑的最后一级，
 * 同时用作浏览器标签页标题。导航项本身的页面不需要设置。
 */
const PageTitleContext = React.createContext<string | null>(null)
const SetPageTitleContext = React.createContext<
  ((title: string | null) => void) | null
>(null)

export function PageTitleProvider({ children }: { children: React.ReactNode }) {
  const [title, setTitle] = React.useState<string | null>(null)

  return (
    <SetPageTitleContext.Provider value={setTitle}>
      <PageTitleContext.Provider value={title}>
        {children}
      </PageTitleContext.Provider>
    </SetPageTitleContext.Provider>
  )
}

/** 由页面调用：声明当前子页面的标题；传 null（如数据还在加载）表示暂不显示。 */
export function usePageTitle(title: string | null | undefined) {
  const setTitle = React.useContext(SetPageTitleContext)

  React.useEffect(() => {
    if (!setTitle || !title) {
      return undefined
    }
    setTitle(title)
    return () => setTitle(null)
  }, [setTitle, title])
}

/** 由布局与面包屑读取当前子页面的标题。 */
export function useCurrentPageTitle() {
  return React.useContext(PageTitleContext)
}
