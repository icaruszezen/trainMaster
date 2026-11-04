/**
 * 标注工作台与审图编辑器的快捷键：按键 → 命令的映射（纯函数，便于测试），以及
 * 帮助面板中的说明。
 *
 * - 字母、数字按物理按键（`KeyboardEvent.code`）匹配，与大小写锁定、键盘布局
 *   无关；输入法正在组字时不处理。
 * - 沿用 LabelImg 的习惯（W 画框、A / D 切换图片、Ctrl+D 复制框、方向键移动），
 *   数字键切换类别（CVAT、X-AnyLabeling）。
 * - 不绑定浏览器保留的组合键（Ctrl+W / T / N / L / R、Ctrl+1–9、Alt+← / → 等）。
 * - 审图时（`review`）另有 S / X / U 标记审核状态，N（空标注）不可用。
 */

export type Tool = "select" | "rect" | "pan"

/** 审图中图片的审核状态：待审核 / 已审核 / 需加样（见 `features/reviews`）。 */
export type ReviewMark = "pending" | "approved" | "needs_sample"

export type Command =
  | { type: "tool"; tool: Tool }
  /**
   * 上一张 / 下一张；`unannotated` 时跳到上一张 / 下一张未标注的图片（审图时是
   * 待审核的图片）。
   */
  | { type: "image"; direction: 1 | -1; unannotated: boolean }
  /** 跳到第 N 张（聚焦图片序号输入框）。 */
  | { type: "goto" }
  | { type: "save" }
  | { type: "undo" }
  | { type: "redo" }
  /** 复制选中的框；没有选中时复制本图所有框。 */
  | { type: "copy" }
  | { type: "paste" }
  | { type: "duplicate" }
  | { type: "delete" }
  /**
   * Tab / Shift+Tab（焦点在画布上时）：依次选中下一个 / 上一个框；到头后取消
   * 选中，焦点照常移出画布。
   */
  | { type: "cycle"; direction: 1 | -1 }
  | { type: "escape" }
  /**
   * 数字键（下标从 0 开始，0 键是第 10 个候选类别，候选的顺序见
   * `buildClassOptions`）：有选中的框时修改它的类别，否则切换当前类别。
   */
  | { type: "class"; index: number }
  /**
   * 打开类别选择框（可搜索）：有选中的框时修改它的类别，否则切换当前类别。
   */
  | { type: "pickClass" }
  | { type: "toggleDifficult" }
  | { type: "toggleTruncated" }
  /** 移动选中的框（像素）。 */
  | { type: "nudge"; dx: number; dy: number }
  /** 调整选中框的右边 / 下边（像素），→ / ↓ 变大。 */
  | { type: "resize"; dx: number; dy: number }
  | { type: "zoom"; direction: 1 | -1 }
  | { type: "fit" }
  | { type: "actualSize" }
  | { type: "zoomToSelection" }
  | { type: "toggleLabels" }
  | { type: "toggleBoxes" }
  /** 标为空标注（无目标，负样本）：写入没有目标的标注文件。审图时没有。 */
  | { type: "negative" }
  /** 审图时标记这张图片的审核状态（S 已审核、X 需加样、U 待审核）。 */
  | { type: "review"; status: ReviewMark }
  | { type: "help" }

/** 与 `KeyboardEvent` 中用到的字段一致，便于测试。 */
export type KeyInput = {
  key: string
  code: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  shiftKey: boolean
}

export type ShortcutContext = {
  /** macOS 上用 ⌘ 代替 Ctrl。 */
  mac: boolean
  /**
   * 有选中（且显示着）的框：方向键移动它；没有时 ← / → 切换图片。隐藏的框
   * （H 隐藏所有框时）不算选中。
   */
  hasSelection: boolean
  /** 审图编辑器：S / X / U 标记审核状态，N（空标注）不可用。 */
  review?: boolean
}

export function isMac() {
  return (
    typeof navigator !== "undefined" &&
    /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  )
}

const ARROWS: Record<string, [number, number]> = {
  ArrowLeft: [-1, 0],
  ArrowRight: [1, 0],
  ArrowUp: [0, -1],
  ArrowDown: [0, 1],
}

function digitIndex(code: string) {
  const match = /^(?:Digit|Numpad)(\d)$/.exec(code)
  if (!match) {
    return null
  }
  const digit = Number(match[1])
  return digit === 0 ? 9 : digit - 1
}

