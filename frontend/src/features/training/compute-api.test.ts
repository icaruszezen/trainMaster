import { afterEach, describe, expect, it, vi } from "vitest"

import {
  cacheNode,
  computeKeys,
  computeNodesQuery,
  computeServerQuery,
  deleteNode,
  forgetNode,
  NODES_REFETCH_MS,
  probeNode,
  SERVER_REFETCH_MS,
  type ComputeNode,
} from "@/features/training/compute-api"
import type { ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
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
  return { path, method: init.method, body: init.body }
}

async function runQuery(options: { queryFn?: unknown }) {
  return (options.queryFn as (context: unknown) => Promise<unknown>)({
    signal: new AbortController().signal,
  })
}

// 缓存操作只关心 id 与名称，其余字段不影响结果。
function node(id: number, name = `node-${id}`) {
  return { id, name } as ComputeNode
}

describe("compute queries", () => {
  it("polls the node list every 5 seconds", async () => {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    await runQuery(computeNodesQuery)
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/compute/nodes",
      method: "GET",
    })
    expect(computeNodesQuery.refetchInterval).toBe(NODES_REFETCH_MS)
    expect(NODES_REFETCH_MS).toBe(5_000)
  })

  it("polls the platform server every 10 seconds", async () => {
    const fetchMock = mockFetch(() => Response.json({ hostname: "app" }))
    await expect(runQuery(computeServerQuery)).resolves.toEqual({
      hostname: "app",
    })
    expect(requestOf(fetchMock).path).toBe("/api/v1/compute/server")
    expect(computeServerQuery.refetchInterval).toBe(SERVER_REFETCH_MS)
    expect(SERVER_REFETCH_MS).toBe(10_000)
  })
})

describe("compute writes", () => {
  it("requests a probe without a body", async () => {
    const fetchMock = mockFetch(() =>
      Response.json({ id: 3, probe_pending: true }, { status: 202 })
    )
    await expect(probeNode(3)).resolves.toEqual({ id: 3, probe_pending: true })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/compute/nodes/3/probe",
      method: "POST",
      body: undefined,
    })
  })

  it("deletes a node", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(deleteNode(3)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/compute/nodes/3",
      method: "DELETE",
    })
  })

  it("passes the server's conflict message on", async () => {
    mockFetch(() =>
      Response.json(
        { error: { code: "conflict", message: "节点离线，无法重新检测" } },
        { status: 409 }
      )
    )
    await expect(probeNode(3)).rejects.toMatchObject({
      status: 409,
      code: "conflict",
      message: "节点离线，无法重新检测",
    })
  })
})

describe("node cache", () => {
  function seed(items: ComputeNode[]) {
    queryClient.setQueryData<ListResponse<ComputeNode>>(computeKeys.nodes(), {
      items,
      total: items.length,
    })
  }

  function cached() {
    return queryClient.getQueryData<ListResponse<ComputeNode>>(
      computeKeys.nodes()
    )
  }

  it("replaces a node with the server's latest copy", () => {
    seed([node(1), node(2)])
    const updated = { ...node(2), probe_pending: true }
    cacheNode(updated)
    expect(cached()?.items).toEqual([node(1), updated])
    expect(queryClient.getQueryState(computeKeys.nodes())?.isInvalidated).toBe(
      true
    )
  })

  it("removes a deleted node and adjusts the total", () => {
    seed([node(1), node(2)])
    forgetNode(1)
    expect(cached()).toEqual({ items: [node(2)], total: 1 })
    forgetNode(1)
    expect(cached()).toEqual({ items: [node(2)], total: 1 })
  })

  it("leaves an empty cache alone", () => {
    cacheNode(node(1))
    forgetNode(1)
    expect(cached()).toBeUndefined()
  })
})
