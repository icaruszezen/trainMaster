/**
 * Ultralytics YOLO 分级训练参数编辑器。
 *
 * 按服务端 `/api/v1/training/params-schema` 下发的 86 项官方参数规格渲染：
 * - 顶部精简为紧凑工具栏（标题、可点击的全局修改筛选徽章、全局搜索、YAML 预览与重置）
 *   以及可选的轻量模板条（`headerSlot`）；
 * - 12 项高频常用参数按「训练节奏与输入」「优化器与增强」「设备与数据加载」三个语义子分组
 *   以等高多列表单栅格直接展示；
 * - 其余 74 项低频参数默认收纳在底部「高级选项」折叠面板中，展开后保留 4 个官方分组 Tab，
 *   每个 Tab 内部按语义拆分子分组，并将非当前任务的专用参数收纳于底部可折叠子区。
 */
import { useId, useMemo, useState, type ReactNode } from "react"
import {
  ChevronDown,
  ChevronRight,
  FileCode2,
  Filter,
  HelpCircle,
  RotateCcw,
  Search,
  SlidersHorizontal,
  X,
} from "lucide-react"

import { ScrollingTabsList } from "@/components/scrolling-tabs-list"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsTrigger } from "@/components/ui/tabs"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type {
  InitMode,
  ParamGroupKey,
  ParamSpec,
  ParamsSchemaResponse,
} from "@/features/training/api"
import { CommandBlock } from "@/features/training/command-block"
import { TASK_SCOPE_LABELS } from "@/features/training/format"
import {
  buildDefaultParams,
  buildTemplateDefaultParams,
  cleanParamLabel,
  countModifiedParams,
  filterParamSpecs,
  formatCompactParamValue,
  formatParamsYaml,
  formatParamValueText,
  getAutoOptimizerHint,
  getModeDefaultValue,
  groupCommonSpecs,
  IMGSZ_QUICK_PRESETS,
  isParamModifiedForMode,
  listModifiedParams,
  parseFreezeText,
  parseIntListText,
  partitionAdvancedSpecs,
  splitSpecsByControlKind,
  splitSpecsByTier,
} from "@/features/training/training-params"
import { cn } from "@/lib/utils"

const MAX_COLLAPSED_CHIPS = 8

export type TrainingParamsEditorProps = {
  schema: ParamsSchemaResponse
  values: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  initMode?: InitMode
  /** 当前训练任务类型（如 detect / segment），用于将非当前任务的专用参数折叠收纳。 */
  activeTask?: string
  /** job：用于新建训练任务；template：用于创建/编辑参数模板（resume 固定托管）。 */
  mode?: "job" | "template"
  disabled?: boolean
  /** 只读预览模式（如系统内置模板详情）：禁用参数编辑并隐藏各类“恢复默认”按钮，仍支持搜索、展开高级项与查看 YAML。 */
  readOnly?: boolean
  /** 自定义标题文案（如在新建训练页中统一编号为「3. YOLO 训练参数配置」）。 */
  title?: string
  /** 顶部轻量模板条扩展槽（去边框后紧接在主工具栏下方）。 */
  headerSlot?: ReactNode
}

