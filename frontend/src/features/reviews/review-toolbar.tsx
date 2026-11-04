import type { ReactNode, Ref } from "react"
import { Link } from "react-router"
import {
  ArrowBigRightDash,
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  Ellipsis,
  Hand,
  Keyboard,
  MousePointer2,
  PanelLeft,
  PanelRight,
  Redo2,
  RefreshCw,
  RotateCcw,
  SkipForward,
  Square,
  Tags,
  Undo2,
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
import { Spinner } from "@/components/ui/spinner"
import { Toggle } from "@/components/ui/toggle"
import { ToggleGroup } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type { SaverState } from "@/features/annotation/autosave"
import type { ViewOptions } from "@/features/annotation/editor-common"
import type { Tool } from "@/features/annotation/shortcuts"
import {
  PositionField,
  SaveStatus,
  ToolbarSeparator,
  ToolButton,
  ToolToggle,
  ViewPopover,
  ViewSwitch,
  ZoomControls,
} from "@/features/annotation/toolbar-parts"

type ReviewToolbarProps = {
  backTo: string
  backLabel: string
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
  onNextPending: () => void
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
  /** 用虚线显示审图标注中已经没有的模型识别框。 */
  showPredictions: boolean
  onShowPredictionsChange: (value: boolean) => void
  autoAdvance: boolean
  onAutoAdvanceChange: (value: boolean) => void
  saveState: SaverState
  savedAt: Date | null
  onRetrySave: () => void
  canReset: boolean
  onReset: () => void
  onReload: () => void
  /** 管理候选类别的页面（模型所属项目的类别列表）；没有时为 null。 */
  classesPath: string | null
  onHelp: () => void
  onOpenImages: () => void
  onOpenPanel: () => void
  /** 编辑器之外的操作（如本地审图程序的“导出审图结果”），放在保存状态旁。 */
  actions?: ReactNode
  mod: string
}

/** 审图编辑器顶部的工具栏：图片切换、工具、撤销、缩放、视图设置、保存状态。 */
export function ReviewToolbar(props: ReviewToolbarProps) {
  const {
    fileName,
    path,
    canPrev,
    canNext,
    navigating,
    tool,
    onToolChange,
    readOnly,
    mod,
  } = props

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-1 gap-y-1 border-b bg-background px-2 py-1.5">
      {/* 第一组：返回、图片列表、文件名与位置、上一张 / 下一张 */}
      <div className="flex min-w-0 flex-1 items-center gap-1 md:flex-none">
        <ToolButton label={props.backLabel} asChild>
          <Link to={props.backTo}>
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
        <div className="flex min-w-0 flex-1 flex-col px-1 md:w-52 md:flex-none xl:w-64">
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
          onClick={props.onPrev}
          disabled={!canPrev || navigating}
        >
          <ChevronLeft />
        </ToolButton>
        <ToolButton
          label="下一张"
          keys={["D"]}
          onClick={props.onNext}
          disabled={!canNext || navigating}
        >
          {navigating ? <Spinner /> : <ChevronRight />}
        </ToolButton>
        <ToolButton
          label="下一张待审核"
          keys={["Shift+D"]}
          onClick={props.onNextPending}
          disabled={navigating}
          className="hidden sm:inline-flex"
        >
          <SkipForward />
        </ToolButton>
        <AutoAdvanceToggle
          pressed={props.autoAdvance}
          onPressedChange={props.onAutoAdvanceChange}
        />
      </div>

      <ToolbarSeparator />

      {/* 第二组：工具、撤销、缩放、视图 */}
      <div className="order-last flex w-full [scrollbar-width:none] items-center gap-1 overflow-x-auto md:order-none md:w-auto md:overflow-visible">
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
        <ToolbarSeparator />
        <ToolButton
          label="撤销"
          keys={[`${mod}+Z`]}
          onClick={props.onUndo}
          disabled={!props.canUndo}
        >
          <Undo2 />
        </ToolButton>
        <ToolButton
          label="重做"
          keys={[`${mod}+Shift+Z`]}
          onClick={props.onRedo}
          disabled={!props.canRedo}
        >
          <Redo2 />
        </ToolButton>
        <ToolbarSeparator />
        <ZoomControls
          zoom={props.zoom}
          onZoom={props.onZoom}
          onFit={props.onFit}
          onActualSize={props.onActualSize}
          mod={mod}
        />
        <ViewPopover view={props.view} onChange={props.onViewChange}>
          <ViewSwitch
            id="view-predictions"
            label="虚线显示被修改的识别框"
            checked={props.showPredictions}
            onCheckedChange={props.onShowPredictionsChange}
          />
        </ViewPopover>
        <div className="ml-auto flex items-center gap-1 md:hidden">
          <MoreMenu {...props} />
          <ToolButton label="审核状态与目标" onClick={props.onOpenPanel}>
            <PanelRight />
          </ToolButton>
        </div>
      </div>

      {/* 第三组：保存状态、编辑器之外的操作、更多 */}
      <div className="flex items-center gap-1 md:ml-auto">
        <SaveStatus
          state={props.saveState}
          savedAt={props.savedAt}
          onRetry={props.onRetrySave}
        />
        {props.actions}
        <div className="hidden items-center gap-1 md:flex">
          <MoreMenu {...props} />
          <ToolButton label="快捷键" keys={["?"]} onClick={props.onHelp}>
            <Keyboard />
          </ToolButton>
          <ToolButton
            label="审核状态与目标"
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

/** “标记后自动跳到下一张”的开关（记在浏览器中）。 */
function AutoAdvanceToggle({
  pressed,
  onPressedChange,
}: {
  pressed: boolean
  onPressedChange: (pressed: boolean) => void
}) {
  const label = pressed
    ? "标记后自动跳到下一张：开"
    : "标记后自动跳到下一张：关"
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Toggle
          size="sm"
          pressed={pressed}
          onPressedChange={onPressedChange}
          aria-label="标记后自动跳到下一张"
          className="px-2 data-[state=on]:text-primary"
        >
          <ArrowBigRightDash />
        </Toggle>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

function MoreMenu(props: ReviewToolbarProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label="更多操作">
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuItem onSelect={props.onReset} disabled={!props.canReset}>
          <RotateCcw />
          恢复模型识别结果
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={props.onReload}>
          <RefreshCw />
          重新读取审图标注
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {props.classesPath && (
          <DropdownMenuItem asChild>
            <Link to={props.classesPath}>
              <Tags />
              在项目中管理类别
            </Link>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onSelect={props.onHelp}>
          <Keyboard />
          快捷键说明
          <DropdownMenuShortcut>?</DropdownMenuShortcut>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
