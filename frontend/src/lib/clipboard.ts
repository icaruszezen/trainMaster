/**
 * 复制文本到剪贴板。navigator.clipboard 只在安全上下文（HTTPS / localhost）
 * 中可用，内网常见的 http://IP 访问时回退到 execCommand。
 * 只有确实写入了剪贴板才返回 true，调用方据此提示“已复制”或“复制失败”。
 */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒绝等：尝试回退方案。
    }
  }
  return copyWithExecCommand(text)
}

/**
 * execCommand("copy") 回退方案：在 copy 事件中直接写入要复制的文字，不依赖
 * 选区与焦点。对话框（Radix 的焦点陷阱）会立刻把焦点从 body 上的 textarea
 * 拉回对话框，选区随之为空，execCommand 照样返回 true 却什么也没有复制；
 * 所以只有 copy 事件确实写入了这段文字才算成功。
 */
function copyWithExecCommand(text: string): boolean {
  let copied = false
  const onCopy = (event: ClipboardEvent) => {
    if (event.clipboardData) {
      event.clipboardData.setData("text/plain", text)
      event.preventDefault()
      copied = true
    } else {
      // 不提供 clipboardData 的浏览器按默认行为复制选中的文字。
      copied = document.getSelection()?.toString() === text
    }
  }

  // 仍然选中一段文字：有的浏览器没有选区时不触发 copy 事件。
  const previous = document.activeElement as HTMLElement | null
  const textarea = document.createElement("textarea")
  textarea.value = text
  textarea.setAttribute("readonly", "")
  textarea.style.position = "fixed"
  textarea.style.opacity = "0"
  document.body.appendChild(textarea)
  textarea.select()
  document.addEventListener("copy", onCopy, true)
  try {
    return document.execCommand("copy") && copied
  } catch {
    return false
  } finally {
    document.removeEventListener("copy", onCopy, true)
    textarea.remove()
    // 焦点还在 textarea 上时随它离开了页面：还给原来的元素（如复制按钮）。
    if (previous !== document.activeElement && previous?.focus) {
      previous.focus({ preventScroll: true })
    }
  }
}
