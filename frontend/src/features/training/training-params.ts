/**
 * YOLO 全参数编辑器的比较、归一化、校验与 YAML 导出工具（纯函数，便于测试）。
 *
 * 所有参数均以服务端 `/api/v1/training/params-schema` 下发的 Ultralytics 官方
 * 默认值为基准。
 */
import type {
  InitMode,
  ParamGroupKey,
  ParamSpec,
} from "@/features/training/api"

/** 深度比较两个参数值是否与官方默认值一致。 */
export function isSameParamValue(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) {
    return true
  }
  if (a == null && b == null) {
    return true
  }
  if (Array.isArray(a) && Array.isArray(b)) {
    return (
      a.length === b.length &&
      a.every((item, index) => isSameParamValue(item, b[index]))
    )
  }
  return false
}

/** 从参数规格列表生成一份全新的官方默认参数拷贝。 */
export function buildDefaultParams(
  specs: readonly ParamSpec[],
  initMode: InitMode = "scratch"
): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const spec of specs) {
    out[spec.key] = Array.isArray(spec.default)
      ? [...spec.default]
      : spec.default
  }
  if ("resume" in out) {
    out.resume = initMode === "resume"
  }
  if (initMode === "scratch" && "pretrained" in out) {
    out.pretrained = false
  }
  return out
}

/** 判断单个参数是否被修改（相对官方默认值；resume 随训练模式联动除外）。 */
export function isParamModified(
  spec: ParamSpec,
  values: Record<string, unknown>
): boolean {
  if (!(spec.key in values)) {
    return false
  }
  return !isSameParamValue(values[spec.key], spec.default)
}

/** 统计全部或某个分组内偏离官方默认值的参数数量。 */
export function countModifiedParams(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  group?: ParamGroupKey
): number {
  let count = 0
  for (const spec of specs) {
    if (group && spec.group !== group) {
      continue
    }
    if (isParamModified(spec, values)) {
      count += 1
    }
  }
  return count
}

/** 按关键词（英文键名、中文名称、说明、任务标签）与“仅显示已修改”筛选参数。 */
export function filterParamSpecs(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  {
    group,
    query = "",
    onlyModified = false,
  }: {
    group?: ParamGroupKey | "all"
    query?: string
    onlyModified?: boolean
  }
): ParamSpec[] {
  const q = query.trim().toLowerCase()
  return specs.filter((spec) => {
    if (group && group !== "all" && spec.group !== group) {
      return false
    }
    if (onlyModified && !isParamModified(spec, values)) {
      return false
    }
    if (!q) {
      return true
    }
    return (
      spec.key.toLowerCase().includes(q) ||
      spec.label.toLowerCase().includes(q) ||
      spec.description.toLowerCase().includes(q) ||
      (spec.task_scope?.toLowerCase().includes(q) ?? false)
    )
  })
}

/** 解析逗号分隔的整数列表（用于 classes 与 freeze 多层列表）。 */
export function parseIntListText(raw: string): {
  value: number[] | null
  error: string | null
} {
  const trimmed = raw.trim()
  if (trimmed === "") {
    return { value: null, error: null }
  }
  const parts = trimmed.split(/[\s,，]+/).filter(Boolean)
  const nums: number[] = []
  for (const part of parts) {
    if (!/^\d+$/.test(part)) {
      return { value: null, error: `“${part}”不是非负整数` }
    }
    const n = Number(part)
    if (!Number.isSafeInteger(n) || n < 0 || n > 10000) {
      return { value: null, error: `“${part}”超出范围（0–10000）` }
    }
    if (!nums.includes(n)) {
      nums.push(n)
    }
  }
  return { value: nums.length > 0 ? nums : null, error: null }
}

/** 解析 freeze 输入：空 → null；单个正整数 → 冻结前 N 层；多个逗号分隔整数 → 冻结指定层索引。 */
export function parseFreezeText(raw: string): {
  value: number | number[] | null
  error: string | null
} {
  const trimmed = raw.trim()
  if (trimmed === "") {
    return { value: null, error: null }
  }
  if (/^\d+$/.test(trimmed)) {
    const n = Number(trimmed)
    if (n < 1 || n > 500) {
      return { value: null, error: "冻结层数应在 1–500 之间（留空表示不冻结）" }
    }
    return { value: n, error: null }
  }
  const parsed = parseIntListText(trimmed)
  if (parsed.error) {
    return { value: null, error: parsed.error }
  }
  return { value: parsed.value, error: null }
}

