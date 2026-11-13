/**
 * Ultralytics YOLO 全量参数编辑器。
 *
 * 按服务端 `/api/v1/training/params-schema` 返回的 86 项官方参数规格渲染分组、
 * 搜索、修改状态追踪、单项/整组恢复官方默认值与 YAML 预览导出。所有参数均开放
 * 可编辑（特定任务专用参数会显示任务范围徽章，但同样允许编辑）。
 */
import { useId, useMemo, useState } from "react"
import {
  FileCode2,
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
  countModifiedParams,
  filterParamSpecs,
  formatParamsYaml,
  formatParamValueText,
  isParamModified,
  parseFreezeText,
  parseIntListText,
} from "@/features/training/training-params"
import { cn } from "@/lib/utils"

export type TrainingParamsEditorProps = {
  schema: ParamsSchemaResponse
  values: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  initMode?: InitMode
  disabled?: boolean
}

export function TrainingParamsEditor({
  schema,
  values,
  onChange,
  initMode = "scratch",
  disabled = false,
}: TrainingParamsEditorProps) {
  const [activeTab, setActiveTab] = useState<ParamGroupKey | "all">("train")
  const [query, setQuery] = useState("")
  const [onlyModified, setOnlyModified] = useState(false)
  const [yamlOpen, setYamlOpen] = useState(false)

  const totalModified = useMemo(
    () => countModifiedParams(schema.specs, values),
    [schema.specs, values]
  )

  // 当用户输入搜索词或开启“仅显示已修改”时，若选在“全部”能跨组浏览；如果当前在具体分组，只筛当前分组，同时支持一键切到全部。
  const effectiveGroup = query.trim() !== "" ? "all" : activeTab

  const filteredSpecs = useMemo(
    () =>
      filterParamSpecs(schema.specs, values, {
        group: effectiveGroup,
        query,
        onlyModified,
      }),
    [schema.specs, values, effectiveGroup, query, onlyModified]
  )

  const activeGroupMeta = schema.groups.find((g) => g.key === activeTab)
  const activeGroupModified =
    activeTab === "all"
      ? totalModified
      : countModifiedParams(schema.specs, values, activeTab)

  const setOneParam = (key: string, nextValue: unknown) => {
    onChange({
      ...values,
      [key]: nextValue,
    })
  }

  const resetOneParam = (spec: ParamSpec) => {
    const fallback =
      spec.key === "resume"
        ? initMode === "resume"
        : spec.key === "pretrained" && initMode === "scratch"
          ? false
          : Array.isArray(spec.default)
            ? [...spec.default]
            : spec.default
    setOneParam(spec.key, fallback)
  }

  const resetGroup = (group: ParamGroupKey) => {
    const defaults = buildDefaultParams(schema.specs, initMode)
    const next = { ...values }
    for (const spec of schema.specs) {
      if (spec.group === group) {
        next[spec.key] = defaults[spec.key]
      }
    }
    onChange(next)
  }

  const resetAll = () => {
    onChange(buildDefaultParams(schema.specs, initMode))
  }

  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-base font-semibold">
              Ultralytics YOLO 全量训练参数
            </h3>
            <Badge variant="outline" className="font-normal tabular-nums">
              共 {schema.specs.length} 项可编辑
            </Badge>
            {totalModified > 0 && (
              <Badge variant="secondary" className="font-normal tabular-nums">
                已修改 {totalModified} 项
              </Badge>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            基础训练参数均按 Ultralytics
            官方默认值（cfg/default.yaml）预填，支持逐项调整、分组重置及导出
            args.yaml。
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

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索英文参数名或中文说明（如 epochs、batch、lr0、mosaic、device、freeze…）"
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

        <label className="flex shrink-0 cursor-pointer items-center gap-2 text-sm select-none">
          <Switch checked={onlyModified} onCheckedChange={setOnlyModified} />
          <span className="tabular-nums">仅看已修改 ({totalModified})</span>
        </label>
      </div>

      {query.trim() === "" && (
        <Tabs
          value={activeTab}
          onValueChange={(next) => setActiveTab(next as ParamGroupKey | "all")}
          className="min-w-0"
        >
          <ScrollingTabsList value={activeTab}>
            {schema.groups.map((group) => {
              const modCount = countModifiedParams(
                schema.specs,
                values,
                group.key
              )
              return (
                <TabsTrigger
                  key={group.key}
                  value={group.key}
                  className="gap-1.5"
                >
                  {group.label}
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
              全部参数 ({schema.specs.length})
            </TabsTrigger>
          </ScrollingTabsList>
        </Tabs>
      )}

      {query.trim() === "" && activeGroupMeta && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3.5 py-2.5 text-xs text-muted-foreground">
          <span>{activeGroupMeta.description}</span>
          {activeGroupModified > 0 && (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              disabled={disabled}
              onClick={() => resetGroup(activeGroupMeta.key)}
            >
              <RotateCcw />
              恢复本组默认 ({activeGroupModified})
            </Button>
          )}
        </div>
      )}

      {filteredSpecs.length === 0 ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed py-10 text-center text-sm text-muted-foreground">
          <SlidersHorizontal className="size-5" />
          <p>
            {onlyModified
              ? "当前范围内没有修改过的参数（全部与官方默认值一致）。"
              : `没有匹配“${query.trim()}”的训练参数。`}
          </p>
          {(onlyModified || query.trim() !== "") && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setOnlyModified(false)
                setQuery("")
              }}
            >
              显示全部参数
            </Button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          {filteredSpecs.map((spec) => (
            <ParamControlCard
              key={spec.key}
              spec={spec}
              value={spec.key in values ? values[spec.key] : spec.default}
              modified={isParamModified(spec, values)}
              disabled={
                disabled || (spec.key === "resume" && initMode === "resume")
              }
              onChange={(nextVal) => setOneParam(spec.key, nextVal)}
              onReset={() => resetOneParam(spec)}
            />
          ))}
        </div>
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
  onChange,
  onReset,
}: {
  spec: ParamSpec
  value: unknown
  modified: boolean
  disabled: boolean
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
            {scopeLabel && (
              <Badge
                variant="outline"
                className="h-5 px-1.5 text-[11px] font-normal"
              >
                {scopeLabel}
              </Badge>
            )}
          </label>

          {modified && (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              disabled={disabled}
              onClick={onReset}
              className="h-6 shrink-0 px-1.5 text-xs text-muted-foreground hover:text-foreground"
              title={`恢复官方默认值：${formatParamValueText(spec.default)}`}
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
          <span>官方默认：{formatParamValueText(spec.default)}</span>
          {spec.min !== undefined && spec.max !== undefined && (
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
