/**
 * 删除（移入回收站）之后的提示：说明保留期，并可以立即撤销（从回收站恢复）。
 */
import { toast } from "sonner"

import { RECYCLE_RETENTION_DAYS } from "@/features/recycle-bin/recycle-format"

export const MOVED_TO_BIN_DESCRIPTION = `${RECYCLE_RETENTION_DAYS} 天内可以在“系统管理 · 回收站”中恢复，之后自动永久删除。`

export function toastMovedToBin({
  title,
  description = MOVED_TO_BIN_DESCRIPTION,
  undo,
  undoneTitle,
}: {
  /** 如“已将数据集“划痕”移入回收站”。 */
  title: string
  description?: string
  /** 撤销：从回收站恢复。省略时不显示“撤销”。 */
  undo?: () => Promise<unknown>
  /** 撤销成功时的提示，如“已恢复数据集“划痕””。 */
  undoneTitle?: string
}) {
  toast.success(title, {
    description,
    duration: 10_000,
    action: undo && {
      label: "撤销",
      onClick: () => {
        undo().then(
          () => toast.success(undoneTitle ?? "已恢复"),
          (error: Error) =>
            toast.error("撤销失败", {
              description: `${error.message}（可以在回收站中恢复）`,
            })
        )
      },
    },
  })
}
