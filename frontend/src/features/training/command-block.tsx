import { useEffect, useState } from "react"
import { Check, Copy } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { copyText } from "@/lib/clipboard"
import { cn } from "@/lib/utils"

/** 复制按钮：成功后短暂显示对勾。 */
export function CopyButton({
  text,
  label,
  className,
}: {
  text: string
  /** 读屏文字，如“复制命令”。 */
  label: string
  className?: string
}) {
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) {
      return undefined
    }
    const timer = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(timer)
  }, [copied])

  const copy = async () => {
    if (await copyText(text)) {
      setCopied(true)
    } else {
      toast.error("复制失败，请手动选择文字复制")
    }
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      onClick={() => void copy()}
      aria-label={copied ? "已复制" : label}
      title={copied ? "已复制" : label}
      className={className}
    >
      {copied ? <Check /> : <Copy />}
    </Button>
  )
}

/** 命令代码块：等宽字体，右上角复制。 */
export function CommandBlock({
  command,
  className,
}: {
  command: string
  className?: string
}) {
  return (
    <div
      className={cn(
        "relative min-w-0 rounded-md border bg-muted/50 text-left",
        className
      )}
    >
      {/* 长命令换行显示而不是横向滚动：手机上也能看到完整的命令。 */}
      <pre className="py-2 pr-10 pl-3 font-mono text-xs leading-relaxed [overflow-wrap:anywhere] whitespace-pre-wrap text-foreground">
        <code>{command}</code>
      </pre>
      <CopyButton
        text={command}
        label="复制命令"
        className="absolute top-1 right-1 bg-muted/80 text-muted-foreground backdrop-blur hover:text-foreground"
      />
    </div>
  )
}
