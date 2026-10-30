/**
 * 导入 / 扫描任务的全局观察：在模块级轮询任务直到结束，结束时刷新数据集相关
 * 的查询并弹出结果提示。上传管理器、详情页、列表页的“重新扫描”都通过它
 * 观察任务，同一任务只轮询一次、只提示一次，切换页面也不会漏掉结果。
 */
import { QueryObserver } from "@tanstack/react-query"
import { toast } from "sonner"

import {
  datasetKeys,
  importQuery,
  isActiveImport,
  type DatasetImport,
} from "@/features/datasets/api"
import { summaryText } from "@/features/datasets/import-status"
import { isPermanentError } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type ImportWatchEvent = { job: DatasetImport } | { error: unknown }
type Listener = (event: ImportWatchEvent) => void

type Watcher = { listeners: Set<Listener>; stop: () => void }

const watchers = new Map<number, Watcher>()
const settledImports = new Set<number>()

/**
 * 新的导入 / 扫描任务开始后刷新列表、数据集详情（显示进行中的任务）与导入记录
 * （出现新的一行，并因此开始轮询）。图片、标签等等任务结束后由 settle 刷新。
 */
export function refreshStartedImport(datasetId: number) {
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.detail(datasetId),
    exact: true,
  })
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.imports(datasetId),
  })
}

/** 任务结束：刷新查询并提示结果（每个任务只提示一次，用户取消的不提示）。 */
function settle(job: DatasetImport, datasetName: string) {
  if (settledImports.has(job.id)) {
    return
  }
  settledImports.add(job.id)

  void queryClient.invalidateQueries({ queryKey: datasetKeys.all })

  const action = job.source === "scan" ? "扫描" : "导入"
  if (job.status === "succeeded") {
    toast.success(`“${datasetName}”${action}完成`, {
      description: summaryText(job),
    })
  } else if (job.status === "failed") {
    toast.error(`“${datasetName}”${action}失败`, {
      description: job.error || undefined,
      duration: 10_000,
    })
  }
}

/**
 * 开始（或继续）观察一个任务。`job` 是已知的最新状态，会先写入查询缓存，
 * 界面可以直接用 `useQuery(importQuery(...))` 读取（参见 useWatchedImport）。
 * 返回值用于取消 `onEvent` 的订阅；轮询本身持续到任务结束。
 */
export function watchImport(
  job: DatasetImport,
  datasetName: string,
  onEvent?: Listener
): () => void {
  if (!isActiveImport(job)) {
    onEvent?.({ job })
    settle(job, datasetName)
    return () => {}
  }

  let watcher = watchers.get(job.id)
  if (!watcher) {
    const options = importQuery(job.dataset_id, job.id)
    if (!queryClient.getQueryData(options.queryKey)) {
      queryClient.setQueryData(options.queryKey, job)
    }

    const listeners = new Set<Listener>()
    const observer = new QueryObserver(queryClient, options)
    const unsubscribe = observer.subscribe((result) => {
      const latest = result.data
      if (latest && result.isSuccess && !result.isFetching) {
        for (const listener of listeners) {
          listener({ job: latest })
        }
        if (!isActiveImport(latest)) {
          stop()
          settle(latest, datasetName)
        }
      } else if (result.isError && isPermanentError(result.error)) {
        // 任务或数据集已被删除：不再轮询。
        for (const listener of listeners) {
          listener({ error: result.error })
        }
        stop()
      }
    })
    const stop = () => {
      unsubscribe()
      watchers.delete(job.id)
    }
    watcher = { listeners, stop }
    watchers.set(job.id, watcher)
  }

  if (!onEvent) {
    return () => {}
  }
  const { listeners } = watcher
  listeners.add(onEvent)
  return () => {
    listeners.delete(onEvent)
  }
}
