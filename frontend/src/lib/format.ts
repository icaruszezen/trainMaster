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
