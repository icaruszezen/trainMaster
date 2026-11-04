import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Copy, FolderOpen, TriangleAlert, Upload } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { copyText } from "@/lib/clipboard"
import { formatBytes, formatDateTime, formatNumber } from "@/lib/format"
import {
  exportResults,
  revealInFolder,
  sessionKey,
  type ExportResult,
} from "@/reviewer/api"
import { flushEditor } from "@/reviewer/editor-controls"
import { exportSummary } from "@/reviewer/format"

/**
 * 导出审图结果：先保存正在编辑的图片，再导出（覆盖之前导出的文件），然后在
 * 对话框中显示文件的位置与包含的修改，提醒到平台上传。
 */
export function useExportResults(reviewName: string) {
  const queryClient = useQueryClient()
  const [result, setResult] = useState<ExportResult | null>(null)
  const mutation = useMutation({
    mutationFn: async () => {
      if (!(await flushEditor())) {
        return null
      }
      return exportResults()
    },
    onSuccess: (exported) => {
      if (exported) {
        setResult(exported)
      }
      void queryClient.invalidateQueries({ queryKey: sessionKey })
    },
    onError: (error) => {
      toast.error("导出审图结果失败", { description: error.message })
    },
  })
  return {
    run: () => mutation.mutate(),
    pending: mutation.isPending,
    dialog: (
      <ExportDialog
        result={result}
        reviewName={reviewName}
        onClose={() => setResult(null)}
      />
    ),
  }
}

function ExportDialog({
  result,
  reviewName,
  onClose,
}: {
  result: ExportResult | null
  reviewName: string
  onClose: () => void
}) {
  // 关闭动画期间仍显示刚才的结果。
  const [shown, setShown] = useState(result)
  if (result && result !== shown) {
    setShown(result)
  }
  const reveal = useMutation({
    mutationFn: () => revealInFolder("results"),
    onError: (error) =>
      toast.error("无法打开文件夹", { description: error.message }),
  })

  return (
    <Dialog open={result !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>审图结果已导出</DialogTitle>
          <DialogDescription>
            {shown ? exportSummary(shown) : null}
          </DialogDescription>
        </DialogHeader>
        {shown && (
          <div className="grid min-w-0 gap-4 text-sm">
            <div className="grid gap-1.5 rounded-md border bg-muted/40 p-3">
              <p className="font-medium break-all">{shown.file_name}</p>
              <p className="font-mono text-xs break-all text-muted-foreground">
                {shown.path}
              </p>
              <p className="text-xs text-muted-foreground tabular-nums">
                {formatBytes(shown.size_bytes)} · 导出于{" "}
                {formatDateTime(shown.exported_at, { withSeconds: true })}
                {" · "}
                {formatNumber(shown.images)} 张图片
              </p>
              <div className="flex flex-wrap gap-2 pt-1">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => reveal.mutate()}
                  disabled={reveal.isPending}
                >
                  <FolderOpen />
                  在文件夹中显示
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    void copyText(shown.path).then((ok) =>
                      ok
                        ? toast.success("已复制文件路径")
                        : toast.error("复制失败，请手动选中路径复制")
                    )
                  }
                >
                  <Copy />
                  复制路径
                </Button>
              </div>
            </div>

            {shown.skipped > 0 && (
              <Alert variant="destructive">
                <TriangleAlert />
                <AlertTitle>
                  {formatNumber(shown.skipped)} 个标注文件无法读取，没有导出
                </AlertTitle>
                <AlertDescription>
                  <p>
                    这些图片的审核状态照常导出。请用标注工具修好这些文件（或在审图中重新保存这些图片的框）后再导出：
                  </p>
                  <ul className="list-inside list-disc font-mono text-xs break-all">
                    {shown.skipped_paths.map((path) => (
                      <li key={path}>{path}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            )}

            <Alert>
              <Upload />
              <AlertTitle>下一步：上传到平台</AlertTitle>
              <AlertDescription>
                <p>
                  在平台上打开审图任务“{reviewName}
                  ”，点击“上传审图结果”，选择这个文件。平台会读取其中的审核状态与修改过的标注。
                </p>
              </AlertDescription>
            </Alert>
          </div>
        )}
        <DialogFooter>
          <DialogClose asChild>
            <Button>完成</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
