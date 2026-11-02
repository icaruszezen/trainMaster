import { useMemo, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { CircleAlert, Cpu, Gpu, MemoryStick, Plug, Server } from "lucide-react"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Button } from "@/components/ui/button"
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
import { LoadError } from "@/features/datasets/load-error"
import { computeNodesQuery } from "@/features/training/compute-api"
import {
  formatGpuModels,
  memoryStat,
  summarizeNodes,
  type ComputeSummary,
} from "@/features/training/compute-status"
import {
  ConnectMethodsBrief,
  ConnectNodeDialog,
} from "@/features/training/connect-node"
import { DeleteNodeDialog } from "@/features/training/node-actions"
import { NodeCard } from "@/features/training/node-card"
import { NodeDetailsDialog } from "@/features/training/node-details-dialog"
import { RefreshStatus } from "@/features/training/refresh-status"
import { returnFocus } from "@/features/training/return-focus"
import { ServerCard } from "@/features/training/server-card"
import { useNow } from "@/hooks/use-now"
import { formatNumber, formatPercent } from "@/lib/format"

export function ComputePage() {
  const nodes = useQuery(computeNodesQuery)
  const now = useNow()
  const [connectOpen, setConnectOpen] = useState(false)
  // 对话框只记住节点 id，内容取列表中的最新数据（每 5 秒刷新）。
  const [detailsId, setDetailsId] = useState<number | null>(null)
  const [deletingId, setDeletingId] = useState<number | null>(null)
  // 打开对话框的元素：对话框没有 DialogTrigger，关闭后由 returnFocus 把焦点还给它。
  const opener = useRef<HTMLElement | null>(null)
  const restoreFocus = (event: Event) => returnFocus(event, opener.current)
  const openConnect = () => {
    // 点击或按键打开时焦点在按钮上（“接入节点”、空状态、平台服务器的提示）。
    opener.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null
    setConnectOpen(true)
  }

  const items = nodes.data?.items
  const summary = useMemo(() => (items ? summarizeNodes(items) : null), [items])
  const findNode = (id: number | null) =>
    (id !== null && items?.find((node) => node.id === id)) || null

  return (
    <>
      <PageHeader
        title="计算资源"
        description="训练节点自动上报显卡、CPU 与 CUDA 环境，页面每 5 秒刷新。"
        actions={
          <Button onClick={openConnect}>
            <Plug />
            接入节点
          </Button>
        }
      />

      <ComputeStats summary={summary} pending={nodes.isPending} />

      <ServerCard nodes={items} onConnect={openConnect} />

      <section aria-labelledby="compute-nodes-title" className="grid gap-4">
        <div className="flex min-w-0 items-center justify-between gap-3">
          <h2
            id="compute-nodes-title"
            className="flex items-baseline gap-2 text-lg font-semibold"
          >
            训练节点
            {items && items.length > 0 && (
              <span className="text-sm font-normal text-muted-foreground tabular-nums">
                {formatNumber(items.length)} 个
              </span>
            )}
          </h2>
          <RefreshStatus
            error={nodes.error}
            hasData={items !== undefined}
            updatedAt={nodes.dataUpdatedAt}
            onRefresh={() => nodes.refetch()}
            label="刷新节点列表"
          />
        </div>

        {nodes.isPending ? (
          <NodesSkeleton />
        ) : nodes.data === undefined ? (
          // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
          <LoadError
            title="无法获取训练节点"
            error={nodes.error}
            retrying={nodes.isFetching}
            onRetry={() => nodes.refetch()}
          />
        ) : nodes.data.items.length === 0 ? (
          <NodesEmptyState onConnect={openConnect} />
        ) : (
          <div className="grid gap-4">
            {nodes.data.items.map((node) => (
              <NodeCard
                key={node.id}
                node={node}
                now={now}
                onShowDetails={(trigger) => {
                  opener.current = trigger
                  setDetailsId(node.id)
                }}
                onDelete={(trigger) => {
                  opener.current = trigger
                  setDeletingId(node.id)
                }}
              />
            ))}
          </div>
        )}
      </section>

      <ConnectNodeDialog
        open={connectOpen}
        onOpenChange={setConnectOpen}
        onCloseAutoFocus={restoreFocus}
      />
      <NodeDetailsDialog
        node={findNode(detailsId)}
        now={now}
        onOpenChange={(open) => !open && setDetailsId(null)}
        onCloseAutoFocus={restoreFocus}
      />
      <DeleteNodeDialog
        node={findNode(deletingId)}
        onOpenChange={(open) => !open && setDeletingId(null)}
        onCloseAutoFocus={restoreFocus}
      />
    </>
  )
}

