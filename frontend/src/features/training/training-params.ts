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
  TrainingParamTemplate,
} from "@/features/training/api"

export type TemplateSourceFilter = "all" | "custom" | "builtin"

/** 将参数模板按来源拆分为“自定义模板”与“系统内置模板”两组（保留服务端排序）。 */
export function partitionTemplatesBySource<
  T extends Pick<TrainingParamTemplate, "is_builtin">,
>(templates: readonly T[]): { custom: T[]; builtin: T[] } {
  const custom: T[] = []
  const builtin: T[] = []
  for (const tpl of templates) {
    if (tpl.is_builtin) {
      builtin.push(tpl)
    } else {
      custom.push(tpl)
    }
  }
  return { custom, builtin }
}

/** 按来源标签筛选参数模板列表。 */
export function filterTemplatesBySource<
  T extends Pick<TrainingParamTemplate, "is_builtin">,
>(templates: readonly T[], source: TemplateSourceFilter): T[] {
  if (source === "custom") {
    return templates.filter((t) => !t.is_builtin)
  }
  if (source === "builtin") {
    return templates.filter((t) => t.is_builtin)
  }
  return [...templates]
}

/** 生成从已有模板复制/派生时的默认名称建议。 */
export function suggestedTemplateCopyName(
  source: Pick<TrainingParamTemplate, "name" | "is_builtin">
): string {
  return source.is_builtin ? `${source.name}-自定义` : `${source.name}-副本`
}

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

/** 常用参数在表单中的推荐展示顺序。 */
export const COMMON_PARAM_ORDER: readonly string[] = [
  "epochs",
  "batch",
  "imgsz",
  "patience",
  "optimizer",
  "lr0",
  "cos_lr",
  "amp",
  "device",
  "workers",
  "cache",
  "mosaic",
]

const COMMON_ORDER_INDEX = new Map(
  COMMON_PARAM_ORDER.map((key, index) => [key, index])
)

/** 将全量参数按 common 标记拆分为“常用参数”与“高级选项”两组，常用项按直观调参顺序排列。 */
export function splitSpecsByTier(specs: readonly ParamSpec[]): {
  common: ParamSpec[]
  advanced: ParamSpec[]
} {
  const common: ParamSpec[] = []
  const advanced: ParamSpec[] = []
  for (const spec of specs) {
    if (spec.common) {
      common.push(spec)
    } else {
      advanced.push(spec)
    }
  }
  common.sort((a, b) => {
    const ai = COMMON_ORDER_INDEX.get(a.key) ?? Number.MAX_SAFE_INTEGER
    const bi = COMMON_ORDER_INDEX.get(b.key) ?? Number.MAX_SAFE_INTEGER
    return ai - bi
  })
  return { common, advanced }
}

export const IMGSZ_QUICK_PRESETS = [640, 960, 1280] as const

export type ParamSubgroup = {
  id: string
  label: string
  specs: ParamSpec[]
  /** 是否优先使用双列宽布局（适用于 batch / imgsz 等组合控件）。 */
  wide?: boolean
}

type SubgroupDefinition = {
  id: string
  label: string
  keys: readonly string[]
  wide?: boolean
}

const COMMON_SUBGROUP_DEFS: readonly SubgroupDefinition[] = [
  {
    id: "common_schedule_input",
    label: "训练节奏与输入",
    keys: ["epochs", "patience", "batch", "imgsz"],
    wide: true,
  },
  {
    id: "common_optim_aug",
    label: "优化器与增强",
    keys: ["optimizer", "lr0", "mosaic", "cos_lr"],
  },
  {
    id: "common_runtime_io",
    label: "设备与数据加载",
    keys: ["device", "workers", "cache", "amp"],
  },
]

