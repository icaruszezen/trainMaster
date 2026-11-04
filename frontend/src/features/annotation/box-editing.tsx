/**
 * 标注工作台与审图编辑器共用的框编辑：画框后选择类别、修改类别与属性、复制
 * 粘贴、键盘移动与缩放框、选中切换、画面缩放与显示开关，以及画布上的类别
 * 选择框。编辑器自己的命令（切换图片、保存、空标注、审核状态等）不在这里。
 */
import { useRef, type Dispatch, type ReactNode, type RefObject } from "react"
import { toast } from "sonner"

import type { CanvasHandle } from "@/features/annotation/annotation-canvas"
import type { ClassOption } from "@/features/annotation/classes"
import {
  cycleSelection,
  duplicateBox,
  pasteObjects,
  removeObject,
  toAnnotationObject,
  updateObject,
  type DocumentAction,
  type DocumentState,
  type EditorObject,
} from "@/features/annotation/document"
import type { Picker, ViewOptions } from "@/features/annotation/editor-common"
import {
  moveBox,
  resizeEdges,
  type Box,
  type OverlayAnchor,
  type Size,
} from "@/features/annotation/geometry"
import {
  LabelPicker,
  type CandidateStatus,
} from "@/features/annotation/label-picker"
import {
  focusCanvas,
  type Command,
  type Tool,
} from "@/features/annotation/shortcuts"
import type { AnnotationObject } from "@/features/datasets/api"
import { formatNumber } from "@/lib/format"

/** 复制的框与它们来自哪张图片（粘贴到同一张时错开一点）。 */
export type Clipboard = { imageId: number; objects: AnnotationObject[] }

type BoxEditingOptions = {
  doc: DocumentState
  dispatch: Dispatch<DocumentAction>
  /** 可以修改框：当前图片已载入且不是只读。 */
  editable: boolean
  size: Size | null
  imageId: number
  view: ViewOptions
  onViewChange: (view: ViewOptions) => void
  tool: Tool
  onToolChange: (tool: Tool) => void
  picker: Picker | null
  onPickerChange: (picker: Picker | null) => void
  /** 候选类别（见 `buildClassOptions`）与它们的读取状态。 */
  classOptions: readonly ClassOption[]
  candidateStatus: CandidateStatus | null
  /** 标出不在类别列表中的候选（见 LabelPicker）。 */
  flagUnlisted: boolean
  activeClass: string | null
  onActiveClassChange: (name: string) => void
  /** 输入了候选中没有的新类别名称。 */
  onTypedName: (name: string) => void
  canvasRef: RefObject<CanvasHandle | null>
  mod: string
}

/** {@link useBoxEditing} 的 `run` 不处理的命令（由编辑器自己处理）。 */
export const UNHANDLED = "unhandled"

