import { useState, type ComponentProps, type ReactNode, type Ref } from "react"
import { Link } from "react-router"
import {
  ArrowLeft,
  Check,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  ClipboardPaste,
  Ellipsis,
  FileX2,
  Hand,
  Keyboard,
  Maximize,
  MousePointer2,
  PanelLeft,
  PanelRight,
  Redo2,
  RefreshCw,
  SkipForward,
  SlidersHorizontal,
  Square,
  SquareDashed,
  Tags,
  TriangleAlert,
  Undo2,
  ZoomIn,
  ZoomOut,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type { ImageAdjustments } from "@/features/annotation/annotation-canvas"
import type { SaverState } from "@/features/annotation/autosave"
import type { Tool } from "@/features/annotation/shortcuts"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

export type ViewOptions = {
  showLabels: boolean
  showBoxes: boolean
  /** 切换到同尺寸的图片时保持缩放与位置（固定相机拍摄的产线图片）。 */
  keepViewport: boolean
} & ImageAdjustments

function ToolButton({
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

type EditorToolbarProps = {
  datasetId: number
  datasetName: string
  fileName: string
  path: string
  /** 当前图片在筛选结果中的位置（从 0 开始），不满足筛选条件时为 null。 */
  position: number | null
  total: number | null
  positionRef: Ref<HTMLInputElement>
  onGoto: (index: number) => void
  canPrev: boolean
  canNext: boolean
  navigating: boolean
  onPrev: () => void
  onNext: () => void
  onNextUnannotated: () => void
  tool: Tool
  onToolChange: (tool: Tool) => void
  readOnly: boolean
  canUndo: boolean
  canRedo: boolean
  onUndo: () => void
  onRedo: () => void
  zoom: number | null
  onZoom: (direction: 1 | -1) => void
  onFit: () => void
  onActualSize: () => void
  view: ViewOptions
  onViewChange: (view: ViewOptions) => void
  saveState: SaverState
  savedAt: Date | null
  onRetrySave: () => void
  canCopyPrevious: boolean
  onCopyPrevious: () => void
  canMarkNegative: boolean
  onMarkNegative: () => void
  canClearFile: boolean
  onClearFile: () => void
  onReload: () => void
  onManageClasses: () => void
  onHelp: () => void
  onOpenImages: () => void
  onOpenPanel: () => void
  mod: string
}

/** 标注工作台顶部的工具栏：图片切换、工具、撤销、缩放、视图设置、保存状态。 */
export function EditorToolbar(props: EditorToolbarProps) {
  const {
    datasetId,
    datasetName,
    fileName,
    path,
    canPrev,
    canNext,
    navigating,
    onPrev,
    onNext,
    onNextUnannotated,
    tool,
    onToolChange,
    readOnly,
    canUndo,
    canRedo,
    onUndo,
    onRedo,
    zoom,
    onZoom,
    onFit,
    onActualSize,
    mod,
  } = props

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-1 gap-y-1 border-b bg-background px-2 py-1.5">
      {/* 第一组：返回、图片列表、文件名与位置、上一张 / 下一张 */}
      <div className="flex min-w-0 flex-1 items-center gap-1 md:flex-none">
        <ToolButton label={`返回数据集“${datasetName}”`} asChild>
          <Link to={`/datasets/${datasetId}`}>
            <ArrowLeft />
          </Link>
        </ToolButton>
        <ToolButton
          label="图片列表"
          onClick={props.onOpenImages}
          className="xl:hidden"
        >
          <PanelLeft />
        </ToolButton>
        <div className="flex min-w-0 flex-1 flex-col px-1 md:w-56 md:flex-none lg:w-64">
          <span className="truncate text-sm leading-5 font-medium" title={path}>
            {fileName}
          </span>
          <PositionField
            position={props.position}
            total={props.total}
            inputRef={props.positionRef}
            onGoto={props.onGoto}
          />
        </div>
        <ToolButton
          label="上一张"
          keys={["A"]}
          onClick={onPrev}
          disabled={!canPrev || navigating}
        >
          <ChevronLeft />
        </ToolButton>
        <ToolButton
          label="下一张"
          keys={["D"]}
          onClick={onNext}
          disabled={!canNext || navigating}
        >
          {navigating ? <Spinner /> : <ChevronRight />}
        </ToolButton>
        <ToolButton
          label="下一张未标注"
          keys={["Shift+D"]}
          onClick={onNextUnannotated}
          disabled={navigating}
          className="hidden sm:inline-flex"
        >
          <SkipForward />
        </ToolButton>
      </div>

      <Separator />

      {/* 第二组：工具、撤销、缩放、视图 */}
      <div className="order-last flex w-full items-center gap-1 md:order-none md:w-auto">
        <ToggleGroup
          type="single"
          size="sm"
          value={tool}
          onValueChange={(value) => value && onToolChange(value as Tool)}
          aria-label="工具"
        >
          <ToolToggle value="select" label="选择 / 编辑" keys={["V"]}>
            <MousePointer2 />
          </ToolToggle>
          <ToolToggle
            value="rect"
            label="画矩形框"
            keys={["W"]}
            disabled={readOnly}
          >
            <Square />
          </ToolToggle>
          <ToolToggle value="pan" label="平移画面（或按住空格）" keys={[]}>
            <Hand />
          </ToolToggle>
        </ToggleGroup>
        <Separator />
        <ToolButton
          label="撤销"
          keys={[`${mod}+Z`]}
          onClick={onUndo}
          disabled={!canUndo}
        >
          <Undo2 />
        </ToolButton>
        <ToolButton
          label="重做"
          keys={[`${mod}+Shift+Z`]}
          onClick={onRedo}
          disabled={!canRedo}
        >
          <Redo2 />
        </ToolButton>
        <Separator />
        {/* 手机上用双指缩放与缩放比例菜单，省出宽度给“类别与目标”按钮。 */}
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
        <ViewPopover view={props.view} onChange={props.onViewChange} />
        <div className="ml-auto flex items-center gap-1 md:hidden">
          <MoreMenu {...props} />
          <ToolButton label="类别与目标" onClick={props.onOpenPanel}>
            <PanelRight />
          </ToolButton>
        </div>
      </div>

      {/* 第三组：保存状态、更多 */}
      <div className="flex items-center gap-1 md:ml-auto">
        <SaveStatus
          state={props.saveState}
          savedAt={props.savedAt}
          onRetry={props.onRetrySave}
        />
        <div className="hidden items-center gap-1 md:flex">
          <MoreMenu {...props} />
          <ToolButton label="快捷键" keys={["?"]} onClick={props.onHelp}>
            <Keyboard />
          </ToolButton>
          <ToolButton
            label="类别与目标"
            onClick={props.onOpenPanel}
            className="lg:hidden"
          >
            <PanelRight />
          </ToolButton>
        </div>
      </div>
    </div>
  )
}

function Separator() {
  return <span className="mx-0.5 hidden h-5 w-px bg-border sm:block" />
}

function ToolToggle({
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

function PositionField({
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

function ViewPopover({
  view,
  onChange,
}: {
  view: ViewOptions
  onChange: (view: ViewOptions) => void
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

function ViewSwitch({
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

function MoreMenu(props: EditorToolbarProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label="更多操作">
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuItem
          onSelect={props.onCopyPrevious}
          disabled={!props.canCopyPrevious}
        >
          <ClipboardPaste />
          沿用上一张的框
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={props.onMarkNegative}
          disabled={!props.canMarkNegative}
        >
          <SquareDashed />
          标为空标注（无目标）
          <DropdownMenuShortcut>N</DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={props.onClearFile}
          disabled={!props.canClearFile}
        >
          <FileX2 />
          删除标注文件（恢复为未标注）
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={props.onManageClasses}>
          <Tags />
          管理类别
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={props.onReload}>
          <RefreshCw />
          重新读取标注文件
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={props.onHelp}>
          <Keyboard />
          快捷键说明
          <DropdownMenuShortcut>?</DropdownMenuShortcut>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
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
          onClick={onRetry}
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
