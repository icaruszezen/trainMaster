/**
 * 一张图片的标注（框列表）在编辑器中的状态：选中项与撤销 / 重做历史。
 * 所有修改都返回新数组（不可变），历史中保存的是修改前的整份框列表。
 */
import type { AnnotationObject } from "@/features/datasets/api"
import type { Box, Size } from "@/features/annotation/geometry"
import { clampBox, moveBox } from "@/features/annotation/geometry"

/** 编辑器中的框：在 VOC 字段之外带一个只在页面内使用的 `uid`。 */
export type EditorObject = AnnotationObject & { uid: string }

/** 撤销历史的最大步数。 */
export const HISTORY_LIMIT = 200

export type DocumentState = {
  /** 载入时给出的 key（哪张图片、第几次载入），见 `append`。 */
  key: string
  objects: EditorObject[]
  /** 选中的框的 uid。 */
  selected: string | null
  past: EditorObject[][]
  future: EditorObject[][]
}

export type DocumentAction =
  /** 载入一张图片的标注（清空历史）；`key` 用于生成稳定的 uid。 */
  | { type: "load"; objects: readonly AnnotationObject[]; key: string }
  /** 一次修改：记入历史，可撤销。`select` 给出修改后选中的框。 */
  | { type: "change"; objects: EditorObject[]; select?: string | null }
  /**
   * 在当前的框列表末尾加入框（可撤销），用于异步操作（如读取上一张图片）完成
   * 之后：基于此刻的框列表，而不是开始时的。`key` 是开始时文档的 key，此后
   * 载入了别的图片（或重新读取）时忽略，不会把框加到别的图片上。
   */
  | {
      type: "append"
      key: string
      objects: EditorObject[]
      select?: string | null
    }
  | { type: "select"; uid: string | null }
  | { type: "undo" }
  | { type: "redo" }

export const emptyDocument: DocumentState = {
  key: "",
  objects: [],
  selected: null,
  past: [],
  future: [],
}

export function loadObjects(
  objects: readonly AnnotationObject[],
  key: string
): EditorObject[] {
  return objects.map((object, index) => ({
    ...toAnnotationObject(object),
    uid: `${key}:${index}`,
  }))
}

function keepSelection(objects: EditorObject[], selected: string | null) {
  return selected !== null && objects.some((o) => o.uid === selected)
    ? selected
    : null
}

export function documentReducer(
  state: DocumentState,
  action: DocumentAction
): DocumentState {
  switch (action.type) {
    case "load":
      return {
        key: action.key,
        objects: loadObjects(action.objects, action.key),
        selected: null,
        past: [],
        future: [],
      }
    case "change": {
      if (action.objects === state.objects) {
        return state
      }
      const selected =
        action.select !== undefined ? action.select : state.selected
      return {
        key: state.key,
        objects: action.objects,
        selected: keepSelection(action.objects, selected),
        past: [...state.past, state.objects].slice(-HISTORY_LIMIT),
        future: [],
      }
    }
    case "append":
      if (action.key !== state.key || action.objects.length === 0) {
        return state
      }
      return documentReducer(state, {
        type: "change",
        objects: [...state.objects, ...action.objects],
        select: action.select,
      })
    case "select":
      return {
        ...state,
        selected: keepSelection(state.objects, action.uid),
      }
    case "undo": {
      const previous = state.past.at(-1)
      if (!previous) {
        return state
      }
      return {
        key: state.key,
        objects: previous,
        selected: keepSelection(previous, state.selected),
        past: state.past.slice(0, -1),
        future: [state.objects, ...state.future],
      }
    }
    case "redo": {
      const [next, ...rest] = state.future
      if (!next) {
        return state
      }
      return {
        key: state.key,
        objects: next,
        selected: keepSelection(next, state.selected),
        past: [...state.past, state.objects].slice(-HISTORY_LIMIT),
        future: rest,
      }
    }
  }
}

/** 只保留 VOC 字段（去掉 uid 等页面内的字段）。 */
export function toAnnotationObject(object: AnnotationObject): AnnotationObject {
  return {
    name: object.name,
    xmin: object.xmin,
    ymin: object.ymin,
    xmax: object.xmax,
    ymax: object.ymax,
    difficult: object.difficult,
    truncated: object.truncated,
  }
}

/** 框列表的规范文本，用于判断是否有未保存的修改。 */
export function serializeObjects(objects: readonly AnnotationObject[]) {
  return JSON.stringify(objects.map(toAnnotationObject))
}

export function updateObject(
  objects: EditorObject[],
  uid: string,
  patch: Partial<AnnotationObject>
): EditorObject[] {
  let changed = false
  const next = objects.map((object) => {
    if (object.uid !== uid) {
      return object
    }
    const updated = { ...object, ...patch }
    if (serializeObjects([updated]) === serializeObjects([object])) {
      return object
    }
    changed = true
    return updated
  })
  return changed ? next : objects
}

export function removeObject(
  objects: EditorObject[],
  uid: string
): EditorObject[] {
  const next = objects.filter((object) => object.uid !== uid)
  return next.length === objects.length ? objects : next
}

/** 把多个框的类别一起改掉（如重命名了类别列表中的类别）。 */
export function renameLabel(
  objects: EditorObject[],
  from: string,
  to: string
): EditorObject[] {
  if (!objects.some((object) => object.name === from)) {
    return objects
  }
  return objects.map((object) =>
    object.name === from ? { ...object, name: to } : object
  )
}

/** 复制一个框，向右下错开 `offset` 像素（放不下时贴着图片边缘）。 */
export function duplicateBox(box: Box, size: Size, offset = 10): Box {
  return moveBox(box, offset, offset, size)
}

/**
 * 粘贴复制的框到另一张图片：限制在图片范围内，完全落在图片外的框丢弃。
 * 返回粘贴的框与被丢弃的数量。
 */
export function pasteObjects(
  copied: readonly AnnotationObject[],
  size: Size,
  newUid: () => string
): { objects: EditorObject[]; dropped: number } {
  const objects: EditorObject[] = []
  let dropped = 0
  for (const object of copied) {
    const box = clampBox(object, size)
    if (!box) {
      dropped += 1
      continue
    }
    objects.push({ ...toAnnotationObject(object), ...box, uid: newUid() })
  }
  return { objects, dropped }
}

/**
 * 下一个 / 上一个框（Tab / Shift+Tab 依次选择）。没有选中时从第一个 / 最后
 * 一个开始；已经是最后一个 / 第一个（或列表为空）时为 null：取消选中，焦点
 * 照常移出画布，不困住键盘用户。
 */
export function cycleSelection(
  objects: readonly { uid: string }[],
  selected: string | null,
  direction: 1 | -1
): string | null {
  if (objects.length === 0) {
    return null
  }
  const index = objects.findIndex((object) => object.uid === selected)
  if (index === -1) {
    return direction === 1 ? objects[0].uid : objects[objects.length - 1].uid
  }
  return objects[index + direction]?.uid ?? null
}

/** 按类别统计框数，按数量降序、名称升序。 */
export function countLabels(objects: readonly { name: string }[]) {
  const counts = new Map<string, number>()
  for (const object of objects) {
    counts.set(object.name, (counts.get(object.name) ?? 0) + 1)
  }
  return counts
}
