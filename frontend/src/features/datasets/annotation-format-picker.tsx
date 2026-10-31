/** 标注格式的单选卡片，导入（上传内容的格式）与下载（导出的格式）共用。 */
import { useId } from "react"

import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import type { AnnotationFormat } from "@/features/datasets/api"
import {
  ANNOTATION_FORMATS,
  annotationExtensions,
  annotationFormatNames,
} from "@/features/datasets/files"
import { cn } from "@/lib/utils"

const formatTitles: Record<AnnotationFormat, string> = {
  ...annotationFormatNames,
  voc: "Pascal VOC",
}

type AnnotationFormatPickerProps = {
  label: string
  value: AnnotationFormat
  onChange: (format: AnnotationFormat) => void
  /** 每种格式一句说明（目录结构 / 转换方式）。 */
  descriptions: Record<AnnotationFormat, string>
  disabled?: boolean
}

export function AnnotationFormatPicker({
  label,
  value,
  onChange,
  descriptions,
  disabled,
}: AnnotationFormatPickerProps) {
  const groupId = useId()

  return (
    <div className="grid gap-3">
      <Label id={groupId}>{label}</Label>
      <RadioGroup
        value={value}
        onValueChange={(next) => onChange(next as AnnotationFormat)}
        aria-labelledby={groupId}
        className="grid gap-2 sm:grid-cols-3"
        disabled={disabled}
      >
        {ANNOTATION_FORMATS.map((format) => {
          const itemId = `${groupId}-${format}`
          return (
            <label
              key={format}
              htmlFor={itemId}
              className={cn(
                "flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40",
                value === format && "border-primary/60 bg-muted/40"
              )}
            >
              <RadioGroupItem id={itemId} value={format} className="mt-0.5" />
              <span className="grid min-w-0 gap-1">
                <span className="text-sm font-medium">
                  {formatTitles[format]}
                  <span className="ml-1 font-mono text-xs font-normal text-muted-foreground">
                    {annotationExtensions[format]}
                  </span>
                </span>
                <span className="text-xs text-pretty text-muted-foreground">
                  {descriptions[format]}
                </span>
              </span>
            </label>
          )
        })}
      </RadioGroup>
    </div>
  )
}
