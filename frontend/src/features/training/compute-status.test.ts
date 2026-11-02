import { describe, expect, it } from "vitest"

import type {
  ComputeNode,
  ComputeServer,
  EnvironmentIssue,
  NodeEnvironment,
  NodeGpu,
  TrainerMode,
} from "@/features/training/compute-api"
import {
  acceleratorLabel,
  dashboardComputeText,
  environmentBadge,
  environmentFacts,
  formatGpuModels,
  gpuMetricsOf,
  groupGpuModels,
  hasOnlineGpuNode,
  isProbing,
  isRedetecting,
  memoryStat,
  minCudaDriver,
  nodeStatusText,
  serverGpuHint,
  sortIssues,
  summarizeNodes,
  versionBelow,
} from "@/features/training/compute-status"

const GiB = 1024 ** 3

function gpu(index: number, overrides: Partial<NodeGpu> = {}): NodeGpu {
  return {
    index,
    uuid: `GPU-${index}`,
    name: "NVIDIA GeForce RTX 4090",
    memory_total_bytes: 24 * GiB,
    compute_capability: "8.9",
    pci_bus_id: `00000000:0${index + 1}:00.0`,
    power_limit_watts: 450,
    mig_enabled: false,
    ...overrides,
  }
}

function environment(
  overrides: Partial<NodeEnvironment> = {}
): NodeEnvironment {
  return {
    status: "ready",
    accelerator: "cuda",
    python_version: "3.12.7",
    torch_version: "2.9.1+cu126",
    torch_cuda_version: "12.6",
    cudnn_version: "9.10.2",
    torch_arch_list: ["sm_80", "sm_86", "sm_89", "sm_90"],
    torchvision_version: "0.24.1+cu126",
    ultralytics_version: "8.4.198",
    torch_variant: "cu126",
    recommended_variant: "cu126",
    cuda_available: true,
    cuda_device_count: 1,
    self_test: {
      status: "passed",
      device: "cuda:0",
      duration_ms: 1830,
      message: "YOLO11n 前向推理正常",
      detail: null,
    },
    issues: [],
    probed_at: "2026-11-02T07:30:00Z",
    ...overrides,
  }
}

function node(overrides: Partial<ComputeNode> = {}): ComputeNode {
  return {
    id: 1,
    node_key: "key-1",
    name: "gpu-01",
    hostname: "gpu-01",
    address: "192.168.1.20",
    agent_version: "0.1.0",
    status: "online",
    probe_pending: false,
    registered_at: "2026-11-02T07:00:00Z",
    last_seen_at: "2026-11-02T07:31:00Z",
    created_at: "2026-11-01T07:00:00Z",
    hardware: {
      os: "Ubuntu 22.04.4 LTS",
      kernel: "5.15.0-105-generic",
      arch: "x86_64",
      in_container: true,
      cpu: {
        model: "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz",
        physical_cores: 28,
        logical_cores: 56,
        max_mhz: 3100,
        limit_cores: null,
      },
      memory: { total_bytes: 128 * GiB, limit_bytes: null },
      shm_bytes: 8 * GiB,
      gpu_driver: {
        driver_version: "550.54.15",
        cuda_driver_version: "12.4",
        nvml_version: "12.550.54.15",
      },
      gpus: [gpu(0)],
      gpu_detection: { method: "nvml", error: null, host_gpu_count: 1 },
    },
    environment: environment(),
    metrics: null,
    history: [],
    ...overrides,
  }
}

function withGpus(gpus: NodeGpu[], overrides: Partial<ComputeNode> = {}) {
  const base = node(overrides)
  return { ...base, hardware: { ...base.hardware, gpus } }
}

function issue(
  level: EnvironmentIssue["level"],
  code: string
): EnvironmentIssue {
  return { level, code, message: code, hint: null }
}