function withMod(code: string, shift: boolean, mac: boolean): Command | null {
  const arrow = ARROWS[code]
  if (arrow) {
    const step = shift ? 10 : 1
    return { type: "resize", dx: arrow[0] * step, dy: arrow[1] * step }
  }
  switch (code) {
    case "KeyS":
      return shift ? null : { type: "save" }
    case "KeyZ":
      return shift ? { type: "redo" } : { type: "undo" }
    case "KeyY":
      // macOS 上 ⌘Y 是浏览器的“历史记录”，重做用 ⌘⇧Z。
      return shift || mac ? null : { type: "redo" }
    case "KeyC":
      return shift ? null : { type: "copy" }
    case "KeyV":
      return shift ? null : { type: "paste" }
    case "KeyD":
      return shift ? null : { type: "duplicate" }
    case "Equal":
    case "NumpadAdd":
      return { type: "zoom", direction: 1 }
    case "Minus":
    case "NumpadSubtract":
      return { type: "zoom", direction: -1 }
    case "Digit0":
    case "Numpad0":
      return shift ? null : { type: "actualSize" }
    default:
      return null
  }
}

/**
 * 按键对应的命令，没有时为 null。调用方负责忽略输入框中的按键（见
 * {@link shouldIgnoreKey}）与按住不放时的重复事件（见 {@link allowsRepeat}）。
 */
export function commandForKey(
  event: KeyInput,
  context: ShortcutContext
): Command | null {
  const mod = context.mac ? event.metaKey : event.ctrlKey
  // macOS 上单独按 Ctrl 的组合、Windows 上的 Win 键组合都不处理。
  const otherMod = context.mac ? event.ctrlKey : event.metaKey
  if (otherMod || event.altKey) {
    return null
  }
  const { code, shiftKey: shift } = event
  if (mod) {
    return withMod(code, shift, context.mac)
  }

  const arrow = ARROWS[code]
  if (arrow) {
    if (context.hasSelection) {
      const step = shift ? 10 : 1
      return { type: "nudge", dx: arrow[0] * step, dy: arrow[1] * step }
    }
    if (arrow[0] !== 0) {
      return {
        type: "image",
        direction: arrow[0] as 1 | -1,
        unannotated: shift,
      }
    }
    return null
  }

  switch (code) {
    case "Escape":
      return { type: "escape" }
    case "Tab":
      return { type: "cycle", direction: shift ? -1 : 1 }
    case "Delete":
    case "Backspace":
      return { type: "delete" }
    case "PageUp":
      return { type: "image", direction: -1, unannotated: false }
    case "PageDown":
      return { type: "image", direction: 1, unannotated: false }
    case "KeyA":
      return { type: "image", direction: -1, unannotated: shift }
    case "KeyD":
      return { type: "image", direction: 1, unannotated: shift }
    case "Equal":
    case "NumpadAdd":
      return { type: "zoom", direction: 1 }
    case "Minus":
    case "NumpadSubtract":
      return { type: "zoom", direction: -1 }
    case "F1":
      return { type: "help" }
    case "Slash":
      return shift ? { type: "help" } : null
  }
  if (event.key === "?") {
    return { type: "help" } // 其它键盘布局上的问号
  }
  if (shift) {
    return null
  }

  switch (code) {
    case "KeyW":
    case "KeyR":
      return { type: "tool", tool: "rect" }
    case "KeyV":
      return { type: "tool", tool: "select" }
    case "KeyG":
      return { type: "goto" }
    case "KeyT":
      return { type: "pickClass" }
    case "KeyQ":
      return { type: "toggleDifficult" }
    case "KeyE":
      return { type: "toggleTruncated" }
    case "KeyF":
      return { type: "fit" }
    case "KeyZ":
      return { type: "zoomToSelection" }
    case "KeyL":
      return { type: "toggleLabels" }
    case "KeyH":
      return { type: "toggleBoxes" }
    case "KeyN":
      return context.review ? null : { type: "negative" }
  }
  if (context.review) {
    switch (code) {
      case "KeyS":
        return { type: "review", status: "approved" }
      case "KeyX":
        return { type: "review", status: "needs_sample" }
      case "KeyU":
        return { type: "review", status: "pending" }
    }
  }
  const index = digitIndex(code)
  return index === null ? null : { type: "class", index }
}

/** 按住不放时是否重复执行（移动、缩放、撤销可以；切换图片、开关类的不行）。 */
export function allowsRepeat(command: Command) {
  return (
    command.type === "nudge" ||
    command.type === "resize" ||
    command.type === "zoom" ||
    command.type === "undo" ||
    command.type === "redo"
  )
}

/** 第 `index` 个候选类别的数字键（前 10 个有），没有时为 null。 */
export function classHotkey(index: number) {
  if (index < 0 || index > 9) {
    return null
  }
  return index === 9 ? "0" : String(index + 1)
}

