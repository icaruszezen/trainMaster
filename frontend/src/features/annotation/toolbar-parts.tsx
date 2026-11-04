/**
 * 标注工作台与审图编辑器的工具栏共用的部件：带提示与快捷键的按钮、工具切换、
 * 图片序号输入框、缩放、视图设置与保存状态。
 */
import { useState, type ComponentProps, type ReactNode, type Ref } from "react"
import {
  Check,
  CircleAlert,
  Maximize,
  SlidersHorizontal,
  TriangleAlert,
  ZoomIn,
  ZoomOut,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Slider } from "@/components/ui/slider"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type { SaverState } from "@/features/annotation/autosave"
import type { ViewOptions } from "@/features/annotation/editor-common"
import { focusCanvas, type Tool } from "@/features/annotation/shortcuts"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 工具栏中的图标按钮，提示中显示名称与快捷键。 */
export function ToolButton({
  label,
  keys,
  children,
  className,
  ...props
}: ComponentProps<typeof Button> & {
  label: string
  keys?: string[]
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={label}
          className={className}
          {...props}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent className="flex items-center gap-2">
        {label}
        {keys?.map((key) => (
          <Kbd key={key}>{key}</Kbd>
        ))}
      </TooltipContent>
    </Tooltip>
  )
}

/** 工具栏中分组之间的竖线（手机上不显示）。 */
export function ToolbarSeparator() {
  return <span className="mx-0.5 hidden h-5 w-px bg-border sm:block" />
}

/** 工具切换（`ToggleGroup` 中的一项），提示中显示名称与快捷键。 */
export function ToolToggle({
  value,
  label,
  keys,
  disabled,
  children,
}: {
  value: Tool
  label: string
  keys: string[]
  disabled?: boolean
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <ToggleGroupItem
          value={value}
          aria-label={label}
          disabled={disabled}
          className="px-2"
        >
          {children}
        </ToggleGroupItem>
      </TooltipTrigger>
      <TooltipContent className="flex items-center gap-2">
        {label}
        {keys.map((key) => (
          <Kbd key={key}>{key}</Kbd>
        ))}
      </TooltipContent>
    </Tooltip>
  )
}

/** 当前图片的序号（第 N / 共 M 张），输入序号后按 Enter 跳转（G 聚焦）。 */
export function PositionField({
  position,
  total,
  inputRef,
  onGoto,
}: {
  position: number | null
  total: number | null
  inputRef: Ref<HTMLInputElement>
  onGoto: (index: number) => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const shown = position === null ? "" : String(position + 1)
  const commit = () => {
    if (draft === null) {
      return
    }
    const value = Number(draft)
    setDraft(null)
    if (Number.isInteger(value) && value >= 1 && total && value <= total) {
      if (value - 1 !== position) {
        onGoto(value - 1)
      }
    }
  }
  return (
    <span className="flex items-center gap-1 text-xs text-muted-foreground tabular-nums">
      第
      <Input
        ref={inputRef}
        value={draft ?? shown}
        placeholder={position === null ? "—" : undefined}
        inputMode="numeric"
        onFocus={(event) => {
          setDraft(shown)
          event.currentTarget.select()
        }}
        onChange={(event) => setDraft(event.target.value.replace(/\D/g, ""))}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            commit()
            event.currentTarget.blur()
          } else if (event.key === "Escape") {
            setDraft(null)
            event.currentTarget.blur()
          }
        }}
        onBlur={() => setDraft(null)}
        aria-label="跳到第几张（G）"
        className="h-5 w-14 rounded-sm px-1 text-center text-xs md:text-xs"
      />
      / {total === null ? "—" : formatNumber(total)} 张
    </span>
  )
}

/**
 * 视图设置（显示类别名称、标注框，保持视口，亮度、对比度）；`children` 是编辑器
 * 自己的其它开关，放在通用的开关之后。
 */
export function ViewPopover({
  view,
  onChange,
  children,
}: {
  view: ViewOptions
  onChange: (view: ViewOptions) => void
  children?: ReactNode
}) {
  const adjusted = view.brightness !== 1 || view.contrast !== 1
  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="视图设置"
              className={cn(adjusted && "text-primary")}
            >
              <SlidersHorizontal />
            </Button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent>视图设置（亮度、对比度、显示）</TooltipContent>
      </Tooltip>
      <PopoverContent align="start" className="grid w-72 gap-4">
        <ViewSwitch
          id="view-labels"
          label="显示类别名称"
          shortcut="L"
          checked={view.showLabels}
          onCheckedChange={(showLabels) => onChange({ ...view, showLabels })}
        />
        <ViewSwitch
          id="view-boxes"
          label="显示标注框"
          shortcut="H"
          checked={view.showBoxes}
          onCheckedChange={(showBoxes) => onChange({ ...view, showBoxes })}
        />
        <ViewSwitch
          id="view-keep"
          label="同尺寸图片保持缩放与位置"
          checked={view.keepViewport}
          onCheckedChange={(keepViewport) =>
            onChange({ ...view, keepViewport })
          }
        />
        {children}
        <AdjustmentSlider
          label="亮度"
          value={view.brightness}
          onChange={(brightness) => onChange({ ...view, brightness })}
        />
        <AdjustmentSlider
          label="对比度"
          value={view.contrast}
          onChange={(contrast) => onChange({ ...view, contrast })}
        />
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs text-muted-foreground">
            亮度、对比度只影响显示，不修改图片。
          </p>
          <Button
            variant="outline"
            size="sm"
            disabled={!adjusted}
            onClick={() => onChange({ ...view, brightness: 1, contrast: 1 })}
          >
            重置
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}