describe("node badges", () => {
  it("labels the accelerator", () => {
    expect(acceleratorLabel(withGpus([gpu(0), gpu(1)]))).toBe("GPU × 2")
    expect(acceleratorLabel(withGpus([]))).toBe("仅 CPU")
  })

  it("derives the environment badge from the status", () => {
    expect(environmentBadge(node())).toMatchObject({
      tone: "ready",
      label: "CUDA 就绪",
    })
    expect(
      environmentBadge(
        node({ environment: environment({ status: "cpu_only" }) })
      )
    ).toMatchObject({ tone: "cpu", label: "CPU 模式" })
    expect(
      environmentBadge(node({ environment: environment({ status: "error" }) }))
    ).toMatchObject({ tone: "error", label: "环境异常" })
  })

  it("shows detection in progress only while the node is online", () => {
    expect(environmentBadge(node({ environment: null }))).toMatchObject({
      tone: "pending",
      label: "正在检测",
    })
    expect(
      environmentBadge(node({ environment: null, status: "offline" }))
    ).toMatchObject({ tone: "unknown", label: "未检测" })
  })

  it("shows re-detection over the previous result", () => {
    // After a restart the server keeps the old environment and marks a
    // probe as pending until the node reports the new one.
    const restarted = node({
      probe_pending: true,
      environment: environment({ status: "error" }),
    })
    expect(isRedetecting(restarted)).toBe(true)
    const badge = environmentBadge(restarted)
    expect(badge).toMatchObject({ tone: "pending", label: "正在重新检测" })
    expect(badge.description).toContain("上次检测的结果为“环境异常”")

    // Not yet detected at all: the plain detecting badge.
    expect(
      isRedetecting(node({ probe_pending: true, environment: null }))
    ).toBe(false)
    expect(
      environmentBadge(node({ probe_pending: true, environment: null }))
    ).toMatchObject({ tone: "pending", label: "正在检测" })

    // Offline nodes cannot detect: they keep their last result.
    const offline = node({
      status: "offline",
      probe_pending: true,
      environment: environment({ status: "error" }),
    })
    expect(isRedetecting(offline)).toBe(false)
    expect(environmentBadge(offline)).toMatchObject({
      tone: "error",
      label: "环境异常",
    })
  })

  it("treats a pending probe on an offline node as not probing", () => {
    expect(isProbing(node({ probe_pending: true }))).toBe(true)
    expect(isProbing(node({ probe_pending: true, status: "offline" }))).toBe(
      false
    )
    expect(isProbing(node())).toBe(false)
  })

  it("describes the online status and the last heartbeat", () => {
    const now = Date.parse("2026-11-02T07:34:30Z")
    expect(nodeStatusText(node(), now)).toBe("在线")
    expect(nodeStatusText(node({ status: "offline" }), now)).toBe(
      "离线 · 最后心跳 3 分钟前"
    )
  })
})

describe("environmentFacts", () => {
  it("lists the known versions in a fixed order", () => {
    expect(environmentFacts(node())).toEqual([
      { label: "PyTorch", value: "2.9.1+cu126" },
      { label: "CUDA", value: "12.6" },
      { label: "cuDNN", value: "9.10.2" },
      { label: "Ultralytics", value: "8.4.198" },
      { label: "驱动", value: "550.54.15" },
      { label: "Python", value: "3.12.7" },
    ])
  })

  it("skips missing values", () => {
    const cpu = node({
      environment: environment({
        torch_cuda_version: null,
        cudnn_version: null,
        ultralytics_version: "  ",
      }),
    })
    cpu.hardware.gpu_driver = null
    expect(environmentFacts(cpu).map((fact) => fact.label)).toEqual([
      "PyTorch",
      "Python",
    ])
  })

  it("shows the driver before the environment has been probed", () => {
    expect(environmentFacts(node({ environment: null }))).toEqual([
      { label: "驱动", value: "550.54.15" },
    ])
  })
})

describe("sortIssues", () => {
  it("orders by severity and keeps the order within a level", () => {
    const sorted = sortIssues([
      issue("info", "no_gpu"),
      issue("warning", "shm_small"),
      issue("error", "arch_unsupported"),
      issue("info", "mig_enabled"),
      issue("error", "self_test_failed"),
    ])
    expect(sorted.map((item) => item.code)).toEqual([
      "arch_unsupported",
      "self_test_failed",
      "shm_small",
      "no_gpu",
      "mig_enabled",
    ])
  })

  it("does not modify the input", () => {
    const issues = [issue("info", "a"), issue("error", "b")]
    sortIssues(issues)
    expect(issues.map((item) => item.code)).toEqual(["a", "b"])
  })
})

