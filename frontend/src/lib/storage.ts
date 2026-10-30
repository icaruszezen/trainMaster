/**
 * localStorage 的安全读写：浏览器禁用站点数据（企业策略、隐私模式、部分内嵌
 * WebView）时访问会抛出异常，此时只是不再记住偏好，页面照常工作。
 */
export function readPreference(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writePreference(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // 忽略：见文件注释
  }
}
