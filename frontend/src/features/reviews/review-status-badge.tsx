import { Check, CircleDashed, ImagePlus } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import type { ReviewImageStatus } from "@/features/reviews/api"
import {
  reviewImageStatusLabels,
  reviewImageStatusStyles,
} from "@/features/reviews/review-status"
import { cn } from "@/lib/utils"

/** 审核状态的图标：待审核虚线圈、已审核对勾、需加样“加图片”。 */
export const reviewImageStatusIcons = {
  pending: CircleDashed,
  approved: Check,
  needs_sample: ImagePlus,
} satisfies Record<ReviewImageStatus, unknown>

/** 审核状态的徽标（待审核灰色、已审核绿色、需加样琥珀色）。 */
export function ReviewImageStatusBadge({
  status,
  className,
}: {
  status: ReviewImageStatus
  className?: string
}) {
  const Icon = reviewImageStatusIcons[status]
  return (
    <Badge
      variant="outline"
      className={cn(reviewImageStatusStyles[status].badge, className)}
    >
      <Icon aria-hidden />
      {reviewImageStatusLabels[status]}
    </Badge>
  )
}

/** 审核状态的圆点（列表、图例中）。 */
export function ReviewStatusDot({
  status,
  className,
}: {
  status: ReviewImageStatus
  className?: string
}) {
  return (
    <span
      className={cn(
        "size-2 shrink-0 rounded-full",
        reviewImageStatusStyles[status].dot,
        className
      )}
      aria-hidden
    />
  )
}
