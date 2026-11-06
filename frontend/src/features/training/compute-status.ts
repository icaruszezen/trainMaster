/**
 * 训练节点的状态判定与汇总统计（纯函数，便于测试）。
 *
 * 汇总只统计在线节点：离线节点的显卡不能用来训练，它的环境信息也可能已经过时。
 */
import type {
  ComputeNode,
  ComputeServer,
  EnvironmentIssue,
  EnvironmentStatus,
  GpuMetrics,
  IssueLevel,
  SelfTestStatus,
} from "@/features/training/compute-api"
import { shortenGpuName } from "@/features/training/compute-format"
import {
  formatBytes,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"

export function isOnline(node: Pick<ComputeNode, "status">) {
  return node.status === "online"
}

/**
 * 节点正在重新检测、且还能完成检测（在线）：请求了重新检测，或节点刚刚重新
 * 启动（注册时还没有检测结果，服务端保留上次的结果并标记为待检测）。
 */
export function isProbing(node: Pick<ComputeNode, "status" | "probe_pending">) {
  return node.probe_pending && isOnline(node)
}

/** 正在重新检测，页面上显示的还是上次的检测结果。 */
export function isRedetecting(
  node: Pick<ComputeNode, "status" | "probe_pending" | "environment">
) {
  return node.environment !== null && isProbing(node)
}

/**
 * Windows 训练节点（直接安装，以计划任务运行）：没有 /dev/shm，停止与删除的
 * 命令也不同。旧版节点不上报 platform（null），按 Linux 处理。
 */
export function isWindowsNode(node: Pick<ComputeNode, "hardware">) {
  return node.hardware.platform === "windows"
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

/**
 * 环境标记：CUDA 就绪 / CPU 模式 / 环境异常 / 正在检测。重新检测时显示
 * “正在重新检测”，上次的结果放在说明中：节点重启后（如按提示修复后）不再
 * 把上次的“环境异常”当作当前状态显示。
 */
export function environmentBadge(
  node: Pick<ComputeNode, "status" | "environment" | "probe_pending">
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
  const last = statusBadge(env.status)
  if (isProbing(node)) {
    return {
      tone: "pending",
      label: "正在重新检测",
      description: `节点正在重新检测 CUDA 环境并运行 YOLO 自检，通常需要几十秒；上次检测的结果为“${last.label}”`,
    }
  }
  return last
}

function statusBadge(status: EnvironmentStatus): EnvironmentBadge {
  switch (status) {
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
  /** 在线显卡的显存合计（未知的不计入）；全部未知时为 0。 */
  memoryTotalBytes: number
  /**
   * 有实时数据的在线显卡的已用 / 总显存与显卡数；都没有实时数据时为 null。
   * 只有部分显卡有实时数据时 total 小于 memoryTotalBytes。
   */
  memoryUsage: { used: number; total: number; gpus: number } | null
  /** 在线节点中 CUDA 就绪的数量（不含正在重新检测的）。 */
  readyCount: number
  /** 在线节点中环境异常的数量（不含正在重新检测的）。 */
  problemCount: number
  /** 在线节点中只能用 CPU 训练的数量（不含正在重新检测的）。 */
  cpuOnlyCount: number
  /**
   * 在线节点中正在检测的数量：还没有完成第一次检测，或正在重新检测（节点重新
   * 启动、请求了重新检测；与节点卡片上的“正在重新检测”一致，不按上次的结果统计）。
   */
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
  let usedGpus = 0

  for (const node of nodes) {
    const { gpus } = node.hardware
    if (!isOnline(node)) {
      summary.offlineGpuCount += gpus.length
      continue
    }

    summary.onlineCount += 1
    summary.gpuCount += gpus.length
    switch (isProbing(node) ? undefined : node.environment?.status) {
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
          usedGpus += 1
        }
      }
    }
  }

  summary.gpuModels = groupGpuModels(names)
  summary.memoryUsage =
    usedGpus > 0 ? { used, total: usedTotal, gpus: usedGpus } : null
  return summary
}

export type MemoryStat = {
  /** 大号数值：在线显卡的显存合计，全部未知时为“—”。 */
  value: string
  /** 进度条的比例；没有与合计对应的实时数据时为 null（不显示进度条）。 */
  percent: number | null
  /** 底部说明。 */
  detail: string
}

/**
 * 顶部“显存”统计：合计与已用使用同一组显卡。只有部分显卡有实时数据时不显示
 * 比例（它只对这几块卡成立，与合计放在一起会误导），说明有几块卡有数据。
 */
export function memoryStat(
  summary: Pick<ComputeSummary, "gpuCount" | "memoryTotalBytes" | "memoryUsage">
): MemoryStat {
  const total = summary.memoryTotalBytes
  const usage = summary.memoryUsage
  const value = total > 0 ? formatBytes(total) : "—"
  if (!usage) {
    return {
      value,
      percent: null,
      detail:
        summary.gpuCount === 0
          ? "在线节点的显存合计"
          : total > 0
            ? "暂无实时数据"
            : "显存大小未知",
    }
  }
  if (usage.total < total) {
    return {
      value,
      percent: null,
      detail: `已用 ${formatBytes(usage.used)}（${formatNumber(usage.gpus)} 块有实时数据）`,
    }
  }
  const percent = percentOf(usage.used, usage.total)
  return {
    value,
    percent,
    detail: `已用 ${formatBytes(usage.used)}（${formatPercent(percent)}）`,
  }
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

/** 训练节点在 TM_TRAINER_MODE=cpu（用户选择了 CPU 模式）的 GPU 机器上上报的问题。 */
export const CPU_MODE_SELECTED = "cpu_mode_selected"

/** 有在线的 GPU 训练节点（可能就在平台服务器上）。 */
export function hasOnlineGpuNode(
  nodes: readonly Pick<ComputeNode, "status" | "hardware">[]
) {
  return nodes.some((node) => isOnline(node) && node.hardware.gpus.length > 0)
}

/**
 * CUDA 版 PyTorch 最低需要的 NVIDIA 驱动：x86_64 上是
 * trainer/trainmaster_trainer/torch_variants.txt 中最低的 min_driver（cu118）；
 * ARM 服务器上只有 cu130 有 ARM 版，需要 580。驱动低于它时 make setup 只能选择
 * CPU 训练节点，也不安装 NVIDIA Container Toolkit。修改变体表时同时核对这里。
 */
export const MIN_CUDA_DRIVER = "450.80.02"
export const MIN_CUDA_DRIVER_ARM = "580"

/** 平台服务器架构（runtime.GOARCH）上 CUDA 版 PyTorch 最低需要的驱动版本。 */
export function minCudaDriver(arch: string | null | undefined) {
  return arch === "arm64" || arch === "aarch64"
    ? MIN_CUDA_DRIVER_ARM
    : MIN_CUDA_DRIVER
}

function versionParts(version: string) {
  const text = version.trim()
  return /^\d+(?:\.\d+)*$/.test(text) ? text.split(".").map(Number) : null
}

/**
 * 点分版本号按数字比较：`versionBelow("440.100", "450.80.02")` 为 true，
 * 缺少的部分按 0（"580" 与 "580.0" 相同）。无法解析的版本号返回 false（不认为过旧）。
 */
export function versionBelow(version: string, minimum: string) {
  const a = versionParts(version)
  const b = versionParts(minimum)
  if (!a || !b) {
    return false
  }
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const diff = (a[i] ?? 0) - (b[i] ?? 0)
    if (diff !== 0) {
      return diff < 0
    }
  }
  return false
}

export type ServerGpuHint =
  | {
      /** driver_missing：只在 PCI 总线上看到显卡；no_gpu_node：驱动正常但没有 GPU 节点在线。 */
      kind: "driver_missing" | "no_gpu_node"
      gpuCount: number
    }
  | {
      /** 驱动版本低于任何 CUDA 版 PyTorch 的要求：先升级驱动。 */
      kind: "driver_too_old"
      gpuCount: number
      driverVersion: string
      /** 这台机器的架构上 CUDA 版 PyTorch 最低需要的驱动版本。 */
      minDriver: string
    }

/**
 * 平台服务器上有 NVIDIA 显卡、但没有在线的 GPU 训练节点时，提示如何接入。
 * 已有 GPU 节点在线（可能就在这台机器上）、这台机器的训练节点有意关闭或选择了
 * CPU 模式（平台的 TM_TRAINER_MODE 为 off / cpu，或在线节点上报
 * `cpu_mode_selected`）时不提示。驱动过旧时先提示升级驱动。
 */
export function serverGpuHint(
  server: Pick<ComputeServer, "nvidia" | "arch"> & {
    node_registration: Pick<
      ComputeServer["node_registration"],
      "local_trainer_mode"
    >
  },
  nodes: readonly Pick<ComputeNode, "status" | "hardware" | "environment">[]
): ServerGpuHint | null {
  const gpuCount = server.nvidia?.gpus.length ?? 0
  if (!server.nvidia || gpuCount === 0 || hasOnlineGpuNode(nodes)) {
    return null
  }
  const mode = server.node_registration.local_trainer_mode
  if (mode === "off" || mode === "cpu") {
    return null
  }
  const cpuModeSelected = nodes.some(
    (node) =>
      isOnline(node) &&
      node.environment?.issues.some((issue) => issue.code === CPU_MODE_SELECTED)
  )
  if (cpuModeSelected) {
    return null
  }
  const driverVersion = server.nvidia.driver_version
  if (driverVersion === null) {
    return { kind: "driver_missing", gpuCount }
  }
  const minDriver = minCudaDriver(server.arch)
  if (versionBelow(driverVersion, minDriver)) {
    return { kind: "driver_too_old", gpuCount, driverVersion, minDriver }
  }
  return { kind: "no_gpu_node", gpuCount }
}