export function formatParamValueText(value: unknown): string {
  if (value === null || value === undefined) {
    return "null（自动 / 不启用）"
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false"
  }
  if (Array.isArray(value)) {
    return `[${value.join(", ")}]`
  }
  if (value === "") {
    return '""（自动）'
  }
  return String(value)
}

function yamlScalar(value: unknown): string {
  if (value === null || value === undefined) {
    return "null"
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false"
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? String(value) : "null"
  }
  if (Array.isArray(value)) {
    return `[${value.map(yamlScalar).join(", ")}]`
  }
  const text = String(value)
  if (
    text === "" ||
    /[:#[\]{}&*!|>'"%@`]/.test(text) ||
    /^(true|false|null|~|\d+(\.\d+)?)$/i.test(text)
  ) {
    return JSON.stringify(text)
  }
  return text
}

/** 导出当前全量参数为 Ultralytics 兼容的 args.yaml 文本。 */
export function formatParamsYaml(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>
): string {
  const lines = ["# Ultralytics YOLO 训练参数（由 trainMaster 生成）"]
  for (const spec of specs) {
    const val = spec.key in values ? values[spec.key] : spec.default
    lines.push(`${spec.key}: ${yamlScalar(val)}`)
  }
  return `${lines.join("\n")}\n`
}

/** 客户端提交前快速校验关键数值范围，返回首个字段错误（无错返回 null）。 */
export function validateTrainingParams(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>
): { key: string; message: string } | null {
  for (const spec of specs) {
    const val = values[spec.key]
    if (val === undefined) {
      continue
    }
    if (
      (spec.type === "nullable_int" ||
        spec.type === "nullable_float" ||
        spec.type === "nullable_bool" ||
        spec.type === "quantize" ||
        spec.type === "freeze" ||
        spec.type === "int_list") &&
      val === null
    ) {
      continue
    }
    if (spec.type === "int" || spec.type === "nullable_int") {
      if (typeof val !== "number" || !Number.isInteger(val)) {
        return { key: spec.key, message: `${spec.label} 必须是整数` }
      }
      if (spec.min !== undefined && val < spec.min) {
        return { key: spec.key, message: `${spec.label} 不能小于 ${spec.min}` }
      }
      if (spec.max !== undefined && val > spec.max) {
        return { key: spec.key, message: `${spec.label} 不能大于 ${spec.max}` }
      }
    } else if (spec.type === "float" || spec.type === "nullable_float") {
      if (typeof val !== "number" || !Number.isFinite(val)) {
        return { key: spec.key, message: `${spec.label} 必须是有效数值` }
      }
      if (spec.min !== undefined && val < spec.min) {
        return { key: spec.key, message: `${spec.label} 不能小于 ${spec.min}` }
      }
      if (spec.max !== undefined && val > spec.max) {
        return { key: spec.key, message: `${spec.label} 不能大于 ${spec.max}` }
      }
    } else if (spec.type === "imgsz") {
      const checkDim = (d: unknown) =>
        typeof d === "number" &&
        Number.isInteger(d) &&
        d >= 32 &&
        d <= 4096 &&
        d % 32 === 0
      if (Array.isArray(val)) {
        if (val.length !== 2 || !checkDim(val[0]) || !checkDim(val[1])) {
          return {
            key: spec.key,
            message: "输入图像尺寸 [高, 宽] 均需为 32–4096 且是 32 的倍数",
          }
        }
      } else if (!checkDim(val)) {
        return {
          key: spec.key,
          message: "输入图像尺寸需为 32–4096 且是 32 的倍数（如 640）",
        }
      }
    } else if (spec.type === "batch") {
      if (typeof val !== "number" || !Number.isFinite(val)) {
        return { key: spec.key, message: "批次大小必须是数值" }
      }
      if (val === -1) {
        continue
      }
      if (val > 0 && val < 1) {
        if (val < 0.05 || val > 0.99) {
          return {
            key: spec.key,
            message: "按显存比例自动计算 batch 时，比例应在 0.05–0.99 之间",
          }
        }
        continue
      }
      if (!Number.isInteger(val) || val < 1 || val > 4096) {
        return {
          key: spec.key,
          message: "固定批次大小应为 1–4096 的整数（或 -1 自动计算）",
        }
      }
    }
  }
  return null
}
