// 上传审图结果：传完后可以“在后台继续”（关闭对话框），平台之后才检查文件。
// 这时被拒绝的上传要提示，否则用户以为审图结果已经应用了。
import { MutationObserver, QueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { notifyUploadFailure } from "@/features/reviews/upload-results-dialog"
import { ApiError } from "@/lib/api"

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))

const wrongReview = () =>
  new ApiError(
    422,
    "invalid_results",
    "这是审图任务 #11 的审图结果，不是这个任务的"
  )

beforeEach(() => {
  vi.mocked(toast.error).mockClear()
})

describe("notifyUploadFailure", () => {
  it("tells about a rejected upload once the dialog is closed", () => {
    notifyUploadFailure(wrongReview(), false)
    expect(toast.error).toHaveBeenCalledWith("审图结果没有应用", {
      description: "这是审图任务 #11 的审图结果，不是这个任务的",
    })
  })

  it("leaves the error to the dialog while it is open", () => {
    notifyUploadFailure(wrongReview(), true)
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("does not report a canceled upload", () => {
    notifyUploadFailure(new DOMException("canceled", "AbortError"), false)
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("still runs after the form unmounts (mutation-level callback)", async () => {
    const client = new QueryClient()
    const uploads: ((error: Error) => void)[] = []
    let open = true
    const observer = new MutationObserver(client, {
      mutationFn: () =>
        new Promise<never>((_, reject) => {
          uploads.push(reject)
        }),
      onError: (error: Error) => notifyUploadFailure(error, open),
    })
    const done = observer.mutate().catch(() => undefined)
    await vi.waitFor(() => expect(uploads).toHaveLength(1))
    // “在后台继续”：对话框关闭，表单（useMutation 的观察者）卸载。
    open = false
    observer.reset()
    uploads[0](wrongReview())
    await done
    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1))
  })
})