const ADVANCED_SUBGROUP_DEFS: Record<ParamGroupKey, readonly SubgroupDefinition[]> =
  {
    train: [
      {
        id: "train_checkpoint",
        label: "检查点与权重",
        keys: ["pretrained", "cls_remap", "resume", "save", "save_period"],
      },
      {
        id: "train_control",
        label: "训练控制与采样",
        keys: [
          "time",
          "close_mosaic",
          "fraction",
          "multi_scale",
          "freeze",
          "seed",
          "deterministic",
          "single_cls",
          "rect",
        ],
      },
      {
        id: "train_runtime",
        label: "编译与运行调试",
        keys: ["compile", "channels_last", "profile", "verbose"],
      },
    ],
    hyperparameters: [
      {
        id: "hyp_lr",
        label: "学习率与正则化",
        keys: ["lrf", "momentum", "weight_decay", "nbs"],
      },
      {
        id: "hyp_warmup",
        label: "预热策略",
        keys: ["warmup_epochs", "warmup_momentum", "warmup_bias_lr"],
      },
      {
        id: "hyp_loss",
        label: "基础损失权重",
        keys: ["box", "cls", "cls_pw", "dfl", "dis"],
      },
    ],
    augmentation: [
      {
        id: "aug_color",
        label: "色彩空间扰动",
        keys: ["hsv_h", "hsv_s", "hsv_v", "bgr"],
      },
      {
        id: "aug_geo",
        label: "几何变换与翻转",
        keys: [
          "degrees",
          "translate",
          "scale",
          "shear",
          "perspective",
          "flipud",
          "fliplr",
        ],
      },
      {
        id: "aug_mix",
        label: "图像混合增强",
        keys: ["mixup", "cutmix"],
      },
    ],
    val: [
      {
        id: "val_eval",
        label: "验证开关与阈值",
        keys: ["val", "split", "iou", "conf", "max_det", "classes"],
      },
      {
        id: "val_infer",
        label: "NMS 与推理精度",
        keys: ["nms", "agnostic_nms", "augment", "dnn", "quantize"],
      },
      {
        id: "val_output",
        label: "图表与结果输出",
        keys: ["plots", "line_width", "save_json", "save_txt", "save_conf"],
      },
    ],
  }

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

/**
 * 清理后端参数名称中与 key 重复的括号后缀，避免与旁边的等宽 key 标签重复展示。
 * 例：`"训练轮数 (epochs)"` -> `"训练轮数"`；`"最大训练时长 (time, 小时)"` -> `"最大训练时长 (小时)"`。
 */
export function cleanParamLabel(label: string, key: string): string {
  const escaped = escapeRegExp(key)
  const withUnit = label.replace(
    new RegExp(`[（(]\\s*${escaped}\\s*[,，]\\s*([^)）]+)[)）]`, "i"),
    "($1)"
  )
  const stripped = withUnit.replace(
    new RegExp(`\\s*[（(]\\s*${escaped}\\s*[)）]\\s*$`, "i"),
    ""
  )
  return stripped.trim() || label.trim()
}

function buildSubgroups(
  specs: readonly ParamSpec[],
  defs: readonly SubgroupDefinition[],
  fallbackId: string,
  fallbackLabel: string
): ParamSubgroup[] {
  const byKey = new Map(specs.map((spec) => [spec.key, spec]))
  const consumed = new Set<string>()
  const subgroups: ParamSubgroup[] = []

  for (const def of defs) {
    const items: ParamSpec[] = []
    for (const key of def.keys) {
      const found = byKey.get(key)
      if (found) {
        items.push(found)
        consumed.add(key)
      }
    }
    if (items.length > 0) {
      subgroups.push({
        id: def.id,
        label: def.label,
        specs: items,
        wide: def.wide,
      })
    }
  }

  const remaining = specs.filter((spec) => !consumed.has(spec.key))
  if (remaining.length > 0) {
    subgroups.push({
      id: fallbackId,
      label: fallbackLabel,
      specs: remaining,
    })
  }
  return subgroups
}

/** 将常用参数按语义划分为「训练节奏与输入」「优化器与增强」「设备与数据加载」等子分组。 */
export function groupCommonSpecs(commonSpecs: readonly ParamSpec[]): ParamSubgroup[] {
  return buildSubgroups(
    commonSpecs,
    COMMON_SUBGROUP_DEFS,
    "common_other",
    "其他常用参数"
  )
}

/**
 * 将单个高级分组内的参数拆分为：
 * - `subgroups`：通用参数（及匹配 `activeTask` 的当前任务专用参数）的语义子分组；
 * - `otherTaskSpecs`：其他任务类型（分割/分类/姿态/旋转框/深度等）专用参数，用于底部折叠收纳。
 */
