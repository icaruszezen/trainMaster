import { useEffect, useRef, useState } from "react"
import { QueryClientProvider, useQuery } from "@tanstack/react-query"
import { Power, RefreshCw } from "lucide-react"
import {
  createBrowserRouter,
  Navigate,
  Outlet,
  useRouteError,
  type RouteObject,
} from "react-router"
import { RouterProvider } from "react-router/dom"
import { toast } from "sonner"

import { ThemeProvider } from "@/components/theme-provider"
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
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { queryClient } from "@/lib/query-client"
import { acknowledgeClosing, quitProgram, sessionQuery } from "@/reviewer/api"
import { flushEditor } from "@/reviewer/editor-controls"
import { QuitContext } from "@/reviewer/quit"
import { ReviewPage } from "@/reviewer/review-page"
import { StartPage } from "@/reviewer/start-page"

declare global {
  interface Window {
    /** 程序窗口的关闭按钮：先保存最后的修改再退出（见 ui_windows.go）。 */
    __tmReviewerRequestClose?: () => void
  }
}

const routes: RouteObject[] = [
  {
    Component: Root,
    ErrorBoundary: RouteError,
    children: [
      { path: "/", Component: StartPage },
      { path: "/review", Component: ReviewPage },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
]

const router = createBrowserRouter(routes)

export function ReviewerApp() {
  return (
    <ThemeProvider storageKey="trainmaster-theme">
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <RouterProvider router={router} />
          <Toaster
            position="top-center"
            closeButton
            containerAriaLabel="通知"
            toastOptions={{ closeButtonAriaLabel: "关闭通知" }}
          />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}

/**
 * 所有页面的外层：退出程序（关闭窗口时先保存最后的修改）、拦下拖进页面的文件
 * （浏览器 / WebView2 会直接打开它，离开审图页面）。
 */
function Root() {
  const session = useQuery(sessionQuery())
  const [quit, setQuit] = useState(false)
  const [unsavedOpen, setUnsavedOpen] = useState(false)

  const doQuit = async () => {
    try {
      await quitProgram()
      setQuit(true)
    } catch (error) {
      toast.error("无法退出本地审图程序", {
        description: error instanceof Error ? error.message : undefined,
      })
    }
  }

  // 保存最后的修改后退出；保存失败时询问。
  const requestQuit = async () => {
    if (await flushEditor()) {
      await doQuit()
    } else {
      setUnsavedOpen(true)
    }
  }

  // 程序窗口的关闭按钮：先告诉程序页面接手了（否则 3 秒后直接关闭窗口）。
  const requestQuitRef = useLatest(requestQuit)
  useEffect(() => {
    window.__tmReviewerRequestClose = () => {
      void acknowledgeClosing()
        .catch(() => undefined)
        .then(() => requestQuitRef.current())
    }
    return () => {
      delete window.__tmReviewerRequestClose
    }
  }, [requestQuitRef])

  useEffect(() => {
    const onDragOver = (event: DragEvent) => {
      if (event.dataTransfer?.types.includes("Files")) {
        event.preventDefault()
        event.dataTransfer.dropEffect = "none"
      }
    }
    const onDrop = (event: DragEvent) => {
      if (!event.dataTransfer?.types.includes("Files")) {
        return
      }
      event.preventDefault()
      toast.info("不能把文件拖进这里打开", {
        description:
          "请点“打开审图包”选择审图包，或者把审图包（.zip）拖到本地审图程序的图标上。",
      })
    }
    window.addEventListener("dragover", onDragOver)
    window.addEventListener("drop", onDrop)
    return () => {
      window.removeEventListener("dragover", onDragOver)
      window.removeEventListener("drop", onDrop)
    }
  }, [])

  if (quit) {
    return <QuitScreen window={session.data?.window ?? false} />
  }
  return (
    <QuitContext value={() => void requestQuit()}>
      <Outlet />
      <AlertDialog open={unsavedOpen} onOpenChange={setUnsavedOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>最后的修改没有保存</AlertDialogTitle>
            <AlertDialogDescription>
              这张图片的修改没有保存成功（原因见工具栏的保存状态）。现在退出，这些修改会丢失。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>继续审图</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void doQuit()}
            >
              仍然退出
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </QuitContext>
  )
}

/** 总是指向最新值的 ref（给只注册一次的事件处理函数用）。 */
function useLatest<T>(value: T) {
  const ref = useRef(value)
  useEffect(() => {
    ref.current = value
  })
  return ref
}

/** 在浏览器中退出后：程序已经停止，页面不再能用。 */
function QuitScreen({ window: inWindow }: { window: boolean }) {
  return (
    <Empty className="min-h-svh">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Power />
        </EmptyMedia>
        <EmptyTitle>本地审图程序已退出</EmptyTitle>
        <EmptyDescription>
          {inWindow
            ? "窗口即将关闭。"
            : "审图进度都已保存在审图包文件夹中，可以关闭这个页面了。下次打开同一个审图包会继续上次的进度。"}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

function RouteError() {
  const error = useRouteError()
  return (
    <Empty className="min-h-svh">
      <EmptyHeader>
        <EmptyTitle>页面出错了</EmptyTitle>
        <EmptyDescription>
          {error instanceof Error ? error.message : "未知错误"}
          。审图进度已保存在审图包文件夹中，重新载入页面即可继续。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button variant="outline" onClick={() => window.location.assign("/")}>
          <RefreshCw />
          重新载入
        </Button>
      </EmptyContent>
    </Empty>
  )
}
