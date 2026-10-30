/**
 * 标签颜色：按标签名哈希到固定调色板，同一标签在所有数据集、页面刷新后
 * 颜色都一样。调色板取饱和度较高的中等明度色，在浅色 / 深色界面以及
 * 各种图片背景上都容易分辨。
 */
export const LABEL_PALETTE = [
  "#ef4444",
  "#f97316",
  "#f59e0b",
  "#eab308",
  "#84cc16",
  "#22c55e",
  "#10b981",
  "#14b8a6",
  "#06b6d4",
  "#0ea5e9",
  "#3b82f6",
  "#6366f1",
  "#8b5cf6",
  "#a855f7",
  "#d946ef",
  "#ec4899",
] as const

/** 32 位 FNV-1a 哈希（按 UTF-16 码元），结果为无符号整数。 */
export function hashString(value: string) {
  let hash = 0x811c9dc5
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return hash >>> 0
}

export function labelColor(name: string): string {
  return LABEL_PALETTE[hashString(name) % LABEL_PALETTE.length]
}

function channel(hex: string, offset: number) {
  const value = parseInt(hex.slice(offset, offset + 2), 16) / 255
  return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
}

/** WCAG 相对亮度，输入 `#rrggbb`。 */
export function relativeLuminance(hex: string) {
  return (
    0.2126 * channel(hex, 1) +
    0.7152 * channel(hex, 3) +
    0.0722 * channel(hex, 5)
  )
}

const DARK_TEXT = "#0a0a0a"
const LIGHT_TEXT = "#ffffff"

/** 在该背景色上对比度更高的文字颜色（深色或白色）。 */
export function readableTextColor(background: string) {
  const luminance = relativeLuminance(background)
  const darkContrast =
    (luminance + 0.05) / (relativeLuminance(DARK_TEXT) + 0.05)
  const lightContrast = 1.05 / (luminance + 0.05)
  return darkContrast >= lightContrast ? DARK_TEXT : LIGHT_TEXT
}
