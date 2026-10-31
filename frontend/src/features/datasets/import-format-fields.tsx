/**
 * 上传压缩包 / 文件夹时选择标注格式；选择 YOLO 时一并填写类别顺序。
 * 新建数据集与向已有数据集导入共用。
 */
import { useMemo, useRef, useState, type Ref } from "react"

import type {
  AnnotationFormat,
  CreateImportInput,
} from "@/features/datasets/api"
import { AnnotationFormatPicker } from "@/features/datasets/annotation-format-picker"
import { parseClassList, readClassFile } from "@/features/datasets/class-list"
import { ClassListField } from "@/features/datasets/class-list-field"
import { findClassesFile } from "@/features/datasets/files"
import type { FolderSelection } from "@/features/datasets/source-pickers"

const importDescriptions: Record<AnnotationFormat, string> = {
  voc: "与图片同目录、同名的 .xml，或 JPEGImages / Annotations 目录结构",
  xanylabeling:
    "与图片同目录、同名的 .json（LabelMe 格式同样适用），导入时转换为 VOC",
  yolo: "与图片同目录或 images/ ↔ labels/ 目录下同名的 .txt，按类别顺序转换为 VOC",
}

export function useImportFormat() {
  const [format, setFormat] = useState<AnnotationFormat>("voc")
  const [classesText, setClassesText] = useState("")
  // 从所选文件夹的 classes.txt 自动填入的内容；文本被修改后不再提示。
  const [prefill, setPrefill] = useState<{
    path: string
    text: string
  } | null>(null)
  // 连续选择文件夹时只采用最后一次的读取结果。
  const prefillRequest = useRef(0)

  const classList = useMemo(() => parseClassList(classesText), [classesText])

  /**
   * 选中文件夹后：若类别顺序还空着（或仍是上一个文件夹自动填入的内容），
   * 用文件夹中层级最浅的 classes.txt 填入。
   */
  const prefillFromFolder = (selection: FolderSelection | null) => {
    prefillRequest.current += 1
    const request = prefillRequest.current
    const previous = prefill
    const replaceable = (text: string) =>
      text.trim() === "" || (previous !== null && text === previous.text)

    const file = selection ? findClassesFile(selection.files) : null
    if (!file) {
      if (previous) {
        setClassesText((text) => (text === previous.text ? "" : text))
        setPrefill(null)
      }
      return
    }
    readClassFile(file.file).then(
      (text) => {
        if (request !== prefillRequest.current) {
          return
        }
        setClassesText((current) => (replaceable(current) ? text : current))
        setPrefill({ path: file.path, text })
      },
      // 读不出来就不预填，仍可手动输入或从文件读取。
      () => {}
    )
  }

  const isYolo = format === "yolo"
  /** 创建导入任务时附带的字段。 */
  const importFields: Pick<CreateImportInput, "annotation_format" | "classes"> =
    isYolo
      ? { annotation_format: format, classes: classList.classes }
      : { annotation_format: format }

  return {
    format,
    setFormat,
    classesText,
    setClassesText,
    classList,
    prefill,
    prefillFromFolder,
    importFields,
    /** 选择了 YOLO 但类别顺序为空或有误：不能提交。 */
    invalid: isYolo && classList.error !== null,
    /** 填写过类别顺序（关闭对话框前需要确认）。 */
    dirty: classesText.trim() !== "",
  }
}

export type ImportFormatState = ReturnType<typeof useImportFormat>

type ImportFormatFieldsProps = {
  state: ImportFormatState
  /** 尝试提交过：类别顺序为空时也提示。 */
  showErrors: boolean
  disabled?: boolean
  classFieldRef?: Ref<HTMLTextAreaElement>
}

export function ImportFormatFields({
  state,
  showErrors,
  disabled,
  classFieldRef,
}: ImportFormatFieldsProps) {
  const { prefill, classesText } = state

  return (
    <div className="grid gap-4">
      <AnnotationFormatPicker
        label="标注格式"
        value={state.format}
        onChange={state.setFormat}
        descriptions={importDescriptions}
        disabled={disabled}
      />
      {state.format === "yolo" && (
        <ClassListField
          value={classesText}
          onChange={state.setClassesText}
          result={state.classList}
          showRequired={showErrors}
          description="YOLO 标注只记录类别编号，请按标注时的顺序填写类别名称（与 classes.txt 一致），导入时据此写入 VOC 的类别名称。"
          hint={
            prefill && prefill.text === classesText
              ? `已从文件夹中的 ${prefill.path} 填入，请确认顺序与标注时一致`
              : undefined
          }
          disabled={disabled}
          textareaRef={classFieldRef}
        />
      )}
    </div>
  )
}
