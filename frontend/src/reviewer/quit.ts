import { createContext, useContext } from "react"

/** 退出程序（先保存最后的修改）；由 Root 提供。 */
export const QuitContext = createContext<() => void>(() => undefined)

export function useQuit() {
  return useContext(QuitContext)
}
