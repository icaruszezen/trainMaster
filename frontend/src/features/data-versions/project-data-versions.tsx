// OWNER: frontend-pages agent. 占位：项目详情页“数据版本”标签页的内容。
export function ProjectDataVersions({
  projectId,
  projectName,
}: {
  projectId: number
  projectName: string
}) {
  return <div data-project-id={projectId}>{projectName} 的数据版本</div>
}
