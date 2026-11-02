import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react"
import { Plus } from "lucide-react"

import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { classNameError, filterClasses } from "@/features/annotation/classes"
import {
  nearestScrollTop,
  placeOverlay,
  pointerMoved,
  type OverlayAnchor,
} from "@/features/annotation/geometry"
import { classHotkey, leaveOverlay } from "@/features/annotation/shortcuts"
import type { DatasetClass } from "@/features/datasets/api"
import { cn } from "@/lib/utils"

type LabelPickerProps = {
  classes: DatasetClass[]
  colorFor: (name: string) => string
  /** 当前的类别（高亮显示）。 */
  current: string | null
  title: string
  /** 选中一个类别；`created` 表示输入的是新类别（调用方把它加入类别列表）。 */
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
  | { kind: "class"; cls: DatasetClass; index: number }
  | { kind: "create"; name: string }

/**
 * 类别选择框（相对画布绝对定位）：输入筛选（不区分大小写），↑ / ↓ 选择，
 * Enter 确定，Esc 关闭。输入的名称不在类别列表中时可以新建（列表项写明“新建
 * 类别”，避免手误建出多余的类别）。
 */
export function LabelPicker({
  classes,
  colorFor,
  current,
  title,
  onPick,
  onClose,
  anchor,
  className,
}: LabelPickerProps) {
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)
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

  const indexed = classes.map((cls, index) => ({ ...cls, index }))
  const matches = filterClasses(indexed, query)
  const name = query.trim()
  const canCreate =
    name !== "" &&
    classNameError(
      name,
      classes.map((cls) => cls.name)
    ) === null
  const options: Option[] = [
    ...matches.map((cls): Option => ({
      kind: "class",
      cls: { name: cls.name, color: cls.color },
      index: cls.index,
    })),
    ...(canCreate ? [{ kind: "create", name } as Option] : []),
  ]
  const activeIndex = Math.min(active, Math.max(0, options.length - 1))

  const pick = (option: Option | undefined) => {
    if (!option) {
      return
    }
    if (option.kind === "class") {
      onPick(option.cls.name, false)
    } else {
      onPick(option.name, true)
    }
  }

  // 用键盘移动时把这一项滚动到可见（只滚动列表本身）。
  const moveActive = (index: number) => {
    setActive(index)
    const list = listRef.current
    const option = document.getElementById(`${listId}-${index}`)
    if (list && option) {
      list.scrollTop = nearestScrollTop(
        list.scrollTop,
        list.clientHeight,
        option.offsetTop,
        option.offsetHeight
      )
    }
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) {
      return
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
    }
  }

  const error = name !== "" && !canCreate && matches.length === 0
  return (
    <div
      ref={rootRef}
      // 宽高都不超过画布（百分比相对画布计算，四周留出边距）：画布较矮时列表
      // 变短、可以滚动到最后一项，选择框不会被画布裁掉。
      className={cn(
        "absolute z-20 flex max-h-[calc(100%-1rem)] w-64 max-w-[calc(100%-1rem)] flex-col gap-2 rounded-lg border bg-popover p-2 text-popover-foreground shadow-lg",
        !anchor && "top-4 left-1/2 max-h-[calc(100%-2rem)] -translate-x-1/2",
        className
      )}
      onPointerDown={(event) => event.stopPropagation()}
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
          setActive(0)
          listRef.current?.scrollTo({ top: 0 })
        }}
        onKeyDown={onKeyDown}
        onBlur={(event) => {
          // 点到选择框以外的地方时关闭。
          if (
            !event.currentTarget.parentElement?.contains(event.relatedTarget)
          ) {
            onClose()
          }
        }}
        placeholder={classes.length > 0 ? "搜索或输入新类别" : "输入类别名称"}
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
          const key =
            option.kind === "class" ? `c:${option.cls.name}` : "create"
          const hotkey =
            option.kind === "class" ? classHotkey(option.index) : null
          return (
            <li
              key={key}
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
                  setActive(index)
                }
              }}
              onMouseDown={(event) => event.preventDefault()}
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
                    style={{ backgroundColor: colorFor(option.cls.name) }}
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1 truncate">
                    {option.cls.name}
                  </span>
                  {option.cls.name === current && (
                    <span className="text-xs text-muted-foreground">当前</span>
                  )}
                  {hotkey && <Kbd>{hotkey}</Kbd>}
                </>
              ) : (
                <>
                  <Plus className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate">
                    新建类别“{option.name}”
                  </span>
                </>
              )}
            </li>
          )
        })}
        {options.length === 0 && (
          <li className="px-2 py-1.5 text-sm text-muted-foreground">
            {error
              ? (classNameError(
                  name,
                  classes.map((cls) => cls.name)
                ) ?? "没有匹配的类别")
              : "还没有类别，输入名称新建"}
          </li>
        )}
      </ul>
    </div>
  )
}
