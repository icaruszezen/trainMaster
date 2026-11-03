import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react"
import { Plus } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Spinner } from "@/components/ui/spinner"
import {
  classNameError,
  filterClasses,
  type ClassOption,
} from "@/features/annotation/classes"
import {
  nearestScrollTop,
  placeOverlay,
  pointerMoved,
  type OverlayAnchor,
} from "@/features/annotation/geometry"
import {
  classHotkey,
  closesOnBlur,
  leaveOverlay,
  pickerDigitIndex,
} from "@/features/annotation/shortcuts"
import { cn } from "@/lib/utils"

/**
 * 候选类别的读取状态（只是提示：读取中、读取失败时照样可以选择已有的候选、
 * 输入新的名称）。
 */
export type CandidateStatus =
  | { state: "loading" }
  | { state: "error"; message: string; retrying: boolean; onRetry: () => void }

type LabelPickerProps = {
  /** 候选类别（见 `buildClassOptions`），前 10 个显示对应的数字键。 */
  options: readonly ClassOption[]
  /**
   * 数据集所属的项目有类别列表：标出不在列表中的候选（只是数据集中已有的
   * 标签，训练时导出 YOLO 不包含它们的框）。没有类别列表时所有候选都是这样，
   * 不再逐个标出。
   */
  flagUnlisted: boolean
  /** 当前的类别：标为“当前”，没有输入时高亮它（Enter 直接确定）。 */
  current: string | null
  title: string
  status?: CandidateStatus | null
  /** 选中一个类别；`created` 表示输入的是候选中没有的新名称。 */
  onPick: (name: string, created: boolean) => void
  onClose: () => void
  /**
   * 框在画布上的位置：选择框放在框下方，放不下时放到上方，超出右边时左移，
   * 上下都放不下时限制在画布内（可能盖住框）；没有时放在画布顶部居中。选择框的
   * 高度不超过画布，画布较矮时列表变短（可以滚动），因此整个选择框都在画布内。
   */
  anchor?: OverlayAnchor | null
  className?: string
}

type Option =
  /** `index`：在全部候选中的位置（决定数字键）。 */
  | { kind: "class"; option: ClassOption; index: number }
  | { kind: "create"; name: string }

const optionKey = (option: Option) =>
  option.kind === "class" ? `c:${option.option.name}` : "create"

/** 把列表中的一项滚动到可见（只滚动列表本身）。 */
function scrollToOption(list: HTMLElement | null, item: HTMLElement | null) {
  if (list && item) {
    list.scrollTop = nearestScrollTop(
      list.scrollTop,
      list.clientHeight,
      item.offsetTop,
      item.offsetHeight
    )
  }
}

/**
 * 类别选择框（相对画布绝对定位）：输入筛选（不区分大小写），↑ / ↓ 选择，
 * Enter 确定，Esc 关闭；还没有输入时数字键直接选择对应的候选（与画布上一样）。
 * 没有输入时高亮当前类别，与 LabelImg 一样画完框直接按 Enter 就沿用上一次的
 * 类别。输入的名称不在候选中时可以直接使用（列表项写明“新类别”，避免手误用了
 * 多余的名称）；它只是框的类别名称，不会加入任何类别列表。
 */