export function useBoxEditing(options: BoxEditingOptions) {
  const {
    doc,
    dispatch,
    editable,
    size,
    imageId,
    view,
    picker,
    onPickerChange: setPicker,
    classOptions,
    candidateStatus,
    onActiveClassChange: setActiveClass,
    canvasRef,
    mod,
  } = options
  const uidCounter = useRef(0)
  const clipboard = useRef<Clipboard>({ imageId: 0, objects: [] })

  const newUid = () => {
    uidCounter.current += 1
    return `n${uidCounter.current}`
  }
  // 正在载入下一张图片时（不可编辑）框列表还是上一张的，不能修改。
  const change = (objects: EditorObject[], select?: string | null) => {
    if (!editable) {
      return
    }
    dispatch({ type: "change", objects, select })
  }
  const select = (uid: string | null) => dispatch({ type: "select", uid })
  const selectedObject = doc.objects.find((o) => o.uid === doc.selected)
  // 键盘操作的对象：隐藏的框（H 隐藏所有框时）不算选中，不会被看不见地移动、
  // 删除，← / → 照常切换图片。
  const activeObject =
    selectedObject && view.showBoxes ? selectedObject : undefined

  const addObject = (box: Box, name: string) => {
    const object: EditorObject = {
      ...box,
      name,
      difficult: false,
      truncated: false,
      uid: newUid(),
    }
    change([...doc.objects, object], object.uid)
  }

  const relabel = (uid: string, name: string) =>
    change(updateObject(doc.objects, uid, { name }))

  // 与 LabelImg 一样，画好框后总是选择它的类别：默认高亮当前类别（Enter 直接
  // 确定），也可以输入筛选或新的名称；关闭选择框（Esc、点击别处）时放弃这个框。
  const onCreate = (box: Box) => setPicker({ mode: "new", box })

  // 输入的新名称只是框的类别，不会加入任何类别列表（类别列表在项目页面中管理）。
  const onPick = (name: string, created: boolean) => {
    const target = picker
    setPicker(null)
    if (!target) {
      return
    }
    if (created) {
      options.onTypedName(name)
    }
    if (target.mode === "new") {
      addObject(target.box, name)
      setActiveClass(name)
    } else if (target.mode === "relabel") {
      relabel(target.uid, name)
    } else {
      setActiveClass(name)
      toast(`当前类别：${name}`, { duration: 1200 })
    }
  }

  const toggleFlag = (flag: "difficult" | "truncated") => {
    if (!activeObject) {
      toast.info("请先选中一个框")
      return
    }
    if (!editable) {
      return
    }
    const value = !activeObject[flag]
    change(updateObject(doc.objects, activeObject.uid, { [flag]: value }))
    const label =
      flag === "difficult" ? "难例（difficult）" : "截断（truncated）"
    toast(value ? `已标记为${label}` : `已取消${label}`, { duration: 1200 })
  }

  const copy = () => {
    const objects = activeObject ? [activeObject] : doc.objects
    if (objects.length === 0) {
      toast.info("这张图片上没有框可以复制")
      return
    }
    clipboard.current = {
      imageId,
      objects: objects.map(toAnnotationObject),
    }
    toast(`已复制 ${formatNumber(objects.length)} 个框`, { duration: 1200 })
  }

  /** 粘贴到 `key` 对应的文档（异步操作开始时的文档；之后切换了图片则不粘贴）。 */
  const paste = (source: Clipboard = clipboard.current, key = doc.key) => {
    if (!size || !editable) {
      return
    }
    if (source.objects.length === 0) {
      toast.info(`还没有复制框（先选中框按 ${mod}+C）`)
      return
    }
    const sameImage = source.imageId === imageId
    const shifted = sameImage
      ? source.objects.map((object) => ({
          ...object,
          ...duplicateBox(object, size),
        }))
      : source.objects
    const { objects, dropped } = pasteObjects(shifted, size, newUid)
    if (objects.length > 0) {
      // 加到此刻的框列表后面，而不是调用时的（异步操作期间可能又有修改）。
      dispatch({
        type: "append",
        key,
        objects,
        select: objects.at(-1)?.uid ?? null,
      })
    }
    if (dropped > 0) {
      toast.warning(
        `${formatNumber(dropped)} 个框在这张图片的范围之外，没有粘贴`
      )
    }
  }

  /**
   * 执行框编辑与画面的命令。返回 false 时这次按键交给浏览器（不阻止默认行为），
   * {@link UNHANDLED} 表示不是这里的命令。
   */
  const run = (command: Command): false | void | typeof UNHANDLED => {
    switch (command.type) {
      case "tool":
        options.onToolChange(command.tool)
        return
      case "undo":
        if (editable) dispatch({ type: "undo" })
        return
      case "redo":
        if (editable) dispatch({ type: "redo" })
        return
      case "copy":
        copy()
        return
      case "paste":
        paste()
        return
      case "duplicate":
        if (activeObject && size && editable) {
          const object = {
            ...activeObject,
            ...duplicateBox(activeObject, size),
            uid: newUid(),
          }
          change([...doc.objects, object], object.uid)
        }
        return
      case "delete":
        if (activeObject && editable) {
          change(removeObject(doc.objects, activeObject.uid), null)
        }
        return
      case "cycle": {
        const uid = cycleSelection(
          view.showBoxes ? doc.objects : [],
          doc.selected,
          command.direction
        )
        select(uid)
        const object = doc.objects.find((o) => o.uid === uid)
        if (!object) {
          return false // 到头了：取消选中，Tab 照常把焦点移出画布
        }
        canvasRef.current?.reveal(object)
        return
      }
      case "escape":
        if (picker) {
          setPicker(null)
        } else if (canvasRef.current?.cancelDrawing()) {
          // 已取消正在画的框
        } else if (doc.selected !== null) {
          select(null)
        } else if (options.tool !== "select") {
          options.onToolChange("select")
        }
        return
      case "class": {
        // 前 10 个候选（顺序见 buildClassOptions）。
        const option = classOptions[command.index]
        if (!option) {
          if (candidateStatus?.state === "error") {
            const { message, onRetry } = candidateStatus
            toast.error("无法读取候选类别", {
              description: message,
              action: {
                label: "重试",
                onClick: (event) => {
                  focusCanvas(event.currentTarget) // 提示随即关闭
                  onRetry()
                },
              },
            })
          } else if (candidateStatus?.state === "loading") {
            toast.info("候选类别还在读取中")
          } else {
            toast.info(
              classOptions.length === 0
                ? "还没有候选类别：画框后输入类别名称"
                : `只有 ${formatNumber(classOptions.length)} 个候选类别`
            )
          }
          return
        }
        if (activeObject && editable) {
          relabel(activeObject.uid, option.name)
        } else {
          setActiveClass(option.name)
          toast(`当前类别：${option.name}`, { duration: 1200 })
        }
        return
      }
      case "pickClass":
        setPicker(
          activeObject && editable
            ? { mode: "relabel", uid: activeObject.uid }
            : { mode: "active" }
        )
        return
      case "toggleDifficult":
        toggleFlag("difficult")
        return
      case "toggleTruncated":
        toggleFlag("truncated")
        return
      case "nudge":
        if (activeObject && size && editable) {
          change(
            updateObject(
              doc.objects,
              activeObject.uid,
              moveBox(activeObject, command.dx, command.dy, size)
            )
          )
        }
        return
      case "resize":
        if (activeObject && size && editable) {
          change(
            updateObject(
              doc.objects,
              activeObject.uid,
              resizeEdges(activeObject, command.dx, command.dy, size)
            )
          )
        }
        return
      case "zoom":
        canvasRef.current?.zoom(command.direction)
        return
      case "fit":
        canvasRef.current?.fit()
        return
      case "actualSize":
        canvasRef.current?.actualSize()
        return
      case "zoomToSelection":
        if (activeObject) {
          canvasRef.current?.zoomToBox(activeObject)
        }
        return
      case "toggleLabels":
        options.onViewChange({ ...view, showLabels: !view.showLabels })
        return
      case "toggleBoxes":
        options.onViewChange({ ...view, showBoxes: !view.showBoxes })
        return
      default:
        return UNHANDLED
    }
  }

  /** 画布上的类别选择框（画布的 `overlay`）。 */
  const overlay = (anchor: (box: Box) => OverlayAnchor | null): ReactNode => {
    if (!picker) {
      return null
    }
    const box =
      picker.mode === "new"
        ? picker.box
        : picker.mode === "relabel"
          ? doc.objects.find((o) => o.uid === picker.uid)
          : undefined
    const point = box ? anchor(box) : null
    return (
      <LabelPicker
        options={classOptions}
        flagUnlisted={options.flagUnlisted}
        current={
          picker.mode === "relabel"
            ? (doc.objects.find((o) => o.uid === picker.uid)?.name ?? null)
            : options.activeClass
        }
        title={
          picker.mode === "new"
            ? "选择这个框的类别"
            : picker.mode === "relabel"
              ? "修改类别"
              : "切换当前类别"
        }
        status={candidateStatus}
        onPick={onPick}
        onClose={() => setPicker(null)}
        anchor={point}
      />
    )
  }

  return {
    newUid,
    change,
    select,
    activeObject,
    onCreate,
    paste,
    run,
    overlay,
  }
}
