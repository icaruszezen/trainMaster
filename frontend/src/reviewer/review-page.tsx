import { useEffect, useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { FileArchive, X } from "lucide-react"
import { Navigate, useNavigate } from "react-router"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { ToolButton } from "@/features/annotation/toolbar-parts"
import { LoadError } from "@/features/datasets/load-error"
import { reviewSummaryQuery } from "@/features/reviews/api"
import {
  ReviewEditor,
  ReviewEditorSkeleton,
  type ReviewEditorControls,
} from "@/features/reviews/review-editor"
import {
  closePackage,
  localReviewSource,
  sessionKey,
  sessionQuery,
  type PackageInfo,
} from "@/reviewer/api"
import {
  discardEditor,
  registerEditorControls,
} from "@/reviewer/editor-controls"
import { useExportResults } from "@/reviewer/export-dialog"
import { packageKey, pageTitle } from "@/reviewer/format"
import { usePageTitle } from "@/reviewer/start-page"

/** `/review?image=`：审打开的审图包（编辑器与平台的网页审图相同）。 */
export function ReviewPage() {
  const session = useQuery(sessionQuery())
  if (session.data === undefined) {
    return (
      <div className="flex h-svh flex-col">
        {session.error ? (
          <div className="p-4">
            <LoadError
              title="无法连接本地审图程序"
              error={session.error}
              retrying={session.isFetching}
              onRetry={() => void session.refetch()}
            />
          </div>
        ) : (
          <ReviewEditorSkeleton />
        )}
      </div>
    )
  }
  const pkg = session.data.package
  // 没有打开的审图包，或正在打开另一个审图包：回到开始页（那里显示打开的进度，
  // 打开完成后进入新的审图包）。编辑器随之卸载并保存最后的修改：程序在打开完成
  // 时才关闭这个审图包，在那之前保存的修改都还写进它。
  if (!pkg || session.data.opening) {
    return <Navigate to="/" replace />
  }
  return <PackageGate pkg={pkg} />
}

/**
 * 审着的审图包被换掉了（如在另一个页面打开了别的审图包，这里没有看到打开的
 * 过程）：程序已经关闭了它，这时保存的修改会写进新的审图包。所以放弃还没有保存
 * 的修改（提示用户），回到开始页。
 */
function PackageGate({ pkg }: { pkg: PackageInfo }) {
  const navigate = useNavigate()
  const [shown] = useState(pkg)
  const switched = packageKey(pkg) !== packageKey(shown)
  useEffect(() => {
    if (!switched) {
      return
    }
    if (discardEditor()) {
      toast.warning("已打开另一个审图包", {
        description: `“${shown.review.name}”中最后的修改没有保存。`,
      })
    }
    navigate("/", { replace: true })
  }, [switched, shown, navigate])
  return <PackageReview pkg={switched ? shown : pkg} />
}

function PackageReview({ pkg }: { pkg: PackageInfo }) {
  const source = useMemo(
    () => localReviewSource(pkg.package_id),
    [pkg.package_id]
  )
  const summary = useQuery(reviewSummaryQuery(source))
  usePageTitle(pageTitle(pkg))

  return (
    <div className="flex h-svh flex-col bg-background">
      {summary.data ? (
        <ReviewEditor
          source={source}
          review={summary.data}
          backTo="/"
          backLabel="返回开始页"
          local
          actions={(controls) => (
            <LocalActions controls={controls} reviewName={pkg.review.name} />
          )}
        />
      ) : summary.error ? (
        <div className="p-4">
          <LoadError
            title="无法读取审图包"
            error={summary.error}
            retrying={summary.isFetching}
            onRetry={() => void summary.refetch()}
          />
        </div>
      ) : (
        <ReviewEditorSkeleton />
      )}
    </div>
  )
}

/** 编辑器工具栏中本地审图程序的操作：导出审图结果、关闭审图包。 */
function LocalActions({
  controls,
  reviewName,
}: {
  controls: ReviewEditorControls
  reviewName: string
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  useEffect(() => registerEditorControls(controls), [controls])
  const exporter = useExportResults(reviewName)
  const close = useMutation({
    mutationFn: async () => {
      if (!(await controls.flush())) {
        return false
      }
      await closePackage()
      return true
    },
    onSuccess: async (closed) => {
      if (!closed) {
        return
      }
      await queryClient.refetchQueries({ queryKey: sessionKey, exact: true })
      navigate("/")
    },
    onError: (error) =>
      toast.error("无法关闭审图包", { description: error.message }),
  })

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={exporter.run}
        disabled={exporter.pending}
        title="导出审图结果（之后在平台上传）"
        aria-label="导出审图结果"
      >
        {exporter.pending ? <Spinner /> : <FileArchive />}
        <span className="hidden xl:inline">导出审图结果</span>
        <span className="hidden sm:inline xl:hidden">导出</span>
      </Button>
      <ToolButton
        label="关闭审图包"
        onClick={() => close.mutate()}
        disabled={close.isPending}
        className="hidden sm:inline-flex"
      >
        <X />
      </ToolButton>
      {exporter.dialog}
    </>
  )
}
