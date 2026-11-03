/**
 * YOLO 版本、任务与 ONNX 转换选项的规则（纯函数，便于测试）。
 *
 * 与后端 `internal/model`（versions.go、options.go）保持一致：版本表、允许的
 * 后处理方式与默认值、输入尺寸的范围、下载文件名。服务端的校验仍是最终结果，
 * 这里用于在界面上给出默认值、只列出可用的选项，并提前提示错误。
 */
import type {
  ExportOptions,
  Model,
  ModelTask,
  Postprocess,
  YoloVersion,
} from "@/features/models/api"

export type YoloVersionInfo = {
  value: YoloVersion
  /** 界面上的名称，如 `YOLO11`。 */
  label: string
  /** 检测头带端到端（one-to-one，NMS-free）分支，ONNX 默认导出为 end2end。 */
  end2end: boolean
  /** 上传时选择版本的简短说明。 */
  description: string
}

/** 支持的版本，从旧到新（与后端 YOLOVersions 一致）。 */
export const yoloVersions: readonly YoloVersionInfo[] = [
  {
    value: "yolov8",
    label: "YOLOv8",
    end2end: false,
    description: "C2f 结构，应用最广；导出后需要自行 NMS",
  },
  {
    value: "yolov9",
    label: "YOLOv9",
    end2end: false,
    description: "GELAN 结构（RepNCSPELAN4、ADown）；导出后需要自行 NMS",
  },
  {
    value: "yolov10",
    label: "YOLOv10",
    end2end: true,
    description: "端到端检测头，推理时不需要 NMS",
  },
  {
    value: "yolo11",
    label: "YOLO11",
    end2end: false,
    description: "C3k2、C2PSA 结构；导出后需要自行 NMS",
  },
  {
    value: "yolo12",
    label: "YOLO12",
    end2end: false,
    description: "以注意力为核心（A2C2f）；导出后需要自行 NMS",
  },
  {
    value: "yolo26",
    label: "YOLO26",
    end2end: true,
    description: "端到端检测头，推理时不需要 NMS，适合边缘部署",
  },
]

export function findYoloVersion(value: string | null | undefined) {
  return yoloVersions.find((version) => version.value === value)
}

/** 版本的显示名称；不认识的值原样显示。 */
export function yoloVersionLabel(value: string | null | undefined) {
  return findYoloVersion(value)?.label ?? (value || "未知版本")
}

/** 版本 + 规模，如 `YOLO11s`、`YOLOv8n`；规模未知时只有版本。 */
export function modelArchLabel(
  model: Pick<Model, "yolo_version"> & { info: { scale: string } }
) {
  return `${yoloVersionLabel(model.yolo_version)}${model.info.scale}`
}

const taskLabels: Record<Exclude<ModelTask, "">, string> = {
  detect: "检测",
  segment: "分割",
  classify: "分类",
  pose: "姿态",
  obb: "旋转框",
  semantic: "语义分割",
  depth: "深度估计",
}

/** 任务的名称，如“检测”；无法识别时为“未知任务”，不认识的值原样显示。 */
export function taskLabel(task: string) {
  if (task === "") {
    return "未知任务"
  }
  return taskLabels[task as keyof typeof taskLabels] ?? task
}

/** 带检测框的任务：只有它们有端到端检测头，也只有它们可以在导出时嵌入 NMS。 */
const NMS_TASKS: readonly string[] = ["detect", "segment", "pose", "obb"]

type ExportTarget = Pick<Model, "yolo_version" | "task"> & {
  info: Pick<Model["info"], "end2end" | "image_size">
}

/**
 * 模型带端到端检测头：YOLOv10、YOLO26 都有，其它版本以文件中的标记为准。
 * 只对带检测框的任务有意义（见 allowedPostprocess）。
 */
export function supportsEnd2end(model: ExportTarget) {
  return (
    findYoloVersion(model.yolo_version)?.end2end === true || model.info.end2end
  )
}

