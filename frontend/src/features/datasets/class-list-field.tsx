/** YOLO 类别顺序的输入：文本框（每行一个）、读取 classes.txt、编号预览与校验提示。 */
import { useId, useRef, useState, type ReactNode, type Ref } from "react"
import { FileText, Info, RotateCcw } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Textarea } from "@/components/ui/textarea"
import {
  readClassFile,
  type ClassListResult,
} from "@/features/datasets/class-list"
import { formatNumber } from "@/lib/format"

/** 预览中最多列出的类别数，其余只显示数量。 */
const PREVIEW_LIMIT = 60

const PLACEHOLDER =
  "每行一个类别名称，第 N 行对应类别编号 N-1，例如：\nscratch\ndent\ncrack"

type ClassListFieldProps = {
  value: string
  onChange: (value: string) => void
  /** `parseClassList(value)` 的结果（由调用方计算，提交时也要用到）。 */
  result: ClassListResult
  /** 为空时是否提示“需要提供类别顺序”，一般在尝试提交之后。 */
  showRequired?: boolean
  description?: ReactNode
  /** 内容来源等提示，如“已从 classes.txt 填入”。 */
  hint?: ReactNode
  /** 传入时显示“恢复默认”按钮。 */
  onReset?: () => void
  disabled?: boolean
  textareaRef?: Ref<HTMLTextAreaElement>
}

export function ClassListField({
  value,
  onChange,
  result,
  showRequired,
  description,
  hint,
  onReset,
  disabled,
  textareaRef,
}: ClassListFieldProps) {
  const id = useId()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [fileError, setFileError] = useState<string | null>(null)
  // 从文件读取的内容；文本被修改后不再显示来源。
  const [loaded, setLoaded] = useState<{ name: string; text: string } | null>(
    null
  )

  const blank = value.trim() === ""
  const error =
    fileError ??
    (result.error && (!blank || showRequired) ? result.error : null)
  const { classes } = result
  const hidden = classes.length - PREVIEW_LIMIT

  const change = (next: string) => {
    setFileError(null)
    onChange(next)
  }

  const load = async (file: File) => {
    try {
      const text = await readClassFile(file)
      change(text)
      setLoaded({ name: file.name, text })
    } catch (cause) {
      setFileError(cause instanceof Error ? cause.message : "无法读取类别文件")
    }
  }

  const sourceHint =
    loaded && loaded.text === value ? `已从 ${loaded.name} 读取` : hint

  return (
    <Field data-invalid={error ? true : undefined} className="gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <FieldLabel htmlFor={id}>类别顺序</FieldLabel>
        <div className="flex items-center gap-1">
          {onReset && (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              className="text-foreground"
              onClick={() => {
                setFileError(null)
                onReset()
              }}
              disabled={disabled}
            >
              <RotateCcw />
              恢复默认
            </Button>
          )}
          <Button
            type="button"
            variant="outline"
            size="xs"
            className="text-foreground"
            onClick={() => fileInputRef.current?.click()}
            disabled={disabled}
          >
            <FileText />
            从文件读取
          </Button>
        </div>
      </div>
      {description && (
        <FieldDescription className="text-xs">{description}</FieldDescription>
      )}
      <Textarea
        ref={textareaRef}
        id={id}
        value={value}
        onChange={(event) => change(event.target.value)}
        placeholder={PLACEHOLDER}
        aria-invalid={error ? true : undefined}
        disabled={disabled}
        spellCheck={false}
        autoComplete="off"
        // 不自动换行：一行就是一个类别，折行会让编号看起来错位。
        wrap="off"
        className="max-h-56 min-h-28 overflow-x-auto font-mono text-sm whitespace-pre"
      />
      <input
        ref={fileInputRef}
        type="file"
        accept=".txt,.names,text/plain"
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => {
          const file = event.target.files?.[0]
          event.target.value = ""
          if (file) {
            void load(file)
          }
        }}
      />
      {error ? (
        <FieldError>{error}</FieldError>
      ) : (
        sourceHint && (
          <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
            <Info className="mt-px size-3.5 shrink-0" />
            {sourceHint}
          </p>
        )
      )}
      {!error && classes.length > 0 && (
        <div className="grid gap-1.5">
          <p className="text-xs text-muted-foreground tabular-nums">
            共 {formatNumber(classes.length)} 个类别（编号 0–
            {formatNumber(classes.length - 1)}）
          </p>
          <ul
            className="flex max-h-32 flex-wrap gap-1.5 overflow-y-auto"
            aria-label="类别编号"
          >
            {classes.slice(0, PREVIEW_LIMIT).map((name, index) => (
              <li key={index} className="max-w-full">
                <Badge
                  variant="outline"
                  className="max-w-48 font-normal"
                  title={`${index} ${name}`}
                >
                  <span className="font-mono text-muted-foreground tabular-nums">
                    {index}
                  </span>
                  <span className="truncate">{name}</span>
                </Badge>
              </li>
            ))}
            {hidden > 0 && (
              <li>
                <Badge variant="secondary" className="font-normal">
                  还有 {formatNumber(hidden)} 个
                </Badge>
              </li>
            )}
          </ul>
        </div>
      )}
    </Field>
  )
}
