/**
 * 类别名称的校验、配色与筛选（标注时的类别选择与项目的类别列表共用），以及
 * 标注时的候选类别。校验规则与后端一致：名称去掉首尾空白后非空、不超过 255 个
 * 字符、不含控制字符、不重复；颜色为 `#rrggbb` 或空（按名称自动配色）。服务端
 * 的校验仍是最终结果。
 */
import type { DatasetLabel } from "@/features/datasets/api"
import { CLASS_NAME_MAX_LENGTH } from "@/features/datasets/class-list"
import { labelColor } from "@/features/datasets/label-colors"
import { characterCount } from "@/features/datasets/naming"
import type { ProjectClass } from "@/features/projects/api"

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
export function classColor(cls: ProjectClass) {
  return cls.color && isHexColor(cls.color)
    ? cls.color.toLowerCase()
    : labelColor(cls.name)
}

/**
 * 类别名称 → 颜色。不在列表中的类别（如其它工具标注的）按名称自动配色。
 */
export function colorResolver(classes: readonly ProjectClass[]) {
  const colors = new Map(classes.map((cls) => [cls.name, classColor(cls)]))
  return (name: string) => colors.get(name) ?? labelColor(name)
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

/**
 * 标注时可选的一个类别。`source` 为 `project`：在数据集所属项目的类别列表中
 * （训练时按列表的顺序导出 YOLO）；`dataset`：只是数据集中已有（或这次标注中
 * 用过）的标签。`color` 是显示颜色。
 */
export type ClassOption = {
  name: string
  color: string
  source: "project" | "dataset"
}

/**
 * 标注时的候选类别，类别选择框、数字键 1–9 与 0（前 10 个）、框的类别下拉框
 * 都按这个顺序：
 *
 * 1. `projectClasses`：数据集所属项目的类别列表（见 projects/api 的
 *    `datasetClassesQuery`），按列表的顺序；
 * 2. `shown`：这次标注中已经给出过的其它候选，按当时的顺序（见下）；
 * 3. `datasetLabels`：数据集中已有、但不在类别列表中的标签，按目标数降序、
 *    名称（码点）升序；
 * 4. `extraNames`：标签统计里还没有的名称（如这次标注中新输入的、本图上的，
 *    保存后统计才会刷新），按出现的顺序。
 *
 * 标签的目标数随标注而变，每次都重新排序的话数字键对应的类别会悄悄改变（刚才
 * 按 2 是 burr，保存后可能就成了 hole），因此调用方把上一次给出的、不在类别
 * 列表中的候选作为 `shown` 传回：它们保持原来的顺序，之后新出现的排在后面；
 * 已经不在数据集中的也保留（仍然是可以使用的名称）。
 *
 * 同名的只保留第一个；空名称跳过（不能作为下拉框的选项）。颜色：类别列表中
 * 设置了颜色的用它，其余按名称自动配色（与 {@link colorResolver} 一致）。
 */
export function buildClassOptions({
  projectClasses,
  datasetLabels,
  extraNames = [],
  shown = [],
}: {
  projectClasses: readonly ProjectClass[]
  datasetLabels: readonly Pick<DatasetLabel, "name" | "object_count">[]
  extraNames?: readonly string[]
  shown?: readonly string[]
}): ClassOption[] {
  const options: ClassOption[] = []
  const seen = new Set<string>()
  const add = (name: string, color: string, source: ClassOption["source"]) => {
    if (name !== "" && !seen.has(name)) {
      seen.add(name)
      options.push({ name, color, source })
    }
  }
  for (const cls of projectClasses) {
    add(cls.name, classColor(cls), "project")
  }
  const labels = [...datasetLabels]
    .sort(
      (a, b) =>
        b.object_count - a.object_count ||
        (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
    )
    .map((label) => label.name)
  for (const name of [...shown, ...labels, ...extraNames]) {
    add(name, labelColor(name), "dataset")
  }
  return options
}

/** 候选中不在类别列表里的名称（按顺序），作为下一次的 `shown` 传给 {@link buildClassOptions}。 */
export function unlistedNames(options: readonly ClassOption[]) {
  return options
    .filter((option) => option.source === "dataset")
    .map((option) => option.name)
}