/**
 * 模型可以使用的后处理方式，默认的在前（与后端 AllowedPostprocess 一致）：只有
 * 带检测框的任务有端到端检测头、可以嵌入 NMS，其它任务（分类、语义分割、深度估计，
 * 即使是 YOLO26）只有原始输出；带端到端检测头的模型默认 end2end（也可以在
 * one-to-many 头上嵌入 NMS）。
 */
export function allowedPostprocess(model: ExportTarget): Postprocess[] {
  if (!NMS_TASKS.includes(model.task)) {
    return ["raw"]
  }
  const modes: Postprocess[] = []
  if (supportsEnd2end(model)) {
    modes.push("end2end")
  }
  modes.push("raw", "nms")
  return modes
}

/** 只有原始输出时的说明，如“语义分割模型只有原始输出。”；还有其它方式时为 null。 */
export function rawOnlyNote(model: ExportTarget) {
  if (NMS_TASKS.includes(model.task)) {
    return null
  }
  return model.task === ""
    ? "无法识别模型的任务，只能导出原始输出。"
    : `${taskLabel(model.task)}模型只有原始输出。`
}

/**
 * 某种后处理方式与动态尺寸冲突时的原因，否则为 null。Ultralytics 按导出时的
 * batch（固定为 1）展开嵌入的 NMS，动态 batch 时第二张图起没有结果，所以嵌入 NMS
 * 的 ONNX 不能使用动态尺寸（与后端 NormalizeExport 一致）。
 */
export function postprocessConflict(mode: Postprocess, dynamic: boolean) {
  return dynamic && mode === "nms"
    ? "嵌入 NMS 的 ONNX 只能固定 batch 为 1，关闭动态尺寸后可以选择"
    : null
}

/**
 * 让选项符合模型的规则：模型不支持的后处理方式、与动态尺寸冲突的 nms 换为默认的
 * 后处理方式（默认值从不是 nms）。用于打开动态尺寸，以及从已有的转换记录（可能
 * 早于这些规则）回填选项。
 */
export function fitExportOptions<
  T extends Pick<ExportOptions, "dynamic" | "postprocess">,
>(options: T, model: ExportTarget): T {
  const usable = allowedPostprocess(model).filter(
    (mode) => postprocessConflict(mode, options.dynamic) === null
  )
  return usable.includes(options.postprocess)
    ? options
    : { ...options, postprocess: usable[0] }
}

export const IMG_SIZE_MIN = 32
export const IMG_SIZE_MAX = 4096
/** 检测头最大的下采样倍数：输入尺寸必须是它的倍数。 */
export const IMG_SIZE_STRIDE = 32
export const DEFAULT_IMG_SIZE = 640
export const OPSET_MIN = 11
export const OPSET_MAX = 21

/** 训练尺寸 → 合法的导出尺寸：向上取 32 的倍数（与 Ultralytics 一致）并限制范围。 */
export function roundImgSize(size: number) {
  const rounded = Math.ceil(size / IMG_SIZE_STRIDE) * IMG_SIZE_STRIDE
  return Math.min(Math.max(rounded, IMG_SIZE_MIN), IMG_SIZE_MAX)
}

/** 默认的转换选项：训练尺寸的正方形（没有时 640）、静态尺寸、默认后处理、自动 opset。 */
export function defaultExportOptions(model: ExportTarget): ExportOptions {
  const trained = model.info.image_size
  const size =
    trained !== null && trained > 0 ? roundImgSize(trained) : DEFAULT_IMG_SIZE
  return {
    imgsz: [size, size],
    dynamic: false,
    postprocess: allowedPostprocess(model)[0],
    opset: null,
  }
}

/** 校验输入框中的尺寸（高或宽），合法时返回 null，否则返回中文提示。 */
export function validateImgSize(raw: string): string | null {
  const text = raw.trim()
  if (text === "") {
    return "请填写尺寸"
  }
  if (!/^\d+$/.test(text)) {
    return "请填写整数"
  }
  const value = Number(text)
  if (value < IMG_SIZE_MIN || value > IMG_SIZE_MAX) {
    return `尺寸应在 ${IMG_SIZE_MIN}–${IMG_SIZE_MAX} 之间`
  }
  if (value % IMG_SIZE_STRIDE !== 0) {
    const lower = Math.max(
      IMG_SIZE_MIN,
      Math.floor(value / IMG_SIZE_STRIDE) * IMG_SIZE_STRIDE
    )
    return `尺寸必须是 ${IMG_SIZE_STRIDE} 的倍数，如 ${lower} 或 ${roundImgSize(value)}`
  }
  return null
}

