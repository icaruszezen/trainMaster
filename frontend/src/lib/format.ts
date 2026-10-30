const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const

/** 以 1024 为进制格式化字节数，例如 `1536` → `"1.5 KB"`。 */
export function formatBytes(bytes: number, fractionDigits = 1): string {
  if (!Number.isFinite(bytes) || bytes < 0) {
    return "—"
  }

  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit += 1
  }

  // 四舍五入后可能进位到 1024（如 1023.96 KB），此时换到下一个单位。
  let rounded = Number(value.toFixed(fractionDigits))
  if (rounded >= 1024 && unit < BYTE_UNITS.length - 1) {
    unit += 1
    rounded = Number((rounded / 1024).toFixed(fractionDigits))
  }

  return `${rounded} ${BYTE_UNITS[unit]}`
}

/** 计算占比（0–100），总量未知或为 0 时返回 0。 */
export function percentOf(part: number, total: number): number {
  if (!(total > 0) || !Number.isFinite(part)) {
    return 0
  }
  return Math.min(100, Math.max(0, (part / total) * 100))
}

/** 把秒数格式化为中文时长，只保留最大的两个单位，例如 `"3 天 2 小时"`。 */
export function formatDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) {
    return "—"
  }

  const seconds = Math.floor(totalSeconds)
  const parts: [number, string][] = [
    [Math.floor(seconds / 86400), "天"],
    [Math.floor((seconds % 86400) / 3600), "小时"],
    [Math.floor((seconds % 3600) / 60), "分钟"],
    [seconds % 60, "秒"],
  ]

  const first = parts.findIndex(([value]) => value > 0)
  if (first === -1) {
    return "0 秒"
  }
  return parts
    .slice(first, first + 2)
    .filter(([value]) => value > 0)
    .map(([value, unit]) => `${value} ${unit}`)
    .join(" ")
}

const numberFormat = new Intl.NumberFormat("zh-CN")

/** 千分位格式化整数 / 小数，例如 `12345` → `"12,345"`。 */
export function formatNumber(value: number): string {
  if (!Number.isFinite(value)) {
    return "—"
  }
  return numberFormat.format(value)
}

/** 格式化百分比（输入 0–100），例如 `82.4` → `"82%"`。 */
export function formatPercent(percent: number, fractionDigits = 0): string {
  if (!Number.isFinite(percent)) {
    return "—"
  }
  return `${Number(percent.toFixed(fractionDigits))}%`
}

function toDate(value: string | number | Date): Date | null {
  const date = value instanceof Date ? value : new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * 相对时间，例如 `"刚刚"`、`"3 分钟前"`、`"2 天前"`、`"1 年前"`。
 * 未来的时间（服务器与浏览器时钟有偏差）按“刚刚”处理。
 */
export function formatRelativeTime(
  value: string | number | Date | null | undefined,
  now: number = Date.now()
): string {
  const date = value == null ? null : toDate(value)
  if (!date) {
    return "—"
  }

  const elapsed = now - date.getTime()
  if (elapsed < MINUTE) {
    return "刚刚"
  }
  if (elapsed < HOUR) {
    return `${Math.floor(elapsed / MINUTE)} 分钟前`
  }
  if (elapsed < DAY) {
    return `${Math.floor(elapsed / HOUR)} 小时前`
  }
  const days = Math.floor(elapsed / DAY)
  if (days < 30) {
    return `${days} 天前`
  }
  if (days < 365) {
    return `${Math.floor(days / 30)} 个月前`
  }
  return `${Math.floor(days / 365)} 年前`
}

const pad = (value: number) => String(value).padStart(2, "0")

/** 本地时间 `"2026-10-30 15:48"`；`withSeconds` 时带秒。 */
export function formatDateTime(
  value: string | number | Date | null | undefined,
  { withSeconds = false }: { withSeconds?: boolean } = {}
): string {
  const date = value == null ? null : toDate(value)
  if (!date) {
    return "—"
  }

  const day = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  return withSeconds
    ? `${day} ${time}:${pad(date.getSeconds())}`
    : `${day} ${time}`
}
