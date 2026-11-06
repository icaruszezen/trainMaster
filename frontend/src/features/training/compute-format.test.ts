import { describe, expect, it } from "vitest"

import {
  formatBytesValue,
  formatClockTime,
  formatCores,
  formatCpuCores,
  formatLoadAverage,
  formatMhz,
  formatMilliseconds,
  formatPercentValue,
  formatPower,
  formatTemperature,
  formatUsage,
  formatWatts,
  hintLayout,
  hintSegments,
  serverUrlPlaceholderReason,
  shortenCpuModel,
  shortenGpuName,
  suggestServerUrl,
} from "@/features/training/compute-format"

describe("shortenGpuName", () => {
  it("drops the NVIDIA and GeForce prefixes", () => {
    expect(shortenGpuName("NVIDIA GeForce RTX 4090")).toBe("RTX 4090")
    expect(shortenGpuName("NVIDIA GeForce RTX 5090 D")).toBe("RTX 5090 D")
    expect(shortenGpuName("NVIDIA A100-SXM4-80GB")).toBe("A100-SXM4-80GB")
    expect(shortenGpuName("GeForce GTX 1080 Ti")).toBe("GTX 1080 Ti")
    expect(shortenGpuName("NVIDIA RTX A6000")).toBe("RTX A6000")
  })

  it("keeps names without the prefixes", () => {
    expect(shortenGpuName("Tesla V100-PCIE-32GB")).toBe("Tesla V100-PCIE-32GB")
    expect(shortenGpuName("Quadro RTX 8000")).toBe("Quadro RTX 8000")
  })

  it("normalizes whitespace and case of the prefixes", () => {
    expect(shortenGpuName("  nvidia   geforce  RTX 3090 ")).toBe("RTX 3090")
  })

  it("keeps a name that is only a prefix", () => {
    expect(shortenGpuName("NVIDIA")).toBe("NVIDIA")
  })

  it("names unknown cards", () => {
    expect(shortenGpuName(null)).toBe("未知显卡")
    expect(shortenGpuName("   ")).toBe("未知显卡")
  })
})

describe("shortenCpuModel", () => {
  it("drops trademarks, frequency and processor suffixes", () => {
    expect(shortenCpuModel("Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz")).toBe(
      "Intel Xeon Gold 6330"
    )
    expect(shortenCpuModel("Intel(R) Core(TM) i9-14900K")).toBe(
      "Intel Core i9-14900K"
    )
    expect(shortenCpuModel("AMD Ryzen 9 7950X 16-Core Processor")).toBe(
      "AMD Ryzen 9 7950X"
    )
    expect(shortenCpuModel("AMD EPYC 7763 64-Core Processor")).toBe(
      "AMD EPYC 7763"
    )
    expect(shortenCpuModel("Neoverse-N1")).toBe("Neoverse-N1")
  })

  it("names unknown models", () => {
    expect(shortenCpuModel(null)).toBe("未知 CPU")
  })
})

describe("power, temperature and frequency", () => {
  it("formats watts", () => {
    expect(formatWatts(312.4)).toBe("312 W")
    expect(formatWatts(45.67)).toBe("45.7 W")
    expect(formatWatts(8)).toBe("8 W")
    expect(formatWatts(0)).toBe("0 W")
    expect(formatWatts(null)).toBe("—")
    expect(formatWatts(Number.NaN)).toBe("—")
    expect(formatWatts(-1)).toBe("—")
  })

  it("formats power against the limit", () => {
    expect(formatPower(312.4, 450)).toBe("312 / 450 W")
    expect(formatPower(45.67, 70)).toBe("45.7 / 70 W")
    expect(formatPower(null, 450)).toBe("— / 450 W")
    expect(formatPower(312.4, null)).toBe("312 W")
    expect(formatPower(null, null)).toBe("—")
  })

  it("formats temperatures in °C", () => {
    expect(formatTemperature(71)).toBe("71 °C")
    expect(formatTemperature(70.6)).toBe("71 °C")
    expect(formatTemperature(-5)).toBe("-5 °C")
    expect(formatTemperature(null)).toBe("—")
    expect(formatTemperature(Number.POSITIVE_INFINITY)).toBe("—")
  })

  it("formats MHz as GHz above 1000", () => {
    expect(formatMhz(3100)).toBe("3.1 GHz")
    expect(formatMhz(2450)).toBe("2.45 GHz")
    expect(formatMhz(3000)).toBe("3 GHz")
    expect(formatMhz(800)).toBe("800 MHz")
    expect(formatMhz(0)).toBe("—")
    expect(formatMhz(null)).toBe("—")
  })
})