/** 可选的 opset，新的在前。 */
export const opsetChoices: readonly number[] = Array.from(
  { length: OPSET_MAX - OPSET_MIN + 1 },
  (_, index) => OPSET_MAX - index
)

/** 两组规范化的选项是否相同（相同的选项共用一份转换结果）。 */
export function sameExportOptions(a: ExportOptions, b: ExportOptions) {
  return (
    a.imgsz[0] === b.imgsz[0] &&
    a.imgsz[1] === b.imgsz[1] &&
    a.dynamic === b.dynamic &&
    a.postprocess === b.postprocess &&
    a.opset === b.opset
  )
}

export const postprocessLabels: Record<Postprocess, string> = {
  raw: "原始输出",
  nms: "嵌入 NMS",
  end2end: "端到端",
}

/** 输入尺寸的简写：正方形为边长，否则为 `高×宽`。 */
export function imgSizeText([height, width]: readonly [number, number]) {
  return height === width ? String(height) : `${height}×${width}`
}

/** 转换选项的简短描述，如 `640 · 端到端 · opset 自动`。 */
export function exportOptionsText(options: ExportOptions) {
  return [
    imgSizeText(options.imgsz),
    options.dynamic ? "动态尺寸" : null,
    postprocessLabels[options.postprocess] ?? options.postprocess,
    options.opset === null ? "opset 自动" : `opset ${options.opset}`,
  ]
    .filter(Boolean)
    .join(" · ")
}

/**
 * 原始输出中每张图的候选框数：三个检测层（下采样 8、16、32 倍）的网格数之和，
 * 640×640 时为 8400。
 */
export function anchorCount(height: number, width: number) {
  return [8, 16, 32].reduce(
    (sum, stride) => sum + (height / stride) * (width / stride),
    0
  )
}

/** ONNX 的一个输出：形状（batch 为 1）与每一维的含义。 */
export type OutputShape = { shape: string; meaning: string }

export type PostprocessDescription = {
  label: string
  /** 一句话说明：是否需要自行 NMS。 */
  summary: string
  outputs: OutputShape[]
}

type DescribeContext = {
  task: string
  /** 类别数；未知时为 null。 */
  classCount: number | null
  imgsz: readonly [number, number]
  dynamic: boolean
}

/**
 * 后处理方式的说明与 ONNX 输出的形状。形状来自 Ultralytics 的导出结果：原始输出为
 * `1 × (4 + 类别数) × 候选框数`（框为中心点与宽高），NMS / 端到端为每张图最多
 * 300 个结果（框为左上角与右下角）。动态尺寸时候选框数随输入变化，记为 N。
 */
