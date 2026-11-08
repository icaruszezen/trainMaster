/**
 * 数据版本的显示文字（纯函数，见 format.test.ts）。
 *
 * 模型也有 v1、v2…，所以界面上总是写“数据版本 v3”，不单独写“v3”。
 */
import type { DataVersion, DataVersionStatus, Split } from "./api"

/** “数据版本 v3”。 */
export function dataVersionTitle(v: Pick<DataVersion, "version">) {
  return `数据版本 v${v.version}`
}

/** “外观检测 · 数据版本 v3”。 */
export function dataVersionFullTitle(
  v: Pick<DataVersion, "project_name" | "version">
) {
  return `${v.project_name} · ${dataVersionTitle(v)}`
}

export const DATA_VERSION_STATUS_LABELS: Record<DataVersionStatus, string> = {
  queued: "排队中",
  building: "生成中",
  ready: "已生成",
  failed: "生成失败",
}

export const SPLIT_LABELS: Record<Split, string> = {
  train: "训练",
  val: "验证",
  test: "测试",
}