describe("gpu models", () => {
  it("groups by short name, most common first", () => {
    const models = groupGpuModels([
      "NVIDIA A100-SXM4-80GB",
      "NVIDIA GeForce RTX 4090",
      "NVIDIA GeForce RTX 4090",
      "NVIDIA GeForce RTX 3090",
    ])
    expect(models).toEqual([
      { name: "RTX 4090", count: 2 },
      { name: "A100-SXM4-80GB", count: 1 },
      { name: "RTX 3090", count: 1 },
    ])
    expect(formatGpuModels(models)).toBe(
      "2 × RTX 4090、1 × A100-SXM4-80GB、1 × RTX 3090"
    )
    expect(formatGpuModels(models, 2)).toBe(
      "2 × RTX 4090、1 × A100-SXM4-80GB 等 3 种"
    )
    expect(formatGpuModels([])).toBe("")
  })
})

describe("summarizeNodes", () => {
  it("counts only online nodes towards GPUs and environments", () => {
    const nodes = [
      withGpus([gpu(0), gpu(1)], {
        id: 1,
        metrics: {
          cpu_percent: 12,
          load_average: null,
          memory_used_bytes: null,
          memory_total_bytes: null,
          disk_path: null,
          disk_total_bytes: null,
          disk_free_bytes: null,
          gpus: [
            {
              index: 0,
              utilization_percent: 90,
              memory_used_bytes: 12 * GiB,
              memory_total_bytes: 24 * GiB,
              temperature_c: 70,
              power_watts: 300,
              fan_percent: 50,
              process_count: 1,
            },
          ],
        },
      }),
      withGpus([gpu(0, { name: "NVIDIA GeForce RTX 5090" })], {
        id: 2,
        environment: environment({ status: "error" }),
      }),
      withGpus([gpu(0)], {
        id: 3,
        status: "offline",
        environment: environment({ status: "error" }),
      }),
      withGpus([], {
        id: 4,
        environment: environment({ status: "cpu_only" }),
      }),
      withGpus([], { id: 5, environment: null }),
    ]

    expect(summarizeNodes(nodes)).toEqual({
      nodeCount: 5,
      onlineCount: 4,
      gpuCount: 3,
      offlineGpuCount: 1,
      gpuModels: [
        { name: "RTX 4090", count: 2 },
        { name: "RTX 5090", count: 1 },
      ],
      memoryTotalBytes: 72 * GiB,
      // 只有一块卡有实时数据：比例按这块卡计算。
      memoryUsage: { used: 12 * GiB, total: 24 * GiB, gpus: 1 },
      readyCount: 1,
      problemCount: 1,
      cpuOnlyCount: 1,
      detectingCount: 1,
    })
  })

  it("falls back to the metrics total and ignores unknown sizes", () => {
    const summary = summarizeNodes([
      withGpus(
        [
          gpu(0, { memory_total_bytes: null }),
          gpu(1, { memory_total_bytes: null }),
        ],
        {
          metrics: {
            cpu_percent: null,
            load_average: null,
            memory_used_bytes: null,
            memory_total_bytes: null,
            disk_path: null,
            disk_total_bytes: null,
            disk_free_bytes: null,
            gpus: [
              {
                index: 0,
                utilization_percent: null,
                memory_used_bytes: 30 * GiB,
                memory_total_bytes: 16 * GiB,
                temperature_c: null,
                power_watts: null,
                fan_percent: null,
                process_count: null,
              },
            ],
          },
        }
      ),
    ])
    expect(summary.memoryTotalBytes).toBe(16 * GiB)
    // 已用超过总量（异常数据）时按总量计。
    expect(summary.memoryUsage).toEqual({
      used: 16 * GiB,
      total: 16 * GiB,
      gpus: 1,
    })
  })

  it("counts a node that is re-detecting as detecting, not by its last result", () => {
    // After a restart the node registers without an environment: the server
    // keeps the last one and marks the node probe_pending (badge 正在重新检测).
    const redetecting = (
      status: NodeEnvironment["status"],
      overrides: Partial<ComputeNode> = {}
    ) =>
      withGpus([gpu(0)], {
        probe_pending: true,
        environment: environment({ status }),
        ...overrides,
      })

    expect(
      summarizeNodes([
        redetecting("error", { id: 1 }),
        redetecting("ready", { id: 2 }),
        redetecting("cpu_only", { id: 3 }),
      ])
    ).toMatchObject({
      onlineCount: 3,
      gpuCount: 3,
      readyCount: 0,
      problemCount: 0,
      cpuOnlyCount: 0,
      detectingCount: 3,
    })
    expect(
      dashboardComputeText(summarizeNodes([redetecting("error")]))
    ).not.toContain("环境异常")
    // An offline node cannot finish the detection: it is not counted online.
    expect(
      summarizeNodes([redetecting("error", { status: "offline" })])
    ).toMatchObject({
      onlineCount: 0,
      problemCount: 0,
      detectingCount: 0,
      offlineGpuCount: 1,
    })
  })

  it("returns empty counts without nodes", () => {
    expect(summarizeNodes([])).toMatchObject({
      nodeCount: 0,
      onlineCount: 0,
      gpuCount: 0,
      gpuModels: [],
      memoryTotalBytes: 0,
      memoryUsage: null,
    })
  })

  it("finds the metrics of a GPU by index", () => {
    const metrics = {
      index: 1,
      utilization_percent: 5,
      memory_used_bytes: null,
      memory_total_bytes: null,
      temperature_c: null,
      power_watts: null,
      fan_percent: null,
      process_count: null,
    }
    const subject = node({
      metrics: {
        cpu_percent: null,
        load_average: null,
        memory_used_bytes: null,
        memory_total_bytes: null,
        disk_path: null,
        disk_total_bytes: null,
        disk_free_bytes: null,
        gpus: [metrics],
      },
    })
    expect(gpuMetricsOf(subject, 1)).toBe(metrics)
    expect(gpuMetricsOf(subject, 0)).toBeUndefined()
    expect(gpuMetricsOf(node(), 0)).toBeUndefined()
  })
})

