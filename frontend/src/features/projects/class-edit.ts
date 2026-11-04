/**
 * 项目类别列表的编辑：增删、改名、颜色、顺序（训练时 YOLO 的类别编号），按
 * classes.txt 重新排列，以及从数据集已有的标签加入。都是不改动参数的纯函数；
 * 校验规则见 annotation/classes.ts 的 classNameError。
 */
import { classNameError, isHexColor } from "@/features/annotation/classes"
import { CLASS_LIST_MAX_COUNT } from "@/features/datasets/class-list"
import type { ProjectClass, ProjectLabel } from "@/features/projects/api"

export type ClassEditResult =
  { classes: ProjectClass[]; error: null } | { classes: null; error: string }

const fail = (error: string): ClassEditResult => ({ classes: null, error })
const ok = (classes: ProjectClass[]): ClassEditResult => ({
  classes,
  error: null,
})

export function addClass(
  classes: readonly ProjectClass[],
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
  classes: readonly ProjectClass[],
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

export function setClassColor<T extends ProjectClass>(
  classes: readonly T[],
  index: number,
  color: string
): T[] {
  const value = isHexColor(color) ? color.toLowerCase() : ""
  return classes.map((cls, i) => (i === index ? { ...cls, color: value } : cls))
}

/** 把第 `from` 个类别移到 `to`（训练时 YOLO 的类别编号按这个顺序）。 */
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

export type ArrangeResult<T> = {
  classes: T[]
  /** `names` 中原来不在列表里、新建的类别数。 */
  added: number
  /** 不在 `names` 中、排在最后的原有类别数。 */
  rest: number
  /** 超出 {@link CLASS_LIST_MAX_COUNT} 而舍弃的类别数（从最后开始舍弃）。 */
  dropped: number
}

/**
 * 按 `names`（如 classes.txt 中的类别顺序）重新排列类别列表，用于沿用已有
 * 模型或其它工具的类别编号：`names` 中的类别按它的顺序排在最前，列表中已有的
 * 沿用原来的项（颜色等都不变），没有的用 `create` 新建；列表中其余的类别保持
 * 原来的相对顺序排在后面，不会丢掉。
 *
 * 名称去掉首尾空白后比较（与保存时一致），列表中有重名时只移动第一个，其余的
 * 留在后面（重名仍由编辑界面提示）。结果最多 {@link CLASS_LIST_MAX_COUNT} 个，
 * 超出时舍弃排在最后的类别。
 */
export function arrangeByNames<T extends { name: string }>(
  classes: readonly T[],
  names: readonly string[],
  create: (name: string) => T
): ArrangeResult<T> {
  const indexOf = new Map<string, number>()
  classes.forEach((cls, index) => {
    const name = cls.name.trim()
    if (!indexOf.has(name)) {
      indexOf.set(name, index)
    }
  })
  const moved = new Set<number>()
  const head: { item: T; created: boolean }[] = []
  for (const name of new Set(names)) {
    const index = indexOf.get(name)
    if (index === undefined) {
      head.push({ item: create(name), created: true })
    } else {
      head.push({ item: classes[index], created: false })
      moved.add(index)
    }
  }
  const rest = classes
    .filter((_, index) => !moved.has(index))
    .map((item) => ({ item, created: false }))
  const all = [...head, ...rest]
  const kept = all.slice(0, CLASS_LIST_MAX_COUNT)
  return {
    classes: kept.map((entry) => entry.item),
    added: kept.filter((entry) => entry.created).length,
    rest: Math.max(0, kept.length - head.length),
    dropped: all.length - kept.length,
  }
}

/** 目标数降序，相同时按名称（码点）升序。 */
function byObjectCount(
  a: Pick<ProjectLabel, "name" | "object_count">,
  b: Pick<ProjectLabel, "name" | "object_count">
) {
  return (
    b.object_count - a.object_count ||
    (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
  )
}

/**
 * 项目的数据集中有、但不在类别列表里的标签（训练导出时会忽略它们的目标），
 * 按 {@link mergeLabels} 追加它们的顺序排列：“全部加入”后在列表中的顺序与
 * 显示的一致。
 */
export function labelsNotInList<
  T extends Pick<ProjectLabel, "name" | "object_count">,
>(classes: readonly ProjectClass[], labels: readonly T[]): T[] {
  const known = new Set(classes.map((cls) => cls.name))
  return labels.filter((label) => !known.has(label.name)).sort(byObjectCount)
}

/**
 * 把数据集中已有、但不在类别列表里的标签追加到列表末尾，返回新列表与追加的
 * 数量。`order` 中的类别（如 YOLO 导入时的 classes.txt，没有目标的也算）按
 * 原顺序排在最前，导出 YOLO 时类别编号保持不变；其余标签按目标数降序、名称
 * 升序。
 */
export function mergeLabels(
  classes: readonly ProjectClass[],
  labels: readonly Pick<ProjectLabel, "name" | "object_count">[],
  order: readonly string[] = []
): { classes: ProjectClass[]; added: number } {
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
    .sort(byObjectCount)
    .map((label) => label.name)
  const extra = [...names, ...rest]
    .slice(0, Math.max(0, CLASS_LIST_MAX_COUNT - classes.length))
    .map((name) => ({ name, color: "" }))
  return { classes: [...classes, ...extra], added: extra.length }
}
