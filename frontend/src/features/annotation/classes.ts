/**
 * 数据集类别列表（标注时可选的类别）的编辑与配色。校验规则与后端一致：
 * 名称去掉首尾空白后非空、不超过 255 个字符、不含控制字符、不重复；颜色为
 * `#rrggbb` 或空（按名称自动配色）。服务端的校验仍是最终结果。
 */
import type { DatasetClass, DatasetLabel } from "@/features/datasets/api"
import {
  CLASS_LIST_MAX_COUNT,
  CLASS_NAME_MAX_LENGTH,
} from "@/features/datasets/class-list"
import { labelColor } from "@/features/datasets/label-colors"
import { characterCount } from "@/features/datasets/naming"

// eslint-disable-next-line no-control-regex -- 控制字符正是要拒绝的内容
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/
const HEX_COLOR = /^#[0-9a-f]{6}$/i

/** 校验类别名称（已去掉首尾空白）；`others` 是其它类别的名称。 */
export function classNameError(
  name: string,
  others: readonly string[]
): string | null {
  if (name === "") {
    return "请输入类别名称"
  }
  if (characterCount(name) > CLASS_NAME_MAX_LENGTH) {
    return `类别名称不能超过 ${CLASS_NAME_MAX_LENGTH} 个字符`
  }
  if (CONTROL_CHARS.test(name)) {
    return "类别名称不能包含换行、制表符等控制字符"
  }
  if (others.includes(name)) {
    return `类别“${name}”已存在`
  }
  return null
}

export function isHexColor(value: string) {
  return HEX_COLOR.test(value)
}

/** 类别的显示颜色：设置了颜色时用它，否则按名称自动配色（与数据集页面一致）。 */
export function classColor(cls: DatasetClass) {
  return cls.color && isHexColor(cls.color)
    ? cls.color.toLowerCase()
    : labelColor(cls.name)
}

/**
 * 类别名称 → 颜色。不在列表中的类别（如其它工具标注的）按名称自动配色。
 */
export function colorResolver(classes: readonly DatasetClass[]) {
  const colors = new Map(classes.map((cls) => [cls.name, classColor(cls)]))
  return (name: string) => colors.get(name) ?? labelColor(name)
}

export type ClassEditResult =
  { classes: DatasetClass[]; error: null } | { classes: null; error: string }

const fail = (error: string): ClassEditResult => ({ classes: null, error })
const ok = (classes: DatasetClass[]): ClassEditResult => ({
  classes,
  error: null,
})

export function addClass(
  classes: readonly DatasetClass[],
  rawName: string,
  color = ""
): ClassEditResult {
  const name = rawName.trim()
  const error = classNameError(
    name,
    classes.map((cls) => cls.name)
  )
  if (error) {
    return fail(error)
  }
  if (classes.length >= CLASS_LIST_MAX_COUNT) {
    return fail(`类别数量不能超过 ${CLASS_LIST_MAX_COUNT} 个`)
  }
  return ok([...classes, { name, color }])
}

export function renameClass(
  classes: readonly DatasetClass[],
  index: number,
  rawName: string
): ClassEditResult {
  const name = rawName.trim()
  const error = classNameError(
    name,
    classes.filter((_, i) => i !== index).map((cls) => cls.name)
  )
  if (error) {
    return fail(error)
  }
  return ok(classes.map((cls, i) => (i === index ? { ...cls, name } : cls)))
}

export function setClassColor<T extends DatasetClass>(
  classes: readonly T[],
  index: number,
  color: string
): T[] {
  const value = isHexColor(color) ? color.toLowerCase() : ""
  return classes.map((cls, i) => (i === index ? { ...cls, color: value } : cls))
}

/** 把第 `from` 个类别移到 `to`（数字键与 YOLO 类别编号按这个顺序）。 */
export function moveClass<T>(classes: readonly T[], from: number, to: number) {
  if (
    from === to ||
    from < 0 ||
    to < 0 ||
    from >= classes.length ||
    to >= classes.length
  ) {
    return [...classes]
  }
  const next = [...classes]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  return next
}

export function removeClass<T>(classes: readonly T[], index: number): T[] {
  return classes.filter((_, i) => i !== index)
}

/**
 * 把数据集中已有、但不在类别列表里的标签追加到列表末尾，返回新列表与追加的
 * 数量。`order` 中的类别（如 YOLO 导入时的 classes.txt，没有目标的也算）按
 * 原顺序排在最前，导出 YOLO 时类别编号保持不变；其余标签按目标数降序、名称
 * 升序。
 */
export function mergeLabels(
  classes: readonly DatasetClass[],
  labels: readonly Pick<DatasetLabel, "name" | "object_count">[],
  order: readonly string[] = []
): { classes: DatasetClass[]; added: number } {
  const known = new Set(classes.map((cls) => cls.name))
  const names: string[] = []
  for (const name of order) {
    if (!known.has(name)) {
      known.add(name)
      names.push(name)
    }
  }
  const rest = labels
    .filter((label) => !known.has(label.name))
    .sort(
      (a, b) =>
        b.object_count - a.object_count ||
        (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
    )
    .map((label) => label.name)
  const extra = [...names, ...rest]
    .slice(0, Math.max(0, CLASS_LIST_MAX_COUNT - classes.length))
    .map((name) => ({ name, color: "" }))
  return { classes: [...classes, ...extra], added: extra.length }
}

/**
 * 在类别列表末尾加入一个类别，已有同名类别时原样返回。用于加入类别时先更新
 * 页面（与服务端 `POST /classes` 的结果一致），之后以服务端返回的列表为准。
 */
export function appendClass<T extends { items: DatasetClass[]; total: number }>(
  list: T,
  name: string,
  color = ""
): T {
  if (list.items.some((cls) => cls.name === name)) {
    return list
  }
  const items = [...list.items, { name, color }]
  return { ...list, items, total: items.length }
}

/** 按输入筛选类别（不区分大小写的包含匹配），完全相同的排在最前。 */
export function filterClasses<T extends { name: string }>(
  classes: readonly T[],
  query: string
): T[] {
  const q = query.trim().toLowerCase()
  if (q === "") {
    return [...classes]
  }
  const matches = classes.filter((cls) => cls.name.toLowerCase().includes(q))
  return [
    ...matches.filter((cls) => cls.name.toLowerCase() === q),
    ...matches.filter((cls) => cls.name.toLowerCase() !== q),
  ]
}
