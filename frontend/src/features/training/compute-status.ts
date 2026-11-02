/**
 * 训练节点的状态判定与汇总统计（纯函数，便于测试）。
 *
 * 汇总只统计在线节点：离线节点的显卡不能用来训练，它的环境信息也可能已经过时。
 */
import type {
  ComputeNode,
  ComputeServer,
  EnvironmentIssue,
  GpuMetrics,
  IssueLevel,
  SelfTestStatus,
} from "@/features/training/compute-api"
import { shortenGpuName } from "@/features/training/compute-format"
import { formatRelativeTime } from "@/lib/format"

export function isOnline(node: Pick<ComputeNode, "status">) {
  return node.status === "online"
}

/** 节点已请求重新检测、且还能完成检测（在线）。 */
export function isProbing(node: Pick<ComputeNode, "status" | "probe_pending">) {
  return node.probe_pending && isOnline(node)
}

/** 加速器标记：“GPU × 2” / “仅 CPU”。 */
export function acceleratorLabel(node: Pick<ComputeNode, "hardware">) {
  const count = node.hardware.gpus.length
  return count > 0 ? `GPU × ${count}` : "仅 CPU"
}

export type EnvironmentTone = "ready" | "cpu" | "error" | "pending" | "unknown"

export type EnvironmentBadge = {
  tone: EnvironmentTone
  label: string
  /** 悬停时的补充说明。 */
  description: string
}

/** 环境标记：CUDA 就绪 / CPU 模式 / 环境异常 / 正在检测。 */
export function environmentBadge(
  node: Pick<ComputeNode, "status" | "environment">
): EnvironmentBadge {
  const env = node.environment
  if (!env) {
    return isOnline(node)
      ? {
          tone: "pending",
          label: "正在检测",
          description: "节点正在检测 CUDA 环境并运行 YOLO 自检，通常需要几十秒",
        }
      : {
          tone: "unknown",
          label: "未检测",
          description: "节点离线前没有完成 CUDA 环境检测",
        }
  }
  switch (env.status) {
    case "ready":
      return {
        tone: "ready",
        label: "CUDA 就绪",
        description: "GPU 与 CUDA 可用，YOLO 自检通过",
      }
    case "cpu_only":
      return {
        tone: "cpu",
        label: "CPU 模式",
        description: "没有可用的 GPU，将使用 CPU 训练（速度慢很多）",
      }
    default:
      return {
        tone: "error",
        label: "环境异常",
        description: "CUDA 环境存在问题，查看下方的提示修复",
      }
  }
}

const levelOrder: Record<IssueLevel, number> = { error: 0, warning: 1, info: 2 }

/** 按严重程度排序问题（error → warning → info），同级保持原顺序。 */
export function sortIssues(issues: readonly EnvironmentIssue[]) {
  return issues
    .map((issue, position) => ({ issue, position }))
    .sort(
      (a, b) =>
        (levelOrder[a.issue.level] ?? 3) - (levelOrder[b.issue.level] ?? 3) ||
        a.position - b.position
    )
    .map(({ issue }) => issue)
}

export type EnvironmentFact = { label: string; value: string }

/**
 * 节点卡片上的环境信息行：PyTorch、CUDA、cuDNN、Ultralytics、驱动、Python。
 * 缺失的项不显示；驱动来自硬件信息，环境还没有检测完成时也能显示。
 */
export function environmentFacts(
  node: Pick<ComputeNode, "environment" | "hardware">
): EnvironmentFact[] {
  const env = node.environment
  const candidates: [string, string | null | undefined][] = [
    ["PyTorch", env?.torch_version],
    ["CUDA", env?.torch_cuda_version],
    ["cuDNN", env?.cudnn_version],
    ["Ultralytics", env?.ultralytics_version],
    ["驱动", node.hardware.gpu_driver?.driver_version],
    ["Python", env?.python_version],
  ]
  return candidates
    .filter((entry): entry is [string, string] => Boolean(entry[1]?.trim()))
    .map(([label, value]) => ({ label, value: value.trim() }))
}

export const selfTestLabels: Record<SelfTestStatus, string> = {
  passed: "通过",
  failed: "失败",
  skipped: "未运行",
}

/** 按下标找到显卡的实时指标。 */
export function gpuMetricsOf(
  node: Pick<ComputeNode, "metrics">,
  index: number
): GpuMetrics | undefined {
  return node.metrics?.gpus.find((gpu) => gpu.index === index)
}

/** 状态文字：“在线”，或“离线 · 最后心跳 3 分钟前”。 */
export function nodeStatusText(
  node: Pick<ComputeNode, "status" | "last_seen_at">,
  now: number
) {
  if (isOnline(node)) {
    return "在线"
  }
  return `离线 · 最后心跳 ${formatRelativeTime(node.last_seen_at, now)}`
}

export type GpuModelCount = { name: string; count: number }

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

/** 按简化后的型号统计显卡数量，数量多的在前，同样多的按名称。 */
export function groupGpuModels(
  names: readonly (string | null | undefined)[]
): GpuModelCount[] {
  const counts = new Map<string, number>()
  for (const name of names) {
    const short = shortenGpuName(name)
    counts.set(short, (counts.get(short) ?? 0) + 1)
  }
  return [...counts]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || nameCollator.compare(a.name, b.name))
}

