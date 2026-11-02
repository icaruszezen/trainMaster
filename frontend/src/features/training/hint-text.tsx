import { CommandBlock } from "@/features/training/command-block"
import { hintLayout, hintSegments } from "@/features/training/compute-format"

/**
 * 修复建议：文字中反引号包住的命令显示为行内代码；整段就是一条命令，或在说明
 * 文字之后以一条命令结尾时，这条命令单独一行显示为可复制的代码块（见 hintLayout）。
 */
export function HintText({ hint }: { hint: string }) {
  const { text, command } = hintLayout(hintSegments(hint))
  const block =
    command === null ? null : (
      <CommandBlock command={command} className="w-full" />
    )
  if (text.length === 0) {
    return block
  }

  const prose = (
    <p className="min-w-0 break-words">
      {text.map((segment, index) =>
        segment.code ? (
          // 在空格处换行，只有一行放不下的长参数才在中间断开（break-all 会把
          // “make” 断成 “mak e”）；anywhere 不让长参数撑宽窄屏上的卡片。
          <code
            key={index}
            className="mx-0.5 rounded bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere text-foreground"
          >
            {segment.text}
          </code>
        ) : (
          <span key={index}>{segment.text}</span>
        )
      )}
    </p>
  )
  if (!block) {
    return prose
  }
  return (
    <div className="grid w-full min-w-0 gap-1.5">
      {prose}
      {block}
    </div>
  )
}
