/**
 * Ultralytics YOLO 分级训练参数编辑器。
 *
 * 按服务端 `/api/v1/training/params-schema` 下发的 86 项官方参数规格渲染：
 * - 12 项高频常用参数（epochs、batch、imgsz、patience、optimizer、lr0、cos_lr、
 *   amp、device、workers、cache、mosaic）直接展示；
 * - 其余 74 项低频参数收起在页内“高级选项”折叠面板中，展开后保留原有 4 分组标签、
 *   搜索、仅看已修改与分组重置；收起状态下若高级参数偏离默认，直接展示已修改项摘要。
 */
import { useId, useMemo, useState, type ReactNode } from "react"
import {
  ChevronDown,
  ChevronUp,
  FileCode2,
  RotateCcw,
  Search,
  SlidersHorizontal,
  Sparkles,
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
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
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
  countModifiedParams,
  filterParamSpecs,
  formatCompactParamValue,
  formatParamsYaml,
  formatParamValueText,
  getModeDefaultValue,
  isParamModifiedForMode,
  listModifiedParams,
  parseFreezeText,
  parseIntListText,
  splitSpecsByTier,
} from "@/features/training/training-params"
import { cn } from "@/lib/utils"

const MAX_COLLAPSED_CHIPS = 8

export type TrainingParamsEditorProps = {
  schema: ParamsSchemaResponse
  values: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  initMode?: InitMode
  /** job：用于新建训练任务；template：用于创建/编辑参数模板（resume 固定托管）。 */
  mode?: "job" | "template"
  disabled?: boolean
  /** 顶部扩展槽（如新建训练页的参数模板快捷选择与保存栏）。 */
  headerSlot?: ReactNode
}

