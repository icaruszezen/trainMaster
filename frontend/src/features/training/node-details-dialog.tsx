import type { ReactNode } from "react"

import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import { CopyButton } from "@/features/training/command-block"
import type {
  ComputeNode,
  EnvironmentStatus,
  GpuDetectionMethod,
  IssueLevel,
  NodeEnvironment,
} from "@/features/training/compute-api"
import {
  formatBytesValue,
  formatCores,
  formatCpuCores,
  formatMhz,
  formatMilliseconds,
  formatWatts,
} from "@/features/training/compute-format"
import {
  environmentBadge,
  isOnline,
  isRedetecting,
  nodeStatusText,
  selfTestLabels,
  sortIssues,
} from "@/features/training/compute-status"
import { HintText } from "@/features/training/hint-text"
import { EnvironmentStatusBadge } from "@/features/training/node-card"
import { formatDateTime, formatRelativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

const detectionLabels: Record<GpuDetectionMethod, string> = {
  nvml: "NVML",
  "nvidia-smi": "nvidia-smi",
  none: "未检测到显卡",
}

const statusLabels: Record<EnvironmentStatus, string> = {
  ready: "CUDA 就绪",
  cpu_only: "CPU 模式",
  error: "环境异常",
}

const levelLabels: Record<IssueLevel, string> = {
  error: "错误",
  warning: "警告",
  info: "提示",
}

/** 节点详情：完整的硬件、环境与自检信息，便于排查问题。 */
export function NodeDetailsDialog({
  node,
  now,
  onOpenChange,
  onCloseAutoFocus,
}: {
  /** 列表中的最新数据；节点被删除后为 null，对话框随之关闭。 */
  node: ComputeNode | null
  now: number
  onOpenChange: (open: boolean) => void
  /** 关闭后焦点去哪里（对话框没有触发按钮，见 returnFocus）。 */
  onCloseAutoFocus?: (event: Event) => void
}) {
  return (
    <Dialog open={node !== null} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        {node && <NodeDetails node={node} now={now} />}
      </DialogContent>
    </Dialog>
  )
}

function NodeDetails({ node, now }: { node: ComputeNode; now: number }) {
  const { hardware, environment: env } = node

  return (
    <>
      <DialogHeader>
        <DialogTitle className="pr-6 break-all">{node.name}</DialogTitle>
        <DialogDescription className="flex flex-wrap items-center justify-center gap-2 sm:justify-start">
          <span>{nodeStatusText(node, now)}</span>
          <EnvironmentStatusBadge badge={environmentBadge(node)} />
        </DialogDescription>
      </DialogHeader>

      <div className="grid min-w-0 gap-6">
        <Section title="节点">
          <Fact label="节点标识" wide>
            <span className="inline-flex min-w-0 items-center gap-1">
              <span className="font-mono break-all">{node.node_key}</span>
              <CopyButton text={node.node_key} label="复制节点标识" />
            </span>
          </Fact>
          <Fact label="主机名">{node.hostname || "—"}</Fact>
          <Fact label="地址">{node.address || "—"}</Fact>
          <Fact label="节点版本">{node.agent_version || "—"}</Fact>
          <Fact label="加入时间">{formatDateTime(node.created_at)}</Fact>
          <Fact label="最近启动">
            {formatDateTime(node.registered_at, { withSeconds: true })}
          </Fact>
          <Fact label="最后心跳">
            {formatDateTime(node.last_seen_at, { withSeconds: true })}
          </Fact>
        </Section>

        <Section title="系统">
          <Fact label="操作系统">{hardware.os ?? "—"}</Fact>
          <Fact label="内核">
            <Mono>{hardware.kernel}</Mono>
          </Fact>
          <Fact label="架构">
            <Mono>{hardware.arch}</Mono>
          </Fact>
          <Fact label="运行方式">
            {hardware.in_container ? "容器内" : "直接运行"}
          </Fact>
          <Fact label="CPU" wide>
            {hardware.cpu.model ?? "—"}
          </Fact>
          <Fact label="核数">{formatCpuCores(hardware.cpu) ?? "—"}</Fact>
          <Fact label="最高频率">{formatMhz(hardware.cpu.max_mhz)}</Fact>
          {hardware.cpu.limit_cores !== null && (
            <Fact label="CPU 限制">
              {formatCores(hardware.cpu.limit_cores)} 核
            </Fact>
          )}
          <Fact label="内存">
            {formatBytesValue(hardware.memory.total_bytes)}
          </Fact>
          {hardware.memory.limit_bytes !== null && (
            <Fact label="内存限制">
              {formatBytesValue(hardware.memory.limit_bytes)}
            </Fact>
          )}
          <Fact label="/dev/shm">{formatBytesValue(hardware.shm_bytes)}</Fact>
        </Section>

        <Section title="显卡与驱动">
          <Fact label="驱动版本">
            <Mono>{hardware.gpu_driver?.driver_version}</Mono>
          </Fact>
          <Fact label="驱动支持的 CUDA">
            <Mono>{hardware.gpu_driver?.cuda_driver_version}</Mono>
          </Fact>
          <Fact label="NVML 版本">
            <Mono>{hardware.gpu_driver?.nvml_version}</Mono>
          </Fact>
          <Fact label="检测方式">
            {detectionLabels[hardware.gpu_detection.method] ??
              hardware.gpu_detection.method}
          </Fact>
          {hardware.gpu_detection.error && (
            <Fact label="检测错误" wide>
              <Mono>{hardware.gpu_detection.error}</Mono>
            </Fact>
          )}
          {hardware.gpu_detection.host_gpu_count !== null && (
            <Fact label="宿主机显卡">
              {hardware.gpu_detection.host_gpu_count} 块
            </Fact>
          )}
        </Section>

        {hardware.gpus.length > 0 && (
          <section className="grid min-w-0 gap-2">
            <SectionHeading>显卡（{hardware.gpus.length} 块）</SectionHeading>
            <ul className="grid gap-2">
              {hardware.gpus.map((gpu) => (
                <li key={gpu.index} className="rounded-lg border p-3">
                  <p className="mb-2 text-sm font-medium break-all">
                    <span className="mr-1.5 font-mono text-muted-foreground">
                      #{gpu.index}
                    </span>
                    {gpu.name}
                  </p>
                  <dl className="grid gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2">
                    <Fact label="显存" compact>
                      {formatBytesValue(gpu.memory_total_bytes)}
                    </Fact>
                    <Fact label="计算能力" compact>
                      <Mono>{gpu.compute_capability}</Mono>
                    </Fact>
                    <Fact label="功率上限" compact>
                      {formatWatts(gpu.power_limit_watts)}
                    </Fact>
                    <Fact label="MIG" compact>
                      {gpu.mig_enabled === null
                        ? "—"
                        : gpu.mig_enabled
                          ? "已开启"
                          : "未开启"}
                    </Fact>
                    <Fact label="PCI 地址" compact>
                      <Mono>{gpu.pci_bus_id}</Mono>
                    </Fact>
                    <Fact label="UUID" compact wide>
                      <Mono>{gpu.uuid}</Mono>
                    </Fact>
                  </dl>
                </li>
              ))}
            </ul>
          </section>
        )}

        {env ? (
          <EnvironmentDetails
            env={env}
            now={now}
            previous={isRedetecting(node)}
          />
        ) : (
          <Section title="CUDA 环境">
            <p className="text-sm text-muted-foreground sm:col-span-2">
              {isOnline(node)
                ? "正在检测 CUDA 环境，完成后这里显示 PyTorch、CUDA 与自检结果。"
                : "节点离线前没有完成 CUDA 环境检测。"}
            </p>
          </Section>
        )}
      </div>
    </>
  )
}

function EnvironmentDetails({
  env,
  now,
  previous,
}: {
  env: NodeEnvironment
  now: number
  /** 正在重新检测：这些是上次检测的结果。 */
  previous: boolean
}) {
  const test = env.self_test
  const variantDiffers =
    env.torch_variant !== null && env.torch_variant !== env.recommended_variant
  const issues = sortIssues(env.issues)
  const suffix = previous ? "（上次检测）" : ""

  return (
    <>
      {previous && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner className="size-3.5 shrink-0" aria-hidden />
          正在重新检测 CUDA 环境…（通常需要几十秒），下面是上次检测的结果
        </p>
      )}
      <Section title={`CUDA 环境${suffix}`}>
        <Fact label="状态">{statusLabels[env.status] ?? env.status}</Fact>
        <Fact label="训练设备">
          {env.accelerator === "cuda" ? "GPU（CUDA）" : "CPU"}
        </Fact>
        <Fact label="CUDA 可用">
          {env.cuda_available ? `是（${env.cuda_device_count} 块 GPU）` : "否"}
        </Fact>
        <Fact label="Python">
          <Mono>{env.python_version}</Mono>
        </Fact>
        <Fact label="PyTorch">
          <Mono>{env.torch_version}</Mono>
        </Fact>
        <Fact label="PyTorch 的 CUDA">
          <Mono>{env.torch_cuda_version}</Mono>
        </Fact>
        <Fact label="cuDNN">
          <Mono>{env.cudnn_version}</Mono>
        </Fact>
        <Fact label="torchvision">
          <Mono>{env.torchvision_version}</Mono>
        </Fact>
        <Fact label="Ultralytics">
          <Mono>{env.ultralytics_version}</Mono>
        </Fact>
        <Fact label="已安装变体">
          <Mono>{env.torch_variant}</Mono>
        </Fact>
        <Fact label="推荐变体">
          <span className={cn(variantDiffers && "font-medium")}>
            <Mono>{env.recommended_variant}</Mono>
            {variantDiffers && (
              <span className="ml-1.5 text-muted-foreground">
                （与已安装的不同）
              </span>
            )}
          </span>
        </Fact>
        <Fact label="检测时间" wide>
          {formatDateTime(env.probed_at, { withSeconds: true })}
          <span className="ml-1.5 text-muted-foreground">
            （{formatRelativeTime(env.probed_at, now)}）
          </span>
        </Fact>
        <div className="grid min-w-0 gap-1.5 sm:col-span-2">
          <dt className="text-muted-foreground">PyTorch 支持的架构</dt>
          <dd className="flex flex-wrap gap-1">
            {env.torch_arch_list.length > 0 ? (
              env.torch_arch_list.map((arch) => (
                <Badge
                  key={arch}
                  variant="outline"
                  className="font-mono font-normal"
                >
                  {arch}
                </Badge>
              ))
            ) : (
              <span>—</span>
            )}
          </dd>
        </div>
      </Section>

      <Section title={`自检${suffix}`}>
        <Fact label="结果">{selfTestLabels[test.status] ?? test.status}</Fact>
        <Fact label="设备">
          <Mono>{test.device}</Mono>
        </Fact>
        <Fact label="耗时">{formatMilliseconds(test.duration_ms)}</Fact>
        <Fact label="说明">{test.message ?? "—"}</Fact>
        {test.detail && (
          <div className="grid min-w-0 gap-1.5 sm:col-span-2">
            <dt className="flex items-center justify-between gap-2 text-muted-foreground">
              错误详情
              <CopyButton text={test.detail} label="复制错误详情" />
            </dt>
            <dd>
              <pre className="max-h-60 overflow-auto rounded-md border bg-muted/50 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                {test.detail}
              </pre>
            </dd>
          </div>
        )}
      </Section>

      {issues.length > 0 && (
        <section className="grid min-w-0 gap-2">
          <SectionHeading>
            {previous ? "上次检测到的问题" : "检测到的问题"}（{issues.length}）
          </SectionHeading>
          <ul className="grid gap-2">
            {issues.map((issue, index) => (
              <li
                key={`${issue.code}-${index}`}
                className="grid min-w-0 gap-1 rounded-lg border p-3 text-sm"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Badge
                    variant={
                      issue.level === "error" ? "destructive" : "outline"
                    }
                  >
                    {levelLabels[issue.level] ?? issue.level}
                  </Badge>
                  <code className="font-mono text-xs text-muted-foreground">
                    {issue.code}
                  </code>
                </div>
                <p className="break-words">{issue.message}</p>
                {issue.hint && (
                  <div className="text-xs text-muted-foreground">
                    <HintText hint={issue.hint} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  )
}

function SectionHeading({ children }: { children: ReactNode }) {
  return <h3 className="text-sm font-semibold">{children}</h3>
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid min-w-0 gap-2">
      <SectionHeading>{title}</SectionHeading>
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        {children}
      </dl>
    </section>
  )
}

function Fact({
  label,
  compact = false,
  wide = false,
  children,
}: {
  label: string
  /** 显卡小卡片中使用较窄的标签列。 */
  compact?: boolean
  /** 较长的值（标识、型号、错误）占满两列。 */
  wide?: boolean
  children: ReactNode
}) {
  return (
    <div
      className={cn(
        "grid min-w-0 grid-cols-[7rem_1fr] items-baseline gap-3 sm:grid-cols-[8rem_1fr]",
        compact && "grid-cols-[4.5rem_1fr] sm:grid-cols-[4.5rem_1fr]",
        wide && "sm:col-span-2"
      )}
    >
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

/** 等宽显示的值（版本号、标识等）；未知时显示“—”。 */
function Mono({ children }: { children: string | null | undefined }) {
  if (!children) {
    return <span>—</span>
  }
  return (
    <span className="font-mono text-[0.8125rem] break-all">{children}</span>
  )
}
