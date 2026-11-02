import { useEffect, useState } from "react"

/**
 * 当前时间（毫秒），每 `intervalMs` 更新一次。用于“3 分钟前”这类相对时间：
 * 数据没有变化（如所有节点都已离线）时页面也会按时刷新这些文字。
 */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])

  return now
}
