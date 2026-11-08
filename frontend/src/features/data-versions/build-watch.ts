/**
 * 数据版本生成的全局观察（与 datasets/import-watch.ts 相同的做法）：在模块级
 * 轮询告诉它的版本，直到生成完成或失败，然后刷新数据版本与项目的查询并弹出
 * 提示（带“查看”）。生成要排队、可能需要几分钟，切换页面也不会漏掉结果；
 * 同一个版本同时只轮询一次，每次生成只提示一次（重新生成后再提示）。
 */
import { QueryObserver } from "@tanstack/react-query"
import { toast } from "sonner"

import {
  dataVersionQuery,
  isActiveDataVersion,
  refreshDataVersionImages,
  refreshDataVersions,
  type DataVersion,
} from "@/features/data-versions/api"
import {
  dataVersionFullTitle,
  splitSummary,
  versionSplitCounts,
} from "@/features/data-versions/format"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import { isPermanentError } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** 提示中“查看”的跳转（页面传入路由的 navigate，否则整页打开）。 */
export type Navigate = (path: string) => void

const watchers = new Map<number, () => void>()
/** 已提示的生成：`<id>:<finished_at>`（重新生成的版本 id 不变）。 */
const settled = new Set<string>()

let navigateTo: Navigate | null = null

/** 页面挂载时登记路由的 navigate，提示中的“查看”不必整页刷新。 */
export function setBuildWatchNavigate(navigate: Navigate | null) {
  navigateTo = navigate
}

function open(path: string) {
  if (navigateTo) {
    navigateTo(path)
  } else {
    window.location.assign(path)
  }
}

/** 生成结束：刷新查询并提示（每次生成只提示一次）。 */
function settle(version: DataVersion) {
  const key = `${version.id}:${version.finished_at ?? ""}`
  if (settled.has(key)) {
    return
  }
  settled.add(key)
  void refreshDataVersions()
  void refreshDataVersionImages(version.id)

  const title = dataVersionFullTitle(version)
  const action = {
    label: "查看",
    onClick: () => open(dataVersionDetailPath(version.id)),
  }
  if (version.status === "ready") {
    toast.success(`${title} 已生成`, {
      description: splitSummary(versionSplitCounts(version)),
      duration: 10_000,
      action,
    })
  } else if (version.status === "failed") {
    toast.error(`${title} 生成失败`, {
      description: version.error || undefined,
      duration: 10_000,
      action,
    })
  }
}

/**
 * 开始观察一个排队中或正在生成的版本（已经结束的不提示）。`version` 是已知的
 * 最新状态，没有缓存时先写入详情的查询缓存。
 */
export function watchDataVersionBuild(version: DataVersion) {
  if (!isActiveDataVersion(version) || watchers.has(version.id)) {
    return
  }
  const options = dataVersionQuery(version.id)
  if (!queryClient.getQueryData(options.queryKey)) {
    queryClient.setQueryData(options.queryKey, version)
  }

  const observer = new QueryObserver(queryClient, options)
  const unsubscribe = observer.subscribe((result) => {
    const latest = result.data
    if (latest && result.isSuccess && !result.isFetching) {
      if (!isActiveDataVersion(latest)) {
        stop()
        settle(latest)
      }
    } else if (result.isError && isPermanentError(result.error)) {
      // 版本已被删除（停止并删除）：不再轮询，也不提示。
      stop()
    }
  })
  const stop = () => {
    unsubscribe()
    watchers.delete(version.id)
  }
  watchers.set(version.id, stop)
}

/** 正在观察的版本数（测试用）。 */
export function watchedBuildCount() {
  return watchers.size
}

/** 停止全部观察并忘记已提示的版本（测试用）。 */
export function resetBuildWatch() {
  for (const stop of [...watchers.values()]) {
    stop()
  }
  settled.clear()
  navigateTo = null
}
