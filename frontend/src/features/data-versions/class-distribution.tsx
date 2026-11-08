/**
 * 各类别在训练 / 验证 / 测试集中的目标数（宽屏时还有图片数），预览与详情页
 * 共用。有目标、但某个划分中没有的单元格标为警告色。
 */
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  SPLITS,
  type DataVersionLabelStats,
  type Split,
  type SplitCounts,
} from "@/features/data-versions/api"
import {
  classColor,
  labelObjects,
  SPLIT_LABELS,
} from "@/features/data-versions/format"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type ClassDistributionProps = {
  labels: readonly DataVersionLabelStats[]
  /** 没有目标的图片（负样本）。 */
  negatives?: SplitCounts
  /** 显示测试集一列（测试比例大于 0 或测试集中有图片时）。 */
  showTest: boolean
  /** 同时显示图片数；手机宽度只显示目标数。 */
  showImages: boolean
  className?: string
}

export function ClassDistribution({
  labels,
  negatives,
  showTest,
  showImages,
  className,
}: ClassDistributionProps) {
  const splits: Split[] = showTest ? [...SPLITS] : ["train", "val"]
  return (
    <div className={cn("overflow-hidden rounded-lg border", className)}>
      <Table className="table-fixed text-xs">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-8 pl-3">类别（目标数）</TableHead>
            {splits.map((split) => (
              <TableHead
                key={split}
                className={cn(
                  "h-8 text-right",
                  showImages ? "w-20" : "w-14",
                  split === splits[splits.length - 1] && "pr-3"
                )}
              >
                {SPLIT_LABELS[split]}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {labels.map((label, index) => {
            const hasObjects = labelObjects(label) > 0
            return (
              <TableRow key={label.name}>
                <TableCell className="py-1.5 pl-3">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className="w-4 shrink-0 font-mono text-muted-foreground tabular-nums">
                      {index}
                    </span>
                    <span
                      className="size-2 shrink-0 rounded-full"
                      style={{ backgroundColor: classColor(label) }}
                      aria-hidden
                    />
                    <span className="truncate" title={label.name}>
                      {label.name}
                    </span>
                  </span>
                </TableCell>
                {splits.map((split) => {
                  const cell = label[split]
                  const missing = hasObjects && cell.objects === 0
                  return (
                    <TableCell
                      key={split}
                      className={cn(
                        "py-1.5 text-right tabular-nums",
                        split === splits[splits.length - 1] && "pr-3",
                        missing &&
                          "font-medium text-amber-700 dark:text-amber-500",
                        !hasObjects && "text-muted-foreground"
                      )}
                      title={
                        missing
                          ? `“${label.name}”在${SPLIT_LABELS[split]}集中没有目标`
                          : undefined
                      }
                    >
                      {formatNumber(cell.objects)}
                      {showImages && (
                        <span className="block text-[11px] font-normal text-muted-foreground">
                          {formatNumber(cell.images)} 张
                        </span>
                      )}
                    </TableCell>
                  )
                })}
              </TableRow>
            )
          })}
          {negatives && (
            <TableRow className="bg-muted/30 hover:bg-muted/30">
              <TableCell className="py-1.5 pl-3 text-muted-foreground">
                负样本（张）
              </TableCell>
              {splits.map((split) => (
                <TableCell
                  key={split}
                  className={cn(
                    "py-1.5 text-right text-muted-foreground tabular-nums",
                    split === splits[splits.length - 1] && "pr-3"
                  )}
                >
                  {formatNumber(negatives[split])}
                </TableCell>
              ))}
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
