/**
 * 审图编辑器在标记、导出、关闭审图包之前保存这张图片的修改，没有保存成功时提示
 * 原因。与界面无关（只用到保存器与提示），便于测试。
 */
import { toast } from "sonner"

import type { AnnotationSaver } from "@/features/annotation/autosave"

/**
 * 保存这张图片的修改；失败或有冲突时提示原因（`action` 是因此没有进行的操作，
 * 如“标记”）。保存期间切换了图片、放弃了修改时不提示。
 *
 * 冲突一直保留到用户处理（载入最新版本或覆盖）：撤销到开始编辑时的框之后同样
 * 提示，否则操作会没有任何反应。
 */
export async function flushWithNotice<C>(
  saver: AnnotationSaver<C>,
  action: string | null
) {
  const context = saver.getContext()
  if (await saver.flush()) {
    return true
  }
  if (saver.getContext() !== context) {
    return false // 保存期间切换了图片
  }
  const state = saver.getState()
  if (state.status === "conflict") {
    toast.error(
      action
        ? `请先处理这张图片的保存冲突，再${action}`
        : "请先处理这张图片的保存冲突"
    )
  } else if (saver.isDirty()) {
    toast.error(
      action ? `保存失败，没有${action}` : "这张图片的修改没有保存成功",
      { description: state.error ?? undefined }
    )
  }
  return false
}
