import { useId, type ReactNode } from "react"
import {
  CircleAlert,
  CircleCheck,
  CircleDashed,
  CircleMinus,
  CircleX,
  Cpu,
  Gpu,
  Info,
  Server,
  TriangleAlert,
} from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import type {
  ComputeNode,
  EnvironmentIssue,
  NodeGpu,
} from "@/features/training/compute-api"
import {
  formatBytesValue,
  formatCores,
  formatCpuCores,
  formatLoadAverage,
  formatMilliseconds,
  formatPercentValue,
  formatPower,
  formatTemperature,
  formatUsage,
  shortenCpuModel,
  shortenGpuName,
} from "@/features/training/compute-format"
import {
  acceleratorLabel,
  environmentBadge,
  environmentFacts,
  gpuMetricsOf,
  isOnline,
  isRedetecting,
  selfTestLabels,
  sortIssues,
  type EnvironmentBadge,
} from "@/features/training/compute-status"
import { HintText } from "@/features/training/hint-text"
import { NodeActionsMenu } from "@/features/training/node-actions"
import { Sparkline } from "@/features/training/sparkline"
import {
  gpuUtilizationSeries,
  historySpanSeconds,
  seriesStats,
} from "@/features/training/sparkline-points"
import { UsageBar } from "@/features/training/usage-bar"
import { formatDuration, formatRelativeTime, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

/** /dev/shm 小于 1 GiB 时，DataLoader 多进程读取数据容易出错。 */
const SHM_MIN_BYTES = 1024 ** 3
/** 剩余空间低于 10% 时提醒。 */
const DISK_LOW_PERCENT = 90
/** 显卡温度达到 85 °C 时提醒（多数显卡开始降频）。 */
const GPU_HOT_CELSIUS = 85

type NodeCardProps = {
  node: ComputeNode
  /** 当前时间，用于“3 分钟前”这类相对时间。 */
  now: number
  /** 参数为打开对话框的按钮，对话框关闭后焦点回到它上面。 */
  onShowDetails: (trigger: HTMLElement | null) => void
  onDelete: (trigger: HTMLElement | null) => void
}

export function NodeCard({
  node,
  now,
  onShowDetails,
  onDelete,
}: NodeCardProps) {
  const titleId = useId()
  const online = isOnline(node)

  return (
    <article
      aria-labelledby={titleId}
      className={cn(
        "flex min-w-0 flex-col rounded-xl border bg-card text-card-foreground shadow-sm",
        // 离线节点：用虚线边框与“离线 / 离线前”标记说明，只淡化进度条与折线图
        // 这些图形；文字不变淡，保持足够的对比度（WCAG AA）。
        !online &&
          "border-dashed shadow-none [&_[data-slot=progress]]:opacity-50 [&_[data-slot=sparkline]]:opacity-50"
      )}
    >
      <header className="flex items-start gap-3 p-4 sm:px-6 sm:pt-5">
        <div className="hidden size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground sm:flex">
          <Server className="size-4" />
        </div>
        <div className="grid min-w-0 flex-1 gap-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5">
            <h3
              id={titleId}
              className="max-w-full truncate leading-snug font-semibold"
              title={node.name}
            >
              {node.name}
            </h3>
            <NodeStatusBadge node={node} now={now} />
            <Badge variant="secondary">
              {node.hardware.gpus.length > 0 ? <Gpu /> : <Cpu />}
              {acceleratorLabel(node)}
            </Badge>
            <EnvironmentStatusBadge badge={environmentBadge(node)} />
          </div>
          <NodeIdentity node={node} />
        </div>
        <NodeActionsMenu
          node={node}
          onShowDetails={onShowDetails}
          onDelete={onDelete}
        />
      </header>

      <div className="grid min-w-0 gap-5 border-t p-4 sm:px-6 sm:pb-5">
        <EnvironmentSummary node={node} />
        <IssueList
          issues={node.environment?.issues ?? []}
          previous={isRedetecting(node)}
        />
        <ResourceSection node={node} />
        {node.hardware.gpus.length > 0 && <GpuSection node={node} />}
        {node.environment && (
          <SelfTestLine node={node} now={now} onShowDetails={onShowDetails} />
        )}
      </div>
    </article>
  )
}

function NodeStatusBadge({ node, now }: { node: ComputeNode; now: number }) {
  if (isOnline(node)) {
    return (
      <Badge variant="outline">
        <span className="size-1.5 rounded-full bg-emerald-500" aria-hidden />
        在线
      </Badge>
    )
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <Badge variant="outline" className="text-muted-foreground">
        <span
          className="size-1.5 rounded-full border border-muted-foreground"
          aria-hidden
        />
        离线
      </Badge>
      <span className="text-xs text-muted-foreground">
        最后心跳 {formatRelativeTime(node.last_seen_at, now)}
      </span>
    </span>
  )
}

const environmentIcons = {
  ready: CircleCheck,
  cpu: Cpu,
  error: CircleAlert,
  unknown: CircleDashed,
} as const

export function EnvironmentStatusBadge({ badge }: { badge: EnvironmentBadge }) {
  if (badge.tone === "pending") {
    return (
      <Badge
        variant="outline"
        className="text-muted-foreground"
        title={badge.description}
      >
        <Spinner className="size-3" aria-hidden />
        {badge.label}
      </Badge>
    )
  }
  const Icon = environmentIcons[badge.tone]
  return (
    <Badge
      variant={badge.tone === "error" ? "destructive" : "outline"}
      className={cn(badge.tone === "unknown" && "text-muted-foreground")}
      title={badge.description}
    >
      <Icon
        className={cn(
          badge.tone === "ready" && "text-emerald-600 dark:text-emerald-500"
        )}
      />
      {badge.label}
    </Badge>
  )
}

/** 地址 · 主机名 · 节点版本（缺失的不显示）。 */
function NodeIdentity({ node }: { node: ComputeNode }) {
  const parts = [
    node.address,
    node.hostname && node.hostname !== node.name ? node.hostname : "",
    node.agent_version ? `节点 v${node.agent_version}` : "",
  ].filter(Boolean)
  if (parts.length === 0) {
    return null
  }
  const text = parts.join(" · ")
  return (
    <p className="truncate text-xs text-muted-foreground" title={text}>
      {text}
    </p>
  )
}

/** 环境信息行：PyTorch、CUDA、cuDNN、Ultralytics、驱动、Python。 */
function EnvironmentSummary({ node }: { node: ComputeNode }) {
  const facts = environmentFacts(node)
  const detecting = node.environment === null

  return (
    <div className="grid gap-2">
      {isRedetecting(node) && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner className="size-3.5 shrink-0" aria-hidden />
          正在重新检测 CUDA 环境…（通常需要几十秒），下面是上次检测的结果
        </p>
      )}
      {detecting && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          {isOnline(node) ? (
            <>
              <Spinner className="size-3.5" aria-hidden />
              正在检测 CUDA 环境…（导入 PyTorch 并运行 YOLO
              自检，通常需要几十秒）
            </>
          ) : (
            <>
              <CircleDashed className="size-3.5" aria-hidden />
              节点离线前没有完成 CUDA 环境检测
            </>
          )}
        </p>
      )}
      {facts.length > 0 && (
        <dl className="flex flex-wrap gap-x-5 gap-y-1.5 text-sm">
          {facts.map((fact) => (
            <div
              key={fact.label}
              className="flex min-w-0 items-baseline gap-1.5"
            >
              <dt className="text-xs text-muted-foreground">{fact.label}</dt>
              <dd className="truncate font-mono text-xs" title={fact.value}>
                {fact.value}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}

/**
 * 环境问题：error 用醒目的提示框，warning 用普通提示框，info 用一行次要文字。
 * `previous`：正在重新检测，这些是上次检测的结果，error 不再用醒目的颜色。
 */
function IssueList({
  issues,
  previous = false,
}: {
  issues: readonly EnvironmentIssue[]
  previous?: boolean
}) {
  if (issues.length === 0) {
    return null
  }
  const sorted = sortIssues(issues)
  const alerts = sorted.filter((issue) => issue.level !== "info")
  const notes = sorted.filter((issue) => issue.level === "info")

  return (
    <div className="grid gap-2">
      {alerts.map((issue, index) => {
        const isError = issue.level === "error"
        const Icon = isError ? CircleX : TriangleAlert
        return (
          // 静态内容，不需要读屏软件在每次刷新时播报：role 改为 note。
          <Alert
            key={`${issue.code}-${index}`}
            role="note"
            variant={isError && !previous ? "destructive" : "default"}
          >
            <Icon
              className={cn(
                previous
                  ? "text-muted-foreground"
                  : !isError && "text-amber-600 dark:text-amber-500"
              )}
            />
            <AlertTitle className="line-clamp-none leading-snug break-words">
              <span className="sr-only">
                {previous && "上次检测的"}
                {isError ? "错误：" : "警告："}
              </span>
              {issue.message}
            </AlertTitle>
            {issue.hint && (
              <AlertDescription className="text-muted-foreground">
                <HintText hint={issue.hint} />
              </AlertDescription>
            )}
          </Alert>
        )
      })}
      {notes.map((issue, index) => (
        <div
          key={`${issue.code}-${index}`}
          className="flex items-start gap-2 text-xs text-muted-foreground"
        >
          <Info className="mt-px size-3.5 shrink-0" aria-hidden />
          <div className="grid min-w-0 gap-1">
            <p>
              <span className="sr-only">提示：</span>
              {issue.message}
            </p>
            {issue.hint && <HintText hint={issue.hint} />}
          </div>
        </div>
      ))}
    </div>
  )
}

function SectionTitle({
  children,
  offline,
}: {
  children: ReactNode
  offline: boolean
}) {
  return (
    <h4 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
      {children}
      {offline && (
        <Badge variant="outline" className="font-normal text-muted-foreground">
          离线前
        </Badge>
      )}
    </h4>
  )
}

function ResourceSection({ node }: { node: ComputeNode }) {
  const { hardware, metrics } = node
  const offline = !isOnline(node)
  const cores = formatCpuCores(hardware.cpu)
  const load = formatLoadAverage(metrics?.load_average)
  const cpuDetail =
    [
      cores,
      hardware.cpu.limit_cores !== null &&
        `容器限制 ${formatCores(hardware.cpu.limit_cores)} 核`,
      load && `负载 ${load}`,
    ]
      .filter(Boolean)
      .join(" · ") || "核数未知"

  const memoryTotal = metrics?.memory_total_bytes ?? hardware.memory.total_bytes
  const memoryUsed = metrics?.memory_used_bytes ?? null
  const memoryPercent =
    memoryUsed !== null && memoryTotal
      ? percentOf(memoryUsed, memoryTotal)
      : null

  const diskTotal = metrics?.disk_total_bytes ?? null
  const diskFree = metrics?.disk_free_bytes ?? null
  const diskUsed =
    diskTotal !== null && diskFree !== null
      ? Math.max(0, diskTotal - diskFree)
      : null
  const diskPercent =
    diskUsed !== null && diskTotal ? percentOf(diskUsed, diskTotal) : null
  const diskLow = diskPercent !== null && diskPercent >= DISK_LOW_PERCENT

  const shmSmall =
    hardware.shm_bytes !== null &&
    hardware.shm_bytes < SHM_MIN_BYTES &&
    hardware.gpus.length > 0

  return (
    <section className="grid gap-3">
      <SectionTitle offline={offline}>资源</SectionTitle>
      <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2 xl:grid-cols-4">
        <UsageBar
          label="CPU"
          ariaLabel={`${node.name} CPU 利用率`}
          value={formatPercentValue(metrics?.cpu_percent)}
          percent={metrics?.cpu_percent ?? null}
          detail={
            <>
              <p className="truncate" title={hardware.cpu.model ?? undefined}>
                {shortenCpuModel(hardware.cpu.model)}
              </p>
              <p className="truncate" title={cpuDetail}>
                {cpuDetail}
              </p>
            </>
          }
        />
        <UsageBar
          label="内存"
          ariaLabel={`${node.name} 内存使用率`}
          value={formatUsage(memoryUsed, memoryTotal)}
          percent={memoryPercent}
          detail={
            hardware.memory.limit_bytes !== null
              ? `容器限制 ${formatBytesValue(hardware.memory.limit_bytes)}`
              : memoryPercent !== null
                ? `已用 ${formatPercentValue(memoryPercent)}`
                : "暂无数据"
          }
        />
        <UsageBar
          label="数据盘"
          ariaLabel={`${node.name} 数据盘使用率`}
          value={formatUsage(diskUsed, diskTotal)}
          percent={diskPercent}
          warning={diskLow}
          detail={
            <p
              className={cn(
                "truncate",
                diskLow && "text-amber-700 dark:text-amber-500"
              )}
              title={metrics?.disk_path ?? undefined}
            >
              {[
                metrics?.disk_path,
                diskFree !== null && `剩余 ${formatBytesValue(diskFree)}`,
                diskLow && "空间不足",
              ]
                .filter(Boolean)
                .join(" · ") || "暂无数据"}
            </p>
          }
        />
        <div className="grid content-start gap-1.5 text-xs">
          <div className="flex items-baseline justify-between gap-3">
            <span className="text-muted-foreground">/dev/shm</span>
            <span className="font-medium tabular-nums">
              {formatBytesValue(hardware.shm_bytes)}
            </span>
          </div>
          <p
            className={cn(
              "text-muted-foreground",
              shmSmall &&
                "flex items-start gap-1 text-amber-700 dark:text-amber-500"
            )}
          >
            {shmSmall && <TriangleAlert className="mt-px size-3 shrink-0" />}
            {shmSmall
              ? "共享内存不足 1 GB，多进程加载数据可能失败"
              : "共享内存，训练时多进程加载数据使用"}
          </p>
        </div>
      </div>
    </section>
  )
}

function GpuSection({ node }: { node: ComputeNode }) {
  const offline = !isOnline(node)
  const span = historySpanSeconds(node.history)

  return (
    <section className="grid gap-3">
      <SectionTitle offline={offline}>
        GPU（{node.hardware.gpus.length} 块）
      </SectionTitle>
      <ul className="grid gap-2">
        {node.hardware.gpus.map((gpu) => (
          <GpuRow key={gpu.index} node={node} gpu={gpu} spanSeconds={span} />
        ))}
      </ul>
    </section>
  )
}

function GpuRow({
  node,
  gpu,
  spanSeconds,
}: {
  node: ComputeNode
  gpu: NodeGpu
  spanSeconds: number
}) {
  const metrics = gpuMetricsOf(node, gpu.index)
  const shortName = shortenGpuName(gpu.name)
  const memoryTotal =
    gpu.memory_total_bytes ?? metrics?.memory_total_bytes ?? null
  const memoryUsed = metrics?.memory_used_bytes ?? null
  const memoryPercent =
    memoryUsed !== null && memoryTotal
      ? percentOf(memoryUsed, memoryTotal)
      : null
  const utilization = metrics?.utilization_percent ?? null
  const series = gpuUtilizationSeries(node.history, gpu.index)
  const stats = seriesStats(series)
  const hot =
    typeof metrics?.temperature_c === "number" &&
    metrics.temperature_c >= GPU_HOT_CELSIUS

  const sparklineLabel = stats
    ? `GPU ${gpu.index} 最近${spanSeconds > 0 ? ` ${formatDuration(spanSeconds)}` : ""}的利用率：当前 ${formatPercentValue(stats.last)}，最高 ${formatPercentValue(stats.max)}，最低 ${formatPercentValue(stats.min)}`
    : `GPU ${gpu.index} 暂无利用率历史`

  return (
    <li className="grid min-w-0 gap-x-6 gap-y-3 rounded-lg border p-3 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)_minmax(0,1.25fr)_minmax(0,1fr)] lg:items-start">
      <div className="grid min-w-0 gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] leading-none text-muted-foreground tabular-nums">
            #{gpu.index}
          </span>
          <span className="truncate text-sm font-medium" title={gpu.name}>
            {shortName}
          </span>
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {[
            gpu.compute_capability && `计算能力 ${gpu.compute_capability}`,
            formatBytesValue(memoryTotal),
            gpu.mig_enabled && "MIG",
          ]
            .filter(Boolean)
            .join(" · ")}
        </p>
      </div>
      <UsageBar
        label="显存"
        ariaLabel={`GPU ${gpu.index} 显存使用率`}
        value={formatUsage(memoryUsed, memoryTotal)}
        percent={memoryPercent}
      />
      <UsageBar
        label="利用率"
        ariaLabel={`GPU ${gpu.index} 利用率`}
        value={formatPercentValue(utilization)}
        percent={utilization}
      >
        <Sparkline values={series} label={sparklineLabel} className="h-5" />
      </UsageBar>
      <dl className="grid grid-cols-3 gap-2 text-xs lg:grid-cols-1 lg:gap-1">
        <GpuStat label="温度" warning={hot}>
          {hot && (
            <TriangleAlert className="size-3 shrink-0" aria-label="温度过高" />
          )}
          {formatTemperature(metrics?.temperature_c)}
        </GpuStat>
        <GpuStat label="功率">
          {formatPower(metrics?.power_watts, gpu.power_limit_watts)}
        </GpuStat>
        <GpuStat label="进程">
          {typeof metrics?.process_count === "number"
            ? `${metrics.process_count} 个`
            : "—"}
        </GpuStat>
      </dl>
    </li>
  )
}

function GpuStat({
  label,
  warning = false,
  children,
}: {
  label: string
  warning?: boolean
  children: ReactNode
}) {
  return (
    <div className="grid min-w-0 gap-0.5 lg:flex lg:items-baseline lg:justify-between lg:gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "flex min-w-0 items-center gap-1 font-medium whitespace-nowrap tabular-nums",
          warning && "text-amber-700 dark:text-amber-500"
        )}
      >
        {children}
      </dd>
    </div>
  )
}

function SelfTestLine({
  node,
  now,
  onShowDetails,
}: {
  node: ComputeNode
  now: number
  onShowDetails: (trigger: HTMLElement | null) => void
}) {
  const env = node.environment
  if (!env) {
    return null
  }
  const { self_test: test } = env
  const Icon =
    test.status === "passed"
      ? CircleCheck
      : test.status === "failed"
        ? CircleX
        : CircleMinus

  const parts = [
    test.message,
    test.device,
    test.duration_ms !== null && formatMilliseconds(test.duration_ms),
    `${isRedetecting(node) ? "上次检测于" : "检测于"} ${formatRelativeTime(env.probed_at, now)}`,
  ].filter(Boolean)

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-t pt-4 text-xs text-muted-foreground">
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1.5 font-medium",
          test.status === "passed" && "text-foreground",
          test.status === "failed" && "text-destructive"
        )}
      >
        <Icon
          className={cn(
            "size-3.5",
            test.status === "passed" && "text-emerald-600 dark:text-emerald-500"
          )}
          aria-hidden
        />
        自检{selfTestLabels[test.status] ?? "未运行"}
      </span>
      <span className="min-w-0 break-words">{parts.join(" · ")}</span>
      {test.status === "failed" && test.detail && (
        <Button
          variant="link"
          size="xs"
          className="h-auto px-0 text-xs"
          onClick={(event) => onShowDetails(event.currentTarget)}
        >
          查看错误详情
        </Button>
      )}
    </div>
  )
}
