import { useEffect, useRef } from "react"

import {
  allowsRepeat,
  commandForKey,
  copiesSelectedText,
  shouldIgnoreKey,
  type Command,
} from "@/features/annotation/shortcuts"

/**
 * 编辑器的快捷键（标注工作台与审图共用）：在 window 上监听按键，按
 * {@link commandForKey} 换成命令交给 `run`。输入框、对话框中的按键（见
 * `shouldIgnoreKey`）、页面上选中了文字时的 Ctrl / ⌘+C、按住不放时不该重复的
 * 命令都不执行；`run` 返回 false 时这次按键交给浏览器（不阻止默认行为）。
 * `run` 与 `hasSelection` 每次渲染后更新，监听器总是用最新的。
 */
export function useEditorKeys(
  run: (command: Command) => false | void,
  {
    mac,
    hasSelection,
    review = false,
  }: { mac: boolean; hasSelection: boolean; review?: boolean }
) {
  const runRef = useRef(run)
  useEffect(() => {
    runRef.current = run
  })
  const hasSelectionRef = useRef(false)
  useEffect(() => {
    hasSelectionRef.current = hasSelection
  })

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const command = commandForKey(event, {
        mac,
        hasSelection: hasSelectionRef.current,
        review,
      })
      if (!command || shouldIgnoreKey(event, command)) {
        return
      }
      if (
        command.type === "copy" &&
        copiesSelectedText(event.target, window.getSelection())
      ) {
        return // 复制页面上选中的文字
      }
      if (event.repeat && !allowsRepeat(command)) {
        event.preventDefault()
        return
      }
      if (runRef.current(command) === false) {
        return
      }
      event.preventDefault()
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [mac, review])
}
