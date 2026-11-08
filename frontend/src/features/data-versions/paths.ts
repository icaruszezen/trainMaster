/** 数据版本页面的地址（与 config/navigation、router 中的一致）。 */

/** 数据版本列表。 */
export const DATA_VERSIONS_PATH = "/datasets/versions"

/** 列表（可按项目筛选）。 */
export function dataVersionsPath(projectId: number | null = null) {
  return projectId === null
    ? DATA_VERSIONS_PATH
    : `${DATA_VERSIONS_PATH}?project=${projectId}`
}

export function dataVersionDetailPath(id: number) {
  return `${DATA_VERSIONS_PATH}/${id}`
}

/** 生成数据版本：预先选中项目；`from` 为模板（以此为模板新建）。 */
export function newDataVersionPath({
  projectId = null,
  from = null,
}: { projectId?: number | null; from?: number | null } = {}) {
  const search = new URLSearchParams()
  if (projectId !== null) {
    search.set("project", String(projectId))
  }
  if (from !== null) {
    search.set("from", String(from))
  }
  const query = search.toString()
  return `${DATA_VERSIONS_PATH}/new${query ? `?${query}` : ""}`
}

/** 项目详情的“数据版本”标签页。 */
export function projectDataVersionsPath(projectId: number) {
  return `/projects/${projectId}?tab=data-versions`
}

/** 项目详情的“数据集”标签页（项目中还没有数据集时引导去添加）。 */
export function projectDatasetsPath(projectId: number) {
  return `/projects/${projectId}?tab=datasets`
}

/** 项目的类别设置所在的页面。 */
export function projectClassesPath(projectId: number) {
  return `/projects/${projectId}?tab=classes`
}

/** 地址中的编号：必须是正整数（与后端一致），否则为 null。 */
export function parsePositiveId(value: string | null | undefined) {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}