export function TrainingParamsEditor({
  schema,
  values,
  onChange,
  initMode = "scratch",
  activeTask,
  mode = "job",
  disabled = false,
  readOnly = false,
  title,
  headerSlot,
}: TrainingParamsEditorProps) {
  const effectiveDisabled = disabled || readOnly
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [activeTab, setActiveTab] = useState<ParamGroupKey>("train")
  const [query, setQuery] = useState("")
  const [onlyModifiedGlobal, setOnlyModifiedGlobal] = useState(false)
  const [onlyModifiedAdvanced, setOnlyModifiedAdvanced] = useState(false)
  const [otherTaskOpenByGroup, setOtherTaskOpenByGroup] = useState<
    Partial<Record<ParamGroupKey, boolean>>
  >({})
  const [yamlOpen, setYamlOpen] = useState(false)

  const effectiveInitMode: InitMode | undefined =
    mode === "job" ? initMode : undefined

  const { common: commonSpecs, advanced: advancedSpecs } = useMemo(
    () => splitSpecsByTier(schema.specs),
    [schema.specs]
  )

  const commonSubgroups = useMemo(
    () => groupCommonSpecs(commonSpecs),
    [commonSpecs]
  )

  const groupLabelByKey = useMemo(
    () => new Map(schema.groups.map((g) => [g.key, g.label])),
    [schema.groups]
  )

  const totalModified = useMemo(
    () => countModifiedParams(schema.specs, values, undefined, effectiveInitMode),
    [schema.specs, values, effectiveInitMode]
  )
  const modifiedCommonItems = useMemo(
    () => listModifiedParams(commonSpecs, values, effectiveInitMode),
    [commonSpecs, values, effectiveInitMode]
  )
  const modifiedAdvancedItems = useMemo(
    () => listModifiedParams(advancedSpecs, values, effectiveInitMode),
    [advancedSpecs, values, effectiveInitMode]
  )

  const commonModified = modifiedCommonItems.length

  const trimmedQuery = query.trim()
  const isSearching = trimmedQuery !== ""

  const searchResults = useMemo(() => {
    if (!isSearching) {
      return []
    }
    const matched = filterParamSpecs(schema.specs, values, {
      group: "all",
      query: trimmedQuery,
      initMode: effectiveInitMode,
    })
    return [...matched].sort((a, b) => Number(b.common) - Number(a.common))
  }, [schema.specs, values, isSearching, trimmedQuery, effectiveInitMode])

  const filteredAdvancedSpecs = useMemo(
    () =>
      filterParamSpecs(advancedSpecs, values, {
        group: activeTab,
        onlyModified: onlyModifiedAdvanced,
        initMode: effectiveInitMode,
      }),
    [
      advancedSpecs,
      values,
      activeTab,
      onlyModifiedAdvanced,
      effectiveInitMode,
    ]
  )

  const { subgroups: advancedSubgroups, otherTaskSpecs } = useMemo(
    () => partitionAdvancedSpecs(filteredAdvancedSpecs, activeTab, activeTask),
    [filteredAdvancedSpecs, activeTab, activeTask]
  )

  const otherTaskModifiedCount = useMemo(
    () =>
      countModifiedParams(otherTaskSpecs, values, undefined, effectiveInitMode),
    [otherTaskSpecs, values, effectiveInitMode]
  )

  const otherTaskScopeSummary = useMemo(() => {
    const labels: string[] = []
    for (const spec of otherTaskSpecs) {
      if (!spec.task_scope) {
        continue
      }
      const label = TASK_SCOPE_LABELS[spec.task_scope] ?? spec.task_scope
      if (!labels.includes(label)) {
        labels.push(label)
      }
    }
    return labels.join(" · ")
  }, [otherTaskSpecs])

  const isOtherTaskOpen =
    otherTaskOpenByGroup[activeTab] ??
    (otherTaskModifiedCount > 0 || onlyModifiedAdvanced)

  const activeGroupMeta = schema.groups.find((g) => g.key === activeTab)
  const activeAdvancedGroupModified = useMemo(
    () =>
      countModifiedParams(
        advancedSpecs,
        values,
        activeTab,
        effectiveInitMode
      ),
    [advancedSpecs, values, activeTab, effectiveInitMode]
  )

  const setOneParam = (key: string, nextValue: unknown) => {
    onChange({
      ...values,
      [key]: nextValue,
    })
  }

  const resetOneParam = (spec: ParamSpec) => {
    const base = getModeDefaultValue(spec, effectiveInitMode)
    setOneParam(spec.key, Array.isArray(base) ? [...base] : base)
  }

  const resetSpecs = (targetSpecs: readonly ParamSpec[]) => {
    const defaults =
      mode === "template"
        ? buildTemplateDefaultParams(schema.specs)
        : buildDefaultParams(schema.specs, initMode)
    const next = { ...values }
    for (const spec of targetSpecs) {
      next[spec.key] = defaults[spec.key]
    }
    onChange(next)
  }

  const resetAdvancedGroup = (group: ParamGroupKey) => {
    resetSpecs(advancedSpecs.filter((s) => s.group === group))
  }

  const resetAll = () => {
    onChange(
      mode === "template"
        ? buildTemplateDefaultParams(schema.specs)
        : buildDefaultParams(schema.specs, initMode)
    )
    setOnlyModifiedGlobal(false)
  }

  const jumpToAdvancedGroup = (group: ParamGroupKey, isTaskScoped: boolean) => {
    setOnlyModifiedGlobal(false)
    setAdvancedOpen(true)
    setActiveTab(group)
    setOnlyModifiedAdvanced(false)
    if (isTaskScoped) {
      setOtherTaskOpenByGroup((prev) => ({ ...prev, [group]: true }))
    }
  }

  const isSpecLocked = (spec: ParamSpec) =>
    effectiveDisabled ||
    (spec.key === "resume" && (mode === "template" || initMode === "resume"))

  const specLockedHint = (spec: ParamSpec) => {
    if (spec.key === "resume" && mode === "template") {
      return "由训练任务初始化模式自动决定，模板中固定为 false"
    }
    if (spec.key === "resume" && initMode === "resume") {
      return "断点续训模式下自动开启"
    }
    return undefined
  }

  const hiddenAdvancedChipCount = Math.max(
    0,
    modifiedAdvancedItems.length - MAX_COLLAPSED_CHIPS
  )

  const resolvedTitle =
    title ??
    (mode === "template" ? "YOLO 模板参数配置" : "Ultralytics YOLO 训练参数")

  const renderSpecGrid = (
    specs: readonly ParamSpec[],
    options?: {
      columns?: 2 | 3
      showTierContext?: boolean
      showTaskScope?: boolean
    }
  ) => (
    <ParamSpecGroupGrid
      specs={specs}
      values={values}
      columns={options?.columns ?? 3}
      showTierContext={options?.showTierContext}
      showTaskScope={options?.showTaskScope}
      groupLabelByKey={groupLabelByKey}
      effectiveInitMode={effectiveInitMode}
      isSpecLocked={isSpecLocked}
      specLockedHint={specLockedHint}
      onChange={setOneParam}
      onReset={resetOneParam}
    />
  )

  return (
    <div className="grid min-w-0 gap-4">
      {/* 顶部紧凑两行工具栏 */}
      <div className="grid min-w-0 gap-3 border-b border-border/70 pb-3.5">
        {/* 第 1 行：标题 + 全局修改筛选徽章 | 紧凑搜索 + YAML + 重置 */}
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          <div className="flex flex-wrap items-center gap-2.5">
            <h3 className="text-base font-semibold tracking-tight">
              {resolvedTitle}
            </h3>

            {totalModified > 0 ? (
              <button
                type="button"
                aria-pressed={onlyModifiedGlobal}
                onClick={() => {
                  setQuery("")
                  setOnlyModifiedGlobal((prev) => !prev)
                }}
                title={
                  onlyModifiedGlobal
                    ? "点击返回完整参数视图"
                    : "点击一键查看全部已修改参数（含高级选项）"
                }
                className={cn(
                  "inline-flex h-6 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium tabular-nums transition-colors",
                  onlyModifiedGlobal
                    ? "border-primary bg-primary text-primary-foreground"
                    : "border-primary/30 bg-primary/[0.06] text-primary hover:border-primary/50 hover:bg-primary/10"
                )}
              >
                <span
                  className={cn(
                    "size-1.5 rounded-full",
                    onlyModifiedGlobal ? "bg-primary-foreground" : "bg-primary"
                  )}
                  aria-hidden
                />
                <span>
                  {onlyModifiedGlobal
                    ? `仅看已修改 (${totalModified})`
                    : `已修改 ${totalModified} 项`}
                </span>
                {onlyModifiedGlobal && <X className="size-3" />}
              </button>
            ) : (
              <span className="text-xs text-muted-foreground">
                官方默认 · 共 {schema.specs.length} 项
              </span>
            )}
          </div>

          <div className="flex w-full flex-wrap items-center justify-end gap-2 sm:w-auto">
            <div className="relative min-w-0 flex-1 sm:w-60 sm:flex-initial md:w-68">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={`搜索 ${schema.specs.length} 项参数名称或说明`}
                className="h-8 pr-7 pl-8 text-xs"
                aria-label="搜索训练参数"
              />
              {query && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  className="absolute top-1/2 right-1 size-6 -translate-y-1/2 text-muted-foreground"
                  onClick={() => setQuery("")}
                  aria-label="清空搜索词"
                >
                  <X />
                </Button>
              )}
            </div>

            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8 px-2.5 text-xs"
              onClick={() => setYamlOpen(true)}
            >
              <FileCode2 className="size-3.5" />
              查看 YAML
            </Button>

            {!readOnly && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-8 px-2.5 text-xs text-muted-foreground hover:text-foreground"
                disabled={effectiveDisabled || totalModified === 0}
                onClick={resetAll}
                title="将全部训练参数恢复为 Ultralytics 官方默认值"
              >
                <RotateCcw className="size-3.5" />
                全部恢复默认
              </Button>
            )}
          </div>
        </div>

        {/* 第 2 行：去边框轻量模板条 */}
        {headerSlot && (
          <div className="border-t border-border/50 pt-3">{headerSlot}</div>
        )}
      </div>

      {/* 主体内容区：搜索视图 / 全局已修改复核视图 / 默认分级视图 */}
      {isSearching ? (
        <div className="grid gap-4">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span className="tabular-nums">
              搜索 “{trimmedQuery}” 匹配 {searchResults.length} 项（含{" "}
              {searchResults.filter((s) => !s.common).length} 项高级参数）
            </span>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={() => setQuery("")}
            >
              清空搜索
            </Button>
          </div>

          {searchResults.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-10 text-center text-sm text-muted-foreground">
              <SlidersHorizontal className="size-5" />
              <p>没有匹配“{trimmedQuery}”的训练参数。</p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setQuery("")}
              >
                返回参数配置
              </Button>
            </div>
          ) : (
            renderSpecGrid(searchResults, {
              columns: 2,
              showTierContext: true,
              showTaskScope: true,
            })
          )}
        </div>
      ) : onlyModifiedGlobal ? (
        <div className="grid gap-4">
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-muted/40 px-3.5 py-2 text-xs">
            <div className="flex flex-wrap items-center gap-2 text-foreground">
              <Filter className="size-3.5 text-primary" />
              <span className="font-medium">已修改参数复核</span>
              <span className="text-muted-foreground tabular-nums">
                常用 {modifiedCommonItems.length} 项 · 高级{" "}
                {modifiedAdvancedItems.length} 项
              </span>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={() => setOnlyModifiedGlobal(false)}
            >
              返回完整配置
            </Button>
          </div>

          {totalModified === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-10 text-center text-sm text-muted-foreground">
              <SlidersHorizontal className="size-5" />
              <p>当前全部参数均与 Ultralytics 官方默认值一致。</p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setOnlyModifiedGlobal(false)}
              >
                返回完整配置
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border/60">
              {modifiedCommonItems.length > 0 && (
                <section className="grid gap-3 pb-5 first:pt-0">
                  <div className="flex items-center justify-between gap-2">
                    <h4 className="text-xs font-medium tracking-wide text-muted-foreground">
                      常用参数修改（{modifiedCommonItems.length}）
                    </h4>
                    {!readOnly && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        className="text-muted-foreground hover:text-foreground"
                        disabled={effectiveDisabled}
                        onClick={() => resetSpecs(commonSpecs)}
                      >
                        <RotateCcw />
                        恢复常用默认
                      </Button>
                    )}
                  </div>
                  {renderSpecGrid(
                    modifiedCommonItems.map((item) => item.spec),
                    { columns: 2 }
                  )}
                </section>
              )}

              {modifiedAdvancedItems.length > 0 && (
                <section className="grid gap-3 pt-5 first:pt-0">
                  <div className="flex items-center justify-between gap-2">
                    <h4 className="text-xs font-medium tracking-wide text-muted-foreground">
                      高级选项修改（{modifiedAdvancedItems.length}）
                    </h4>
                    {!readOnly && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        className="text-muted-foreground hover:text-foreground"
                        disabled={effectiveDisabled}
                        onClick={() => resetSpecs(advancedSpecs)}
                      >
                        <RotateCcw />
                        恢复高级默认
                      </Button>
                    )}
                  </div>
                  {renderSpecGrid(
                    modifiedAdvancedItems.map((item) => item.spec),
                    {
                      columns: 2,
                      showTierContext: true,
                      showTaskScope: true,
                    }
                  )}
                </section>
              )}
            </div>
          )}
        </div>
      ) : (
        <div className="grid min-w-0 gap-5">
          {/* 一、常用参数（按语义分块、去卡片化等高栅格） */}
          <section className="grid min-w-0 gap-1" aria-label="常用训练参数">
            <div className="flex flex-wrap items-center justify-between gap-2 pb-1">
              <div className="flex items-center gap-2">
                <h4 className="text-sm font-semibold">常用参数</h4>
                <span className="text-xs text-muted-foreground tabular-nums">
                  {commonSpecs.length} 项
                </span>
              </div>

              {!readOnly && commonModified > 0 && (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  className="text-muted-foreground hover:text-foreground"
                  disabled={effectiveDisabled}
                  onClick={() => resetSpecs(commonSpecs)}
                >
                  <RotateCcw />
                  恢复常用默认 ({commonModified})
                </Button>
              )}
            </div>

            <div className="divide-y divide-border/60">
              {commonSubgroups.map((subgroup) => (
                <div
                  key={subgroup.id}
                  className="grid min-w-0 gap-3 py-4 first:pt-2 last:pb-1"
                >
                  <h5 className="text-xs font-medium tracking-wide text-muted-foreground">
                    {subgroup.label}
                  </h5>
                  {renderSpecGrid(subgroup.specs, {
                    columns: subgroup.wide ? 2 : 3,
                  })}
                </div>
              ))}
            </div>
          </section>

          {/* 二、高级选项（默认折叠，展开后平铺分块，无嵌套卡片） */}
          <section
            className={cn(
              "overflow-hidden rounded-lg border transition-colors",
              modifiedAdvancedItems.length > 0 && !advancedOpen
                ? "border-primary/30 bg-primary/[0.015]"
                : "border-border/80 bg-muted/15"
            )}
            aria-label="高级训练选项"
          >
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <button
                type="button"
                aria-expanded={advancedOpen}
                onClick={() => setAdvancedOpen((prev) => !prev)}
                className="flex min-w-0 flex-1 items-center gap-2.5 text-left"
              >
                <SlidersHorizontal className="size-4 shrink-0 text-muted-foreground" />
                <span className="text-sm font-semibold">高级选项</span>
                <span className="text-xs text-muted-foreground tabular-nums">
                  {advancedSpecs.length} 项
                </span>
                {modifiedAdvancedItems.length > 0 && (
                  <Badge
                    variant="secondary"
                    className="h-5 px-1.5 text-[11px] font-medium text-primary tabular-nums"
                  >
                    已改 {modifiedAdvancedItems.length} 项
                  </Badge>
                )}
                <span className="hidden truncate text-xs text-muted-foreground lg:inline">
                  — 学习率衰减、损失权重、数据增强、验证与可视化等低频参数
                </span>
              </button>

              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 gap-1 px-2.5 text-xs"
                aria-expanded={advancedOpen}
                onClick={() => setAdvancedOpen((prev) => !prev)}
              >
                {advancedOpen ? (
                  <>
                    收起
                    <ChevronDown className="size-3.5 rotate-180 transition-transform" />
                  </>
                ) : (
                  <>
                    展开高级选项
                    <ChevronDown className="size-3.5 transition-transform" />
                  </>
                )}
              </Button>
            </div>

            {/* 收起态已修改高级参数摘要 */}
            {!advancedOpen && modifiedAdvancedItems.length > 0 && (
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/60 bg-background/70 px-4 py-2">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-xs text-muted-foreground">
                    已自定义高级项：
                  </span>
                  {modifiedAdvancedItems
                    .slice(0, MAX_COLLAPSED_CHIPS)
                    .map(({ spec, value }) => (
                      <button
                        key={spec.key}
                        type="button"
                        onClick={() =>
                          jumpToAdvancedGroup(
                            spec.group,
                            Boolean(spec.task_scope)
                          )
                        }
                        title={`${cleanParamLabel(spec.label, spec.key)}（官方默认：${formatParamValueText(spec.default)}），点击定位`}
                        className="inline-flex items-center gap-1 rounded border border-primary/25 bg-background px-1.5 py-0.5 font-mono text-[11px] text-foreground transition-colors hover:border-primary/50 hover:bg-primary/5"
                      >
                        <span className="text-muted-foreground">
                          {spec.key}=
                        </span>
                        <span className="font-semibold text-primary">
                          {formatCompactParamValue(value)}
                        </span>
                      </button>
                    ))}
                  {hiddenAdvancedChipCount > 0 && (
                    <span className="text-[11px] text-muted-foreground tabular-nums">
                      +{hiddenAdvancedChipCount} 项
                    </span>
                  )}
                </div>

                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  className="text-primary hover:text-primary"
                  onClick={() => setOnlyModifiedGlobal(true)}
                >
                  查看全部修改
                </Button>
              </div>
            )}

            {/* 展开态高级参数内容 */}
            {advancedOpen && (
              <div className="grid min-w-0 gap-4 border-t border-border/70 bg-background px-4 py-4">
                <div className="flex flex-wrap items-center justify-between gap-2.5 border-b border-border/60 pb-3">
                  <Tabs
                    value={activeTab}
                    onValueChange={(next) =>
                      setActiveTab(next as ParamGroupKey)
                    }
                    className="max-w-full min-w-0 [&>div]:overflow-y-hidden"
                  >
                    <ScrollingTabsList value={activeTab}>
                      {schema.groups.map((group) => {
                        const groupCount = advancedSpecs.filter(
                          (s) => s.group === group.key
                        ).length
                        const modCount = countModifiedParams(
                          advancedSpecs,
                          values,
                          group.key,
                          effectiveInitMode
                        )
                        return (
                          <TabsTrigger
                            key={group.key}
                            value={group.key}
                            className="gap-1.5 text-xs"
                          >
                            <span>{group.label}</span>
                            <span className="text-[11px] text-muted-foreground tabular-nums">
                              {groupCount}
                            </span>
                            {modCount > 0 && (
                              <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary/15 px-1 text-[10px] font-semibold text-primary tabular-nums">
                                {modCount}
                              </span>
                            )}
                          </TabsTrigger>
                        )
                      })}
                    </ScrollingTabsList>
                  </Tabs>

                  <div className="flex shrink-0 flex-wrap items-center gap-2.5">
                    <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground select-none hover:text-foreground">
                      <Switch
                        size="sm"
                        checked={onlyModifiedAdvanced}
                        onCheckedChange={setOnlyModifiedAdvanced}
                      />
                      <span className="tabular-nums">
                        仅看已改 ({activeAdvancedGroupModified})
                      </span>
                    </label>

                    {!readOnly && activeGroupMeta && activeAdvancedGroupModified > 0 && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        className="text-muted-foreground hover:text-foreground"
                        disabled={effectiveDisabled}
                        onClick={() => resetAdvancedGroup(activeGroupMeta.key)}
                      >
                        <RotateCcw />
                        重置本组
                      </Button>
                    )}
                  </div>
                </div>

                {filteredAdvancedSpecs.length === 0 ? (
                  <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                    <SlidersHorizontal className="size-4" />
                    <p className="text-xs">
                      本分组下暂无修改过的参数（全部与官方默认值一致）。
                    </p>
                    {onlyModifiedAdvanced && (
                      <Button
                        type="button"
                        variant="outline"
                        size="xs"
                        onClick={() => setOnlyModifiedAdvanced(false)}
                      >
                        显示本组全部参数
                      </Button>
                    )}
                  </div>
                ) : (
                  <div className="grid min-w-0 gap-4">
                    {advancedSubgroups.length > 0 && (
                      <div className="divide-y divide-border/60">
                        {advancedSubgroups.map((subgroup) => (
                          <div
                            key={subgroup.id}
                            className="grid min-w-0 gap-3 py-4 first:pt-0 last:pb-1"
                          >
                            <h5 className="text-xs font-medium tracking-wide text-muted-foreground">
                              {subgroup.label}
                            </h5>
                            {renderSpecGrid(subgroup.specs, {
                              columns: 3,
                              showTaskScope: subgroup.id.endsWith("_active_task"),
                            })}
                          </div>
                        ))}
                      </div>
                    )}

                    {/* 非当前任务的专用参数折叠子区 */}
                    {otherTaskSpecs.length > 0 && (
                      <div className="rounded-lg border border-dashed border-border/80 bg-muted/15">
                        <button
                          type="button"
                          aria-expanded={isOtherTaskOpen}
                          onClick={() =>
                            setOtherTaskOpenByGroup((prev) => ({
                              ...prev,
                              [activeTab]: !isOtherTaskOpen,
                            }))
                          }
                          className="flex w-full flex-wrap items-center justify-between gap-2 px-3.5 py-2.5 text-left text-xs transition-colors hover:bg-muted/30"
                        >
                          <span className="flex flex-wrap items-center gap-2">
                            {isOtherTaskOpen ? (
                              <ChevronDown className="size-3.5 text-muted-foreground" />
                            ) : (
                              <ChevronRight className="size-3.5 text-muted-foreground" />
                            )}
                            <span className="font-medium text-foreground/85">
                              其他任务专用参数
                            </span>
                            {otherTaskScopeSummary && (
                              <span className="text-muted-foreground">
                                {otherTaskScopeSummary}
                              </span>
                            )}
                            <span className="text-muted-foreground tabular-nums">
                              · {otherTaskSpecs.length} 项
                            </span>
                            {otherTaskModifiedCount > 0 && (
                              <Badge
                                variant="secondary"
                                className="h-4 px-1.5 text-[10px] font-medium text-primary tabular-nums"
                              >
                                已改 {otherTaskModifiedCount} 项
                              </Badge>
                            )}
                          </span>
                          <span className="text-[11px] text-muted-foreground">
                            {isOtherTaskOpen ? "收起" : "展开"}
                          </span>
                        </button>

                        {isOtherTaskOpen && (
                          <div className="border-t border-border/60 bg-background/80 px-3.5 py-3.5">
                            {renderSpecGrid(otherTaskSpecs, {
                              columns: 3,
                              showTaskScope: true,
                            })}
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </section>
        </div>
      )}

      {/* YAML 预览弹窗 */}
      <Dialog open={yamlOpen} onOpenChange={setYamlOpen}>
        <DialogContent className="max-h-[85vh] sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Ultralytics 训练参数预览（args.yaml）</DialogTitle>
            <DialogDescription>
              共 {schema.specs.length} 项训练参数，其中 {totalModified}{" "}
              项相对官方默认值做了调整。
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[55vh] overflow-y-auto">
            <CommandBlock command={formatParamsYaml(schema.specs, values)} />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setYamlOpen(false)}
            >
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ParamSpecGroupGrid({
  specs,
  values,
  columns,
  showTierContext = false,
  showTaskScope = false,
  groupLabelByKey,
  effectiveInitMode,
  isSpecLocked,
  specLockedHint,
  onChange,
  onReset,
}: {
  specs: readonly ParamSpec[]
  values: Record<string, unknown>
  columns: 2 | 3
  showTierContext?: boolean
  showTaskScope?: boolean
  groupLabelByKey: ReadonlyMap<ParamGroupKey, string>
  effectiveInitMode: InitMode | undefined
  isSpecLocked: (spec: ParamSpec) => boolean
  specLockedHint: (spec: ParamSpec) => string | undefined
  onChange: (key: string, nextValue: unknown) => void
  onReset: (spec: ParamSpec) => void
}) {
  const { fields, toggles } = useMemo(
    () => splitSpecsByControlKind(specs),
    [specs]
  )

  return (
    <div className="grid min-w-0 gap-3.5">
      {fields.length > 0 && (
        <div
          className={cn(
            "grid min-w-0 grid-cols-1 gap-x-5 gap-y-3.5",
            columns === 2
              ? "md:grid-cols-2"
              : "md:grid-cols-2 xl:grid-cols-3"
          )}
        >
          {fields.map((spec) => (
            <ParamField
              key={spec.key}
              spec={spec}
              value={spec.key in values ? values[spec.key] : spec.default}
              allValues={values}
              modified={isParamModifiedForMode(spec, values, effectiveInitMode)}
              disabled={isSpecLocked(spec)}
              lockedHint={specLockedHint(spec)}
              contextLabel={
                showTierContext
                  ? spec.common
                    ? "常用"
                    : (groupLabelByKey.get(spec.group) ?? spec.group)
                  : undefined
              }
              showTaskScope={showTaskScope}
              onChange={(nextVal) => onChange(spec.key, nextVal)}
              onReset={() => onReset(spec)}
            />
          ))}
        </div>
      )}

      {toggles.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-6 gap-y-2.5 rounded-lg bg-muted/35 px-3.5 py-2.5">
          {toggles.map((spec) => (
            <ParamToggleItem
              key={spec.key}
              spec={spec}
              value={spec.key in values ? values[spec.key] : spec.default}
              modified={isParamModifiedForMode(spec, values, effectiveInitMode)}
              disabled={isSpecLocked(spec)}
              lockedHint={specLockedHint(spec)}
              contextLabel={
                showTierContext
                  ? spec.common
                    ? "常用"
                    : (groupLabelByKey.get(spec.group) ?? spec.group)
                  : undefined
              }
              showTaskScope={showTaskScope}
              onChange={(nextVal) => onChange(spec.key, nextVal)}
              onReset={() => onReset(spec)}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function ParamHelpTooltip({
  spec,
  cleanLabel,
  lockedHint,
  autoHintDetail,
}: {
  spec: ParamSpec
  cleanLabel: string
  lockedHint?: string
  autoHintDetail?: string
}) {
  const scopeLabel = spec.task_scope
    ? (TASK_SCOPE_LABELS[spec.task_scope] ?? spec.task_scope)
    : null

  return (
    <Tooltip delayDuration={150}>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="inline-flex size-4 shrink-0 items-center justify-center rounded-full text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          aria-label={`${cleanLabel}（${spec.key}）说明`}
        >
          <HelpCircle className="size-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent
        side="top"
        align="start"
        sideOffset={6}
        className="max-w-72 space-y-1.5 px-3 py-2 text-left leading-relaxed font-normal"
      >
        <p>{spec.description}</p>
        {autoHintDetail && (
          <p className="text-amber-300 dark:text-amber-700">{autoHintDetail}</p>
        )}
        {lockedHint && <p className="opacity-85">注：{lockedHint}</p>}
        <div className="flex flex-wrap gap-x-3 gap-y-0.5 border-t border-background/15 pt-1.5 text-[11px] opacity-80 tabular-nums">
          <span>官方默认：{formatParamValueText(spec.default)}</span>
          {spec.min !== undefined && spec.max !== undefined && (
            <span>
              范围：{spec.min} – {spec.max}
            </span>
          )}
          {scopeLabel && <span>适用：{scopeLabel}</span>}
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

function ParamField({
  spec,
  value,
  allValues,
  modified,
  disabled,
  lockedHint,
  contextLabel,
  showTaskScope,
  onChange,
  onReset,
}: {
  spec: ParamSpec
  value: unknown
  allValues: Record<string, unknown>
  modified: boolean
  disabled: boolean
  lockedHint?: string
  contextLabel?: string
  showTaskScope?: boolean
  onChange: (next: unknown) => void
  onReset: () => void
}) {
  const inputId = useId()
  const cleanLabel = cleanParamLabel(spec.label, spec.key)
  const scopeLabel =
    showTaskScope && spec.task_scope
      ? (TASK_SCOPE_LABELS[spec.task_scope] ?? spec.task_scope)
      : null
  const autoHint = getAutoOptimizerHint(spec.key, allValues)

  return (
    <div className="group/field grid min-w-0 content-start gap-1.5">
      <div className="flex min-h-5 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1.5">
          {modified && (
            <span
              className="size-1.5 shrink-0 rounded-full bg-primary"
              title={`已修改（官方默认：${formatParamValueText(spec.default)}）`}
              aria-hidden
            />
          )}
          <label
            htmlFor={inputId}
            className={cn(
              "truncate text-xs font-medium",
              modified ? "text-foreground" : "text-foreground/90"
            )}
          >
            {cleanLabel}
          </label>
          <code className="shrink-0 font-mono text-[11px] text-muted-foreground/80">
            {spec.key}
          </code>

          {autoHint && (
            <span className="shrink-0 rounded bg-amber-500/12 px-1.5 py-0.5 text-[10px] leading-none font-medium text-amber-700 dark:text-amber-400">
              {autoHint.badge}
            </span>
          )}
          {scopeLabel && (
            <Badge
              variant="outline"
              className="h-4 shrink-0 px-1.5 text-[10px] font-normal"
            >
              {scopeLabel}
            </Badge>
          )}
          {contextLabel && (
            <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] leading-none text-muted-foreground">
              {contextLabel}
            </span>
          )}

          <ParamHelpTooltip
            spec={spec}
            cleanLabel={cleanLabel}
            lockedHint={lockedHint}
            autoHintDetail={autoHint?.detail}
          />
        </div>

        {modified && !disabled && (
          <Tooltip delayDuration={150}>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={onReset}
                className="inline-flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground/75 transition-colors hover:bg-muted hover:text-foreground"
                aria-label={`恢复 ${cleanLabel} 默认值`}
              >
                <RotateCcw className="size-3" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="top" sideOffset={4}>
              恢复默认：{formatParamValueText(spec.default)}
            </TooltipContent>
          </Tooltip>
        )}
      </div>

      <ParamInputControl
        id={inputId}
        spec={spec}
        value={value}
        modified={modified}
        disabled={disabled}
        onChange={onChange}
      />
    </div>
  )
}

function ParamToggleItem({
  spec,
  value,
  modified,
  disabled,
  lockedHint,
  contextLabel,
  showTaskScope,
  onChange,
  onReset,
}: {
  spec: ParamSpec
  value: unknown
  modified: boolean
  disabled: boolean
  lockedHint?: string
  contextLabel?: string
  showTaskScope?: boolean
  onChange: (next: unknown) => void
  onReset: () => void
}) {
  const inputId = useId()
  const checked = Boolean(value)
  const cleanLabel = cleanParamLabel(spec.label, spec.key)
  const scopeLabel =
    showTaskScope && spec.task_scope
      ? (TASK_SCOPE_LABELS[spec.task_scope] ?? spec.task_scope)
      : null

  return (
    <div className="flex items-center gap-2">
      <Switch
        id={inputId}
        size="sm"
        checked={checked}
        disabled={disabled}
        onCheckedChange={onChange}
      />
      <div className="flex items-center gap-1.5">
        {modified && (
          <span
            className="size-1.5 shrink-0 rounded-full bg-primary"
            title={`已修改（官方默认：${formatParamValueText(spec.default)}）`}
            aria-hidden
          />
        )}
        <label
          htmlFor={inputId}
          className={cn(
            "text-xs font-medium select-none",
            disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer",
            modified ? "text-foreground" : "text-foreground/90"
          )}
        >
          {cleanLabel}
        </label>
        <code className="font-mono text-[11px] text-muted-foreground/80">
          {spec.key}
        </code>
        {scopeLabel && (
          <Badge
            variant="outline"
            className="h-4 px-1.5 text-[10px] font-normal"
          >
            {scopeLabel}
          </Badge>
        )}
        {contextLabel && (
          <span className="rounded bg-background/80 px-1.5 py-0.5 text-[10px] leading-none text-muted-foreground">
            {contextLabel}
          </span>
        )}
        <ParamHelpTooltip
          spec={spec}
          cleanLabel={cleanLabel}
          lockedHint={lockedHint}
        />
        {modified && !disabled && (
          <Tooltip delayDuration={150}>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={onReset}
                className="inline-flex size-5 items-center justify-center rounded text-muted-foreground/75 transition-colors hover:bg-background hover:text-foreground"
                aria-label={`恢复 ${cleanLabel} 默认值`}
              >
                <RotateCcw className="size-3" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="top" sideOffset={4}>
              恢复默认：{formatParamValueText(spec.default)}
            </TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  )
}

function ParamInputControl({
  id,
  spec,
  value,
  modified,
  disabled,
  onChange,
}: {
  id: string
  spec: ParamSpec
  value: unknown
  modified: boolean
  disabled: boolean
  onChange: (next: unknown) => void
}) {
  const modifiedRing = modified
    ? "border-primary/45 bg-primary/[0.02]"
    : undefined

  switch (spec.type) {
    case "bool": {
      const checked = Boolean(value)
      return (
        <div
          className={cn(
            "flex h-9 items-center justify-between rounded-md border bg-background px-3",
            modifiedRing
          )}
        >
          <span className="text-xs text-muted-foreground">
            {checked ? "已开启" : "已关闭"}
          </span>
          <Switch
            id={id}
            size="sm"
            checked={checked}
            disabled={disabled}
            onCheckedChange={onChange}
          />
        </div>
      )
    }

    case "nullable_bool":
    case "enum":
    case "cache":
    case "compile":
    case "quantize": {
      const options =
        spec.options && spec.options.length > 0
          ? spec.options
          : [
              { value: null, label: "null（自动）" },
              { value: true, label: "true（开启）" },
              { value: false, label: "false（关闭）" },
            ]
      const encoded = JSON.stringify(value ?? null)
      return (
        <Select
          value={encoded}
          disabled={disabled}
          onValueChange={(raw) => {
            try {
              onChange(JSON.parse(raw))
            } catch {
              onChange(raw)
            }
          }}
        >
          <SelectTrigger id={id} className={cn("h-9 w-full", modifiedRing)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((opt) => {
              const optVal = JSON.stringify(opt.value ?? null)
              return (
                <SelectItem key={optVal} value={optVal}>
                  {opt.label}
                </SelectItem>
              )
            })}
          </SelectContent>
        </Select>
      )
    }

    case "int":
    case "float":
      return (
        <NumberParamInput
          id={id}
          value={typeof value === "number" ? value : Number(spec.default ?? 0)}
          integer={spec.type === "int"}
          nullable={false}
          min={spec.min}
          max={spec.max}
          step={spec.step}
          disabled={disabled}
          className={modifiedRing}
          onChange={(next) => {
            if (next !== null) {
              onChange(next)
            }
          }}
        />
      )

    case "nullable_int":
    case "nullable_float":
      return (
        <NumberParamInput
          id={id}
          value={typeof value === "number" ? value : null}
          integer={spec.type === "nullable_int"}
          nullable
          min={spec.min}
          max={spec.max}
          step={spec.step}
          disabled={disabled}
          placeholder="自动 / 不限制"
          className={modifiedRing}
          onChange={onChange}
        />
      )

    case "string":
      return (
        <Input
          id={id}
          value={value == null ? "" : String(value)}
          disabled={disabled}
          className={cn("h-9", modifiedRing)}
          placeholder={
            spec.key === "device" ? "自动选择 GPU，或填 0 / 0,1 / cpu" : "默认"
          }
          onChange={(event) => {
            const raw = event.target.value
            if (raw.trim() === "" && spec.default === null) {
              onChange(null)
            } else {
              onChange(raw)
            }
          }}
        />
      )

    case "imgsz":
      return (
        <ImgszInput
          id={id}
          value={value}
          modified={modified}
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "batch":
      return (
        <BatchInput
          id={id}
          value={value}
          modified={modified}
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "freeze":
      return (
        <FreezeInput
          id={id}
          value={value}
          modified={modified}
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "int_list":
      return (
        <IntListInput
          id={id}
          value={value}
          modified={modified}
          disabled={disabled}
          onChange={onChange}
        />
      )
  }
}

function NumberParamInput({
  id,
  value,
  integer,
  nullable,
  min,
  max,
  step,
  placeholder,
  disabled,
  bare = false,
  className,
  onChange,
}: {
  id: string
  value: number | null
  integer: boolean
  nullable: boolean
  min?: number
  max?: number
  step?: number
  placeholder?: string
  disabled: boolean
  bare?: boolean
  className?: string
  onChange: (next: number | null) => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const display = draft !== null ? draft : value === null ? "" : String(value)

  return (
    <Input
      id={id}
      type="number"
      inputMode={integer ? "numeric" : "decimal"}
      min={min}
      max={max}
      step={step ?? (integer ? 1 : "any")}
      value={display}
      placeholder={placeholder}
      disabled={disabled}
      className={cn(
        "tabular-nums",
        bare
          ? "h-full min-w-0 flex-1 rounded-none border-0 bg-transparent px-2.5 shadow-none focus-visible:ring-0"
          : "h-9",
        className
      )}
      onChange={(event) => {
        const raw = event.target.value
        setDraft(raw)
        if (raw.trim() === "") {
          if (nullable) {
            onChange(null)
          }
          return
        }
        const num = Number(raw)
        if (Number.isFinite(num)) {
          onChange(integer ? Math.trunc(num) : num)
        }
      }}
      onBlur={() => setDraft(null)}
    />
  )
}

function ImgszInput({
  id,
  value,
  modified,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  modified: boolean
  disabled: boolean
  onChange: (next: number | [number, number]) => void
}) {
  const isRect = Array.isArray(value) && value.length === 2
  const squareSize =
    typeof value === "number" && Number.isFinite(value) ? value : 640
  const rectH = isRect && typeof value[0] === "number" ? value[0] : squareSize
  const rectW = isRect && typeof value[1] === "number" ? value[1] : squareSize

  return (
    <div
      className={cn(
        "flex h-9 w-full items-center overflow-hidden rounded-md border border-input bg-background shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50",
        modified && "border-primary/45 bg-primary/[0.02]",
        disabled && "opacity-60"
      )}
    >
      <div
        role="group"
        aria-label="图像尺寸模式"
        className="flex h-full shrink-0 items-center border-r border-input bg-muted/40 p-0.5 text-[11px]"
      >
        <button
          type="button"
          disabled={disabled}
          aria-pressed={!isRect}
          onClick={() => {
            if (isRect) {
              onChange(rectH)
            }
          }}
          className={cn(
            "h-full rounded-[4px] px-2 transition-colors",
            !isRect
              ? "bg-background font-medium text-foreground shadow-xs"
              : "text-muted-foreground hover:text-foreground"
          )}
        >
          正方形
        </button>
        <button
          type="button"
          disabled={disabled}
          aria-pressed={isRect}
          onClick={() => {
            if (!isRect) {
              onChange([squareSize, squareSize])
            }
          }}
          className={cn(
            "h-full rounded-[4px] px-2 transition-colors",
            isRect
              ? "bg-background font-medium text-foreground shadow-xs"
              : "text-muted-foreground hover:text-foreground"
          )}
        >
          高×宽
        </button>
      </div>

      {isRect ? (
        <div className="flex h-full min-w-0 flex-1 items-center">
          <NumberParamInput
            id={id}
            value={rectH}
            integer
            nullable={false}
            min={32}
            max={4096}
            step={32}
            placeholder="高 H"
            disabled={disabled}
            bare
            onChange={(h) => {
              if (h !== null) {
                onChange([h, rectW])
              }
            }}
          />
          <span className="shrink-0 text-xs text-muted-foreground select-none">
            ×
          </span>
          <NumberParamInput
            id={`${id}-w`}
            value={rectW}
            integer
            nullable={false}
            min={32}
            max={4096}
            step={32}
            placeholder="宽 W"
            disabled={disabled}
            bare
            onChange={(w) => {
              if (w !== null) {
                onChange([rectH, w])
              }
            }}
          />
        </div>
      ) : (
        <>
          <NumberParamInput
            id={id}
            value={squareSize}
            integer
            nullable={false}
            min={32}
            max={4096}
            step={32}
            disabled={disabled}
            bare
            onChange={(size) => {
              if (size !== null) {
                onChange(size)
              }
            }}
          />
          {!disabled && (
            <div className="hidden h-full shrink-0 items-center gap-0.5 border-l border-input/70 bg-muted/20 px-1.5 sm:flex">
              {IMGSZ_QUICK_PRESETS.map((preset) => {
                const active = squareSize === preset
                return (
                  <button
                    key={preset}
                    type="button"
                    onClick={() => onChange(preset)}
                    className={cn(
                      "h-6 rounded px-1.5 text-[11px] tabular-nums transition-colors",
                      active
                        ? "bg-primary font-medium text-primary-foreground"
                        : "text-muted-foreground hover:bg-muted hover:text-foreground"
                    )}
                    title={`快速填入 ${preset}`}
                  >
                    {preset}
                  </button>
                )
              })}
            </div>
          )}
        </>
      )}
    </div>
  )
}

function BatchInput({
  id,
  value,
  modified,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  modified: boolean
  disabled: boolean
  onChange: (next: number) => void
}) {
  const numVal =
    typeof value === "number" && Number.isFinite(value) ? value : 16
  const mode =
    numVal === -1 ? "auto" : numVal > 0 && numVal < 1 ? "fraction" : "fixed"

  return (
    <div
      className={cn(
        "flex h-9 w-full items-center overflow-hidden rounded-md border border-input bg-background shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50",
        modified && "border-primary/45 bg-primary/[0.02]",
        disabled && "opacity-60"
      )}
    >
      <Select
        value={mode}
        disabled={disabled}
        onValueChange={(nextMode) => {
          if (nextMode === "auto") {
            onChange(-1)
          } else if (nextMode === "fraction") {
            onChange(0.6)
          } else {
            onChange(16)
          }
        }}
      >
        <SelectTrigger
          className="h-full w-auto min-w-[96px] shrink-0 rounded-none border-0 border-r border-input bg-muted/40 px-2.5 text-xs shadow-none focus-visible:ring-0"
          aria-label="批次大小计算模式"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="fixed">固定批次</SelectItem>
          <SelectItem value="auto">自动 (-1)</SelectItem>
          <SelectItem value="fraction">显存占比</SelectItem>
        </SelectContent>
      </Select>

      {mode === "fixed" && (
        <NumberParamInput
          id={id}
          value={Math.max(1, Math.trunc(numVal))}
          integer
          nullable={false}
          min={1}
          max={4096}
          step={1}
          disabled={disabled}
          bare
          onChange={(next) => {
            if (next !== null) {
              onChange(next)
            }
          }}
        />
      )}

      {mode === "fraction" && (
        <div className="flex h-full min-w-0 flex-1 items-center">
          <NumberParamInput
            id={id}
            value={numVal}
            integer={false}
            nullable={false}
            min={0.05}
            max={0.99}
            step={0.05}
            disabled={disabled}
            bare
            onChange={(next) => {
              if (next !== null) {
                onChange(next)
              }
            }}
          />
          <span className="shrink-0 pr-2.5 text-[11px] text-muted-foreground tabular-nums">
            0.05–0.99
          </span>
        </div>
      )}

      {mode === "auto" && (
        <span className="truncate px-3 text-xs text-muted-foreground">
          按 60% 显存自动计算
        </span>
      )}
    </div>
  )
}

function FreezeInput({
  id,
  value,
  modified,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  modified: boolean
  disabled: boolean
  onChange: (next: number | number[] | null) => void
}) {
  const formatted =
    value == null ? "" : Array.isArray(value) ? value.join(", ") : String(value)
  const [draft, setDraft] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const display = draft !== null ? draft : formatted

  return (
    <div className="grid gap-1">
      <Input
        id={id}
        value={display}
        disabled={disabled}
        className={cn("h-9", modified && "border-primary/45 bg-primary/[0.02]")}
        placeholder="留空不冻结；填 10 或 0,1,2"
        aria-invalid={error ? true : undefined}
        onChange={(event) => {
          const raw = event.target.value
          setDraft(raw)
          const parsed = parseFreezeText(raw)
          setError(parsed.error)
          if (!parsed.error) {
            onChange(parsed.value)
          }
        }}
        onBlur={() => {
          if (!error) {
            setDraft(null)
          }
        }}
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

function IntListInput({
  id,
  value,
  modified,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  modified: boolean
  disabled: boolean
  onChange: (next: number[] | null) => void
}) {
  const formatted = Array.isArray(value) ? value.join(", ") : ""
  const [draft, setDraft] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const display = draft !== null ? draft : formatted

  return (
    <div className="grid gap-1">
      <Input
        id={id}
        value={display}
        disabled={disabled}
        className={cn("h-9", modified && "border-primary/45 bg-primary/[0.02]")}
        placeholder="留空全部类别；或填 0, 2, 5"
        aria-invalid={error ? true : undefined}
        onChange={(event) => {
          const raw = event.target.value
          setDraft(raw)
          const parsed = parseIntListText(raw)
          setError(parsed.error)
          if (!parsed.error) {
            onChange(parsed.value)
          }
        }}
        onBlur={() => {
          if (!error) {
            setDraft(null)
          }
        }}
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}