export function TrainingParamsEditor({
  schema,
  values,
  onChange,
  initMode = "scratch",
  mode = "job",
  disabled = false,
  headerSlot,
}: TrainingParamsEditorProps) {
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [activeTab, setActiveTab] = useState<ParamGroupKey | "all">("train")
  const [query, setQuery] = useState("")
  const [onlyModifiedAdvanced, setOnlyModifiedAdvanced] = useState(false)
  const [yamlOpen, setYamlOpen] = useState(false)

  const effectiveInitMode: InitMode | undefined =
    mode === "job" ? initMode : undefined

  const { common: commonSpecs, advanced: advancedSpecs } = useMemo(
    () => splitSpecsByTier(schema.specs),
    [schema.specs]
  )

  const groupLabelByKey = useMemo(
    () => new Map(schema.groups.map((g) => [g.key, g.label])),
    [schema.groups]
  )

  const totalModified = useMemo(
    () => countModifiedParams(schema.specs, values, undefined, effectiveInitMode),
    [schema.specs, values, effectiveInitMode]
  )
  const commonModified = useMemo(
    () => countModifiedParams(commonSpecs, values, undefined, effectiveInitMode),
    [commonSpecs, values, effectiveInitMode]
  )
  const modifiedAdvancedItems = useMemo(
    () => listModifiedParams(advancedSpecs, values, effectiveInitMode),
    [advancedSpecs, values, effectiveInitMode]
  )

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
    // 常用参数优先排在搜索结果前面，便于快速定位
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

  const activeGroupMeta = schema.groups.find((g) => g.key === activeTab)
  const activeAdvancedGroupModified =
    activeTab === "all"
      ? modifiedAdvancedItems.length
      : countModifiedParams(
          advancedSpecs,
          values,
          activeTab,
          effectiveInitMode
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
  }

  const openAdvancedModifiedView = () => {
    setAdvancedOpen(true)
    setActiveTab("all")
    setOnlyModifiedAdvanced(true)
  }

  const isSpecLocked = (spec: ParamSpec) =>
    disabled ||
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

  return (
    <div className="grid min-w-0 gap-5">
      {/* 标题与全局操作栏 */}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-base font-semibold">
              {mode === "template"
                ? "YOLO 模板参数配置"
                : "Ultralytics YOLO 训练参数"}
            </h3>
            <Badge variant="outline" className="font-normal tabular-nums">
              常用 {commonSpecs.length} 项 · 高级 {advancedSpecs.length} 项
            </Badge>
            {totalModified > 0 && (
              <Badge variant="secondary" className="font-normal tabular-nums">
                已修改 {totalModified} 项
              </Badge>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            直接展示 {commonSpecs.length} 项高频调参项，其余{" "}
            {advancedSpecs.length} 项低频参数默认收纳在下方“高级选项”中；所有参数均以
            Ultralytics 官方默认值（cfg/default.yaml）为基准。
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setYamlOpen(true)}
          >
            <FileCode2 />
            查看 YAML
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled || totalModified === 0}
            onClick={resetAll}
          >
            <RotateCcw />
            全部恢复默认
          </Button>
        </div>
      </div>

      {/* 模板快捷栏等外部扩展槽 */}
      {headerSlot}

      {/* 跨层级全局搜索 */}
      <div className="relative min-w-0">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="搜索全部 86 项参数（含高级选项）：输入英文参数名或中文说明，如 epochs、lr0、fliplr、close_mosaic、freeze…"
          className="pr-9 pl-9"
          aria-label="搜索训练参数"
        />
        {query && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="absolute top-1/2 right-2 -translate-y-1/2 text-muted-foreground"
            onClick={() => setQuery("")}
            aria-label="清空搜索词"
          >
            <X />
          </Button>
        )}
      </div>

      {isSearching ? (
        <div className="grid gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span className="tabular-nums">
              匹配 “{trimmedQuery}” 共 {searchResults.length} 项参数（含{" "}
              {searchResults.filter((s) => !s.common).length} 项高级选项）
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
            <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed py-10 text-center text-sm text-muted-foreground">
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
            <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
              {searchResults.map((spec) => (
                <ParamControlCard
                  key={spec.key}
                  spec={spec}
                  value={spec.key in values ? values[spec.key] : spec.default}
                  modified={isParamModifiedForMode(
                    spec,
                    values,
                    effectiveInitMode
                  )}
                  disabled={isSpecLocked(spec)}
                  lockedHint={specLockedHint(spec)}
                  tierBadge={
                    spec.common
                      ? "常用"
                      : `高级 · ${groupLabelByKey.get(spec.group) ?? spec.group}`
                  }
                  onChange={(nextVal) => setOneParam(spec.key, nextVal)}
                  onReset={() => resetOneParam(spec)}
                />
              ))}
            </div>
          )}
        </div>
      ) : (
        <>
          {/* 一、常用参数区（直接展示） */}
          <section className="grid gap-3" aria-label="常用训练参数">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <Sparkles className="size-4 text-primary" />
                <h4 className="text-sm font-semibold">常用参数</h4>
                <Badge variant="outline" className="font-normal tabular-nums">
                  {commonSpecs.length} 项
                </Badge>
                {commonModified > 0 && (
                  <Badge
                    variant="secondary"
                    className="font-normal tabular-nums"
                  >
                    已修改 {commonModified} 项
                  </Badge>
                )}
              </div>

              {commonModified > 0 && (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  disabled={disabled}
                  onClick={() => resetSpecs(commonSpecs)}
                >
                  <RotateCcw />
                  恢复常用默认 ({commonModified})
                </Button>
              )}
            </div>

            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
              {commonSpecs.map((spec) => (
                <ParamControlCard
                  key={spec.key}
                  spec={spec}
                  value={spec.key in values ? values[spec.key] : spec.default}
                  modified={isParamModifiedForMode(
                    spec,
                    values,
                    effectiveInitMode
                  )}
                  disabled={isSpecLocked(spec)}
                  lockedHint={specLockedHint(spec)}
                  onChange={(nextVal) => setOneParam(spec.key, nextVal)}
                  onReset={() => resetOneParam(spec)}
                />
              ))}
            </div>
          </section>

          {/* 二、高级选项折叠面板（其余 74 项） */}
          <section
            className={cn(
              "overflow-hidden rounded-xl border transition-colors",
              modifiedAdvancedItems.length > 0 && !advancedOpen
                ? "border-primary/35 bg-primary/[0.015]"
                : "bg-muted/15"
            )}
            aria-label="高级训练选项"
          >
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
              <button
                type="button"
                aria-expanded={advancedOpen}
                onClick={() => setAdvancedOpen((prev) => !prev)}
                className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
              >
                <SlidersHorizontal className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                <div className="grid min-w-0 gap-0.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold">高级选项</span>
                    <Badge
                      variant="outline"
                      className="font-normal tabular-nums"
                    >
                      共 {advancedSpecs.length} 项
                    </Badge>
                    {modifiedAdvancedItems.length > 0 && (
                      <Badge
                        variant="secondary"
                        className="font-normal text-primary tabular-nums"
                      >
                        已自定义 {modifiedAdvancedItems.length} 项
                      </Badge>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    学习率衰减与损失函数权重、几何与颜色数据增强、网络层冻结、验证阈值及可视化等低频参数。
                  </p>
                </div>
              </button>

              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-expanded={advancedOpen}
                onClick={() => setAdvancedOpen((prev) => !prev)}
              >
                {advancedOpen ? (
                  <>
                    收起高级选项
                    <ChevronUp />
                  </>
                ) : (
                  <>
                    展开高级选项
                    <ChevronDown />
                  </>
                )}
              </Button>
            </div>

            {/* 收起态已修改高级参数摘要 */}
            {!advancedOpen && modifiedAdvancedItems.length > 0 && (
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-primary/15 bg-primary/[0.03] px-4 py-2.5">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-xs font-medium text-muted-foreground">
                    已自定义的高级参数：
                  </span>
                  {modifiedAdvancedItems
                    .slice(0, MAX_COLLAPSED_CHIPS)
                    .map(({ spec, value }) => (
                      <button
                        key={spec.key}
                        type="button"
                        onClick={openAdvancedModifiedView}
                        title={`${spec.label}（官方默认：${formatParamValueText(spec.default)}）`}
                        className="inline-flex items-center gap-1 rounded-md border border-primary/25 bg-background px-2 py-0.5 font-mono text-[11px] text-foreground transition-colors hover:border-primary/50 hover:bg-primary/5"
                      >
                        <span className="text-muted-foreground">{spec.key}=</span>
                        <span className="font-semibold text-primary">
                          {formatCompactParamValue(value)}
                        </span>
                      </button>
                    ))}
                  {hiddenAdvancedChipCount > 0 && (
                    <Badge
                      variant="outline"
                      className="h-5 px-1.5 text-[11px] font-normal tabular-nums"
                    >
                      +{hiddenAdvancedChipCount} 项
                    </Badge>
                  )}
                </div>

                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  onClick={openAdvancedModifiedView}
                >
                  展开仅看已修改
                </Button>
              </div>
            )}

            {/* 展开态高级参数内容 */}
            {advancedOpen && (
              <div className="grid min-w-0 gap-4 border-t bg-background p-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <Tabs
                    value={activeTab}
                    onValueChange={(next) =>
                      setActiveTab(next as ParamGroupKey | "all")
                    }
                    className="min-w-0 flex-1"
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
                            className="gap-1.5"
                          >
                            {group.label}
                            <span className="text-[11px] text-muted-foreground tabular-nums">
                              ({groupCount})
                            </span>
                            {modCount > 0 && (
                              <Badge
                                variant="secondary"
                                className="h-5 px-1.5 text-[11px] tabular-nums"
                              >
                                {modCount}
                              </Badge>
                            )}
                          </TabsTrigger>
                        )
                      })}
                      <TabsTrigger value="all" className="gap-1.5">
                        全部高级 ({advancedSpecs.length})
                      </TabsTrigger>
                    </ScrollingTabsList>
                  </Tabs>

                  <div className="flex shrink-0 flex-wrap items-center gap-3">
                    <label className="flex cursor-pointer items-center gap-2 text-xs select-none">
                      <Switch
                        checked={onlyModifiedAdvanced}
                        onCheckedChange={setOnlyModifiedAdvanced}
                      />
                      <span className="tabular-nums">
                        仅看已修改 ({modifiedAdvancedItems.length})
                      </span>
                    </label>

                    {modifiedAdvancedItems.length > 0 && (
                      <Button
                        type="button"
                        variant="outline"
                        size="xs"
                        disabled={disabled}
                        onClick={() => resetSpecs(advancedSpecs)}
                      >
                        <RotateCcw />
                        重置全部高级
                      </Button>
                    )}
                  </div>
                </div>

                {activeGroupMeta && (
                  <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3.5 py-2 text-xs text-muted-foreground">
                    <span>{activeGroupMeta.description}</span>
                    {activeAdvancedGroupModified > 0 && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        disabled={disabled}
                        onClick={() => resetAdvancedGroup(activeGroupMeta.key)}
                      >
                        <RotateCcw />
                        恢复本组默认 ({activeAdvancedGroupModified})
                      </Button>
                    )}
                  </div>
                )}

                {filteredAdvancedSpecs.length === 0 ? (
                  <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed py-10 text-center text-sm text-muted-foreground">
                    <SlidersHorizontal className="size-5" />
                    <p>
                      当前高级分组中没有修改过的参数（全部与官方默认值一致）。
                    </p>
                    {onlyModifiedAdvanced && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => setOnlyModifiedAdvanced(false)}
                      >
                        显示本组全部高级参数
                      </Button>
                    )}
                  </div>
                ) : (
                  <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
                    {filteredAdvancedSpecs.map((spec) => (
                      <ParamControlCard
                        key={spec.key}
                        spec={spec}
                        value={
                          spec.key in values ? values[spec.key] : spec.default
                        }
                        modified={isParamModifiedForMode(
                          spec,
                          values,
                          effectiveInitMode
                        )}
                        disabled={isSpecLocked(spec)}
                        lockedHint={specLockedHint(spec)}
                        onChange={(nextVal) => setOneParam(spec.key, nextVal)}
                        onReset={() => resetOneParam(spec)}
                      />
                    ))}
                  </div>
                )}
              </div>
            )}
          </section>
        </>
      )}

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

