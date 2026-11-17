import { QueryClientProvider } from "@tanstack/react-query"
import { renderToStaticMarkup } from "react-dom/server"
import { MemoryRouter } from "react-router"
import { afterEach, describe, expect, it, vi } from "vitest"

import { StorageNodeBadge } from "@/components/storage-node-badge"
import { TooltipProvider } from "@/components/ui/tooltip"
import { systemInfoQuery, type SystemInfo } from "@/features/system/api"
import {
  activateStorageNode,
  BUILTIN_STORAGE_NODE_REF,
  createStorageMigrations,
  createStorageNode,
  migratableItemsQuery,
  storageMigrationsQuery,
  storageNodesQuery,
  testStorageNode,
  trainerCachePolicyQuery,
  updateTrainerCachePolicy,
  type MigratableItem,
  type StorageMigration,
  type StorageNode,
} from "@/features/system/storage-api"
import { StoragePage } from "@/features/system/storage-page"
import type { ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  queryClient.clear()
})

function mockFetch(response: () => Response) {
  const fetchMock = vi.fn(async () => response())
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function requestOf(fetchMock: ReturnType<typeof mockFetch>) {
  const [path, init] = fetchMock.mock.calls[0] as unknown as [
    string,
    RequestInit,
  ]
  return {
    path,
    method: init.method ?? "GET",
    body: init.body === undefined ? undefined : JSON.parse(String(init.body)),
  }
}

describe("storage API client", () => {
  it("tests, creates, and activates storage nodes via /api/v1/storage", async () => {
    const fetchMock = mockFetch(() =>
      Response.json({
        ok: true,
        latency_ms: 4.2,
        display_target: "s3://vision-datasets/trainmaster",
        message: "连接与读写正常",
      })
    )
    const probe = await testStorageNode({
      kind: "s3",
      s3: {
        endpoint: "http://192.168.10.15:9000",
        region: "us-east-1",
        bucket: "vision-datasets",
        prefix: "trainmaster",
        access_key_id: "minioadmin",
        force_path_style: true,
        insecure_skip_verify: false,
      },
      secret_access_key: "secret123",
    })
    expect(probe.ok).toBe(true)
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/storage/nodes/test",
      method: "POST",
    })

    fetchMock.mockClear()
    await createStorageNode({
      name: "机房 MinIO",
      kind: "s3",
      s3: {
        endpoint: "http://192.168.10.15:9000",
        region: "us-east-1",
        bucket: "vision-datasets",
        prefix: "trainmaster",
        access_key_id: "minioadmin",
        force_path_style: true,
        insecure_skip_verify: false,
      },
      secret_access_key: "secret123",
      activate: true,
    })
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/storage/nodes",
      method: "POST",
      body: { name: "机房 MinIO", kind: "s3", activate: true },
    })

    fetchMock.mockClear()
    await activateStorageNode(2)
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/storage/nodes/2/activate",
      method: "POST",
      body: undefined,
    })
  })

  it("updates trainer cache policy and starts online migrations", async () => {
    const fetchMock = mockFetch(() =>
      Response.json({ enabled: true, max_versions: 3, ttl_hours: 48 })
    )
    await updateTrainerCachePolicy({
      enabled: true,
      max_versions: 3,
      ttl_hours: 48,
    })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/storage/cache-policy",
      method: "PUT",
      body: { enabled: true, max_versions: 3, ttl_hours: 48 },
    })

    fetchMock.mockClear()
    vi.stubGlobal(
      "fetch",
      fetchMock.mockImplementation(async () =>
        Response.json({ items: [], total: 0 })
      )
    )
    await createStorageMigrations({
      targets: [{ target_type: "dataset", target_id: 7 }],
      delete_source: true,
    })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/storage/migrations",
      method: "POST",
      body: {
        targets: [{ target_type: "dataset", target_id: 7 }],
        delete_source: true,
      },
    })
  })
})

