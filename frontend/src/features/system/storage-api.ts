/**
 * 存储管理模块的 API 类型、查询与写操作（对应后端 `internal/storagenode` 与
 * `internal/httpapi/storage.go` 的 `/api/v1/storage` 接口）。
 */
import { queryOptions, useQuery } from "@tanstack/react-query"

import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type StorageNodeKind = "local" | "s3" | "smb"

export type StorageNodeStatus = "unknown" | "ok" | "error"

/** 嵌入在 Dataset、DataVersion 与迁移记录中的精简存储节点引用。 */
export type StorageNodeRef = {
  id: number
  name: string
  kind: StorageNodeKind
  is_builtin: boolean
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
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
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
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  secret_access_key?: string
  password?: string
  activate: boolean
}

export type UpdateStorageNodeInput = {
  name?: string
  local?: LocalStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
  secret_access_key?: string
  password?: string
  clear_secret?: boolean
}

export type TestStorageNodeInput = {
  node_id?: number
  kind: StorageNodeKind
  local?: LocalStorageConfig
  s3?: S3StorageConfig
  smb?: SMBStorageConfig
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
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "canceled"

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
  s3: "S3 兼容对象存储（MinIO / RustFS / 云 S3）",
  smb: "SMB 共享文件夹（Windows / NAS）",
  local: "本机挂载目录",
}

export const STORAGE_KIND_SHORT_LABELS: Record<StorageNodeKind, string> = {
  s3: "S3 对象存储",
  smb: "SMB 共享",
  local: "本机目录",
}

export const MIGRATION_STATUS_LABELS: Record<MigrationStatus, string> = {
  queued: "排队中",
  running: "迁移中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
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
