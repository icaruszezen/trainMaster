import type { ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Gpu, Plug, Server, TriangleAlert } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { LoadError } from "@/features/datasets/load-error"
import {
  computeServerQuery,
  type ComputeNode,
  type ComputeServer,
} from "@/features/training/compute-api"
import {
  formatBytesValue,
  formatCores,
  formatCpuCores,
  formatLoadAverage,
  formatPercentValue,
  formatUsage,
  shortenCpuModel,
  shortenGpuName,
} from "@/features/training/compute-format"
import {
  serverGpuHint,
  type ServerGpuHint,
} from "@/features/training/compute-status"
import { RefreshStatus } from "@/features/training/refresh-status"
import { UsageBar } from "@/features/training/usage-bar"
import { formatDuration, percentOf } from "@/lib/format"

/**
 * 平台服务器（运行 trainMaster 的机器）自身的 CPU、内存与显卡。`nodes` 为
 * undefined（节点列表还没加载）时不显示“没有 GPU 节点”的提示，避免闪现。
 */
export function ServerCard({
  nodes,
  onConnect,
}: {
  nodes: readonly ComputeNode[] | undefined
  onConnect: () => void
}) {
  const query = useQuery(computeServerQuery)
  const { data } = query

  return (
    <Card className="gap-5">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Server className="size-4 text-muted-foreground" />
          平台服务器
        </CardTitle>
        <CardDescription className="min-w-0 truncate">
          运行 trainMaster 服务的机器
          {data && (
            <>
              {" · "}
              <span title={data.hostname}>{data.hostname}</span>
            </>
          )}
        </CardDescription>
        <CardAction>
          <RefreshStatus
            error={query.error}
            hasData={data !== undefined}
            updatedAt={query.dataUpdatedAt}
            onRefresh={() => query.refetch()}
            label="刷新平台服务器信息"
          />
        </CardAction>
      </CardHeader>
      <CardContent>
        {query.isPending ? (
          <ServerSkeleton />
        ) : data === undefined ? (
          <LoadError
            title="无法获取平台服务器信息"
            error={query.error}
            retrying={query.isFetching}
            onRetry={() => query.refetch()}
            className="p-4 md:p-6"
          />
        ) : (
          <ServerDetails server={data} nodes={nodes} onConnect={onConnect} />
        )}
      </CardContent>
    </Card>
  )
}

function ServerDetails({
  server,
  nodes,
  onConnect,
}: {
  server: ComputeServer
  nodes: readonly ComputeNode[] | undefined
  onConnect: () => void
}) {
  const { cpu, memory } = server
  const hint = nodes ? serverGpuHint(server, nodes) : null
  const load = formatLoadAverage(server.load_average)
  const memoryPercent =
    memory.used_bytes !== null && memory.total_bytes
      ? percentOf(memory.used_bytes, memory.total_bytes)
      : null
  const cores = [
    formatCpuCores(cpu),
    cpu.limit_cores !== null && `容器限制 ${formatCores(cpu.limit_cores)} 核`,
  ]
    .filter(Boolean)
    .join(" · ")

  return (
    <div className="grid gap-5">
      {hint && <ServerGpuAlert hint={hint} onConnect={onConnect} />}
      <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2 xl:grid-cols-4">
        <UsageBar
          label="CPU"
          ariaLabel="平台服务器 CPU 利用率"
          value={
            cpu.usage_percent === null
              ? "采样中…"
              : formatPercentValue(cpu.usage_percent)
          }
          percent={cpu.usage_percent}
          detail={
            <>
              <p className="truncate" title={cpu.model ?? undefined}>
                {shortenCpuModel(cpu.model)}
              </p>
              <p className="truncate">{cores || "核数未知"}</p>
            </>
          }
        />
        <UsageBar
          label="内存"
          ariaLabel="平台服务器内存使用率"
          value={formatUsage(memory.used_bytes, memory.total_bytes)}
          percent={memoryPercent}
          detail={
            [
              memory.available_bytes !== null &&
                `可用 ${formatBytesValue(memory.available_bytes)}`,
              memory.limit_bytes !== null &&
                `容器限制 ${formatBytesValue(memory.limit_bytes)}`,
            ]
              .filter(Boolean)
              .join(" · ") || "暂无数据"
          }
        />
        <dl className="grid content-start gap-1.5 text-xs">
          <ServerFact label="负载">{load ?? "—"}</ServerFact>
          <ServerFact label="开机时长">
            {server.uptime_seconds === null
              ? "—"
              : formatDuration(server.uptime_seconds)}
          </ServerFact>
          <ServerFact label="系统">
            {[`${server.os} / ${server.arch}`, server.in_container && "容器内"]
              .filter(Boolean)
              .join(" · ")}
          </ServerFact>
          {server.kernel && (
            <ServerFact label="内核">
              <span className="font-mono">{server.kernel}</span>
            </ServerFact>
          )}
        </dl>
        <ServerGpus server={server} />
      </div>
    </div>
  )
}

