/**
 * 计算资源模块的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/compute`、`internal/hostinfo` 以及训练节点
 * （`trainer/trainmaster_trainer`）共同遵守的数据契约：字段名、取值保持一致。
 * 约定：未知的值为 `null`（不用 0 或 "" 表示未知），数组字段永远是数组。
 * 时间为 RFC 3339 字符串，字节数为整数，百分比为 0–100，温度为摄氏度，功率为瓦。
 */
import { queryOptions } from "@tanstack/react-query"

import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** 训练节点上的一块显卡（静态信息）。 */
export type NodeGpu = {
  /** NVML 下标，与 metrics.gpus[].index 对应。 */
  index: number
  uuid: string | null
  name: string
  memory_total_bytes: number | null
  /** 计算能力，"主.次"，如 "8.9"。 */
  compute_capability: string | null
  pci_bus_id: string | null
  power_limit_watts: number | null
  mig_enabled: boolean | null
}

export type GpuDetectionMethod = "nvml" | "nvidia-smi" | "none"

/** 训练节点的硬件信息：节点启动和“重新检测”时上报。 */
export type NodeHardware = {
  /** 容器内为镜像的系统（/etc/os-release 的 PRETTY_NAME）。 */
  os: string | null
  kernel: string | null
  arch: string | null
  in_container: boolean
  cpu: {
    model: string | null
    physical_cores: number | null
    logical_cores: number | null
    max_mhz: number | null
    /** cgroup CPU 配额折算的核数；没有限制时为 null。 */
    limit_cores: number | null
  }
  memory: {
    total_bytes: number | null
    /** cgroup 内存上限；没有限制或不小于物理内存时为 null。 */
    limit_bytes: number | null
  }
  /** /dev/shm 的大小。 */
  shm_bytes: number | null
  /** 没有 NVIDIA 驱动或无法探测时为 null。 */
  gpu_driver: {
    driver_version: string
    /** 驱动支持的最高 CUDA 版本。 */
    cuda_driver_version: string | null
    nvml_version: string | null
  } | null
  gpus: NodeGpu[]
  gpu_detection: {
    method: GpuDetectionMethod
    /** 探测失败的原始错误（英文）。 */
    error: string | null
    /** 容器内从 /proc/driver/nvidia/gpus 看到的宿主机显卡数量；读不到时为 null。 */
    host_gpu_count: number | null
  }
}

export type IssueLevel = "error" | "warning" | "info"

/**
 * 环境检测发现的问题；前端只展示 message / hint，不需要认识每个 code（只有
 * `cpu_mode_selected` 用来隐藏平台服务器卡片上的 GPU 提示，见 serverGpuHint）。
 */
export type EnvironmentIssue = {
  level: IssueLevel
  code: string
  message: string
  /** 修复建议，其中用反引号包住的部分是命令。 */
  hint: string | null
}

/**
 * - ready：GPU 与 CUDA 可用且自检通过；
 * - cpu_only：没有可用的 GPU，CPU 自检通过；
 * - error：存在 error 级问题（含自检失败）。
 */
export type EnvironmentStatus = "ready" | "cpu_only" | "error"

export type SelfTestStatus = "passed" | "failed" | "skipped"

/** CUDA / PyTorch / YOLO 环境：节点在子进程中检测后上报。 */
export type NodeEnvironment = {
  status: EnvironmentStatus
  /** 训练将使用的设备。 */
  accelerator: "cuda" | "cpu"
  python_version: string | null
  /** 未安装时为 null。 */
  torch_version: string | null
  /** torch.version.cuda，CPU 构建为 null。 */
  torch_cuda_version: string | null
  cudnn_version: string | null
  /** torch.cuda.get_arch_list()，如 ["sm_80", "sm_86"]。 */
  torch_arch_list: string[]
  torchvision_version: string | null
  ultralytics_version: string | null
  /** 已安装的 PyTorch 变体（如 "cu126"）；未知为 null。 */
  torch_variant: string | null
  /** 按本机驱动与显卡算出的推荐变体。 */
  recommended_variant: string
  cuda_available: boolean
  cuda_device_count: number
  self_test: {
    status: SelfTestStatus
    /** "cuda:0" / "cpu"。 */
    device: string | null
    duration_ms: number | null
    message: string | null
    /** 失败时的原始错误。 */
    detail: string | null
  }
  /** 已按严重程度排序：error → warning → info。 */
  issues: EnvironmentIssue[]
  /** 节点完成检测的时间（节点本机时钟）。 */
  probed_at: string
}