/**
 * 类别选择框中按下的数字键对应的候选下标（与 {@link classHotkey} 一致：1–9 是
 * 前 9 个，0 是第 10 个），不是数字时为 null。按输入的字符（`key`）而不是物理
 * 按键匹配：这里拦下的是“往搜索框里输入了一个数字”，用哪个键输入数字随键盘布局
 * 而不同；小键盘关闭 NumLock 时输入的是方向键，照常移动高亮。输入法处理中的按键
 * （`key` 为 `Process`）不是数字。
 */
export function pickerDigitIndex(
  event: Pick<KeyInput, "key" | "ctrlKey" | "metaKey" | "altKey">
) {
  if (
    event.ctrlKey ||
    event.metaKey ||
    event.altKey ||
    !/^[0-9]$/.test(event.key)
  ) {
    return null
  }
  const digit = Number(event.key)
  return digit === 0 ? 9 : digit - 1
}

/**
 * 焦点是否在输入控件中（输入框、下拉框、可编辑内容），或在对话框、菜单等
 * 浮层中：这时快捷键交给控件本身。
 */
export function isTypingTarget(target: EventTarget | null) {
  if (typeof Element === "undefined" || !(target instanceof Element)) {
    return false
  }
  if (target instanceof HTMLElement && target.isContentEditable) {
    return true
  }
  return (
    target.closest(
      'input, textarea, select, [role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"], [data-radix-popper-content-wrapper]'
    ) !== null
  )
}

/** 标注画布（`role="application"`）。 */
const CANVAS = '[role="application"]'

function asElement(target: EventTarget | Node | null): Element | null {
  if (typeof Element === "undefined" || target === null) {
    return null
  }
  if (target instanceof Element) {
    return target
  }
  // 文字节点：取它所在的元素。
  return typeof Node !== "undefined" && target instanceof Node
    ? target.parentElement
    : null
}

/** 输入一个字符的按键（字母、数字、标点、空格），没有按 Ctrl / ⌘ / Alt。 */
function isPrintableKey(event: KeyInput) {
  return (
    event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey
  )
}

// 自己用方向键的控件（单选组、标签页、滑块、菜单、列表等）。
const ARROW_CONTROLS =
  '[role="radio"], [role="tab"], [role="slider"], [role="menuitem"], [role="option"], [role="combobox"], [role="spinbutton"], [role="switch"], [role="checkbox"]'

/**
 * 是否忽略这次按键：
 * - 输入法正在组字；
 * - 焦点在输入控件 / 浮层中（Ctrl+S 例外，在输入框里也保存，并且不弹出浏览器
 *   的“保存网页”）；
 * - 焦点在关闭的下拉框（如类别下拉框）上时输入字符的按键：下拉框可能按首字母
 *   直接选中匹配的选项，快捷键再执行一次就会同时改掉框的类别（工作台中的下拉
 *   框用 {@link preventTypeahead} 拦下了这些按键，这时按键什么也不做）；
 * - 焦点不在画布上时的 Tab（页面本身、按钮等控件上照常移动焦点），以及自己
 *   用方向键的控件上的方向键。
 */
export function shouldIgnoreKey(
  event: KeyInput & {
    isComposing: boolean
    keyCode: number
    target: EventTarget | null
  },
  command: Command
) {
  if (event.isComposing || event.keyCode === 229) {
    return true
  }
  if (command.type === "save") {
    return false
  }
  const { target } = event
  if (isTypingTarget(target)) {
    return true
  }
  const element = asElement(target)
  if (event.code === "Tab") {
    // 只在焦点位于画布上时用 Tab 切换框。
    return element?.closest(CANVAS) == null
  }
  if (element === null) {
    return false
  }
  if (isPrintableKey(event) && element.closest('[role="combobox"]') !== null) {
    return true
  }
  return (
    event.code.startsWith("Arrow") && element.closest(ARROW_CONTROLS) !== null
  )
}

/**
 * 工作台中下拉框（类别、筛选条件）按钮的 onKeyDown：拦下不带 Ctrl / ⌘ / Alt
 * 的单个字符（空格除外，空格打开下拉框）。关闭的下拉框会按首字母直接选中匹配
 * 的选项，焦点留在按钮上时误按的快捷键会悄悄改掉框的类别或筛选条件；阻止默认
 * 行为后 Radix 不再处理这次按键。打开后的列表中照常按首字母查找。
 */