/** 视图设置中的一个开关。 */
export function ViewSwitch({
  id,
  label,
  shortcut,
  checked,
  onCheckedChange,
}: {
  id: string
  label: string
  shortcut?: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <Label htmlFor={id} className="font-normal">
        {label}
        {shortcut && <Kbd>{shortcut}</Kbd>}
      </Label>
      <Switch id={id} checked={checked} onCheckedChange={onCheckedChange} />
    </div>
  )
}

function AdjustmentSlider({
  label,
  value,
  onChange,
}: {
  label: string
  value: number
  onChange: (value: number) => void
}) {
  return (
    <div className="grid gap-2">
      <div className="flex items-center justify-between text-sm">
        <span>{label}</span>
        <span className="text-muted-foreground tabular-nums">
          {Math.round(value * 100)}%
        </span>
      </div>
      <Slider
        min={0.2}
        max={3}
        step={0.05}
        value={[value]}
        onValueChange={([next]) => onChange(next)}
        aria-label={label}
      />
    </div>
  )
}

/**
 * 缩放：缩小、缩放比例菜单（适应窗口、原图大小；手机上也有放大 / 缩小）、放大、
 * 适应窗口。手机上用双指缩放与缩放比例菜单，省出宽度给其它按钮。
 */
export function ZoomControls({
  zoom,
  onZoom,
  onFit,
  onActualSize,
  mod,
}: {
  zoom: number | null
  onZoom: (direction: 1 | -1) => void
  onFit: () => void
  onActualSize: () => void
  mod: string
}) {
  return (
    <>
      <ToolButton
        label="缩小"
        keys={["-"]}
        onClick={() => onZoom(-1)}
        className="hidden sm:inline-flex"
      >
        <ZoomOut />
      </ToolButton>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="sm"
            className="w-14 px-1 font-normal tabular-nums"
            aria-label="缩放比例"
          >
            {zoom === null ? "—" : `${Math.round(zoom * 100)}%`}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="center">
          <DropdownMenuItem onSelect={() => onZoom(1)} className="sm:hidden">
            放大
            <DropdownMenuShortcut>+</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => onZoom(-1)} className="sm:hidden">
            缩小
            <DropdownMenuShortcut>-</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={onFit}>
            适应窗口
            <DropdownMenuShortcut>F</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={onActualSize}>
            原图大小（100%）
            <DropdownMenuShortcut>{mod}+0</DropdownMenuShortcut>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ToolButton
        label="放大"
        keys={["+"]}
        onClick={() => onZoom(1)}
        className="hidden sm:inline-flex"
      >
        <ZoomIn />
      </ToolButton>
      <ToolButton
        label="适应窗口"
        keys={["F"]}
        onClick={onFit}
        className="hidden sm:inline-flex"
      >
        <Maximize />
      </ToolButton>
    </>
  )
}

function formatTime(date: Date) {
  return date.toLocaleTimeString("zh-CN", { hour12: false })
}

/** 保存状态：未保存 / 保存中 / 已保存（时间）/ 保存失败（重试）/ 冲突。 */
export function SaveStatus({
  state,
  savedAt,
  onRetry,
}: {
  state: SaverState
  savedAt: Date | null
  onRetry: () => void
}) {
  const base =
    "flex h-8 items-center gap-1.5 rounded-md px-2 text-xs whitespace-nowrap"
  switch (state.status) {
    case "dirty":
      return (
        <span className={cn(base, "text-muted-foreground")} role="status">
          <span className="size-2 rounded-full bg-amber-500" aria-hidden />
          未保存
        </span>
      )
    case "saving":
      return (
        <span className={cn(base, "text-muted-foreground")} role="status">
          <Spinner className="size-3.5" />
          保存中…
        </span>
      )
    case "saved":
      return (
        <span className={cn(base, "text-muted-foreground")} role="status">
          <Check className="size-3.5 text-emerald-600 dark:text-emerald-500" />
          已保存
          {savedAt && (
            <span className="hidden tabular-nums sm:inline">
              {formatTime(savedAt)}
            </span>
          )}
        </span>
      )
    case "error":
      return (
        <button
          type="button"
          onClick={(event) => {
            // 按钮随后变为“保存中”：焦点先移到画布上，不落到 body 上。
            focusCanvas(event.currentTarget)
            onRetry()
          }}
          title={state.error ?? undefined}
          className={cn(
            base,
            "text-destructive hover:bg-destructive/10 focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
          )}
        >
          <CircleAlert className="size-3.5" />
          保存失败，重试
        </button>
      )
    case "conflict":
      return (
        <span
          className={cn(base, "text-amber-700 dark:text-amber-500")}
          role="status"
        >
          <TriangleAlert className="size-3.5" />
          保存冲突
        </span>
      )
    default:
      return (
        <span className={cn(base, "text-muted-foreground")} role="status">
          没有修改
        </span>
      )
  }
}