/** 一块显卡的实时指标。 */
export type GpuMetrics = {
  /** 对应 hardware.gpus[].index。 */
  index: number
  utilization_percent: number | null
  memory_used_bytes: number | null
  memory_total_bytes: number | null
  temperature_c: number | null
  power_watts: number | null
  fan_percent: number | null
  /** 使用该卡的计算进程数。 */
  process_count: number | null
}

/** 实时指标：每次心跳上报。 */
export type NodeMetrics = {
  /** 整机 CPU 利用率；节点刚启动时为 null。 */
  cpu_percent: number | null
  /** 1、5、15 分钟平均负载（3 个数）。 */
  load_average: number[] | null
  memory_used_bytes: number | null
  memory_total_bytes: number | null
  /** 节点的数据目录（TM_DATA_DIR）。 */
  disk_path: string | null
  disk_total_bytes: number | null
  disk_free_bytes: number | null
  gpus: GpuMetrics[]
}

/** 历史指标中的一个点。 */
export type NodeHistoryPoint = {
  t: string
  cpu_percent: number | null
  memory_used_bytes: number | null
  gpus: {
    index: number
    utilization_percent: number | null
    memory_used_bytes: number | null
  }[]
}

export type NodeStatus = "online" | "offline"

/** 训练节点。 */
export type ComputeNode = {
  id: number
  /** 节点自己生成并持久化的标识。 */
  node_key: string
  name: string
  hostname: string
  /** 服务端看到的节点 IP，可能为 ""。 */
  address: string
  agent_version: string
  /** 服务端按最后心跳时间计算。 */
  status: NodeStatus
  /**
   * 已请求重新检测，或节点刚刚重新启动（注册时还没有检测结果，environment
   * 保留上次的结果），节点尚未上报新的检测结果。
   */
  probe_pending: boolean
  /** 节点最近一次启动（注册）的时间。 */
  registered_at: string
  /** 最近一次注册 / 心跳的时间（服务端时钟）。 */
  last_seen_at: string
  created_at: string
  hardware: NodeHardware
  /** 尚未完成第一次检测时为 null。 */
  environment: NodeEnvironment | null
  metrics: NodeMetrics | null
  /** 最近的指标，按时间升序，最多 60 个点；只保存在服务端内存中，重启后清空。 */
  history: NodeHistoryPoint[]
}

/** 平台服务器（Go 服务所在机器）检测到的 NVIDIA 显卡。 */
export type ServerGpu = {
  /** 只在 PCI 总线上看到（驱动未加载）时为 null。 */
  name: string | null
  uuid: string | null
  bus_id: string
}