describe("StoragePage & StorageNodeBadge rendering", () => {
  it("renders storage nodes, migratable items, migration progress, and cache policy", () => {
    const nodes: StorageNode[] = [
      {
        id: 1,
        name: "本机默认存储",
        kind: "local",
        is_builtin: true,
        is_active: false,
        has_secret: false,
        display_target: "/data",
        config: { local: { path: "/data" } },
        status: "ok",
        usage: {
          total_bytes: 500_000_000_000,
          free_bytes: 320_000_000_000,
          used_bytes: 180_000_000_000,
        },
        stats: {
          dataset_count: 1,
          trashed_dataset_count: 0,
          data_version_count: 1,
          trashed_data_version_count: 0,
          stored_bytes: 256_000_000,
        },
        created_at: "2026-11-01T00:00:00Z",
        updated_at: "2026-11-01T00:00:00Z",
      },
      {
        id: 2,
        name: "机房存储服务器 MinIO",
        kind: "s3",
        is_builtin: false,
        is_active: true,
        has_secret: true,
        display_target: "s3://vision-datasets/trainmaster",
        config: {
          s3: {
            endpoint: "http://192.168.10.15:9000",
            region: "us-east-1",
            bucket: "vision-datasets",
            prefix: "trainmaster",
            access_key_id: "minioadmin",
            force_path_style: true,
            insecure_skip_verify: false,
          },
        },
        status: "ok",
        stats: {
          dataset_count: 3,
          trashed_dataset_count: 0,
          data_version_count: 2,
          trashed_data_version_count: 0,
          stored_bytes: 4_800_000_000,
        },
        created_at: "2026-11-10T00:00:00Z",
        updated_at: "2026-11-10T00:00:00Z",
      },
    ]

    const migratable: MigratableItem[] = [
      {
        target_type: "dataset",
        target_id: 5,
        name: "产线A-历史样本",
        dir_name: "line_a_legacy",
        image_count: 240,
        total_bytes: 128_000_000,
        storage_node: BUILTIN_STORAGE_NODE_REF,
        created_at: "2026-11-02T00:00:00Z",
      },
    ]

    const migrations: StorageMigration[] = [
      {
        id: 9,
        target_type: "data_version",
        target_id: 3,
        target_name: "外观检测 · 数据版本 v2",
        from_node: BUILTIN_STORAGE_NODE_REF,
        to_node: {
          id: 2,
          name: "机房存储服务器 MinIO",
          kind: "s3",
          is_builtin: false,
        },
        delete_source: true,
        status: "succeeded",
        files_done: 480,
        files_total: 480,
        bytes_done: 256_000_000,
        bytes_total: 256_000_000,
        created_at: "2026-11-15T08:00:00Z",
        updated_at: "2026-11-15T08:02:00Z",
      },
    ]

    const sysInfo: SystemInfo = {
      name: "trainMaster",
      version: "dev",
      commit: "abc1234",
      build_time: "2026-11-17T00:00:00Z",
      go_version: "go1.25",
      started_at: "2026-11-17T00:00:00Z",
      uptime_seconds: 3600,
      database: { status: "ok", server_version: "15.8", schema_version: 17 },
      storage: {
        root: "/data",
        total_bytes: 500_000_000_000,
        free_bytes: 320_000_000_000,
        used_bytes: 180_000_000_000,
        directories: [
          { key: "datasets", path: "/data/datasets", writable: true },
          { key: "models", path: "/data/models", writable: true },
        ],
      },
    }

    queryClient.setQueryData<ListResponse<StorageNode>>(
      storageNodesQuery.queryKey,
      { items: nodes, total: nodes.length }
    )
    queryClient.setQueryData<ListResponse<MigratableItem>>(
      migratableItemsQuery.queryKey,
      { items: migratable, total: migratable.length }
    )
    queryClient.setQueryData<ListResponse<StorageMigration>>(
      storageMigrationsQuery.queryKey,
      { items: migrations, total: migrations.length }
    )
    queryClient.setQueryData(trainerCachePolicyQuery.queryKey, {
      enabled: false,
      max_versions: 2,
      ttl_hours: 24,
    })
    queryClient.setQueryData(systemInfoQuery.queryKey, sysInfo)

    const html = renderToStaticMarkup(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <TooltipProvider>
            <StoragePage />
          </TooltipProvider>
        </QueryClientProvider>
      </MemoryRouter>
    )

    for (const expected of [
      "存储管理",
      "本机默认存储",
      "机房存储服务器 MinIO",
      "当前使用中",
      "s3://vision-datasets/trainmaster",
      "产线A-历史样本",
      "一键全部迁移（1）",
      "外观检测 · 数据版本 v2",
      "训练机短期数据集缓存策略",
      "训练结束立即删除本地数据集",
      "平台本机系统目录",
    ]) {
      expect(html).toContain(expected)
    }

    const badgeHtml = renderToStaticMarkup(
      <StorageNodeBadge
        node={{
          id: 2,
          name: "机房存储服务器 MinIO",
          kind: "s3",
          is_builtin: false,
        }}
      />
    )
    expect(badgeHtml).toContain("机房存储服务器 MinIO")
    expect(
      renderToStaticMarkup(
        <StorageNodeBadge node={BUILTIN_STORAGE_NODE_REF} hideBuiltin />
      )
    ).toBe("")
  })
})
