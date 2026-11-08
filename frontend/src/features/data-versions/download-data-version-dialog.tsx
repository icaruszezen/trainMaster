/**
 * 下载数据版本：YOLO / Pascal VOC / COCO 三种格式（都按训练 / 验证 / 测试划分
 * 好），可以不含增强图片。压缩包在下载时由服务端流式生成，交给浏览器原生
 * 下载，不读进内存。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  createDataVersionExport,
  refreshDataVersions,
  type DataVersion,
  type DataVersionFormat,
} from "@/features/data-versions/api"
import {
  dataVersionFullTitle,
  splitSummary,
  versionSplitCounts,
} from "@/features/data-versions/format"
import { isPermanentError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

export const DATA_VERSION_FORMATS: readonly {
  id: DataVersionFormat
  title: string
  description: string
}[] = [
  {
    id: "yolo",
    title: "YOLO",
    description:
      "Ultralytics 布局：images/、labels/ 下按划分分文件夹，附 data.yaml 与 classes.txt，可以直接训练",
  },
  {
    id: "voc",
    title: "Pascal VOC",
    description:
      "JPEGImages/、Annotations/（.xml）与 ImageSets/Main/ 的划分列表；不是 JPEG 的图片保留原格式",
  },
  {
    id: "coco",
    title: "COCO",
    description:
      "images/ 下按划分分文件夹，每个划分一个 annotations/instances_<划分>.json",
  },
]

type DownloadDataVersionDialogProps = {
  version: DataVersion | null
  onOpenChange: (open: boolean) => void
}

export function DownloadDataVersionDialog({
  version,
  onOpenChange,
}: DownloadDataVersionDialogProps) {
  return (
    <Dialog open={version !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        {version && (
          <DownloadForm version={version} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DownloadForm({
  version,
  onDone,
}: {
  version: DataVersion
  onDone: () => void
}) {
  const groupId = useId()
  const augmentedId = useId()
  const [format, setFormat] = useState<DataVersionFormat>("yolo")
  const [includeAugmented, setIncludeAugmented] = useState(true)
  const hasAugmented = version.augmented_count > 0

  const mutation = useMutation({
    mutationFn: () =>
      createDataVersionExport(version.id, {
        format,
        include_augmented: hasAugmented && includeAugmented,
      }),
    onSuccess: (result) => {
      startDownload(result.url, result.file_name)
      toast.success("已开始下载", {
        description:
          "压缩包在下载时实时生成，数据版本较大时需要一些时间，可在浏览器的下载列表中查看进度。",
      })
      onDone()
    },
    onError: (error) => {
      // 版本可能已被删除或移入回收站：刷新列表与详情。
      if (isPermanentError(error)) {
        void refreshDataVersions()
      }
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!mutation.isPending) {
      mutation.mutate()
    }
  }

  const counts =
    hasAugmented && !includeAugmented
      ? {
          train: version.train_count - version.augmented_count,
          val: version.val_count,
          test: version.test_count,
        }
      : versionSplitCounts(version)

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle className="break-words">
          下载“{dataVersionFullTitle(version)}”
        </DialogTitle>
        <DialogDescription>
          按生成时的划分打包：{splitSummary(counts)}
          。类别编号按版本冻结的类别列表（共{" "}
          {formatNumber(version.classes.length)} 个，第一个为 0）。
        </DialogDescription>
      </DialogHeader>

      <div className="grid gap-3">
        <Label id={groupId}>格式</Label>
        <RadioGroup
          value={format}
          onValueChange={(next) => {
            setFormat(next as DataVersionFormat)
            mutation.reset()
          }}
          aria-labelledby={groupId}
          className="grid gap-2 sm:grid-cols-3"
          disabled={mutation.isPending}
        >
          {DATA_VERSION_FORMATS.map((option) => {
            const itemId = `${groupId}-${option.id}`
            return (
              <label
                key={option.id}
                htmlFor={itemId}
                className={cn(
                  "flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
                  format === option.id && "border-primary/60 bg-muted/40"
                )}
              >
                <RadioGroupItem
                  id={itemId}
                  value={option.id}
                  className="mt-0.5"
                />
                <span className="grid min-w-0 gap-1">
                  <span className="text-sm font-medium">{option.title}</span>
                  <span className="text-xs text-pretty text-muted-foreground">
                    {option.description}
                  </span>
                </span>
              </label>
            )
          })}
        </RadioGroup>
      </div>

      {hasAugmented && (
        <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
          <div className="grid min-w-0 gap-1">
            <Label htmlFor={augmentedId}>包含增强图片</Label>
            <p className="text-xs text-pretty text-muted-foreground">
              {formatNumber(version.augmented_count)}{" "}
              张增强图片都在训练集中。用支持在线增强的训练工具（如
              Ultralytics）时也可以只下载原图。
            </p>
          </div>
          <Switch
            id={augmentedId}
            checked={includeAugmented}
            onCheckedChange={(checked) => {
              setIncludeAugmented(checked)
              mutation.reset()
            }}
            disabled={mutation.isPending}
          />
        </div>
      )}

      {mutation.error && (
        <p role="alert" className="text-sm break-words text-destructive">
          {mutation.error.message}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onDone}
          disabled={mutation.isPending}
        >
          取消
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <Spinner aria-hidden />}
          下载
        </Button>
      </DialogFooter>
    </form>
  )
}