export function preventTypeahead(
  event: KeyInput & { preventDefault: () => void }
) {
  if (isPrintableKey(event) && event.key !== " ") {
    event.preventDefault()
  }
}

/**
 * 下拉框关闭后把焦点移到标注画布上（快捷键随即可用），返回是否移动了。
 * `from`（下拉框的按钮）在弹出的面板（对话框）中时不移动：面板会把焦点留在
 * 自己里面；画布不在页面上（图片还在读取或无法打开）时也不移动。
 */
export function focusCanvas(from: Element | null) {
  if (!from || from.closest('[role="dialog"]')) {
    return false
  }
  const canvas = from.ownerDocument.querySelector<HTMLElement>(CANVAS)
  if (!canvas) {
    return false
  }
  canvas.focus({ preventScroll: true })
  return true
}

/**
 * 画布上的浮层（类别选择框）卸载时调用（React 在移除元素之前清理）：焦点还在
 * 浮层中，就在浮层移除、焦点落到 body 上之后移回它所在的画布，Tab 随即可以
 * 依次选中框。用户点到了别的控件上时不移动；画布也一起移除了（切换图片）时
 * 由新的画布自己获得焦点。浮层之后还在页面上时（开发模式下 StrictMode 模拟的
 * 卸载再挂载）也不移动，否则输入框失去焦点、选择框随即关闭。
 */
export function leaveOverlay(overlay: Element | null) {
  if (!overlay) {
    return
  }
  const doc = overlay.ownerDocument
  if (!overlay.contains(doc.activeElement)) {
    return
  }
  const canvas = overlay.closest<HTMLElement>(CANVAS)
  queueMicrotask(() => {
    const focused = doc.activeElement
    if (
      canvas?.isConnected &&
      !overlay.isConnected &&
      (focused === null || focused === doc.body)
    ) {
      canvas.focus({ preventScroll: true })
    }
  })
}

/**
 * 浮层（类别选择框）中的输入框失去焦点时是否关闭浮层（刚画的框随之放弃），
 * `next` 是随后获得焦点的元素（`relatedTarget`）：
 * - 焦点移到浮层以外（点到别的控件、画布或页面上不能获得焦点的地方）时关闭；
 * - 整个窗口失去焦点（切换到别的标签页或应用、打开开发者工具）时不关闭：用户
 *   可能是去查目标的名称，回来时浏览器把焦点还给输入框，选择框和框都还在。
 *   不能按 `next` 为 null 判断，点到页面上不能获得焦点的地方时它也是 null。
 */
export function closesOnBlur(overlay: Element, next: Node | null) {
  if (!overlay.ownerDocument.hasFocus()) {
    return false
  }
  return !overlay.contains(next)
}

const OPEN_DIALOG =
  '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]'

/**
 * 工作台的对话框（快捷键说明、离开与删除的确认）的
 * `onCloseAutoFocus`：它们没有触发按钮，Radix 默认不移动焦点，焦点落到 body
 * 上，Tab 不再切换框。这里改为移到标注画布上；窄屏时弹出的面板（也是对话框）
 * 还开着时移到面板中，不离开面板。画布还不在页面上（图片还在读取）时保持默认
 * 行为，画布显示后自己获得焦点。Radix 调用时关闭的对话框已经从页面上移除。
 */
export function focusAfterDialog(
  event: { preventDefault: () => void },
  doc: Pick<Document, "querySelector" | "querySelectorAll"> = document
) {
  const target =
    [...doc.querySelectorAll<HTMLElement>(OPEN_DIALOG)].at(-1) ??
    doc.querySelector<HTMLElement>(CANVAS)
  if (!target) {
    return
  }
  event.preventDefault()
  target.focus({ preventScroll: true })
}

/** 与 `Selection` 中用到的字段一致，便于测试。 */
export type SelectionInput = Pick<
  Selection,
  "isCollapsed" | "rangeCount" | "anchorNode"
>

/**
 * Ctrl / ⌘+C 是否交给浏览器复制文字：焦点不在画布上，并且页面上（画布以外）
 * 选中了文字，如工具栏中的路径、提示中的错误信息。
 */
export function copiesSelectedText(
  target: EventTarget | null,
  selection: SelectionInput | null
) {
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) {
    return false
  }
  if (asElement(target)?.closest(CANVAS) != null) {
    return false
  }
  return asElement(selection.anchorNode)?.closest(CANVAS) == null
}

export type ShortcutHelp = {
  title: string
  items: { keys: string[]; label: string }[]
}

/**
 * 帮助面板（? 键）中的快捷键说明；`review` 时是审图编辑器的（多了审核状态，
 * 没有空标注）。
 */