describe("memoryStat", () => {
  const live = (index: number, used: number) => ({
    index,
    utilization_percent: null,
    memory_used_bytes: used,
    memory_total_bytes: null,
    temperature_c: null,
    power_watts: null,
    fan_percent: null,
    process_count: null,
  })
  const withMetrics = (gpus: NodeGpu[], metrics: ReturnType<typeof live>[]) =>
    withGpus(gpus, {
      metrics: {
        cpu_percent: null,
        load_average: null,
        memory_used_bytes: null,
        memory_total_bytes: null,
        disk_path: null,
        disk_total_bytes: null,
        disk_free_bytes: null,
        gpus: metrics,
      },
    })

  it("shows the usage against the total it is shown with", () => {
    const stat = memoryStat(
      summarizeNodes([
        withMetrics([gpu(0), gpu(1)], [live(0, 12 * GiB), live(1, 12 * GiB)]),
      ])
    )
    expect(stat).toEqual({
      value: "48 GB",
      percent: 50,
      detail: "已用 24 GB（50%）",
    })
  })

  it("does not put a partial usage under the full total", () => {
    // Two 24 GB cards, only one with live data: 50% would be wrong for 48 GB.
    const stat = memoryStat(
      summarizeNodes([withMetrics([gpu(0), gpu(1)], [live(0, 12 * GiB)])])
    )
    expect(stat).toEqual({
      value: "48 GB",
      percent: null,
      detail: "已用 12 GB（1 块有实时数据）",
    })
  })

  it("shows a dash instead of 0 B when no size is known", () => {
    const stat = memoryStat(
      summarizeNodes([
        withGpus([
          gpu(0, { memory_total_bytes: null }),
          gpu(1, { memory_total_bytes: null }),
        ]),
      ])
    )
    expect(stat).toEqual({ value: "—", percent: null, detail: "显存大小未知" })
  })

  it("explains an empty or not yet measured total", () => {
    expect(memoryStat(summarizeNodes([]))).toEqual({
      value: "—",
      percent: null,
      detail: "在线节点的显存合计",
    })
    expect(memoryStat(summarizeNodes([withGpus([gpu(0)])]))).toEqual({
      value: "24 GB",
      percent: null,
      detail: "暂无实时数据",
    })
  })
})