describe("percentages, bytes and cores", () => {
  it("formats known percentages and caps them at 100", () => {
    expect(formatPercentValue(87.4)).toBe("87%")
    expect(formatPercentValue(0)).toBe("0%")
    expect(formatPercentValue(100.3)).toBe("100%")
    expect(formatPercentValue(null)).toBe("—")
    expect(formatPercentValue(undefined)).toBe("—")
  })

  it("formats usage against a total", () => {
    expect(formatUsage(19 * 1024 ** 3, 24 * 1024 ** 3)).toBe("19 GB / 24 GB")
    expect(formatUsage(null, 24 * 1024 ** 3)).toBe("— / 24 GB")
    expect(formatUsage(512, null)).toBe("512 B")
    expect(formatUsage(null, null)).toBe("—")
    expect(formatBytesValue(8 * 1024 ** 3)).toBe("8 GB")
    expect(formatBytesValue(null)).toBe("—")
  })

  it("formats fractional core limits", () => {
    expect(formatCores(8)).toBe("8")
    expect(formatCores(2.5)).toBe("2.5")
    expect(formatCores(0.25)).toBe("0.25")
  })

  it("describes physical and logical cores", () => {
    expect(formatCpuCores({ physical_cores: 28, logical_cores: 56 })).toBe(
      "28 核 / 56 线程"
    )
    expect(formatCpuCores({ physical_cores: 8, logical_cores: 8 })).toBe("8 核")
    expect(formatCpuCores({ physical_cores: null, logical_cores: 16 })).toBe(
      "16 线程"
    )
    expect(formatCpuCores({ physical_cores: 4, logical_cores: null })).toBe(
      "4 核"
    )
    expect(
      formatCpuCores({ physical_cores: null, logical_cores: null })
    ).toBeNull()
  })

  it("formats the load average", () => {
    expect(formatLoadAverage([0.52, 0.61, 0.7])).toBe("0.52 / 0.61 / 0.70")
    expect(formatLoadAverage([])).toBeNull()
    expect(formatLoadAverage(null)).toBeNull()
  })
})

describe("formatMilliseconds", () => {
  it("picks a unit by magnitude", () => {
    expect(formatMilliseconds(350)).toBe("350 毫秒")
    expect(formatMilliseconds(1830)).toBe("1.8 秒")
    expect(formatMilliseconds(12_000)).toBe("12 秒")
    expect(formatMilliseconds(65_000)).toBe("1 分钟 5 秒")
    expect(formatMilliseconds(null)).toBe("—")
  })
})

describe("formatClockTime", () => {
  it("shows only the time for today and the full date otherwise", () => {
    const now = new Date(2026, 10, 2, 15, 0, 0).getTime()
    expect(formatClockTime(new Date(2026, 10, 2, 9, 5, 7), now)).toBe(
      "09:05:07"
    )
    expect(formatClockTime(new Date(2026, 10, 1, 23, 59, 59), now)).toBe(
      "2026-11-01 23:59:59"
    )
  })
})

