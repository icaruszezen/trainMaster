/**
 * 存储管理模块的 API 类型、查询与写操作（对应后端 `internal/storagenode` 与
 * `internal/httpapi/storage.go` 的 `/api/v1/storage` 接口）。
 */
import { queryOptions, useQuery } from "@tanstack/react-query"

import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type StorageNodeKind = "local" | "docker" | "s3" | "smb"

export type StorageNodeStatus = "unknown" | "ok" | "error"

/** 嵌入在 Dataset、DataVersion 与迁移记录中的精简存储节点引用。 */
export type StorageNodeRef = {
  id: number
  name: string
  kind: StorageNodeKind
  is_builtin: boolean
  supports_archive?: boolean
}

export const BUILTIN_STORAGE_NODE_REF: StorageNodeRef = {
  id: 1,
  name: "本机默认存储",
  kind: "local",
  is_builtin: true,
}

export type LocalStorageConfig = {
  path: string
}

export type DockerStorageConfig = {
  endpoint: string
  insecure_skip_verify: boolean
}

export type S3StorageConfig = {
  endpoint: string
  region: string
  bucket: string
  prefix: string
  access_key_id: string
  force_path_style: boolean
  insecure_skip_verify: boolean
}

export type SMBStorageConfig = {
  host: string
  port: number
  share: string
  base_path: string
  username: string
  domain: string
}

export type StorageNodeConfig = {
  local?: LocalStorageConfig
  docker?: DockerStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  /** 兼容顶层扁平化配置字段返回 */
  endpoint?: string
  insecure_skip_verify?: boolean
}

/** 兼容嵌套 `config.docker` 与顶层扁平 `config.endpoint` 两种服务端响应格式。 */
export function getDockerConfig(
  config?: StorageNodeConfig | null
): DockerStorageConfig | undefined {
  if (!config) {
    return undefined
  }
  if (config.docker) {
    return config.docker
  }
  if (typeof config.endpoint === "string" && config.endpoint !== "") {
    return {
      endpoint: config.endpoint,
      insecure_skip_verify: Boolean(config.insecure_skip_verify),
    }
  }
  return undefined
}

export type StorageUsageInfo = {
  total_bytes: number
  free_bytes: number
  used_bytes: number
}

export type StorageNodeStats = {
  dataset_count: number
  trashed_dataset_count: number
  data_version_count: number
  trashed_data_version_count: number
  stored_bytes: number
}

export type StorageNode = {
  id: number
  name: string
  kind: StorageNodeKind
  is_builtin: boolean
  is_active: boolean
  has_secret: boolean
  supports_archive?: boolean
  display_target: string
  config: StorageNodeConfig
  status: StorageNodeStatus
  status_error?: string
  last_checked_at?: string
  usage?: StorageUsageInfo
  stats: StorageNodeStats
  created_at: string
  updated_at: string
}

export type CreateStorageNodeInput = {
  name: string
  kind: StorageNodeKind
  local?: LocalStorageConfig
  docker?: DockerStorageConfig
  config?: DockerStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  token?: string
  secret_access_key?: string
  password?: string
  activate: boolean
}

export type UpdateStorageNodeInput = {
  name?: string
  local?: LocalStorageConfig
  docker?: DockerStorageConfig
  config?: DockerStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  token?: string
  secret_access_key?: string
  password?: string
  clear_secret?: boolean
}

export type TestStorageNodeInput = {
  node_id?: number
  kind: StorageNodeKind
  local?: LocalStorageConfig
  docker?: DockerStorageConfig
  config?: DockerStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  token?: string
  secret_access_key?: string
  password?: string
}

export type TestStorageNodeResult = {
  ok: boolean
  latency_ms: number
  display_target: string
  usage?: StorageUsageInfo
  message: string
}

export type TrainerCachePolicy = {
  enabled: boolean
  max_versions: number
  ttl_hours: number
}

export type MigrationTargetType = "dataset" | "data_version"

export type MigrationStatus =
  "queued" | "running" | "succeeded" | "failed" | "canceled"

export type MigrationTransferMode = "archive" | "direct"

export type MigrationStage =
  "" | "packing" | "transferring" | "extracting" | "verifying" | "cleaning"

export type MigratableItem = {
  target_type: MigrationTargetType
  target_id: number
  name: string
  project_name?: string
  dir_name: string
  image_count: number
  total_bytes: number
  storage_node: StorageNodeRef
  created_at: string
}

