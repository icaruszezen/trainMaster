/**
 * 上传模型时的文件与说明规则，与后端 internal/model/naming.go 保持一致；
 * 服务端的校验仍是最终结果。
 */
import { characterCount } from "@/features/datasets/naming"
import { formatBytes } from "@/lib/format"

/** 模型文件最大 8 GiB。 */
export const MODEL_MAX_BYTES = 8 * 1024 ** 3
export const MODEL_FILE_NAME_MAX_LENGTH = 255
export const MODEL_DESCRIPTION_MAX_LENGTH = 500
/** 文件选择框的 accept。 */
export const MODEL_ACCEPT = ".pt"

// eslint-disable-next-line no-control-regex -- 控制字符正是要拒绝的内容
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/
// eslint-disable-next-line no-control-regex -- 同上；说明中可以有换行与制表符
const DESCRIPTION_INVALID = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/

/** 常见的其它模型格式：给出更具体的提示。 */
const otherFormats: [RegExp, string][] = [
  [
    /\.onnx$/i,
    "这是 ONNX 文件。请上传 PyTorch 权重（.pt），ONNX 可在下载时自动转换",
  ],
  [
    /\.(engine|trt)$/i,
    "这是 TensorRT 引擎文件，请上传训练得到的 PyTorch 权重（.pt）",
  ],
  [
    /\.(pth|ckpt|bin|safetensors)$/i,
    "请上传 Ultralytics 训练得到的 .pt 文件（如 runs/detect/train/weights/best.pt）",
  ],
]

/** 校验选择的模型文件，合法时返回 null，否则返回中文提示。 */
export function validateModelFile(file: { name: string; size: number }) {
  const name = file.name.trim()
  for (const [pattern, message] of otherFormats) {
    if (pattern.test(name)) {
      return message
    }
  }
  if (!name.toLowerCase().endsWith(".pt")) {
    return "请选择 .pt 格式的模型文件（PyTorch 权重，如 best.pt）"
  }
  if (CONTROL_CHARS.test(name)) {
    return "文件名包含无效字符，请重命名后再上传"
  }
  if (characterCount(name) > MODEL_FILE_NAME_MAX_LENGTH) {
    return `文件名不能超过 ${MODEL_FILE_NAME_MAX_LENGTH} 个字符，请重命名后再上传`
  }
  if (file.size === 0) {
    return "模型文件是空文件"
  }
  if (file.size > MODEL_MAX_BYTES) {
    return `模型文件不能超过 ${formatBytes(MODEL_MAX_BYTES, 0)}`
  }
  return null
}

/** 校验说明（会先去掉首尾空白），合法时返回 null。 */
export function validateModelDescription(description: string) {
  const value = description.trim()
  if (DESCRIPTION_INVALID.test(value)) {
    return "说明包含无效字符"
  }
  if (characterCount(value) > MODEL_DESCRIPTION_MAX_LENGTH) {
    return `说明不能超过 ${MODEL_DESCRIPTION_MAX_LENGTH} 个字符`
  }
  return null
}
