/** 计算资源页面的格式化函数（纯函数，便于测试）。未知的值统一显示为“—”。 */
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatPercent,
} from "@/lib/format"

const DASH = "—"

function isKnown(value: number | null | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
}

/** 去掉末尾无意义的 0：`2.50` → `"2.5"`，`8.00` → `"8"`。 */
function trimNumber(value: number, fractionDigits: number) {
  return String(Number(value.toFixed(fractionDigits)))
}

/**
 * 简化显卡名称：去掉 "NVIDIA "、"GeForce " 前缀，
 * 如 "NVIDIA GeForce RTX 4090" → "RTX 4090"、"NVIDIA A100-SXM4-80GB" → "A100-SXM4-80GB"。
 */
export function shortenGpuName(name: string | null | undefined): string {
  const original = (name ?? "").replace(/\s+/g, " ").trim()
  if (!original) {
    return "未知显卡"
  }
  const short = original.replace(/^(?:(?:NVIDIA|GeForce)\s+)+/i, "").trim()
  return short || original
}

/**
 * 简化 CPU 型号：去掉商标符号与频率、“Processor”等后缀，
 * 如 "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz" → "Intel Xeon Gold 6330"。
 */
export function shortenCpuModel(model: string | null | undefined): string {
  const original = (model ?? "").replace(/\s+/g, " ").trim()
  if (!original) {
    return "未知 CPU"
  }
  const short = original
    .replace(/\((?:R|TM|C)\)/gi, "")
    .replace(/\s*(?:CPU\s*)?@\s*[\d.]+\s*[GM]Hz$/i, "")
    .replace(/\s+\d+-Core Processor$/i, "")
    .replace(/\s+(?:Processor|CPU)$/i, "")
    .replace(/\s+/g, " ")
    .trim()
  return short || original
}

/** 功率，如 `312.4` → `"312 W"`，`45.67` → `"45.7 W"`。 */
export function formatWatts(watts: number | null | undefined): string {
  if (!isKnown(watts)) {
    return DASH
  }
  return `${trimNumber(watts, watts >= 100 ? 0 : 1)} W`
}

/** 功率与上限，如 `"312 / 450 W"`；只知道其中一个时显示一个。 */
export function formatPower(
  used: number | null | undefined,
  limit: number | null | undefined
): string {
  if (!isKnown(limit)) {
    return formatWatts(used)
  }
  const usedText = isKnown(used) ? trimNumber(used, used >= 100 ? 0 : 1) : DASH
  return `${usedText} / ${formatWatts(limit)}`
}

/** 温度，如 `71` → `"71 °C"`。 */
export function formatTemperature(celsius: number | null | undefined): string {
  // 温度可以低于 0（极少见），只排除非数字。
  if (typeof celsius !== "number" || !Number.isFinite(celsius)) {
    return DASH
  }
  return `${Math.round(celsius)} °C`
}

/** 频率，如 `3100` → `"3.1 GHz"`，`800` → `"800 MHz"`。 */
export function formatMhz(mhz: number | null | undefined): string {
  if (!isKnown(mhz) || mhz === 0) {
    return DASH
  }
  if (mhz >= 1000) {
    return `${trimNumber(mhz / 1000, 2)} GHz`
  }
  return `${Math.round(mhz)} MHz`
}

/** 百分比；未知时显示“—”。 */
export function formatPercentValue(percent: number | null | undefined) {
  return isKnown(percent) ? formatPercent(Math.min(percent, 100)) : DASH
}

/** 核数（cgroup 配额可以是小数），如 `8` → `"8"`、`2.5` → `"2.5"`。 */
export function formatCores(cores: number): string {
  return trimNumber(cores, 2)
}

/** 物理核与逻辑核，如 `"28 核 / 56 线程"`；两者相同时只显示核数。都未知时返回 null。 */
export function formatCpuCores(cpu: {
  physical_cores: number | null
  logical_cores: number | null
}): string | null {
  const physical = isKnown(cpu.physical_cores) ? cpu.physical_cores : null
  const logical = isKnown(cpu.logical_cores) ? cpu.logical_cores : null
  if (physical && logical && physical !== logical) {
    return `${physical} 核 / ${logical} 线程`
  }
  if (physical) {
    return `${physical} 核`
  }
  if (logical) {
    return `${logical} 线程`
  }
  return null
}

/** 平均负载，如 `"0.52 / 0.61 / 0.70"`；未知时返回 null。 */
export function formatLoadAverage(
  load: readonly number[] | null | undefined
): string | null {
  const values = (load ?? []).filter((value) => isKnown(value))
  if (values.length === 0) {
    return null
  }
  return values.map((value) => value.toFixed(2)).join(" / ")
}

/** 用量与总量，如 `"19 GB / 24 GB"`；总量未知时只显示用量。 */
export function formatUsage(
  used: number | null | undefined,
  total: number | null | undefined
): string {
  const usedText = isKnown(used) ? formatBytes(used) : DASH
  if (!isKnown(total)) {
    return usedText
  }
  return `${usedText} / ${formatBytes(total)}`
}

/** 字节数；未知时显示“—”。 */
export function formatBytesValue(bytes: number | null | undefined) {
  return isKnown(bytes) ? formatBytes(bytes) : DASH
}

