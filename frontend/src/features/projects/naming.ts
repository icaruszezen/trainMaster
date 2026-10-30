/**
 * 项目名称规则，与后端 internal/project 的校验保持一致；服务端的校验仍是最终结果。
 * 项目不对应磁盘上的文件夹，所以只限制长度与控制字符（不像数据集名称那样
 * 需要兼顾文件名规则）。描述的长度限制与数据集相同，见 datasets/naming.ts；
 * 描述中的其它控制字符（换行、制表符除外）由服务端拒绝。
 */
import { characterCount } from "@/features/datasets/naming"

export const PROJECT_NAME_MAX_LENGTH = 64

// eslint-disable-next-line no-control-regex -- 控制字符正是要拒绝的内容
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/

/** 校验项目名称，合法时返回 null，否则返回中文错误提示（会先去掉首尾空白）。 */
export function validateProjectName(raw: string): string | null {
  const name = raw.trim()
  if (name === "") {
    return "名称不能为空"
  }
  if (characterCount(name) > PROJECT_NAME_MAX_LENGTH) {
    return `名称不能超过 ${PROJECT_NAME_MAX_LENGTH} 个字符`
  }
  if (CONTROL_CHARS.test(name)) {
    return "名称不能包含换行、制表符等控制字符"
  }
  return null
}