/** 顶部统计：在线节点、GPU、显存、CUDA 环境（只统计在线节点）。 */
function ComputeStats({
  summary,
  pending,
}: {
  summary: ComputeSummary | null
  pending: boolean
}) {
  if (!summary) {
    const value = pending ? <Skeleton className="h-8 w-16" /> : "—"
    const footer = pending ? "正在加载…" : "无法获取数据"
    return (
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard title="在线节点" icon={Server} value={value}>
          {footer}
        </StatCard>
        <StatCard title="GPU" icon={Gpu} value={value}>
          {footer}
        </StatCard>
        <StatCard title="显存" icon={MemoryStick} value={value}>
          {footer}
        </StatCard>
        <StatCard title="CUDA 环境" icon={Cpu} value={value}>
          {footer}
        </StatCard>
      </div>
    )
  }

  const offline = summary.nodeCount - summary.onlineCount
  const models = formatGpuModels(summary.gpuModels)
  const memory = memoryStat(summary)

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="在线节点"
        icon={Server}
        value={
          <>
            {formatNumber(summary.onlineCount)}
            <span className="text-base font-normal text-muted-foreground">
              {" "}
              / {formatNumber(summary.nodeCount)}
            </span>
          </>
        }
      >
        <span>
          {summary.nodeCount === 0
            ? "还没有训练节点"
            : offline > 0
              ? `共 ${formatNumber(summary.nodeCount)} 个，${formatNumber(offline)} 个离线`
              : `共 ${formatNumber(summary.nodeCount)} 个，全部在线`}
        </span>
      </StatCard>
      <StatCard title="GPU" icon={Gpu} value={formatNumber(summary.gpuCount)}>
        <span className="truncate" title={models || undefined}>
          {models ||
            (summary.offlineGpuCount > 0
              ? `${formatNumber(summary.offlineGpuCount)} 块在离线节点上`
              : "没有在线的 GPU")}
        </span>
      </StatCard>
      <StatCard title="显存" icon={MemoryStick} value={memory.value}>
        {memory.percent !== null && (
          <Progress
            value={memory.percent}
            className="h-1.5"
            aria-label={`显存已用 ${formatPercent(memory.percent)}`}
          />
        )}
        <span className="tabular-nums">{memory.detail}</span>
      </StatCard>
      <StatCard
        title="CUDA 环境"
        icon={Cpu}
        value={
          <>
            {formatNumber(summary.readyCount)}
            <span className="text-base font-normal text-muted-foreground">
              {" "}
              个就绪
            </span>
          </>
        }
      >
        <CudaFooter summary={summary} />
      </StatCard>
    </div>
  )
}

function CudaFooter({ summary }: { summary: ComputeSummary }) {
  if (summary.problemCount > 0) {
    return (
      <span className="inline-flex items-center gap-1 text-destructive">
        <CircleAlert className="size-3 shrink-0" aria-hidden />
        有问题 {formatNumber(summary.problemCount)} 个
      </span>
    )
  }
  const parts = [
    summary.detectingCount > 0 &&
      `${formatNumber(summary.detectingCount)} 个正在检测`,
    summary.cpuOnlyCount > 0 &&
      `${formatNumber(summary.cpuOnlyCount)} 个 CPU 模式`,
  ].filter(Boolean)
  if (parts.length > 0) {
    return <span>{parts.join("，")}</span>
  }
  return (
    <span>{summary.onlineCount > 0 ? "在线节点均无问题" : "暂无在线节点"}</span>
  )
}

function NodesEmptyState({ onConnect }: { onConnect: () => void }) {
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-xl">
        <EmptyMedia variant="icon">
          <Server />
        </EmptyMedia>
        <EmptyTitle>还没有训练节点</EmptyTitle>
        <EmptyDescription>
          训练节点是运行在 GPU 机器上的 trainer 服务：它自动识别显卡、CPU
          与内存，准备好 YOLO 训练所需的 PyTorch / CUDA
          环境并自检，定时向平台上报状态。可以在本机或其它机器上接入一个或多个节点。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="max-w-3xl">
        <ConnectMethodsBrief />
        <Button onClick={onConnect}>
          <Plug />
          查看完整接入步骤
        </Button>
      </EmptyContent>
    </Empty>
  )
}

function NodesSkeleton() {
  return (
    <div className="grid gap-4" aria-busy="true" aria-label="正在加载">
      {[0, 1].map((index) => (
        <div key={index} className="grid gap-4 rounded-xl border p-4 sm:p-6">
          <div className="flex items-center gap-3">
            <Skeleton className="size-9 rounded-lg" />
            <div className="grid flex-1 gap-2">
              <Skeleton className="h-5 w-48 max-w-full" />
              <Skeleton className="h-3 w-64 max-w-full" />
            </div>
          </div>
          <Skeleton className="h-4 w-full max-w-xl" />
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {[0, 1, 2, 3].map((cell) => (
              <div key={cell} className="grid gap-2">
                <Skeleton className="h-3 w-20" />
                <Skeleton className="h-1.5 w-full" />
              </div>
            ))}
          </div>
          <Skeleton className="h-16 w-full" />
        </div>
      ))}
    </div>
  )
}