export function partitionAdvancedSpecs(
  specs: readonly ParamSpec[],
  group: ParamGroupKey,
  activeTask?: string
): {
  subgroups: ParamSubgroup[]
  otherTaskSpecs: ParamSpec[]
} {
  const generalSpecs: ParamSpec[] = []
  const matchingTaskSpecs: ParamSpec[] = []
  const otherTaskSpecs: ParamSpec[] = []

  for (const spec of specs) {
    if (!spec.task_scope) {
      generalSpecs.push(spec)
    } else if (activeTask && spec.task_scope === activeTask) {
      matchingTaskSpecs.push(spec)
    } else {
      otherTaskSpecs.push(spec)
    }
  }

  const subgroups = buildSubgroups(
    generalSpecs,
    ADVANCED_SUBGROUP_DEFS[group],
    `${group}_other`,
    "其他参数"
  )

  if (matchingTaskSpecs.length > 0) {
    subgroups.push({
      id: `${group}_active_task`,
      label: "当前任务专用",
      specs: matchingTaskSpecs,
    })
  }

  return { subgroups, otherTaskSpecs }
}

/** 将一组参数拆分为常规表单控件项与纯布尔开关项，保持各自原始顺序。 */
export function splitSpecsByControlKind(specs: readonly ParamSpec[]): {
  fields: ParamSpec[]
  toggles: ParamSpec[]
} {
  const fields: ParamSpec[] = []
  const toggles: ParamSpec[] = []
  for (const spec of specs) {
    if (spec.type === "bool") {
      toggles.push(spec)
    } else {
      fields.push(spec)
    }
  }
  return { fields, toggles }
}

/**
 * 当 optimizer=auto 时，Ultralytics 会自动计算并覆盖 lr0 与 momentum；
 * 为这两个字段返回轻量提示，避免用户误以为调整后已生效。
 */
export function getAutoOptimizerHint(
  specKey: string,
  values: Record<string, unknown>
): { badge: string; detail: string } | null {
  if (specKey !== "lr0" && specKey !== "momentum") {
    return null
  }
  const optimizer = values.optimizer ?? "auto"
  if (optimizer !== "auto") {
    return null
  }
  return {
    badge: "auto 接管",
    detail: `当前 optimizer 为 auto，Ultralytics 会在训练启动时自动计算并覆盖 ${specKey}；如需手动生效，请先切换为 SGD / AdamW 等具体优化器。`,
  }
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

/** 生成用于参数模板编辑的官方默认参数（固定 resume=false，保留 pretrained=true）。 */
export function buildTemplateDefaultParams(
  specs: readonly ParamSpec[]
): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const spec of specs) {
    out[spec.key] = Array.isArray(spec.default)
      ? [...spec.default]
      : spec.default
  }
  if ("resume" in out) {
    out.resume = false
  }
  return out
}

/**
 * 将模板参数加载到新建训练任务表单中：
 * 以官方默认为底，叠加模板设定，并保持 resume 与 scratch 下 pretrained
 * 同当前初始化模式一致。
 */
export function applyTemplateParams(
  specs: readonly ParamSpec[],
  templateParams: Record<string, unknown>,
  initMode: InitMode
): Record<string, unknown> {
  const out = buildDefaultParams(specs, initMode)
  for (const spec of specs) {
    if (spec.key === "resume") {
      continue
    }
    if (spec.key === "pretrained" && initMode === "scratch") {
      continue
    }
    if (spec.key in templateParams) {
      const val = templateParams[spec.key]
      out[spec.key] = Array.isArray(val) ? [...val] : val
    }
  }
  if ("resume" in out) {
    out.resume = initMode === "resume"
  }
  if (initMode === "scratch" && "pretrained" in out) {
    out.pretrained = false
  }
  return out
}

/**
 * 从当前表单值提取适合保存为通用参数模板的参数对象：
 * 固定 resume=false；若当前处于 scratch 模式则还原 pretrained=true，避免保存后的模板影响微调模式。
 */
export function buildTemplateParamsPayload(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  initMode?: InitMode
): Record<string, unknown> {
  const out = buildTemplateDefaultParams(specs)
  for (const spec of specs) {
    if (spec.key === "resume") {
      out.resume = false
      continue
    }
    if (spec.key === "pretrained" && initMode === "scratch") {
      out.pretrained = spec.default
      continue
    }
    if (spec.key in values) {
      const val = values[spec.key]
      out[spec.key] = Array.isArray(val) ? [...val] : val
    }
  }
  return out
}