/** 平台服务器自身的资源（`GET /api/v1/compute/server`）。 */
export type ComputeServer = {
  hostname: string
  /** runtime.GOOS / GOARCH，如 "linux" / "amd64"。 */
  os: string
  arch: string
  kernel: string | null
  in_container: boolean
  /** 宿主机开机时长。 */
  uptime_seconds: number | null
  cpu: {
    model: string | null
    logical_cores: number
    physical_cores: number | null
    limit_cores: number | null
    /** 服务端每 5 秒采样；第一次采样前为 null。 */
    usage_percent: number | null
  }
  memory: {
    total_bytes: number | null
    available_bytes: number | null
    used_bytes: number | null
    limit_bytes: number | null
  }
  /** 1、5、15 分钟平均负载（3 个数）。 */
  load_average: number[] | null
  /** 没有检测到 NVIDIA 显卡时为 null。 */
  nvidia: {
    /** 有 PCI 设备但驱动未加载时为 null。 */
    driver_version: string | null
    gpus: ServerGpu[]
  } | null
  node_registration: {
    /** 是否配置了 TM_NODE_TOKEN。 */
    token_required: boolean
    heartbeat_interval_seconds: number
    offline_after_seconds: number
    /**
     * 这台机器上训练节点的模式（make setup 保存在 .env 中的 TM_TRAINER_MODE，平台启动时读取）；
     * 不知道时（不是用 docker compose 启动的）为 null。
     */
    local_trainer_mode: TrainerMode | null
  }
}

/** make setup 的 --mode：auto 按显卡自动选择，gpu / cpu 固定，off 不在这台机器上运行训练节点。 */
export type TrainerMode = "auto" | "gpu" | "cpu" | "off"

const BASE = "/api/v1/compute"

export const nodePath = (id: number) => `${BASE}/nodes/${id}`

/** 节点列表的刷新间隔：与默认心跳间隔（10 秒）配合，状态变化最多延迟一个心跳。 */
export const NODES_REFETCH_MS = 5_000
export const SERVER_REFETCH_MS = 10_000

export const computeKeys = {
  all: ["compute"] as const,
  server: () => [...computeKeys.all, "server"] as const,
  nodes: () => [...computeKeys.all, "nodes"] as const,
}

/** 平台服务器的资源，每 10 秒刷新（CPU 利用率由服务端每 5 秒采样）。 */
export const computeServerQuery = queryOptions({
  queryKey: computeKeys.server(),
  queryFn: ({ signal }) => apiGet<ComputeServer>(`${BASE}/server`, { signal }),
  refetchInterval: SERVER_REFETCH_MS,
})

/** 训练节点列表（在线在前，再按名称），每 5 秒刷新。 */
export const computeNodesQuery = queryOptions({
  queryKey: computeKeys.nodes(),
  queryFn: ({ signal }) =>
    apiGet<ListResponse<ComputeNode>>(`${BASE}/nodes`, { signal }),
  refetchInterval: NODES_REFETCH_MS,
})

/** 请求节点在下一次心跳时重新检测硬件与 CUDA 环境；节点离线时 409。 */
export function probeNode(id: number) {
  return apiSend<ComputeNode>("POST", `${nodePath(id)}/probe`)
}

/** 删除节点记录与其历史指标；节点在线时 409。 */
export function deleteNode(id: number) {
  return apiSend("DELETE", nodePath(id))
}

/** 在后台重新请求节点列表（写操作失败时状态可能已经变化，如节点刚刚上线 / 离线）。 */
export function refreshNodes() {
  void queryClient.invalidateQueries({ queryKey: computeKeys.nodes() })
}

/** 用写操作返回的最新节点替换列表缓存中的旧值，然后在后台刷新列表。 */
export function cacheNode(node: ComputeNode) {
  queryClient.setQueryData<ListResponse<ComputeNode>>(
    computeKeys.nodes(),
    (list) =>
      list && {
        ...list,
        items: list.items.map((item) => (item.id === node.id ? node : item)),
      }
  )
  refreshNodes()
}

/** 节点删除后立即从列表缓存中去掉，然后在后台刷新列表。 */
export function forgetNode(id: number) {
  queryClient.setQueryData<ListResponse<ComputeNode>>(
    computeKeys.nodes(),
    (list) => {
      if (!list || !list.items.some((item) => item.id === id)) {
        return list
      }
      return {
        items: list.items.filter((item) => item.id !== id),
        total: Math.max(0, list.total - 1),
      }
    }
  )
  refreshNodes()
}
