import { useCallback, useEffect, useRef, useState } from "react"
import { useBlocker, type BlockerFunction } from "react-router"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import type {
  AnnotationSaver,
  SaveRequest,
  SaverState,
} from "@/features/annotation/autosave"
import { focusAfterDialog } from "@/features/annotation/shortcuts"
import { leavesImage } from "@/features/annotation/workspace-params"

type Blocker = ReturnType<typeof useBlocker>

/**
 * 编辑器（标注工作台、审图）中未保存的修改的守护：
 *
 * - 离开这张图片（浏览器后退 / 前进、站内链接、侧边栏等改变路径或 `?image=`）
 *   前先保存；保存失败或有冲突时不离开，`leavePromptOpen` 为 true，由
 *   {@link UnsavedChangesDialog} 提示可以留下处理或放弃修改。上一张 / 下一张等
 *   按钮自己先保存，保存成功后这里不再拦截。
 * - 离开编辑器（卸载）时保存最后的修改，失败时提示 `leaveError`。
 * - 关闭 / 刷新页面：有未保存的修改时提示，并用 `persist`（带 `keepalive` 的
 *   请求，规则与平时的保存相同）尽量补发。
 */
export function useSaveGuard<C>(
  saver: AnnotationSaver<C>,
  {
    leaveError,
    persist,
  }: {
    leaveError: string
    persist: (request: SaveRequest<C>, init: RequestInit) => Promise<unknown>
  }
) {
  const shouldBlock = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) =>
      leavesImage(currentLocation, nextLocation) && saver.hasPendingWork(),
    [saver]
  )
  const blocker = useBlocker(shouldBlock)
  // 保存失败、拦下的是这一次离开：显示提示。
  const [leaveFailed, setLeaveFailed] = useState<Blocker | null>(null)
  useEffect(() => {
    if (blocker.state !== "blocked") {
      return
    }
    let active = true
    void saver.flush().then((ok) => {
      if (!active) {
        return
      }
      if (ok) {
        blocker.proceed()
      } else {
        setLeaveFailed(blocker)
      }
    })
    return () => {
      active = false
    }
  }, [blocker, saver])
  const leavePromptOpen = blocker.state === "blocked" && leaveFailed === blocker

  // 离开编辑器时保存未保存的修改。
  const leaveErrorRef = useRef(leaveError)
  const persistRef = useRef(persist)
  useEffect(() => {
    leaveErrorRef.current = leaveError
    persistRef.current = persist
  })
  useEffect(
    () => () => {
      void saver.flush().then((ok) => {
        if (!ok && saver.isDirty()) {
          toast.error(leaveErrorRef.current, {
            description: saver.getState().error ?? undefined,
          })
        }
      })
    },
    [saver]
  )

  // 关闭 / 刷新页面：有未保存的修改时提示，并尽量用 keepalive 请求补发。
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      const { status } = saver.getState()
      if (saver.isDirty() || status === "saving") {
        event.preventDefault()
      }
    }
    const onPageHide = () => {
      const pending = saver.pending()
      if (!pending || saver.getState().status === "saving") {
        return
      }
      void persistRef
        .current(pending, { keepalive: true })
        .catch(() => undefined)
    }
    window.addEventListener("beforeunload", onBeforeUnload)
    window.addEventListener("pagehide", onPageHide)
    return () => {
      window.removeEventListener("beforeunload", onBeforeUnload)
      window.removeEventListener("pagehide", onPageHide)
    }
  }, [saver])

  return { blocker, leavePromptOpen }
}

/**
 * 离开时保存失败或有冲突的提示（见 {@link useSaveGuard}）：留在这里处理，或
 * 放弃修改并离开。`onDiscard` 在继续离开之前调用：放弃后卸载时的保存、自动保存
 * 都不再写入这些修改。`conflictMessage` 是有冲突时的说明。
 */
export function UnsavedChangesDialog({
  blocker,
  open,
  saveState,
  conflictMessage,
  onDiscard,
}: {
  blocker: Blocker
  open: boolean
  saveState: SaverState
  conflictMessage: string
  onDiscard: () => void
}) {
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          blocker.reset?.()
        }
      }}
    >
      <AlertDialogContent onCloseAutoFocus={(event) => focusAfterDialog(event)}>
        <AlertDialogHeader>
          <AlertDialogTitle>修改还没有保存</AlertDialogTitle>
          <AlertDialogDescription>
            {saveState.status === "conflict"
              ? conflictMessage
              : `最后的修改没有保存成功（${saveState.error ?? "未知错误"}）。留在这里可以重试保存；离开则放弃这些修改。`}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>留在这里</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={(event) => {
              // 不经过 onOpenChange（那里会取消这次离开）：继续离开后提示随
              // 拦截状态一起关闭。
              event.preventDefault()
              onDiscard()
              blocker.proceed?.()
            }}
          >
            放弃修改并离开
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