describe("dashboardComputeText", () => {
  it("summarizes online nodes and GPU models", () => {
    expect(
      dashboardComputeText(summarizeNodes([withGpus([gpu(0), gpu(1)])]))
    ).toBe("1 个节点在线 · 2 块 GPU（RTX 4090）")
  })

  it("lists at most two models", () => {
    const text = dashboardComputeText(
      summarizeNodes([
        withGpus([
          gpu(0),
          gpu(1, { name: "NVIDIA A100-PCIE-40GB" }),
          gpu(2, { name: "NVIDIA GeForce RTX 3090" }),
        ]),
      ])
    )
    expect(text).toBe("1 个节点在线 · 3 块 GPU（A100-PCIE-40GB、RTX 3090 等）")
  })

  it("mentions CPU-only nodes and problems", () => {
    expect(
      dashboardComputeText(
        summarizeNodes([
          withGpus([], { environment: environment({ status: "error" }) }),
        ])
      )
    ).toBe("1 个节点在线 · 仅 CPU · 1 个环境异常")
  })

  it("handles no nodes and all nodes offline", () => {
    expect(dashboardComputeText(summarizeNodes([]))).toBe("还没有训练节点")
    expect(
      dashboardComputeText(
        summarizeNodes([
          node({ status: "offline" }),
          node({ id: 2, status: "offline" }),
        ])
      )
    ).toBe("2 个节点均离线")
  })
})

describe("serverGpuHint", () => {
  const nvidia = {
    driver_version: "550.54.15",
    gpus: [
      {
        name: "NVIDIA GeForce RTX 4090",
        uuid: "GPU-1",
        bus_id: "0000:01:00.0",
      },
      {
        name: "NVIDIA GeForce RTX 4090",
        uuid: "GPU-2",
        bus_id: "0000:02:00.0",
      },
    ],
  }

  const server = (
    overrides: Partial<Pick<ComputeServer, "nvidia" | "arch">> = {},
    localTrainerMode: TrainerMode | null = "auto"
  ) => ({
    nvidia,
    arch: "amd64",
    ...overrides,
    node_registration: { local_trainer_mode: localTrainerMode },
  })

  it("hints when the server has GPUs but no GPU node is online", () => {
    expect(serverGpuHint(server(), [])).toEqual({
      kind: "no_gpu_node",
      gpuCount: 2,
    })
    expect(
      serverGpuHint(server(), [
        withGpus([gpu(0)], { status: "offline" }),
        withGpus([]),
      ])
    ).toEqual({ kind: "no_gpu_node", gpuCount: 2 })
    // Outside docker compose the platform does not know the local mode.
    expect(serverGpuHint(server({}, null), [])).toEqual({
      kind: "no_gpu_node",
      gpuCount: 2,
    })
  })

  it("asks for a driver when the GPUs are only visible on the PCI bus", () => {
    expect(
      serverGpuHint(
        server({
          nvidia: {
            driver_version: null,
            gpus: [{ name: null, uuid: null, bus_id: "0000:01:00.0" }],
          },
        }),
        []
      )
    ).toEqual({ kind: "driver_missing", gpuCount: 1 })
  })

  it("asks for a newer driver when no CUDA PyTorch supports it", () => {
    const oldDriver = (driver_version: string, arch = "amd64") =>
      server({ nvidia: { ...nvidia, driver_version }, arch })

    // Below cu118's 450.80.02: make setup chooses CPU and installs no toolkit.
    expect(serverGpuHint(oldDriver("440.100"), [])).toEqual({
      kind: "driver_too_old",
      gpuCount: 2,
      driverVersion: "440.100",
      minDriver: "450.80.02",
    })
    // It replaces the no_gpu_node hint, also next to an online CPU node.
    expect(serverGpuHint(oldDriver("450.80.01"), [withGpus([])])).toMatchObject(
      { kind: "driver_too_old" }
    )
    expect(serverGpuHint(oldDriver("450.80.02"), [])).toMatchObject({
      kind: "no_gpu_node",
    })
    expect(serverGpuHint(oldDriver("470.256.02"), [])).toMatchObject({
      kind: "no_gpu_node",
    })
    // ARM servers only have the CUDA 13 build, which needs 580.
    expect(serverGpuHint(oldDriver("575.57.08", "arm64"), [])).toEqual({
      kind: "driver_too_old",
      gpuCount: 2,
      driverVersion: "575.57.08",
      minDriver: "580",
    })
    expect(serverGpuHint(oldDriver("580.65.06", "arm64"), [])).toMatchObject({
      kind: "no_gpu_node",
    })
    // An online GPU node elsewhere still hides it.
    expect(serverGpuHint(oldDriver("440.100"), [withGpus([gpu(0)])])).toBeNull()
  })

  it("stays quiet when a GPU node is online or the server has no GPU", () => {
    expect(serverGpuHint(server(), [withGpus([gpu(0)])])).toBeNull()
    expect(serverGpuHint(server({ nvidia: null }), [])).toBeNull()
    expect(
      serverGpuHint(
        server({ nvidia: { driver_version: "550.54.15", gpus: [] } }),
        []
      )
    ).toBeNull()
  })

  it("stays quiet when this machine's trainer is off or in CPU mode", () => {
    // make up SETUP_ARGS="--mode off": no node runs here, so none can report
    // cpu_mode_selected; the platform reads the mode from its own .env.
    for (const mode of ["off", "cpu"] as const) {
      expect(serverGpuHint(server({}, mode), [])).toBeNull()
      expect(
        serverGpuHint(
          server({ nvidia: { ...nvidia, driver_version: null } }, mode),
          []
        )
      ).toBeNull()
      expect(
        serverGpuHint(
          server({ nvidia: { ...nvidia, driver_version: "440.100" } }, mode),
          []
        )
      ).toBeNull()
    }
    expect(serverGpuHint(server({}, "gpu"), [])).toEqual({
      kind: "no_gpu_node",
      gpuCount: 2,
    })
  })

  it("stays quiet when an online node runs in the chosen CPU mode", () => {
    const cpuMode = (overrides: Partial<ComputeNode> = {}) =>
      withGpus([], {
        environment: environment({
          status: "cpu_only",
          issues: [issue("info", "cpu_mode_selected")],
        }),
        ...overrides,
      })

    expect(serverGpuHint(server(), [cpuMode()])).toBeNull()
    // Also with a driver too old for CUDA PyTorch: CPU was chosen on purpose.
    expect(
      serverGpuHint(
        server({ nvidia: { ...nvidia, driver_version: "440.100" } }),
        [cpuMode()]
      )
    ).toBeNull()
    // Only online nodes count: an old offline record does not hide the hint.
    expect(serverGpuHint(server(), [cpuMode({ status: "offline" })])).toEqual({
      kind: "no_gpu_node",
      gpuCount: 2,
    })
    // A CPU node that did not choose CPU mode (e.g. no toolkit) keeps it.
    expect(
      serverGpuHint(server(), [
        withGpus([], {
          environment: environment({
            status: "cpu_only",
            issues: [issue("warning", "gpu_not_passed_through")],
          }),
        }),
      ])
    ).toEqual({ kind: "no_gpu_node", gpuCount: 2 })
  })
})