export function describePostprocess(
  mode: Postprocess,
  { task, classCount, imgsz, dynamic }: DescribeContext
): PostprocessDescription {
  const label = postprocessLabels[mode]
  const nc = classCount === null ? "类别数" : String(classCount)
  const [height, width] = imgsz
  const anchors = dynamic ? "N" : String(anchorCount(height, width))
  const candidates = dynamic
    ? "N 个候选框（随输入尺寸变化）"
    : `${anchors} 个候选框`
  // 已知类别数时写出通道数，否则写出算式。
  const channels = (fixed: number, formula: string) =>
    classCount === null ? formula : String(fixed + classCount)
  const proto: OutputShape = {
    shape: `1 × 32 × ${dynamic ? "H/4 × W/4" : `${height / 4} × ${width / 4}`}`,
    meaning: "掩码原型，与掩码系数相乘得到每个目标的掩码",
  }

  if (mode === "raw") {
    switch (task) {
      case "classify":
        return {
          label,
          summary: "输出各类别的概率，取最大值即可",
          outputs: [{ shape: `1 × ${nc}`, meaning: "各类别的概率" }],
        }
      case "segment":
        return {
          label,
          summary: "需要自行 NMS",
          outputs: [
            {
              shape: `1 × ${channels(36, "(4 + 类别数 + 32)")} × ${anchors}`,
              meaning: `${candidates}，每个为 4 个框坐标（中心点与宽高）、${nc} 个类别分数与 32 个掩码系数`,
            },
            proto,
          ],
        }
      case "pose":
        return {
          label,
          summary: "需要自行 NMS",
          outputs: [
            {
              shape: `1 × (4 + ${nc} + 关键点数 × 3) × ${anchors}`,
              meaning: `${candidates}，每个为框坐标、类别分数与关键点（x, y, 可见度）`,
            },
          ],
        }
      case "obb":
        return {
          label,
          summary: "需要自行 NMS",
          outputs: [
            {
              shape: `1 × ${channels(5, "(4 + 类别数 + 1)")} × ${anchors}`,
              meaning: `${candidates}，每个为 4 个框坐标、${nc} 个类别分数与旋转角度`,
            },
          ],
        }
      case "detect":
        return {
          label,
          summary: "需要自行 NMS",
          outputs: [
            {
              shape: `1 × ${channels(4, "(4 + 类别数)")} × ${anchors}`,
              meaning: `${candidates}，每个为 4 个框坐标（中心点与宽高）与 ${nc} 个类别分数`,
            },
          ],
        }
      default:
        return { label, summary: "模型的原始输出", outputs: [] }
    }
  }

  const summary = mode === "end2end" ? "不需要 NMS" : "NMS 已嵌入模型"
  const boxes = "每张图最多 300 个目标，每个为 x1, y1, x2, y2, 置信度, 类别"
  switch (task) {
    case "segment":
      return {
        label,
        summary,
        outputs: [
          { shape: "1 × 300 × 38", meaning: `${boxes}与 32 个掩码系数` },
          proto,
        ],
      }
    case "pose":
      return {
        label,
        summary,
        outputs: [
          {
            shape: "1 × 300 × (6 + 关键点数 × 3)",
            meaning: `${boxes}与关键点（x, y, 可见度）`,
          },
        ],
      }
    case "obb":
      return {
        label,
        summary,
        outputs: [
          {
            shape: "1 × 300 × 7",
            meaning: "每张图最多 300 个目标，每个为旋转框、置信度、类别与角度",
          },
        ],
      }
    default:
      return {
        label,
        summary,
        outputs: [{ shape: "1 × 300 × 6", meaning: boxes }],
      }
  }
}

/** 下载文件名中不能出现的字符（Windows），以及控制字符。 */
// eslint-disable-next-line no-control-regex -- 控制字符正是要替换的内容
const FILE_NAME_UNSAFE = /[\\/:*?"<>|\u0000-\u001f\u007f]/g

/** 项目名用作下载文件名的前缀（与后端 safeFileStem 一致）。 */
export function safeFileStem(name: string) {
  const stem = name.replace(FILE_NAME_UNSAFE, "_")
  return stem === "" ? "model" : stem
}

/** PT 的下载文件名：`<项目名>_v<版本>.pt`。 */
export function modelFileName(projectName: string, version: number) {
  return `${safeFileStem(projectName)}_v${version}.pt`
}

/**
 * ONNX 的下载文件名：`<项目名>_v<版本>_<尺寸>[_dynamic][_nms|_end2end][_opset<N>].onnx`，
 * 尺寸为正方形的边长或 `<高>x<宽>`（与后端 ExportFileName 一致）。
 */
export function exportFileName(
  projectName: string,
  version: number,
  options: ExportOptions
) {
  const [height, width] = options.imgsz
  let name = `${safeFileStem(projectName)}_v${version}_`
  name += height === width ? String(height) : `${height}x${width}`
  if (options.dynamic) {
    name += "_dynamic"
  }
  if (options.postprocess !== "raw") {
    name += `_${options.postprocess}`
  }
  if (options.opset !== null) {
    name += `_opset${options.opset}`
  }
  return `${name}.onnx`
}
