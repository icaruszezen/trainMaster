import { useId, useState, type CSSProperties, type KeyboardEvent } from "react"
import { Plus } from "lucide-react"

import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { classNameError, filterClasses } from "@/features/annotation/classes"
import { classHotkey } from "@/features/annotation/shortcuts"
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
  className?: string
  style?: CSSProperties
}

type Option =
  | { kind: "class"; cls: DatasetClass; index: number }
  | { kind: "create"; name: string }

/**
 * 类别选择框：输入筛选（不区分大小写），↑ / ↓ 选择，Enter 确定，Esc 关闭。
 * 输入的名称不在类别列表中时可以新建（列表项写明“新建类别”，避免手误建出
 * 多余的类别）。
 */
export function LabelPicker({
  classes,
  colorFor,
  current,
  title,
  onPick,
  onClose,
  className,
  style,
}: LabelPickerProps) {
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)
  const listId = useId()

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

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) {
      return
    }
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault()
        setActive((activeIndex + 1) % Math.max(1, options.length))
        return
      case "ArrowUp":
        event.preventDefault()
        setActive(
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
      className={cn(
        "z-20 flex w-64 flex-col gap-2 rounded-lg border bg-popover p-2 text-popover-foreground shadow-lg",
        className
      )}
      style={style}
      onPointerDown={(event) => event.stopPropagation()}
      onDoubleClick={(event) => event.stopPropagation()}
    >
      <p className="px-1 text-xs font-medium text-muted-foreground">{title}</p>
      <Input
        autoFocus
        value={query}
        onChange={(event) => {
          setQuery(event.target.value)
          setActive(0)
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
        className="h-8"
      />
      <ul
        id={listId}
        role="listbox"
        aria-label="类别"
        className="max-h-60 overflow-y-auto"
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
              onPointerEnter={() => setActive(index)}
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
