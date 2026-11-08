/**
 * 离线增强的方法（与后端 internal/augment 一致：方法的 id、显示顺序与
 * MaxCopies 的规则）。只增强训练集图片，验证集与测试集保持真实图片。
 */
import type { AugmentMethod } from "@/features/data-versions/api"

export type AugmentMethodInfo = {
  id: AugmentMethod
  label: string
  /** 一句话说明，包括可能的风险。 */
  description: string
  /** 改变几何形状（框随之移动，坐标仍然准确）。 */
  geometric: boolean
}

/** 全部方法，按显示顺序（也是请求中 methods 的顺序）。 */
export const AUGMENT_METHODS: readonly AugmentMethodInfo[] = [
  {
    id: "hflip",
    label: "水平翻转",
    description:
      "左右镜像，框的位置精确跟随。有方向的零件（如装反的部件、文字、箭头）翻转后可能看起来像缺陷，这类数据不要选。",
    geometric: true,
  },
  {
    id: "vflip",
    label: "垂直翻转",
    description:
      "上下镜像，框的位置精确跟随。拍摄方向固定的产线图片很少出现上下颠倒，可能让模型学到不存在的情况。",
    geometric: true,
  },
  {
    id: "rotate90",
    label: "旋转 90°",
    description:
      "旋转 90°、180° 或 270°，框的位置精确跟随（90° / 270° 时宽高互换）。与翻转一样，只适合方向无关的目标。",
    geometric: true,
  },
  {
    id: "brightness_contrast",
    label: "亮度 / 对比度",
    description:
      "亮度与对比度各在 ±20% 内随机变化，模拟光照差异。很浅的划痕、污渍在变暗或降低对比度后可能更难看清。",
    geometric: false,
  },
  {
    id: "blur",
    label: "轻微模糊",
    description:
      "轻微的高斯模糊（σ 0.3–1.0），模拟对焦不准。图片中最小的框边长不到 8 像素时不模糊这张图片，避免抹掉细小缺陷。",
    geometric: false,
  },
  {
    id: "noise",
    label: "高斯噪声",
    description:
      "叠加轻微的高斯噪声（σ 2–6），模拟传感器噪点。很小的点状缺陷可能与噪点混在一起。",
    geometric: false,
  },
]

const METHOD_INFO = new Map(AUGMENT_METHODS.map((info) => [info.id, info]))

/** 后端每张图片最多生成的增强图片数（与方法无关的上限）。 */
export const AUGMENT_COPIES_LIMIT = 5

export function augmentMethodLabel(method: AugmentMethod) {
  return METHOD_INFO.get(method)?.label ?? method
}

export function isGeometricMethod(method: AugmentMethod) {
  return METHOD_INFO.get(method)?.geometric ?? false
}

/** 去重并按显示顺序排列（请求中的顺序）。 */
export function normalizeMethods(
  methods: Iterable<AugmentMethod>
): AugmentMethod[] {
  const selected = new Set(methods)
  return AUGMENT_METHODS.map((info) => info.id).filter((id) => selected.has(id))
}

/**
 * 所选方法最多能生成多少份互不相同的副本（与后端 augment.MaxCopies 一致）：
 * 只有几何方法时是它们组合出的非原样方向数（水平或垂直翻转 1，两种翻转 3，
 * 旋转 90° 3，旋转 90° 加任一翻转 7）；有任何一种像素方法时为 5。没有选择
 * 方法时为 0。
 */
export function maxCopies(methods: readonly AugmentMethod[]) {
  if (methods.length === 0) {
    return 0
  }
  if (methods.some((method) => !isGeometricMethod(method))) {
    return AUGMENT_COPIES_LIMIT
  }
  const rotate = methods.includes("rotate90")
  const flips =
    Number(methods.includes("hflip")) + Number(methods.includes("vflip"))
  if (rotate) {
    return flips > 0 ? 7 : 3
  }
  return flips === 2 ? 3 : 1
}

/** 表单中“每张生成几份”的上限：maxCopies 与 5 中较小的一个。 */
export function copiesLimit(methods: readonly AugmentMethod[]) {
  return Math.min(maxCopies(methods), AUGMENT_COPIES_LIMIT)
}

/** 增强方法的名称，如“水平翻转、亮度 / 对比度”。 */
export function augmentMethodsText(methods: readonly AugmentMethod[]) {
  return methods.map(augmentMethodLabel).join("、")
}