export function shortcutHelp(
  mac: boolean,
  { review = false }: { review?: boolean } = {}
): ShortcutHelp[] {
  const mod = mac ? "⌘" : "Ctrl"
  const reviewGroup: ShortcutHelp = {
    title: "审核",
    items: [
      {
        keys: ["S"],
        label: "标为已审核（识别结果正确，模型不需要再学习这张图片）",
      },
      { keys: ["X"], label: "标为需加样（这张图片需要参与后续的训练）" },
      { keys: ["U"], label: "改回待审核" },
      {
        keys: ["Shift+D", "Shift+A"],
        label: "下一张 / 上一张待审核的图片",
      },
    ],
  }
  return [
    ...(review ? [reviewGroup] : []),
    {
      title: "画框与选择",
      items: [
        { keys: ["W", "R"], label: "画矩形框：拖动，或先后点击两个对角" },
        { keys: ["Shift"], label: "画框时按住：画正方形" },
        {
          keys: ["Enter"],
          label:
            "画好框后确定它的类别（默认是当前类别；可按数字键直接选择、输入筛选，或输入新的类别名称）",
        },
        { keys: ["V"], label: "选择 / 编辑框（拖动空白处平移画面）" },
        {
          keys: ["Esc"],
          label:
            "放弃刚画、还没有选择类别的框 → 取消正在画的框 → 取消选中 → 回到选择工具",
        },
        {
          keys: ["Tab", "Shift+Tab"],
          label: "画布有焦点时选中下一个 / 上一个框（到头后焦点移出画布）",
        },
        { keys: ["单击重叠处"], label: "再次单击依次选中下面的框" },
      ],
    },
    {
      title: "类别与属性",
      items: [
        {
          keys: ["1 – 9", "0"],
          label:
            "前 10 个候选类别：选中框时修改它的类别，否则切换当前类别；类别选择框中还没有输入时直接选择（名称以数字开头时先输入空格）",
        },
        {
          keys: ["T"],
          label: "搜索类别：选中框时修改它的类别，否则切换当前类别",
        },
        { keys: ["Q"], label: "切换“难例”（difficult，虚线框）" },
        { keys: ["E"], label: "切换“截断”（truncated）" },
      ],
    },
    {
      title: "编辑",
      items: [
        { keys: ["方向键"], label: "移动选中的框 1 像素（加 Shift：10 像素）" },
        {
          keys: [`${mod}+方向键`],
          label: "调整右边 / 下边 1 像素（加 Shift：10 像素）",
        },
        { keys: ["Delete", "Backspace"], label: "删除选中的框" },
        {
          keys: [`${mod}+C`, `${mod}+V`],
          label:
            "复制 / 粘贴框，可粘贴到别的图片（没有选中时复制全部；选中了页面上的文字时复制文字）",
        },
        { keys: [`${mod}+D`], label: "原地复制一份选中的框" },
        { keys: [`${mod}+Z`], label: "撤销" },
        {
          keys: mac ? [`${mod}+Shift+Z`] : [`${mod}+Shift+Z`, `${mod}+Y`],
          label: "重做",
        },
      ],
    },
    {
      title: "图片与保存",
      items: [
        { keys: ["D", "A"], label: "下一张 / 上一张（自动保存）" },
        ...(review
          ? []
          : [
              {
                keys: ["Shift+D", "Shift+A"],
                label: "下一张 / 上一张未标注的图片",
              },
            ]),
        {
          keys: ["← / →"],
          label: "没有选中框（或选中的框已隐藏）时切换图片",
        },
        { keys: ["G"], label: "跳到第 N 张" },
        ...(review
          ? []
          : [{ keys: ["N"], label: "标为空标注（图片上没有目标，负样本）" }]),
        { keys: [`${mod}+S`], label: "立即保存（停止编辑后也会自动保存）" },
      ],
    },
    {
      title: "视图",
      items: [
        { keys: ["滚轮", "双指捏合"], label: "以光标为中心缩放" },
        {
          keys: ["空格+拖动", "中键拖动"],
          label: "平移画面",
        },
        { keys: ["+", "-"], label: "放大 / 缩小" },
        { keys: ["F"], label: "适应窗口" },
        { keys: [`${mod}+0`], label: "原图大小（100%）" },
        { keys: ["Z"], label: "缩放到选中的框" },
        { keys: ["H"], label: "显示 / 隐藏所有框" },
        { keys: ["L"], label: "显示 / 隐藏类别名称" },
        { keys: ["?", "F1"], label: "快捷键说明" },
      ],
    },
  ]
}