export type MigrationTarget = {
  target_type: MigrationTargetType
  target_id: number
}

export type CreateMigrationsInput = {
  targets: MigrationTarget[]
  delete_source?: boolean
}

export type StorageMigration = {
  id: number
  target_type: MigrationTargetType
  target_id: number
  target_name: string
  from_node: StorageNodeRef
  to_node: StorageNodeRef
  delete_source: boolean
  transfer_mode?: MigrationTransferMode
  stage?: MigrationStage | (string & {})
  archive_bytes?: number
  status: MigrationStatus
  files_done: number
  files_total: number
  bytes_done: number
  bytes_total: number
  error?: string
  created_at: string
  started_at?: string
  finished_at?: string
  updated_at: string
}

export const STORAGE_KIND_LABELS: Record<StorageNodeKind, string> = {
  docker: "存储服务容器 (Docker)",
  s3: "S3 兼容对象存储（MinIO / RustFS / 云 S3）",
  smb: "SMB 共享文件夹（Windows / NAS）",
  local: "本机挂载目录",
}

export const STORAGE_KIND_SHORT_LABELS: Record<StorageNodeKind, string> = {
  docker: "存储容器",
  s3: "S3 对象存储",
  smb: "SMB 共享",
  local: "本机目录",
}

export function storageKindLabel(kind: string): string {
  return STORAGE_KIND_LABELS[kind as StorageNodeKind] ?? kind
}

export function storageKindShortLabel(kind: string): string {
  return STORAGE_KIND_SHORT_LABELS[kind as StorageNodeKind] ?? kind
}

export const MIGRATION_STATUS_LABELS: Record<MigrationStatus, string> = {
  queued: "排队中",
  running: "迁移中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
}

export const MIGRATION_STAGE_LABELS: Record<
  Exclude<MigrationStage, "">,
  string
> = {
  packing: "正在打包压缩…",
  transferring: "正在传输压缩包…",
  extracting: "正在解压落盘…",
  verifying: "正在校验数据…",
  cleaning: "正在清理临时包…",
}

export function migrationStageLabel(stage?: string | null): string | null {
  if (!stage) {
    return null
  }
  return MIGRATION_STAGE_LABELS[stage as Exclude<MigrationStage, "">] ?? stage
}

/** 判断存储节点是否具备存储容器打包/解压加速能力。 */
export function nodeSupportsArchive(
  node?: Pick<StorageNodeRef, "kind" | "supports_archive"> | null
): boolean {
  return Boolean(
    node && (node.supports_archive === true || node.kind === "docker")
  )
}

/** 根据源节点与目标节点的能力决定迁移传输模式（与后端 CanArchiveBetween 严格一致）。 */
export function resolveMigrationTransferMode(
  fromNode?:
    | (Pick<StorageNodeRef, "kind" | "supports_archive"> & {
        id?: number
        is_builtin?: boolean
      })
    | null,
  toNode?:
    | (Pick<StorageNodeRef, "kind" | "supports_archive"> & {
        id?: number
        is_builtin?: boolean
      })
    | null
): MigrationTransferMode {
  if (!fromNode || !toNode) {
    return "direct"
  }
  if (
    typeof fromNode.id === "number" &&
    typeof toNode.id === "number" &&
    fromNode.id === toNode.id
  ) {
    return "direct"
  }
  const aDocker = fromNode.kind === "docker"
  const bDocker = toNode.kind === "docker"
  const aBuiltin = Boolean(fromNode.is_builtin)
  const bBuiltin = Boolean(toNode.is_builtin)
  if ((aDocker && bDocker) || (aDocker && bBuiltin) || (aBuiltin && bDocker)) {
    return "archive"
  }
  if (
    nodeSupportsArchive(fromNode) &&
    nodeSupportsArchive(toNode) &&
    (aDocker || bDocker)
  ) {
    return "archive"
  }
  return "direct"
}

export type TrainingDatasetCopyMode = "shared_local" | "archive" | "direct"

export const TRAINING_DATASET_COPY_LABELS: Record<
  TrainingDatasetCopyMode,
  string
> = {
  shared_local:
    "本机共享零拷贝（远程训练机拉取时走兼容存储流式拉取，不提供打包）",
  archive: "存储容器打包拉取并解压（先打压缩包再传输，训练机接收后校验解压）",
  direct: "兼容存储流式拉取（按现有方式接入，不提供打包操作）",
}

export const BUILTIN_ARCHIVE_TRAINING_COPY_LABEL =
  "本机共享零拷贝（远程训练机拉取时走存储容器打包解压）"

