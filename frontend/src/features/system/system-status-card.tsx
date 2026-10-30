import type { ReactNode } from "react"
import { RefreshCw, ServerCrash } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { useSystemInfo, type SystemInfo } from "@/features/system/api"
import { formatBytes, formatDuration, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

export function SystemStatusCard({ className }: { className?: string }) {
  const { data, error, isPending, isFetching, refetch } = useSystemInfo()

  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle>系统状态</CardTitle>
        <CardDescription>后端服务、数据库与存储的运行情况</CardDescription>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => refetch()}
            disabled={isFetching}
          >
            <RefreshCw className={cn(isFetching && "animate-spin")} />
            <span className="sr-only">刷新</span>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col">
        {isPending ? (
          <SystemStatusSkeleton />
        ) : error ? (
          <SystemInfoError
            message={error.message}
            retrying={isFetching}
            onRetry={() => refetch()}
          />
        ) : (
          <SystemStatusDetails info={data} />
        )}
      </CardContent>
    </Card>
  )
}

function SystemStatusDetails({ info }: { info: SystemInfo }) {
  const { database } = info
  const commit = info.commit !== "unknown" ? info.commit.slice(0, 7) : null

  return (
    <dl className="grid gap-4 text-sm">
      <StatusRow label="版本">
        <span className="font-mono">{info.version}</span>
        {commit && (
          <span className="ml-1.5 font-mono text-muted-foreground">
            ({commit})
          </span>
        )}
      </StatusRow>
      <StatusRow label="运行时长">
        {formatDuration(info.uptime_seconds)}
      </StatusRow>
      <StatusRow label="数据库">
        {database.status === "ok" ? (
          <span className="inline-flex items-center gap-2">
            <Badge variant="outline">
              <span className="size-1.5 rounded-full bg-emerald-500" />
              正常
            </Badge>
            {database.server_version && (
              <span className="text-muted-foreground">
                PostgreSQL {database.server_version}
              </span>
            )}
          </span>
        ) : (
          <span className="inline-flex flex-wrap items-center justify-end gap-2">
            <Badge variant="destructive">不可用</Badge>
            {database.error && (
              <span className="text-xs break-all text-muted-foreground">
                {database.error}
              </span>
            )}
          </span>
        )}
      </StatusRow>
      <div className="grid gap-2">
        <dt className="text-muted-foreground">存储空间</dt>
        <dd>
          <StorageUsage storage={info.storage} />
        </dd>
      </div>
    </dl>
  )
}

function StatusRow({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right">{children}</dd>
    </div>
  )
}

/** 存储容量进度条；非 Linux 环境后端无法统计容量时显示“容量未知”。 */
export function StorageUsage({ storage }: { storage: SystemInfo["storage"] }) {
  if (!(storage.total_bytes > 0)) {
    return <p className="text-sm text-muted-foreground">容量未知</p>
  }

  const usedPercent = percentOf(storage.used_bytes, storage.total_bytes)
  return (
    <div className="grid gap-2">
      <Progress
        value={usedPercent}
        aria-label={`已使用 ${usedPercent.toFixed(0)}%`}
      />
      <div className="flex flex-wrap justify-between gap-x-4 text-xs text-muted-foreground">
        <span>
          已用 {formatBytes(storage.used_bytes)}（{usedPercent.toFixed(0)}%）
        </span>
        <span>
          剩余 {formatBytes(storage.free_bytes)} / 共{" "}
          {formatBytes(storage.total_bytes)}
        </span>
      </div>
    </div>
  )
}

export function SystemInfoError({
  message,
  retrying,
  onRetry,
}: {
  message: string
  retrying: boolean
  onRetry: () => void
}) {
  return (
    <Empty className="p-4 md:p-6">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <ServerCrash />
        </EmptyMedia>
        <EmptyTitle className="text-base">无法获取系统状态</EmptyTitle>
        <EmptyDescription>
          <p>{message}</p>
          <p>请确认后端服务已启动，然后重试。</p>
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button
          variant="outline"
          size="sm"
          onClick={onRetry}
          disabled={retrying}
        >
          <RefreshCw className={cn(retrying && "animate-spin")} />
          {retrying ? "正在重试…" : "重试"}
        </Button>
      </EmptyContent>
    </Empty>
  )
}

function SystemStatusSkeleton() {
  return (
    <div className="grid gap-4" aria-busy="true" aria-label="正在加载">
      {[0, 1, 2].map((row) => (
        <div key={row} className="flex justify-between gap-4">
          <Skeleton className="h-4 w-16" />
          <Skeleton className="h-4 w-28" />
        </div>
      ))}
      <Skeleton className="h-4 w-16" />
      <Skeleton className="h-2 w-full" />
    </div>
  )
}
