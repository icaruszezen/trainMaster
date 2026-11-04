import type { Ref } from "react"
import { Link } from "react-router"
import {
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  ClipboardPaste,
  Ellipsis,
  FileX2,
  Hand,
  Keyboard,
  MousePointer2,
  PanelLeft,
  PanelRight,
  Redo2,
  RefreshCw,
  SkipForward,
  Square,
  SquareDashed,
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
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Spinner } from "@/components/ui/spinner"
import { ToggleGroup } from "@/components/ui/toggle-group"
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
  ZoomControls,
} from "@/features/annotation/toolbar-parts"
import type { DatasetProjectRef } from "@/features/datasets/api"

type EditorToolbarProps = {
  datasetId: number
  datasetName: string
  /** 数据集所属的项目：类别列表在项目页面中管理。 */
  projects: DatasetProjectRef[]
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
        <ToolbarSeparator />
        <ZoomControls
          zoom={zoom}
          onZoom={onZoom}
          onFit={onFit}
          onActualSize={onActualSize}
          mod={mod}
        />
        <ViewPopover view={props.view} onChange={props.onViewChange} />
        <div className="ml-auto flex items-center gap-1 md:hidden">
          <MoreMenu {...props} />
          <ToolButton label="目标与图片信息" onClick={props.onOpenPanel}>
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
            label="目标与图片信息"
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
          删除标注文件
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <ManageClassesItem projects={props.projects} />
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

const classesPath = (projectId: number) => `/projects/${projectId}?tab=classes`

/**
 * 前往项目页面的“类别”：类别列表属于项目（训练时按它的顺序导出 YOLO），在那里
 * 管理；数据集属于几个项目时选择其中一个。离开前照常先保存这张图片（见标注
 * 工作台的 useBlocker）。
 */
function ManageClassesItem({ projects }: { projects: DatasetProjectRef[] }) {
  if (projects.length === 0) {
    return (
      <DropdownMenuItem disabled>
        <Tags />
        <span className="grid">
          在项目中管理类别
          <span className="text-xs text-muted-foreground">
            数据集未纳入项目
          </span>
        </span>
      </DropdownMenuItem>
    )
  }
  if (projects.length === 1) {
    const [project] = projects
    return (
      <DropdownMenuItem asChild>
        <Link to={classesPath(project.id)}>
          <Tags />
          <span className="grid min-w-0">
            在项目中管理类别
            <span className="truncate text-xs text-muted-foreground">
              {project.name}
            </span>
          </span>
        </Link>
      </DropdownMenuItem>
    )
  }
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <Tags />
        在项目中管理类别
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="max-w-64">
        {projects.map((project) => (
          <DropdownMenuItem key={project.id} asChild>
            <Link to={classesPath(project.id)}>
              <span className="truncate">{project.name}</span>
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}