export function LabelPicker({
  options: candidates,
  flagUnlisted,
  current,
  title,
  status,
  onPick,
  onClose,
  anchor,
  className,
}: LabelPickerProps) {
  const [query, setQuery] = useState("")
  /**
   * 用键盘或指针移到的一项（按名称记住：候选随后载入、顺序变化时仍是同一项）；
   * null 时为默认的一项。
   */
  const [activeKey, setActiveKey] = useState<string | null>(null)
  const listId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  /** 上一次指针在列表项上移动的位置：只有指针真正移动时，高亮才跟随指针。 */
  const lastPointer = useRef<{ x: number; y: number } | null>(null)

  // 量出选择框的尺寸后放到框旁边（绘制前完成，不会闪动）；框随缩放移动、
  // 筛选后列表变短时重新计算。
  useLayoutEffect(() => {
    const element = rootRef.current
    if (!element) {
      return
    }
    const position = anchor
      ? placeOverlay(anchor, {
          width: element.offsetWidth,
          height: element.offsetHeight,
        })
      : null
    const left = position ? `${Math.round(position.x)}px` : ""
    const top = position ? `${Math.round(position.y)}px` : ""
    if (element.style.left !== left || element.style.top !== top) {
      element.style.left = left
      element.style.top = top
    }
  })

  // 不用 autoFocus：获得焦点时不滚动任何祖先元素（画布滚开后指针位置就对不上了）。
  // 关闭时（选好类别、Esc 等）焦点还在选择框中就移回画布，否则焦点随选择框
  // 一起消失、落到 body 上，Tab 不再切换框。
  useLayoutEffect(() => {
    inputRef.current?.focus({ preventScroll: true })
    const root = rootRef.current
    return () => leaveOverlay(root)
  }, [])

  const indexed = candidates.map((option, index) => ({
    name: option.name,
    option,
    index,
  }))
  const matches = filterClasses(indexed, query)
  const name = query.trim()
  const names = candidates.map((option) => option.name)
  const canCreate = name !== "" && classNameError(name, names) === null
  const options: Option[] = [
    ...matches.map((match): Option => ({
      kind: "class",
      option: match.option,
      index: match.index,
    })),
    ...(canCreate ? [{ kind: "create", name } as Option] : []),
  ]
  // 默认的一项：没有输入时是当前类别，输入后是最匹配的第一项。
  const currentIndex =
    name === ""
      ? options.findIndex(
          (option) => option.kind === "class" && option.option.name === current
        )
      : -1
  const chosenIndex =
    activeKey === null
      ? -1
      : options.findIndex((option) => optionKey(option) === activeKey)
  const activeIndex =
    chosenIndex !== -1 ? chosenIndex : Math.max(0, currentIndex)

  // 打开时（以及候选随后载入、默认的一项换了位置时）把默认的一项滚动到可见。
  useLayoutEffect(() => {
    if (activeKey === null) {
      scrollToOption(
        listRef.current,
        document.getElementById(`${listId}-${activeIndex}`)
      )
    }
  }, [activeKey, activeIndex, listId])

  const pick = (option: Option | undefined) => {
    if (!option) {
      return
    }
    if (option.kind === "class") {
      onPick(option.option.name, false)
    } else {
      onPick(option.name, true)
    }
  }

  // 用键盘移动时把这一项滚动到可见。
  const moveActive = (index: number) => {
    const option = options[index]
    if (!option) {
      return
    }
    setActiveKey(optionKey(option))
    scrollToOption(
      listRef.current,
      document.getElementById(`${listId}-${index}`)
    )
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) {
      return
    }
    // 还没有输入时，数字键与画布上一样直接选择对应的候选（列表中标出的数字），
    // 不输入到搜索框：画完框随手按数字键是常见的习惯，输入进去的话列表中只剩
    // 新类别“2”一项，Enter 后框的类别成了“2”，还会成为之后每个框默认的类别。
    // 没有对应的候选时照常输入；名称以数字开头时先输入空格（名称去掉首尾空格，
    // 输入了空格后数字照常输入）。
    if (query === "") {
      const index = pickerDigitIndex(event)
      const option = index === null ? undefined : candidates[index]
      if (option) {
        event.preventDefault()
        onPick(option.name, false)
        return
      }
    }
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault()
        moveActive((activeIndex + 1) % Math.max(1, options.length))
        return
      case "ArrowUp":
        event.preventDefault()
        moveActive(
          (activeIndex - 1 + options.length) % Math.max(1, options.length)
        )
        return
      case "Enter":
        event.preventDefault()
        pick(options[activeIndex])
        return
      case "Escape":
        event.preventDefault()
        event.stopPropagation()
        onClose()
        return
      case "Tab":
        // 列表项和重试按钮都不接受焦点，Tab 会把焦点移出选择框、关掉它，刚画的
        // 框也随之放弃：焦点留在输入框中（Enter 确定、Esc 关闭）。
        event.preventDefault()
        return
    }
  }

  const error = name !== "" && !canCreate && matches.length === 0
  return (
    <div
      ref={rootRef}
      // 宽高都不超过画布（百分比相对画布计算，四周留出边距）：画布较矮时列表
      // 变短、可以滚动到最后一项，选择框不会被画布裁掉。
      className={cn(
        "absolute z-20 flex max-h-[calc(100%-1rem)] w-72 max-w-[calc(100%-1rem)] flex-col gap-2 rounded-lg border bg-popover p-2 text-popover-foreground shadow-lg",
        !anchor && "top-4 left-1/2 max-h-[calc(100%-2rem)] -translate-x-1/2",
        className
      )}
      onPointerDown={(event) => event.stopPropagation()}
      // 按下时焦点留在输入框中：点到标题、空白、提示文字、列表项等不能获得焦点
      // 的地方时，浏览器默认把焦点移到能获得焦点的祖先（画布）上，输入框失去
      // 焦点、选择框随即关闭，刚画的框也随之放弃。输入框本身照常处理（放置
      // 光标、选择文字）。
      onMouseDown={(event) => {
        if (event.target !== inputRef.current) {
          event.preventDefault()
        }
      }}
      onDoubleClick={(event) => event.stopPropagation()}
    >
      <p className="shrink-0 px-1 text-xs font-medium text-muted-foreground">
        {title}
      </p>
      <Input
        ref={inputRef}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value)
          setActiveKey(null)
          listRef.current?.scrollTo({ top: 0 })
        }}
        onKeyDown={onKeyDown}
        onBlur={(event) => {
          // 焦点移到选择框以外时关闭；窗口失去焦点（切换标签页等）时不关闭，
          // 见 closesOnBlur。
          const root = rootRef.current
          if (root && closesOnBlur(root, event.relatedTarget)) {
            onClose()
          }
        }}
        placeholder="搜索或输入类别名称"
        aria-label="类别"
        role="combobox"
        aria-expanded
        aria-controls={listId}
        aria-activedescendant={
          options.length > 0 ? `${listId}-${activeIndex}` : undefined
        }
        className="h-8 shrink-0"
      />
      <ul
        ref={listRef}
        id={listId}
        role="listbox"
        aria-label="类别"
        // relative：列表项的 offsetTop 相对列表计算，用于滚动到可见。
        // min-h-0：选择框高度受限时只有列表变短。
        className="relative max-h-60 min-h-0 overflow-y-auto"
      >
        {options.map((option, index) => {
          const hotkey =
            option.kind === "class" ? classHotkey(option.index) : null
          return (
            <li
              key={optionKey(option)}
              id={`${listId}-${index}`}
              role="option"
              aria-selected={index === activeIndex}
              tabIndex={-1}
              // 不用 pointerenter：用键盘移动、列表滚动后，浏览器会对停在原处的
              // 指针下的这一项发出 pointerenter，有的浏览器还会补发位置不变的移动
              // 事件（选择框刚在静止的指针下打开时也是），高亮就跳到指针所在的项了。
              // 因此只在指针真正移动时（位置与上次不同；还没有记录时看事件的位移）
              // 跟随指针。
              onPointerMove={(event) => {
                const point = { x: event.clientX, y: event.clientY }
                const moved = pointerMoved(lastPointer.current, point, {
                  x: event.movementX,
                  y: event.movementY,
                })
                lastPointer.current = point
                if (moved) {
                  setActiveKey(optionKey(option))
                }
              }}
              onClick={() => pick(option)}
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm",
                index === activeIndex && "bg-accent text-accent-foreground"
              )}
            >
              {option.kind === "class" ? (
                <>
                  <span
                    className="size-2.5 shrink-0 rounded-sm"
                    style={{ backgroundColor: option.option.color }}
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1 truncate">
                    {option.option.name}
                  </span>
                  {flagUnlisted && option.option.source === "dataset" && (
                    <Badge
                      variant="outline"
                      className="h-5 border-amber-500/50 px-1.5 text-[10px] font-normal text-amber-700 dark:text-amber-500"
                      title="不在数据集所属项目的类别列表中：训练时导出 YOLO 不包含这个类别的框"
                    >
                      不在类别列表
                    </Badge>
                  )}
                  {option.option.name === current && (
                    <span className="shrink-0 text-xs text-muted-foreground">
                      当前
                    </span>
                  )}
                  {hotkey && <Kbd>{hotkey}</Kbd>}
                </>
              ) : (
                <>
                  <Plus className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate">
                    新类别“{option.name}”
                  </span>
                </>
              )}
            </li>
          )
        })}
        {options.length === 0 && (
          <li className="px-2 py-1.5 text-sm text-muted-foreground">
            {error
              ? (classNameError(name, names) ?? "没有匹配的类别")
              : "输入类别名称"}
          </li>
        )}
      </ul>
      {status && <StatusLine status={status} />}
    </div>
  )
}

/**
 * 候选类别读取中 / 读取失败的提示。重试按钮不获得焦点（按下时也不夺走输入框的
 * 焦点，见选择框的 onMouseDown），选择框不会因此关闭；读取成功后提示消失，焦点
 * 仍在输入框中。
 */
function StatusLine({ status }: { status: CandidateStatus }) {
  return (
    <div className="flex shrink-0 items-center gap-1.5 px-1 text-xs text-muted-foreground">
      {status.state === "loading" ? (
        <>
          <Spinner className="size-3" />
          正在读取候选类别…
        </>
      ) : (
        <>
          <span className="min-w-0 flex-1 truncate" title={status.message}>
            部分候选类别读取失败
          </span>
          <button
            type="button"
            tabIndex={-1}
            onClick={() => {
              if (!status.retrying) {
                status.onRetry()
              }
            }}
            className="flex shrink-0 items-center gap-1 rounded-sm px-1 text-foreground underline-offset-2 hover:underline"
          >
            {status.retrying && <Spinner className="size-3" />}
            重试
          </button>
        </>
      )}
    </div>
  )
}
