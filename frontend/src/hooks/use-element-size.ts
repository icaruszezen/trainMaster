import { useEffect, useRef, useState } from "react"

/** 观察元素的内容尺寸（ResizeObserver），挂载前为 0 × 0。 */
export function useElementSize<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })

  useEffect(() => {
    const element = ref.current
    if (!element) {
      return
    }
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect
      setSize((previous) =>
        previous.width === width && previous.height === height
          ? previous
          : { width, height }
      )
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  return [ref, size] as const
}