function ServerFact({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-3">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right font-medium tabular-nums">
        {children}
      </dd>
    </div>
  )
}

function ServerGpus({ server }: { server: ComputeServer }) {
  const { nvidia } = server

  return (
    <div className="grid min-w-0 content-start gap-1.5 text-xs">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-muted-foreground">NVIDIA 显卡</span>
        <span className="truncate font-medium">
          {!nvidia
            ? "未检测到"
            : nvidia.driver_version
              ? `驱动 ${nvidia.driver_version}`
              : "驱动未加载"}
        </span>
      </div>
      {nvidia && nvidia.gpus.length > 0 && (
        <ul className="grid gap-1">
          {nvidia.gpus.map((gpu, index) => (
            <li
              key={gpu.uuid ?? gpu.bus_id ?? index}
              className="flex min-w-0 items-center gap-1.5 text-muted-foreground"
            >
              <Gpu className="size-3 shrink-0" aria-hidden />
              <span
                className="truncate text-foreground"
                title={gpu.name ?? undefined}
              >
                {gpu.name ? shortenGpuName(gpu.name) : "NVIDIA 显卡"}
              </span>
              <span className="ml-auto shrink-0 font-mono">{gpu.bus_id}</span>
            </li>
          ))}
        </ul>
      )}
      {!nvidia && (
        <p className="text-muted-foreground">
          这台机器上没有 NVIDIA 显卡，训练节点可以部署在其它 GPU 机器上。
        </p>
      )}
    </div>
  )
}

function ServerGpuAlert({
  hint,
  onConnect,
}: {
  hint: ServerGpuHint
  onConnect: () => void
}) {
  const command = (
    <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
      make setup && make up
    </code>
  )

  if (hint.kind === "driver_missing") {
    return (
      <Alert role="note">
        <TriangleAlert className="text-amber-600 dark:text-amber-500" />
        <AlertTitle className="line-clamp-none leading-snug">
          检测到 {hint.gpuCount} 块 NVIDIA 显卡，但驱动未加载
        </AlertTitle>
        <AlertDescription>
          <p>
            先在这台机器上安装 NVIDIA 驱动（如 Ubuntu 上运行{" "}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
              sudo ubuntu-drivers install
            </code>
            ）并重启，然后在部署目录中运行 {command}，自动配置 Docker
            使用显卡并启动 GPU 训练节点。
          </p>
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert role="note">
      <Gpu />
      <AlertTitle className="line-clamp-none leading-snug">
        检测到 {hint.gpuCount} 块 NVIDIA 显卡，但还没有 GPU 训练节点在线
      </AlertTitle>
      <AlertDescription>
        <p>
          在部署目录中运行 {command}：检查 Docker 能否使用显卡、按驱动选择
          PyTorch 的 CUDA 版本，然后启动 GPU 训练节点。
        </p>
        <Button
          variant="outline"
          size="sm"
          className="mt-1"
          onClick={onConnect}
        >
          <Plug />
          查看接入步骤
        </Button>
      </AlertDescription>
    </Alert>
  )
}

function ServerSkeleton() {
  return (
    <div
      className="grid gap-5 sm:grid-cols-2 xl:grid-cols-4"
      aria-busy="true"
      aria-label="正在加载"
    >
      {[0, 1, 2, 3].map((index) => (
        <div key={index} className="grid gap-2">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-1.5 w-full" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      ))}
    </div>
  )
}