function ParamControlCard({
  spec,
  value,
  modified,
  disabled,
  lockedHint,
  tierBadge,
  onChange,
  onReset,
}: {
  spec: ParamSpec
  value: unknown
  modified: boolean
  disabled: boolean
  lockedHint?: string
  tierBadge?: string
  onChange: (next: unknown) => void
  onReset: () => void
}) {
  const inputId = useId()
  const scopeLabel = spec.task_scope
    ? (TASK_SCOPE_LABELS[spec.task_scope] ?? spec.task_scope)
    : null

  return (
    <div
      className={cn(
        "grid min-w-0 content-between gap-2.5 rounded-xl border bg-card p-3.5 transition-colors",
        modified && "border-primary/40 bg-primary/[0.02]"
      )}
    >
      <div className="grid gap-1.5">
        <div className="flex items-start justify-between gap-2">
          <label
            htmlFor={inputId}
            className="flex flex-wrap items-center gap-1.5 text-sm leading-snug font-medium"
          >
            <span>{spec.label}</span>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] font-normal text-muted-foreground">
              {spec.key}
            </code>
            {tierBadge && (
              <Badge
                variant={spec.common ? "secondary" : "outline"}
                className="h-5 px-1.5 text-[10px] font-normal"
              >
                {tierBadge}
              </Badge>
            )}
            {scopeLabel && (
              <Badge
                variant="outline"
                className="h-5 px-1.5 text-[11px] font-normal"
              >
                {scopeLabel}
              </Badge>
            )}
          </label>

          {modified && !disabled && (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={onReset}
              className="h-6 shrink-0 px-1.5 text-xs text-muted-foreground hover:text-foreground"
              title={`恢复默认值：${formatParamValueText(spec.default)}`}
            >
              <RotateCcw className="size-3" />
              恢复默认
            </Button>
          )}
        </div>

        <p className="text-xs leading-relaxed text-muted-foreground">
          {spec.description}
        </p>
      </div>

      <div className="grid gap-1.5 pt-0.5">
        <ParamInputControl
          id={inputId}
          spec={spec}
          value={value}
          disabled={disabled}
          onChange={onChange}
        />
        <div className="flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground tabular-nums">
          <span>
            {lockedHint ?? `官方默认：${formatParamValueText(spec.default)}`}
          </span>
          {!lockedHint && spec.min !== undefined && spec.max !== undefined && (
            <span>
              范围：{spec.min} – {spec.max}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

function ParamInputControl({
  id,
  spec,
  value,
  disabled,
  onChange,
}: {
  id: string
  spec: ParamSpec
  value: unknown
  disabled: boolean
  onChange: (next: unknown) => void
}) {
  switch (spec.type) {
    case "bool": {
      const checked = Boolean(value)
      return (
        <div className="flex h-9 items-center justify-between rounded-md border bg-background px-3">
          <span className="text-xs text-muted-foreground">
            {checked ? "已开启 (true)" : "已关闭 (false)"}
          </span>
          <Switch
            id={id}
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
          <SelectTrigger id={id} className="w-full">
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
          placeholder="留空表示自动 / 不限制 (null)"
          onChange={onChange}
        />
      )

    case "string":
      return (
        <Input
          id={id}
          value={value == null ? "" : String(value)}
          disabled={disabled}
          placeholder={
            spec.key === "device"
              ? "留空自动选择可用 GPU；或填 0、0,1、cpu"
              : "留空使用默认值"
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
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "batch":
      return (
        <BatchInput
          id={id}
          value={value}
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "freeze":
      return (
        <FreezeInput
          id={id}
          value={value}
          disabled={disabled}
          onChange={onChange}
        />
      )

    case "int_list":
      return (
        <IntListInput
          id={id}
          value={value}
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
      className="tabular-nums"
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
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  disabled: boolean
  onChange: (next: number | [number, number]) => void
}) {
  const isRect = Array.isArray(value) && value.length === 2
  const squareSize =
    typeof value === "number" && Number.isFinite(value) ? value : 640
  const rectH = isRect && typeof value[0] === "number" ? value[0] : squareSize
  const rectW = isRect && typeof value[1] === "number" ? value[1] : squareSize

  return (
    <div className="grid gap-2">
      <Tabs
        value={isRect ? "rect" : "square"}
        onValueChange={(mode) => {
          if (mode === "rect") {
            onChange([squareSize, squareSize])
          } else {
            onChange(rectH)
          }
        }}
      >
        <TabsList className="h-8 w-full">
          <TabsTrigger
            value="square"
            disabled={disabled}
            className="flex-1 text-xs"
          >
            正方形边长
          </TabsTrigger>
          <TabsTrigger
            value="rect"
            disabled={disabled}
            className="flex-1 text-xs"
          >
            指定 [高, 宽]
          </TabsTrigger>
        </TabsList>
      </Tabs>

      {isRect ? (
        <div className="grid grid-cols-2 gap-2">
          <NumberParamInput
            id={id}
            value={rectH}
            integer
            nullable={false}
            min={32}
            max={4096}
            step={32}
            placeholder="高 (H)"
            disabled={disabled}
            onChange={(h) => {
              if (h !== null) {
                onChange([h, rectW])
              }
            }}
          />
          <NumberParamInput
            id={`${id}-w`}
            value={rectW}
            integer
            nullable={false}
            min={32}
            max={4096}
            step={32}
            placeholder="宽 (W)"
            disabled={disabled}
            onChange={(w) => {
              if (w !== null) {
                onChange([rectH, w])
              }
            }}
          />
        </div>
      ) : (
        <NumberParamInput
          id={id}
          value={squareSize}
          integer
          nullable={false}
          min={32}
          max={4096}
          step={32}
          disabled={disabled}
          onChange={(size) => {
            if (size !== null) {
              onChange(size)
            }
          }}
        />
      )}
    </div>
  )
}

function BatchInput({
  id,
  value,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  disabled: boolean
  onChange: (next: number) => void
}) {
  const numVal =
    typeof value === "number" && Number.isFinite(value) ? value : 16
  const mode =
    numVal === -1 ? "auto" : numVal > 0 && numVal < 1 ? "fraction" : "fixed"

  return (
    <div className="grid gap-2">
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
        <SelectTrigger className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="fixed">固定批次大小（默认 16）</SelectItem>
          <SelectItem value="auto">自动按 60% 显存计算 (-1)</SelectItem>
          <SelectItem value="fraction">
            按指定显存比例自动计算 (0.05–0.99)
          </SelectItem>
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
          onChange={(next) => {
            if (next !== null) {
              onChange(next)
            }
          }}
        />
      )}

      {mode === "fraction" && (
        <NumberParamInput
          id={id}
          value={numVal}
          integer={false}
          nullable={false}
          min={0.05}
          max={0.99}
          step={0.05}
          disabled={disabled}
          onChange={(next) => {
            if (next !== null) {
              onChange(next)
            }
          }}
        />
      )}
    </div>
  )
}

function FreezeInput({
  id,
  value,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
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
        placeholder="留空不冻结；填 10 冻结前 10 层，或填 0,1,2"
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
  disabled,
  onChange,
}: {
  id: string
  value: unknown
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
        placeholder="留空评估全部类别；或填逗号分隔的类别编号（如 0, 2, 5）"
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
