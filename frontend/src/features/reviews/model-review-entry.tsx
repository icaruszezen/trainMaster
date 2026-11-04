/**
 * 模型详情页上的审图入口：“用这个模型审图”（打开新建对话框，选中这个模型），
 * 以及用这个模型的审图任务的链接。
 */
import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ScanEye } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import type { Model } from "@/features/models/api"
import { ALL_REVIEWS, reviewsQuery } from "@/features/reviews/api"
import { CreateReviewDialog } from "@/features/reviews/create-review-dialog"
import { reviewModelBlocker } from "@/features/reviews/review-format"
import { formatNumber } from "@/lib/format"

/** “用这个模型审图”按钮与新建审图任务的对话框。 */
export function ReviewWithModelButton({ model }: { model: Model }) {
  const [open, setOpen] = useState(false)
  // 每次打开换一个 key，表单从头开始。
  const [key, setKey] = useState(0)
  const blocker = reviewModelBlocker(model)
  return (
    <>
      <Button
        variant="outline"
        onClick={() => {
          setKey((value) => value + 1)
          setOpen(true)
        }}
        disabled={blocker !== null}
        title={blocker ?? "用这个模型识别数据集，逐张确认识别结果"}
      >
        <ScanEye />
        用这个模型审图
      </Button>
      <CreateReviewDialog
        key={key}
        open={open}
        onOpenChange={setOpen}
        modelId={model.id}
      />
    </>
  )
}

/** 用这个模型的审图任务数，链接到按模型筛选的审图任务列表；没有时不显示。 */
export function ModelReviewsLink({ modelId }: { modelId: number }) {
  const query = useQuery(reviewsQuery({ ...ALL_REVIEWS, modelId }))
  const count = query.data?.total ?? 0
  if (count === 0) {
    return null
  }
  return (
    <Link
      to={`/models/reviews?model=${modelId}`}
      className="inline-flex items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
    >
      <ScanEye className="size-3.5 shrink-0" />
      {formatNumber(count)} 个审图任务
    </Link>
  )
}