describe("hintSegments", () => {
  it("splits commands quoted with backticks", () => {
    expect(
      hintSegments("在宿主机运行 `make setup`，然后 `docker compose up -d`。")
    ).toEqual([
      { text: "在宿主机运行 ", code: false },
      { text: "make setup", code: true },
      { text: "，然后 ", code: false },
      { text: "docker compose up -d", code: true },
      { text: "。", code: false },
    ])
  })

  it("shows each command of a multi-command hint as code", () => {
    expect(
      hintSegments("`make setup` 然后 `docker compose up -d --build trainer`")
    ).toEqual([
      { text: "make setup", code: true },
      { text: " 然后 ", code: false },
      { text: "docker compose up -d --build trainer", code: true },
    ])
    // Quotes inside a command stay part of it.
    expect(
      hintSegments(
        '改用 PyTorch 变体 cu128 重新构建镜像：`make setup SETUP_ARGS="--variant cu128"` 然后 `docker compose up -d --build trainer-cpu`'
      )
    ).toEqual([
      { text: "改用 PyTorch 变体 cu128 重新构建镜像：", code: false },
      { text: 'make setup SETUP_ARGS="--variant cu128"', code: true },
      { text: " 然后 ", code: false },
      { text: "docker compose up -d --build trainer-cpu", code: true },
    ])
    expect(
      hintSegments(
        '安装并配置 NVIDIA Container Toolkit（需要 sudo），然后以 GPU 模式启动：`make setup SETUP_ARGS="--install-toolkit"`，再按它最后输出的服务名启动（通常是 `docker compose up -d --build trainer`）'
      ).filter((segment) => segment.code)
    ).toEqual([
      { text: 'make setup SETUP_ARGS="--install-toolkit"', code: true },
      { text: "docker compose up -d --build trainer", code: true },
    ])
  })

  it("keeps a hint that is one quoted command as a single command", () => {
    // HintText shows it as a command block with a copy button.
    expect(
      hintSegments(
        "`docker compose exec trainer python -m trainmaster_trainer selftest`"
      )
    ).toEqual([
      {
        text: "docker compose exec trainer python -m trainmaster_trainer selftest",
        code: true,
      },
    ])
    expect(
      hintSegments("`sudo trainer/scripts/install.sh --variant cu128`")
    ).toEqual([
      { text: "sudo trainer/scripts/install.sh --variant cu128", code: true },
    ])
  })

  it("keeps an unclosed backtick as text", () => {
    expect(hintSegments("运行 `make setup` 或 `make")).toEqual([
      { text: "运行 ", code: false },
      { text: "make setup", code: true },
      { text: " 或 `make", code: false },
    ])
  })

  it("treats a hint without Chinese text as one command", () => {
    expect(hintSegments(" TM_TORCH_VARIANT=cu128 make up ")).toEqual([
      { text: "TM_TORCH_VARIANT=cu128 make up", code: true },
    ])
    expect(hintSegments("重启机器或重新加载驱动")).toEqual([
      { text: "重启机器或重新加载驱动", code: false },
    ])
  })

  it("drops empty parts", () => {
    expect(hintSegments("``")).toEqual([])
    expect(hintSegments("  ")).toEqual([])
    expect(hintSegments("`make up`")).toEqual([{ text: "make up", code: true }])
  })
})

describe("hintLayout", () => {
  const layout = (hint: string) => hintLayout(hintSegments(hint))

  it("puts a trailing command on its own line with a copy button", () => {
    expect(
      layout(
        '安装并配置 NVIDIA Container Toolkit（需要 sudo），然后以 GPU 模式启动：`make up SETUP_ARGS="--install-toolkit"`'
      )
    ).toEqual({
      text: [
        {
          text: "安装并配置 NVIDIA Container Toolkit（需要 sudo），然后以 GPU 模式启动：",
          code: false,
        },
      ],
      command: 'make up SETUP_ARGS="--install-toolkit"',
    })
    expect(
      layout(
        "改用 PyTorch 变体 cu128 重新安装（在 trainMaster 仓库目录中运行）：`sudo trainer/scripts/install.sh --variant cu128`"
      ).command
    ).toBe("sudo trainer/scripts/install.sh --variant cu128")
    // Windows nodes suggest a PowerShell command: quotes, brackets and
    // parentheses stay as they are.
    expect(
      layout(
        "改用 PyTorch 变体 cu126 重新安装（在管理员 PowerShell 中运行）：`& ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -Variant cu126`"
      )
    ).toEqual({
      text: [
        {
          text: "改用 PyTorch 变体 cu126 重新安装（在管理员 PowerShell 中运行）：",
          code: false,
        },
      ],
      command:
        "& ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -Variant cu126",
    })
    // Trailing spaces after the command do not count as text after it.
    expect(layout("然后运行 `make up`  ")).toEqual({
      text: [{ text: "然后运行 ", code: false }],
      command: "make up",
    })
  })

  it("shows a hint that is one command as a block only", () => {
    expect(layout("`make up`")).toEqual({ text: [], command: "make up" })
    expect(layout("  `make up` ")).toEqual({ text: [], command: "make up" })
    expect(layout("TM_TORCH_VARIANT=cu128 make up")).toEqual({
      text: [],
      command: "TM_TORCH_VARIANT=cu128 make up",
    })
  })

  it("keeps the other commands of the prose inline", () => {
    // Only the trailing command becomes a block; the step before it stays in
    // the sentence that leads to it.
    expect(
      layout(
        '改用 PyTorch 变体 cu128 重新构建镜像：`make setup SETUP_ARGS="--variant cu128"` 然后 `docker compose up -d --build trainer-cpu`'
      )
    ).toEqual({
      text: [
        { text: "改用 PyTorch 变体 cu128 重新构建镜像：", code: false },
        { text: 'make setup SETUP_ARGS="--variant cu128"', code: true },
        { text: " 然后 ", code: false },
      ],
      command: "docker compose up -d --build trainer-cpu",
    })
    expect(
      layout("`make setup` 然后 `docker compose up -d --build trainer`")
    ).toEqual({
      text: [
        { text: "make setup", code: true },
        { text: " 然后 ", code: false },
      ],
      command: "docker compose up -d --build trainer",
    })
  })

  it("keeps commands inline when text follows them or no text precedes", () => {
    // A command in the middle of a sentence.
    const middle = layout(
      '`make setup SETUP_ARGS="--install-toolkit"`，再按它最后输出的服务名启动（通常是 `docker compose up -d --build trainer`）'
    )
    expect(middle.command).toBeNull()
    expect(middle.text.filter((segment) => segment.code)).toHaveLength(2)
    expect(layout("在宿主机运行 `nvidia-smi` 检查驱动。")).toEqual({
      text: [
        { text: "在宿主机运行 ", code: false },
        { text: "nvidia-smi", code: true },
        { text: " 检查驱动。", code: false },
      ],
      command: null,
    })
    // Two commands with no prose: not "a command preceded by prose".
    expect(layout("`make setup` `make up`")).toEqual({
      text: [
        { text: "make setup", code: true },
        { text: " ", code: false },
        { text: "make up", code: true },
      ],
      command: null,
    })
  })

  it("leaves plain text and empty hints alone", () => {
    expect(layout("重启机器或重新加载驱动")).toEqual({
      text: [{ text: "重启机器或重新加载驱动", code: false }],
      command: null,
    })
    expect(layout("")).toEqual({ text: [], command: null })
    expect(layout("运行 `make")).toEqual({
      text: [{ text: "运行 `make", code: false }],
      command: null,
    })
  })
})