/** 型号汇总，如 “2 × RTX 4090、1 × A100”；型号太多时只列出前几种。 */
export function formatGpuModels(models: readonly GpuModelCount[], max = 3) {
  const listed = models
    .slice(0, max)
    .map((model) => `${model.count} × ${model.name}`)
    .join("、")
  return models.length > max ? `${listed} 等 ${models.length} 种` : listed
}

export type ComputeSummary = {
  nodeCount: number
  onlineCount: number
  /** 在线节点上的显卡数。 */
  gpuCount: number
  /** 离线节点上的显卡数（不计入 gpuCount）。 */
  offlineGpuCount: number
  /** 在线显卡按型号汇总。 */
  gpuModels: GpuModelCount[]
  /** 在线显卡的显存合计（未知的不计入）。 */
  memoryTotalBytes: number
  /** 有实时数据的在线显卡的已用 / 总显存；都没有实时数据时为 null。 */
  memoryUsage: { used: number; total: number } | null
  /** 在线节点中 CUDA 就绪的数量。 */
  readyCount: number
  /** 在线节点中环境异常的数量。 */
  problemCount: number
  /** 在线节点中只能用 CPU 训练的数量。 */
  cpuOnlyCount: number
  /** 在线节点中还没有完成第一次检测的数量。 */
  detectingCount: number
}

function knownBytes(value: number | null | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
}

/** 页面顶部统计卡片与工作台摘要使用的汇总数据。 */
export function summarizeNodes(nodes: readonly ComputeNode[]): ComputeSummary {
  const summary: ComputeSummary = {
    nodeCount: nodes.length,
    onlineCount: 0,
    gpuCount: 0,
    offlineGpuCount: 0,
    gpuModels: [],
    memoryTotalBytes: 0,
    memoryUsage: null,
    readyCount: 0,
    problemCount: 0,
    cpuOnlyCount: 0,
    detectingCount: 0,
  }
  const names: string[] = []
  let used = 0
  let usedTotal = 0
  let hasUsage = false

  for (const node of nodes) {
    const { gpus } = node.hardware
    if (!isOnline(node)) {
      summary.offlineGpuCount += gpus.length
      continue
    }

    summary.onlineCount += 1
    summary.gpuCount += gpus.length
    switch (node.environment?.status) {
      case undefined:
        summary.detectingCount += 1
        break
      case "ready":
        summary.readyCount += 1
        break
      case "cpu_only":
        summary.cpuOnlyCount += 1
        break
      default:
        summary.problemCount += 1
    }

    for (const gpu of gpus) {
      names.push(gpu.name)
      const metrics = gpuMetricsOf(node, gpu.index)
      const total = knownBytes(gpu.memory_total_bytes)
        ? gpu.memory_total_bytes
        : metrics?.memory_total_bytes
      if (knownBytes(total)) {
        summary.memoryTotalBytes += total
        if (knownBytes(metrics?.memory_used_bytes) && total > 0) {
          used += Math.min(metrics.memory_used_bytes, total)
          usedTotal += total
          hasUsage = true
        }
      }
    }
  }

  summary.gpuModels = groupGpuModels(names)
  summary.memoryUsage = hasUsage ? { used, total: usedTotal } : null
  return summary
}

/** 工作台上的一行摘要，如 “1 个节点在线 · 2 块 GPU（RTX 4090）”。 */
export function dashboardComputeText(summary: ComputeSummary): string {
  if (summary.nodeCount === 0) {
    return "还没有训练节点"
  }
  if (summary.onlineCount === 0) {
    return `${summary.nodeCount} 个节点均离线`
  }

  const parts = [`${summary.onlineCount} 个节点在线`]
  if (summary.gpuCount > 0) {
    const names = summary.gpuModels.map((model) => model.name)
    const models =
      names.length > 2 ? `${names.slice(0, 2).join("、")} 等` : names.join("、")
    parts.push(`${summary.gpuCount} 块 GPU（${models}）`)
  } else {
    parts.push("仅 CPU")
  }
  if (summary.problemCount > 0) {
    parts.push(`${summary.problemCount} 个环境异常`)
  }
  return parts.join(" · ")
}

export type ServerGpuHint = {
  /** driver_missing：只在 PCI 总线上看到显卡；no_gpu_node：驱动正常但没有 GPU 节点在线。 */
  kind: "driver_missing" | "no_gpu_node"
  gpuCount: number
}

/**
 * 平台服务器上有 NVIDIA 显卡、但没有在线的 GPU 训练节点时，提示如何接入。
 * 已有 GPU 节点在线（可能就在这台机器上）时不提示。
 */
export function serverGpuHint(
  server: Pick<ComputeServer, "nvidia">,
  nodes: readonly Pick<ComputeNode, "status" | "hardware">[]
): ServerGpuHint | null {
  const gpuCount = server.nvidia?.gpus.length ?? 0
  if (!server.nvidia || gpuCount === 0) {
    return null
  }
  if (nodes.some((node) => isOnline(node) && node.hardware.gpus.length > 0)) {
    return null
  }
  return {
    kind:
      server.nvidia.driver_version === null ? "driver_missing" : "no_gpu_node",
    gpuCount,
  }
}
