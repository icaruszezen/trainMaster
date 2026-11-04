/**
 * 正在审图时编辑器交出的操作（{@link ReviewEditorControls}）：退出程序、关闭审图包
 * 之前先保存这张图片的修改。编辑器的工具栏（本地审图程序的操作按钮）挂载时登记，
 * 卸载时撤销。
 */
import type { ReviewEditorControls } from "@/features/reviews/review-editor"

let current: ReviewEditorControls | null = null

/** 登记编辑器的操作；返回撤销登记的函数。 */
export function registerEditorControls(controls: ReviewEditorControls) {
  current = controls
  return () => {
    if (current === controls) {
      current = null
    }
  }
}

/** 保存正在编辑的图片的修改；没有在审图时直接返回 true。 */
export function flushEditor(): Promise<boolean> {
  return current ? current.flush() : Promise.resolve(true)
}