/** 返回某个参数在当前模式下的基准默认值（用于过滤 scratch/resume 自动联动字段造成的“伪修改”）。 */
export function getModeDefaultValue(
  spec: ParamSpec,
  initMode?: InitMode
): unknown {
  if (initMode === "resume" && spec.key === "resume") {
    return true
  }
  if (initMode === "scratch" && spec.key === "pretrained") {
    return false
  }
  return spec.default
}

/** 判断单个参数是否被修改（相对官方默认值）。 */
export function isParamModified(
  spec: ParamSpec,
  values: Record<string, unknown>
): boolean {
  if (!(spec.key in values)) {
    return false
  }
  return !isSameParamValue(values[spec.key], spec.default)
}

/** 判断单个参数是否相对当前模式基准默认值被用户主动修改。 */
export function isParamModifiedForMode(
  spec: ParamSpec,
  values: Record<string, unknown>,
  initMode?: InitMode
): boolean {
  if (!(spec.key in values)) {
    return false
  }
  const value = values[spec.key]
  if (isSameParamValue(value, spec.default)) {
    return false
  }
  return !isSameParamValue(value, getModeDefaultValue(spec, initMode))
}

/** 统计全部或某个分组内偏离官方默认值的参数数量。 */
export function countModifiedParams(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  group?: ParamGroupKey,
  initMode?: InitMode
): number {
  let count = 0
  for (const spec of specs) {
    if (group && spec.group !== group) {
      continue
    }
    const modified =
      initMode !== undefined
        ? isParamModifiedForMode(spec, values, initMode)
        : isParamModified(spec, values)
    if (modified) {
      count += 1
    }
  }
  return count
}

/** 列出指定规格集中被修改的参数项（用于高级选项收起态摘要与模板预览）。 */
export function listModifiedParams(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  initMode?: InitMode
): Array<{ spec: ParamSpec; value: unknown }> {
  const out: Array<{ spec: ParamSpec; value: unknown }> = []
  for (const spec of specs) {
    const modified =
      initMode !== undefined
        ? isParamModifiedForMode(spec, values, initMode)
        : isParamModified(spec, values)
    if (modified) {
      out.push({ spec, value: values[spec.key] })
    }
  }
  return out
}

/** 统计当前表单参数相对指定模板的微调项数（自动忽略与 initMode 联动的字段）。 */
export function countTemplateParamDiffs(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  templateParams: Record<string, unknown>,
  initMode: InitMode
): number {
  const baseline = applyTemplateParams(specs, templateParams, initMode)
  let count = 0
  for (const spec of specs) {
    if (spec.key === "resume") {
      continue
    }
    if (spec.key === "pretrained" && initMode === "scratch") {
      continue
    }
    const cur = spec.key in values ? values[spec.key] : baseline[spec.key]
    if (!isSameParamValue(cur, baseline[spec.key])) {
      count += 1
    }
  }
  return count
}

/** 紧凑格式化参数值，适合徽章与摘要标签展示。 */
export function formatCompactParamValue(value: unknown): string {
  if (value === null || value === undefined) {
    return "自动"
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false"
  }
  if (Array.isArray(value)) {
    return `[${value.join(",")}]`
  }
  if (value === "") {
    return "禁用"
  }
  return String(value)
}

/** 按关键词（英文键名、中文名称、说明、任务标签）与“仅显示已修改”筛选参数。 */
export function filterParamSpecs(
  specs: readonly ParamSpec[],
  values: Record<string, unknown>,
  {
    group,
    query = "",
    onlyModified = false,
    initMode,
  }: {
    group?: ParamGroupKey | "all"
    query?: string
    onlyModified?: boolean
    initMode?: InitMode
  }
): ParamSpec[] {
  const q = query.trim().toLowerCase()
  return specs.filter((spec) => {
    if (group && group !== "all" && spec.group !== group) {
      return false
    }
    if (onlyModified) {
      const modified =
        initMode !== undefined
          ? isParamModifiedForMode(spec, values, initMode)
          : isParamModified(spec, values)
      if (!modified) {
        return false
      }
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
