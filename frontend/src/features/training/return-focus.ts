/** 能接收焦点的元素；测试中可以用替身。 */
export type FocusTarget = Pick<HTMLElement, "focus" | "isConnected">

/**
 * 受控对话框（从菜单项或页面上的按钮打开，没有 DialogTrigger）的
 * `onCloseAutoFocus`：Radix 只把焦点还给 DialogTrigger，没有时焦点落到 body
 * 上，键盘与读屏用户在节点列表中失去位置。这里改为还给打开对话框的元素（如
 * 节点的“更多操作”按钮）；它已经不在页面上（如节点刚被删除）时保持默认行为。
 */
export function returnFocus(
  event: { preventDefault: () => void },
  target: FocusTarget | null | undefined
) {
  if (!target?.isConnected) {
    return
  }
  event.preventDefault()
  target.focus()
}
