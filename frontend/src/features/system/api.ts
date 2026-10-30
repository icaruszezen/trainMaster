import { queryOptions, useQuery } from "@tanstack/react-query"

import { apiGet } from "@/lib/api"

/** `GET /api/v1/system/info` 的响应，字段与后端保持一致。 */
export type SystemInfo = {
  name: string
  version: string
  commit: string
  build_time: string
  go_version: string
  started_at: string
  uptime_seconds: number
  database: {
    status: "ok" | "unavailable"
    server_version?: string
    schema_version?: number
    error?: string
  }
  storage: {
    root: string
    total_bytes: number
    free_bytes: number
    used_bytes: number
    directories: StorageDirectory[]
  }
}

export type StorageDirectory = {
  key: string
  path: string
  writable: boolean
}

export const systemInfoQuery = queryOptions({
  queryKey: ["system", "info"],
  queryFn: ({ signal }) =>
    apiGet<SystemInfo>("/api/v1/system/info", { signal }),
  refetchInterval: 30_000,
})

export function useSystemInfo() {
  return useQuery(systemInfoQuery)
}