describe("suggestServerUrl", () => {
  it("uses the current address when other machines can reach it", () => {
    expect(
      suggestServerUrl({
        protocol: "http:",
        host: "192.168.1.10:8080",
        hostname: "192.168.1.10",
        port: "8080",
      })
    ).toBe("http://192.168.1.10:8080")
    expect(
      suggestServerUrl({
        protocol: "https:",
        host: "ai.example.com",
        hostname: "ai.example.com",
        port: "",
      })
    ).toBe("https://ai.example.com")
  })

  it("replaces a loopback host but keeps the protocol and port", () => {
    for (const hostname of [
      "localhost",
      "127.0.0.1",
      "[::1]",
      "app.localhost",
    ]) {
      expect(
        suggestServerUrl({
          protocol: "http:",
          host: `${hostname}:18080`,
          hostname,
          port: "18080",
        })
      ).toBe("http://<平台服务器 IP>:18080")
    }
    expect(
      suggestServerUrl({
        protocol: "http:",
        host: "localhost:8080",
        hostname: "localhost",
        port: "8080",
      })
    ).toBe("http://<平台服务器 IP>:8080")
    // The default port of the protocol: no port in the URL.
    expect(
      suggestServerUrl({
        protocol: "https:",
        host: "localhost",
        hostname: "localhost",
        port: "",
      })
    ).toBe("https://<平台服务器 IP>")
  })

  it("replaces an IPv6 host, which the trainer containers cannot reach", () => {
    // The compose network has no IPv6 by default, and make setup rejects it.
    const ipv6 = {
      protocol: "http:",
      host: "[fd00::10]:8080",
      hostname: "[fd00::10]",
      port: "8080",
    }
    expect(suggestServerUrl(ipv6)).toBe("http://<平台服务器 IP>:8080")
    expect(serverUrlPlaceholderReason(ipv6)).toBe("ipv6")
    expect(
      suggestServerUrl({
        protocol: "https:",
        host: "[2001:db8::1]",
        hostname: "[2001:db8::1]",
        port: "",
      })
    ).toBe("https://<平台服务器 IP>")
  })

  it("says why the address is a placeholder", () => {
    const at = (hostname: string) => ({
      protocol: "http:",
      host: `${hostname}:8080`,
      hostname,
      port: "8080",
    })
    expect(serverUrlPlaceholderReason(at("192.168.1.10"))).toBeNull()
    expect(serverUrlPlaceholderReason(at("ai.example.com"))).toBeNull()
    expect(serverUrlPlaceholderReason(at("localhost"))).toBe("local")
    // The IPv6 loopback address is a local address first.
    expect(serverUrlPlaceholderReason(at("[::1]"))).toBe("local")
    expect(
      serverUrlPlaceholderReason({
        protocol: "file:",
        host: "",
        hostname: "",
        port: "",
      })
    ).toBe("local")
  })

  it("falls back to the default port when the address is unknown", () => {
    expect(
      suggestServerUrl({ protocol: "file:", host: "", hostname: "", port: "" })
    ).toBe("http://<平台服务器 IP>:8080")
  })
})