describe("driver versions", () => {
  it("compares dotted versions numerically", () => {
    expect(versionBelow("440.100", "450.80.02")).toBe(true)
    expect(versionBelow("450.80.01", "450.80.02")).toBe(true)
    expect(versionBelow("450.80.02", "450.80.02")).toBe(false)
    // 450.100 > 450.80 even though "1" < "8" as text.
    expect(versionBelow("450.100.01", "450.80.02")).toBe(false)
    expect(versionBelow("1000.1", "580")).toBe(false)
    expect(versionBelow("575.57.08", "580")).toBe(true)
    expect(versionBelow("580", "580.0")).toBe(false)
    expect(versionBelow(" 440.100 ", "450.80.02")).toBe(true)
  })

  it("does not call an unparsable version too old", () => {
    expect(versionBelow("", "450.80.02")).toBe(false)
    expect(versionBelow("unknown", "450.80.02")).toBe(false)
    expect(versionBelow("440.100-beta", "450.80.02")).toBe(false)
  })

  it("needs the CUDA 13 driver on ARM servers", () => {
    expect(minCudaDriver("amd64")).toBe("450.80.02")
    expect(minCudaDriver("arm64")).toBe("580")
    expect(minCudaDriver("aarch64")).toBe("580")
    expect(minCudaDriver(null)).toBe("450.80.02")
  })
})

describe("hasOnlineGpuNode", () => {
  it("looks for an online node with GPUs", () => {
    expect(hasOnlineGpuNode([withGpus([gpu(0)])])).toBe(true)
    expect(hasOnlineGpuNode([withGpus([gpu(0)], { status: "offline" })])).toBe(
      false
    )
    expect(hasOnlineGpuNode([withGpus([])])).toBe(false)
    expect(hasOnlineGpuNode([])).toBe(false)
  })
})