export function resolveTrainingDatasetCopyMode(
  node?: Pick<StorageNodeRef, "kind" | "is_builtin" | "supports_archive"> | null
): TrainingDatasetCopyMode {
  if (!node || node.is_builtin) {
    return "shared_local"
  }
  return nodeSupportsArchive(node) ? "archive" : "direct"
}

export function trainingDatasetCopyLabel(
  node?: Pick<StorageNodeRef, "kind" | "is_builtin" | "supports_archive"> | null
): string {
  const mode = resolveTrainingDatasetCopyMode(node)
  if (mode === "shared_local" && node && nodeSupportsArchive(node)) {
    return BUILTIN_ARCHIVE_TRAINING_COPY_LABEL
  }
  return TRAINING_DATASET_COPY_LABELS[mode]
}

export function isActiveMigration(
  migration: Pick<StorageMigration, "status">
): boolean {
  return migration.status === "queued" || migration.status === "running"
}

export function migrationKey(item: MigrationTarget): string {
  return `${item.target_type}:${item.target_id}`
}

const BASE = "/api/v1/storage"

export const storageKeys = {
  all: ["storage"] as const,
  nodes: () => [...storageKeys.all, "nodes"] as const,
  cachePolicy: () => [...storageKeys.all, "cache-policy"] as const,
  migratable: () => [...storageKeys.all, "migratable"] as const,
  migrations: () => [...storageKeys.all, "migrations"] as const,
}

export const storageNodesQuery = queryOptions({
  queryKey: storageKeys.nodes(),
  queryFn: ({ signal }) =>
    apiGet<ListResponse<StorageNode>>(`${BASE}/nodes`, { signal }),
  refetchInterval: 30_000,
})

export const trainerCachePolicyQuery = queryOptions({
  queryKey: storageKeys.cachePolicy(),
  queryFn: ({ signal }) =>
    apiGet<TrainerCachePolicy>(`${BASE}/cache-policy`, { signal }),
})

export const migratableItemsQuery = queryOptions({
  queryKey: storageKeys.migratable(),
  queryFn: ({ signal }) =>
    apiGet<ListResponse<MigratableItem>>(`${BASE}/migratable`, { signal }),
})

export const storageMigrationsQuery = queryOptions({
  queryKey: storageKeys.migrations(),
  queryFn: ({ signal }) =>
    apiGet<ListResponse<StorageMigration>>(`${BASE}/migrations`, { signal }),
  refetchInterval: (query) =>
    query.state.data?.items.some(isActiveMigration) ? 1_500 : false,
})

export function useActiveStorageNode(): StorageNode | null {
  const { data } = useQuery(storageNodesQuery)
  return data?.items.find((node) => node.is_active) ?? null
}

export function createStorageNode(input: CreateStorageNodeInput) {
  return apiSend<StorageNode>("POST", `${BASE}/nodes`, input)
}

export function updateStorageNode(id: number, input: UpdateStorageNodeInput) {
  return apiSend<StorageNode>("PATCH", `${BASE}/nodes/${id}`, input)
}

export function testStorageNode(input: TestStorageNodeInput) {
  return apiSend<TestStorageNodeResult>("POST", `${BASE}/nodes/test`, input)
}

export function activateStorageNode(id: number) {
  return apiSend<StorageNode>("POST", `${BASE}/nodes/${id}/activate`)
}

export function deleteStorageNode(id: number) {
  return apiSend<void>("DELETE", `${BASE}/nodes/${id}`)
}

export function updateTrainerCachePolicy(input: TrainerCachePolicy) {
  return apiSend<TrainerCachePolicy>("PUT", `${BASE}/cache-policy`, input)
}

export function createStorageMigrations(input: CreateMigrationsInput) {
  return apiSend<ListResponse<StorageMigration>>(
    "POST",
    `${BASE}/migrations`,
    input
  )
}

export function cancelStorageMigration(id: number) {
  return apiSend<StorageMigration>("POST", `${BASE}/migrations/${id}/cancel`)
}

/** 节点切换、增删或迁移状态变化后刷新相关缓存。 */
export function refreshStorageQueries() {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: storageKeys.all }),
    queryClient.invalidateQueries({ queryKey: ["datasets"] }),
    queryClient.invalidateQueries({ queryKey: ["data-versions"] }),
    queryClient.invalidateQueries({ queryKey: ["recycle-bin"] }),
  ])
}
