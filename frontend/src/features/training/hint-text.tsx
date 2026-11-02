import { CommandBlock } from "@/features/training/command-block"
import { hintSegments } from "@/features/training/compute-format"

/**
 * 修复建议：文字中反引号包住的命令显示为行内代码；整段就是一条命令时
 * 显示为可复制的代码块。
 */
export function HintText({ hint }: { hint: string }) {
  const segments = hintSegments(hint)
  if (segments.length === 1 && segments[0].code) {
    return <CommandBlock command={segments[0].text} className="w-full" />
  }
  return (
    <p className="min-w-0 break-words">
      {segments.map((segment, index) =>
        segment.code ? (
          <code
            key={index}
            className="mx-0.5 rounded bg-muted px-1.5 py-0.5 font-mono text-xs break-all text-foreground"
          >
            {segment.text}
          </code>
        ) : (
          <span key={index}>{segment.text}</span>
        )
      )}
    </p>
  )
}