/** 耗时，如 `350` → `"350 毫秒"`、`1830` → `"1.8 秒"`、`65000` → `"1 分钟 5 秒"`。 */
export function formatMilliseconds(ms: number | null | undefined): string {
  if (!isKnown(ms)) {
    return DASH
  }
  if (ms < 1000) {
    return `${Math.round(ms)} 毫秒`
  }
  if (ms < 60_000) {
    return `${trimNumber(ms / 1000, 1)} 秒`
  }
  return formatDuration(ms / 1000)
}

/** 本地时间的时分秒，如 `"15:48:12"`；当天以外的时间带上日期。 */
export function formatClockTime(
  value: number | string | Date,
  now: number = Date.now()
): string {
  const text = formatDateTime(value, { withSeconds: true })
  const today = formatDateTime(now).slice(0, 10)
  return text.startsWith(today) ? text.slice(11) : text
}

export type TextSegment = { text: string; code: boolean }

// 中日韩文字与全角标点：含有它们的提示是说明文字，而不是一条命令。
const CJK = /[\u3000-\u303f\u3400-\u9fff\uff00-\uffef]/

/**
 * 把修复建议拆成普通文字与命令：反引号包住的部分是命令（如
 * "在宿主机运行 `make setup`"）；没有反引号且不含中文的整段视为一条命令。
 * 不成对的反引号按普通字符保留。
 */
export function hintSegments(text: string): TextSegment[] {
  if (!text.includes("`")) {
    const trimmed = text.trim()
    if (!trimmed) {
      return []
    }
    return [{ text: trimmed, code: !CJK.test(trimmed) }]
  }

  const parts = text.split("`")
  // 反引号为奇数个时最后一段没有闭合，与前一段普通文字合并。
  if (parts.length % 2 === 0) {
    const unclosed = parts.pop() ?? ""
    parts[parts.length - 1] += `\`${unclosed}`
  }

  const segments: TextSegment[] = []
  parts.forEach((part, index) => {
    const code = index % 2 === 1
    const value = code ? part.trim() : part
    if (value) {
      segments.push({ text: value, code })
    }
  })
  return segments
}

export type HintLayout = {
  /** 命令前面的文字（其中的其它命令显示为行内代码）；没有时为空数组。 */
  text: TextSegment[]
  /** 单独一行显示、可以复制的命令；没有时为 null。 */
  command: string | null
}

/**
 * 修复建议的排版（输入为 hintSegments 的结果）：整段就是一条命令，或以唯一的
 * 一条命令结尾（如 "…，然后以 GPU 模式启动：`make up …`"）时，这条命令单独一行
 * 显示为可复制的代码块，前面的文字照常显示。含有多条命令时（如 "`make setup`
 * 然后 `docker compose up -d --build trainer`"）都显示为行内代码：只给最后一条
 * 复制按钮，容易漏掉前面的步骤。
 */
export function hintLayout(segments: readonly TextSegment[]): HintLayout {
  const blank = (segment: TextSegment) => !segment.code && !segment.text.trim()
  let end = segments.length
  while (end > 0 && blank(segments[end - 1])) {
    end -= 1
  }
  const last = segments[end - 1]
  const commands = segments.filter((segment) => segment.code).length
  if (!last?.code || commands !== 1) {
    return { text: [...segments], command: null }
  }
  const text = segments.slice(0, end - 1)
  return {
    text: text.every(blank) ? [] : text,
    command: last.text,
  }
}

/**
 * 平台地址中的主机占位符：通过 localhost 或 IPv6 地址访问时，需要换成平台服务器的
 * IP（IPv4）或域名。
 */
export const SERVER_HOST_PLACEHOLDER = "<平台服务器 IP>"

type PageLocation = {
  protocol: string
  host: string
  hostname: string
  port: string
}

/**
 * 接入说明中的平台地址为什么不能直接用当前地址：local 为本机地址（localhost
 * 等，其它机器无法使用；不知道地址时也按它处理），ipv6 为 IPv6 地址（训练节点
 * 容器的 Docker 网络默认没有 IPv6，make setup 也不接受）。可以直接使用时为 null。
 */
export function serverUrlPlaceholderReason(
  location: PageLocation
): "local" | "ipv6" | null {
  if (!location.host || !/^https?:$/.test(location.protocol)) {
    return "local"
  }
  const hostname = location.hostname.replace(/^\[|\]$/g, "").toLowerCase()
  const loopback =
    hostname === "localhost" ||
    hostname.endsWith(".localhost") ||
    hostname === "::1" ||
    hostname === "0.0.0.0" ||
    /^127\./.test(hostname)
  if (loopback) {
    return "local"
  }
  return location.hostname.startsWith("[") ? "ipv6" : null
}

/**
 * 节点接入说明中的平台地址：通过 IPv4 地址 / 域名访问时就是当前地址；通过
 * localhost 或 IPv6 地址访问时（见 serverUrlPlaceholderReason）主机名换成占位符，
 * 协议与端口沿用当前地址（.env 中的 TM_HTTP_PORT 不一定是 8080）。不知道地址时
 * 按默认部署的 8080 端口。
 */
export function suggestServerUrl(location: PageLocation): string {
  if (!location.host || !/^https?:$/.test(location.protocol)) {
    return `http://${SERVER_HOST_PLACEHOLDER}:8080`
  }
  if (serverUrlPlaceholderReason(location)) {
    const port = location.port ? `:${location.port}` : ""
    return `${location.protocol}//${SERVER_HOST_PLACEHOLDER}${port}`
  }
  return `${location.protocol}//${location.host}`
}
