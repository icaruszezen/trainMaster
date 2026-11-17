import { useEffect, useId, useMemo, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  ArrowRightLeft,
  CheckCircle2,
  Cpu,
  HardDrive,
  Pencil,
  PlugZap,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import {
  StorageKindIcon,
  StorageNodeBadge,
} from "@/components/storage-node-badge"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import { useSystemInfo } from "@/features/system/api"
import {
  activateStorageNode,
  cancelStorageMigration,
  createStorageMigrations,
  deleteStorageNode,
  isActiveMigration,
  migratableItemsQuery,
  MIGRATION_STATUS_LABELS,
  migrationKey,
  refreshStorageQueries,
  STORAGE_KIND_SHORT_LABELS,
  storageMigrationsQuery,
  storageNodesQuery,
  testStorageNode,
  trainerCachePolicyQuery,
  updateTrainerCachePolicy,
  type MigratableItem,
  type StorageMigration,
  type StorageNode,
  type StorageUsageInfo,
  type TrainerCachePolicy,
} from "@/features/system/storage-api"
import { StorageNodeDialog } from "@/features/system/storage-node-dialog"
import {
  StorageUsage,
  SystemInfoError,
} from "@/features/system/system-status-card"
import { usePageTitle } from "@/hooks/use-page-title"
import {
  formatBytes,
  formatNumber,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"
import { cn } from "@/lib/utils"

/** 平台本机目录用途说明（datasets 与 exports 随当前激活存储节点动态切换）。 */
const directoryPurposes: Record<string, string> = {
  datasets:
    "本机默认存储的数据集目录；切换到远程存储节点后，新建数据集保存在对应存储节点上",
  exports:
    "本机默认存储的数据版本目录；切换到远程存储节点后，新建数据版本保存在对应存储节点上",
  models: "模型仓库权重与转换缓存（始终保存在平台本机）",
  runs: "训练与评估过程指标、曲线与图表产物（始终保存在平台本机）",
  reviews: "模型审图任务的临时推理标注（始终保存在平台本机）",
  cache: "图片浏览缩略图缓存，可随时再生（始终保存在平台本机）",
  tmp: "上传分片与归档流式解压临时目录（始终保存在平台本机）",
}

export function StoragePage() {
  usePageTitle("存储管理")

  const nodesQuery = useQuery(storageNodesQuery)
  const migratableQuery = useQuery(migratableItemsQuery)
  const migrationsQuery = useQuery(storageMigrationsQuery)
  const cachePolicyQuery = useQuery(trainerCachePolicyQuery)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [dialogKey, setDialogKey] = useState(0)
  const [editingNode, setEditingNode] = useState<StorageNode | null>(null)
  const [deletingNode, setDeletingNode] = useState<StorageNode | null>(null)
  const [deletingBusy, setDeletingBusy] = useState(false)

  // 当后台迁移任务从进行中变为结束时，自动刷新存储节点统计与待迁移列表。
  const hasActive = useMemo(
    () => migrationsQuery.data?.items.some(isActiveMigration) ?? false,
    [migrationsQuery.data]
  )
  const prevActiveRef = useRef(false)
  useEffect(() => {
    if (prevActiveRef.current && !hasActive) {
      void refreshStorageQueries()
    }
    prevActiveRef.current = hasActive
  }, [hasActive])

  const openCreate = () => {
    setEditingNode(null)
    setDialogKey((k) => k + 1)
    setDialogOpen(true)
  }

  const openEdit = (node: StorageNode) => {
    setEditingNode(node)
    setDialogKey((k) => k + 1)
    setDialogOpen(true)
  }

  const refreshAll = () => {
    void nodesQuery.refetch()
    void migratableQuery.refetch()
    void migrationsQuery.refetch()
    void cachePolicyQuery.refetch()
  }

  const confirmDelete = async () => {
    if (!deletingNode || deletingBusy) return
    setDeletingBusy(true)
    try {
      await deleteStorageNode(deletingNode.id)
      await refreshStorageQueries()
      toast.success(`已删除存储节点“${deletingNode.name}”`)
      setDeletingNode(null)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "删除存储节点失败")
    } finally {
      setDeletingBusy(false)
    }
  }

  const activeNode =
    nodesQuery.data?.items.find((n) => n.is_active) ?? null

  const refreshing =
    nodesQuery.isFetching ||
    migratableQuery.isFetching ||
    migrationsQuery.isFetching

  return (
    <div className="grid gap-6">
      <PageHeader
        title="存储管理"
        description="将数据集与冻结的数据版本保存在独立存储服务器上（S3 对象存储 / SMB 共享文件夹 / 挂载目录），训练服务器平时不占用数据集磁盘空间，仅在训练或审图时由平台流式传输。"
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              onClick={refreshAll}
              disabled={refreshing}
            >
              <RefreshCw className={cn(refreshing && "animate-spin")} />
              刷新
            </Button>
            <Button size="sm" onClick={openCreate}>
              <Plus />
              新增存储节点
            </Button>
          </>
        }
      />

      <StorageNodesSection
        nodes={nodesQuery.data?.items}
        isPending={nodesQuery.isPending}
        error={nodesQuery.error}
        onRetry={() => void nodesQuery.refetch()}
        onAdd={openCreate}
        onEdit={openEdit}
        onDelete={setDeletingNode}
      />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1.45fr)_minmax(0,1fr)]">
        <StorageMigrationSection
          activeNode={activeNode}
          migratable={migratableQuery.data?.items ?? []}
          migratablePending={migratableQuery.isPending}
          migrations={migrationsQuery.data?.items ?? []}
          migrationsPending={migrationsQuery.isPending}
        />
        <TrainerCachePolicyCard
          policy={cachePolicyQuery.data}
          isPending={cachePolicyQuery.isPending}
        />
      </div>

      <StorageDirectoriesCard />

      <StorageNodeDialog
        key={dialogKey}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        node={editingNode}
      />

      <AlertDialog
        open={deletingNode !== null}
        onOpenChange={(open) => {
          if (!open && !deletingBusy) {
            setDeletingNode(null)
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除存储节点“{deletingNode?.name}”？
            </AlertDialogTitle>
            <AlertDialogDescription>
              只会从平台移除该存储节点的连接配置，不会删除远端存储服务器上的任何文件。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deletingBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault()
                void confirmDelete()
              }}
              disabled={deletingBusy}
              className="bg-destructive text-white hover:bg-destructive/90"
            >
              {deletingBusy && <Spinner aria-hidden />}
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function StorageNodesSection({
  nodes,
  isPending,
  error,
  onRetry,
  onAdd,
  onEdit,
  onDelete,
}: {
  nodes: StorageNode[] | undefined
  isPending: boolean
  error: Error | null
  onRetry: () => void
  onAdd: () => void
  onEdit: (node: StorageNode) => void
  onDelete: (node: StorageNode) => void
}) {
  const [probingId, setProbingId] = useState<number | null>(null)
  const [activatingId, setActivatingId] = useState<number | null>(null)

  const handleProbe = async (node: StorageNode) => {
    if (probingId !== null) return
    setProbingId(node.id)
    try {
      const res = await testStorageNode({
        node_id: node.id,
        kind: node.kind,
        local: node.config.local,
        s3: node.config.s3,
        smb: node.config.smb,
      })
      await refreshStorageQueries()
      if (res.ok) {
        toast.success(`“${node.name}”连接与读写正常`, {
          description: `探测延迟 ${res.latency_ms.toFixed(1)} ms`,
        })
      } else {
        toast.error(`“${node.name}”检测未通过`, {
          description: res.message,
        })
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "测试连接失败")
    } finally {
      setProbingId(null)
    }
  }

  const handleActivate = async (node: StorageNode) => {
    if (activatingId !== null) return
    setActivatingId(node.id)
    try {
      await activateStorageNode(node.id)
      await refreshStorageQueries()
      toast.success(`已将“${node.name}”设为当前存储节点`, {
        description:
          "新建的数据集与数据版本将写入该节点；已有数据可在下方一键迁移。",
      })
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "切换存储节点失败")
    } finally {
      setActivatingId(null)
    }
  }

  if (isPending) {
    return (
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-56 rounded-xl" />
        ))}
      </div>
    )
  }

  if (error || !nodes) {
    return (
      <Card>
        <CardContent className="pt-6">
          <SystemInfoError
            message={error?.message ?? "无法加载存储节点列表"}
            retrying={false}
            onRetry={onRetry}
          />
        </CardContent>
      </Card>
    )
  }

  return (
    <section className="grid gap-3" aria-label="存储节点列表">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-medium">
          <span>存储节点</span>
          <Badge variant="secondary" className="tabular-nums">
            {nodes.length}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground">
          全局同时启用一个“当前使用中”的存储节点，无需在每次上传数据集时重复选择。
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {nodes.map((node) => {
          const totalItems =
            node.stats.dataset_count +
            node.stats.trashed_dataset_count +
            node.stats.data_version_count +
            node.stats.trashed_data_version_count
          const deleteBlockedReason = node.is_builtin
            ? "内置默认存储节点不可删除"
            : node.is_active
              ? "当前正在使用的存储节点不可删除，请先切换到其他节点"
              : totalItems > 0
                ? "节点上仍存有数据集或数据版本（含回收站），请先迁移或清理后再删除"
                : null

          return (
            <Card
              key={node.id}
              className={cn(
                "flex flex-col justify-between transition-shadow",
                node.is_active &&
                  "border-emerald-500/50 ring-1 ring-emerald-500/25 dark:border-emerald-400/40"
              )}
            >
              <CardHeader className="gap-2 pb-3">
                <div className="flex items-start justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <div
                      className={cn(
                        "flex size-9 shrink-0 items-center justify-center rounded-lg border",
                        node.is_active
                          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
                          : "bg-muted text-muted-foreground"
                      )}
                    >
                      <StorageKindIcon kind={node.kind} className="size-4" />
                    </div>
                    <div className="min-w-0">
                      <CardTitle className="truncate text-base">
                        {node.name}
                      </CardTitle>
                      <p className="text-xs text-muted-foreground">
                        {STORAGE_KIND_SHORT_LABELS[node.kind]}
                        {node.is_builtin ? " · 平台内置" : ""}
                      </p>
                    </div>
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
                    {node.is_active && (
                      <Badge className="border-emerald-600/20 bg-emerald-600 text-white hover:bg-emerald-600">
                        <span className="size-1.5 rounded-full bg-white" />
                        当前使用中
                      </Badge>
                    )}
                    <NodeStatusBadge status={node.status} />
                  </div>
                </div>

                <code
                  className="block truncate rounded-md bg-muted px-2.5 py-1.5 font-mono text-xs text-foreground"
                  title={node.display_target}
                >
                  {node.display_target}
                </code>
              </CardHeader>

              <CardContent className="grid flex-1 content-between gap-4">
                <div className="grid gap-3">
                  <dl className="grid grid-cols-3 gap-2 rounded-lg border bg-muted/25 px-3 py-2 text-xs">
                    <div className="grid gap-0.5">
                      <dt className="text-muted-foreground">数据集</dt>
                      <dd className="font-semibold tabular-nums text-sm">
                        {formatNumber(node.stats.dataset_count)}
                      </dd>
                    </div>
                    <div className="grid gap-0.5">
                      <dt className="text-muted-foreground">数据版本</dt>
                      <dd className="font-semibold tabular-nums text-sm">
                        {formatNumber(node.stats.data_version_count)}
                      </dd>
                    </div>
                    <div className="grid gap-0.5">
                      <dt className="text-muted-foreground">已存数据</dt>
                      <dd className="font-semibold tabular-nums text-sm">
                        {formatBytes(node.stats.stored_bytes)}
                      </dd>
                    </div>
                  </dl>

                  {node.usage && node.usage.total_bytes > 0 ? (
                    <NodeCapacityBar usage={node.usage} />
                  ) : node.kind === "s3" ? (
                    <p className="text-xs text-muted-foreground">
                      对象存储按需弹性扩展，凭证已加密保存在数据库中。
                    </p>
                  ) : null}

                  {node.status === "error" && node.status_error && (
                    <p
                      className="line-clamp-2 rounded-md bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
                      title={node.status_error}
                    >
                      {node.status_error}
                    </p>
                  )}
                </div>

                <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
                  <div className="flex flex-wrap items-center gap-1.5">
                    {!node.is_active ? (
                      <Button
                        size="xs"
                        onClick={() => void handleActivate(node)}
                        disabled={activatingId !== null}
                      >
                        {activatingId === node.id ? (
                          <Spinner className="size-3" aria-hidden />
                        ) : (
                          <ShieldCheck />
                        )}
                        设为当前存储
                      </Button>
                    ) : (
                      <span className="text-xs text-muted-foreground">
                        {node.last_checked_at
                          ? `检测于 ${formatRelativeTime(node.last_checked_at)}`
                          : "新数据默认写入此节点"}
                      </span>
                    )}
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() => void handleProbe(node)}
                      disabled={probingId !== null}
                    >
                      {probingId === node.id ? (
                        <Spinner className="size-3" aria-hidden />
                      ) : (
                        <PlugZap />
                      )}
                      测试连接
                    </Button>
                  </div>

                  {!node.is_builtin && (
                    <div className="flex items-center gap-1">
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => onEdit(node)}
                            aria-label={`编辑 ${node.name}`}
                          >
                            <Pencil />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>编辑连接配置</TooltipContent>
                      </Tooltip>

                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span className="inline-flex">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => onDelete(node)}
                              disabled={deleteBlockedReason !== null}
                              aria-label={`删除 ${node.name}`}
                              className="text-muted-foreground hover:text-destructive"
                            >
                              <Trash2 />
                            </Button>
                          </span>
                        </TooltipTrigger>
                        <TooltipContent>
                          {deleteBlockedReason ?? "删除存储节点"}
                        </TooltipContent>
                      </Tooltip>
                    </div>
                  )}
                </div>
              </CardContent>
            </Card>
          )
        })}

        {nodes.length === 1 && (
          <button
            type="button"
            onClick={onAdd}
            className="flex min-h-52 flex-col items-center justify-center gap-2 rounded-xl border border-dashed bg-muted/15 p-6 text-center transition-colors hover:bg-muted/35"
          >
            <div className="flex size-10 items-center justify-center rounded-full border bg-background shadow-xs">
              <Plus className="size-5 text-muted-foreground" />
            </div>
            <p className="text-sm font-medium">添加外部存储服务器</p>
            <p className="max-w-xs text-xs text-muted-foreground">
              支持 S3 兼容存储（MinIO / RustFS）与 Windows / NAS SMB
              共享目录，把图片存放在专用存储机上以释放训练服务器空间。
            </p>
          </button>
        )}
      </div>
    </section>
  )
}

function NodeStatusBadge({ status }: { status: StorageNode["status"] }) {
  if (status === "ok") {
    return (
      <Badge variant="outline" className="gap-1 font-normal">
        <span className="size-1.5 rounded-full bg-emerald-500" />
        连接正常
      </Badge>
    )
  }
  if (status === "error") {
    return <Badge variant="destructive">连接异常</Badge>
  }
  return (
    <Badge variant="outline" className="font-normal text-muted-foreground">
      待检测
    </Badge>
  )
}

function NodeCapacityBar({ usage }: { usage: StorageUsageInfo }) {
  const usedPercent = percentOf(usage.used_bytes, usage.total_bytes)
  return (
    <div className="grid gap-1.5">
      <Progress
        value={usedPercent}
        className="h-1.5"
        aria-label={`已使用 ${usedPercent.toFixed(0)}%`}
      />
      <div className="flex flex-wrap justify-between gap-x-3 text-xs text-muted-foreground tabular-nums">
        <span>
          已用 {formatBytes(usage.used_bytes)}（{usedPercent.toFixed(0)}%）
        </span>
        <span>
          可用 {formatBytes(usage.free_bytes)} / {formatBytes(usage.total_bytes)}
        </span>
      </div>
    </div>
  )
}

function StorageMigrationSection({
  activeNode,
  migratable,
  migratablePending,
  migrations,
  migrationsPending,
}: {
  activeNode: StorageNode | null
  migratable: MigratableItem[]
  migratablePending: boolean
  migrations: StorageMigration[]
  migrationsPending: boolean
}) {
  const [selectedKeys, setSelectedKeys] = useState<ReadonlySet<string>>(
    new Set()
  )
  const [deleteSource, setDeleteSource] = useState(true)
  const [starting, setStarting] = useState(false)
  const [cancelingId, setCancelingId] = useState<number | null>(null)

  const activeMigrationKeys = useMemo(() => {
    const set = new Set<string>()
    for (const m of migrations) {
      if (isActiveMigration(m)) {
        set.add(migrationKey(m))
      }
    }
    return set
  }, [migrations])

  const selectableItems = useMemo(
    () => migratable.filter((item) => !activeMigrationKeys.has(migrationKey(item))),
    [migratable, activeMigrationKeys]
  )

  const validSelected = useMemo(() => {
    const allowed = new Set(selectableItems.map(migrationKey))
    return [...selectedKeys].filter((k) => allowed.has(k))
  }, [selectedKeys, selectableItems])

  const allSelected =
    selectableItems.length > 0 && validSelected.length === selectableItems.length

  const toggleAll = (checked: boolean) => {
    if (checked) {
      setSelectedKeys(new Set(selectableItems.map(migrationKey)))
    } else {
      setSelectedKeys(new Set())
    }
  }

  const toggleOne = (key: string, checked: boolean) => {
    setSelectedKeys((prev) => {
      const next = new Set(prev)
      if (checked) {
        next.add(key)
      } else {
        next.delete(key)
      }
      return next
    })
  }

  const startMigration = async (targets: MigratableItem[]) => {
    if (starting || targets.length === 0) return
    setStarting(true)
    try {
      const res = await createStorageMigrations({
        targets: targets.map((t) => ({
          target_type: t.target_type,
          target_id: t.target_id,
        })),
        delete_source: deleteSource,
      })
      setSelectedKeys(new Set())
      await refreshStorageQueries()
      toast.success(`已创建 ${res.items.length} 个后台迁移任务`, {
        description: deleteSource
          ? "文件逐个校验通过后将自动清理原存储节点上的文件。"
          : "迁移完成后原存储节点上的文件将保留。",
      })
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "发起迁移任务失败")
    } finally {
      setStarting(false)
    }
  }

  const handleCancel = async (migration: StorageMigration) => {
    if (cancelingId !== null) return
    setCancelingId(migration.id)
    try {
      await cancelStorageMigration(migration.id)
      await refreshStorageQueries()
      toast.success(`已取消“${migration.target_name}”的迁移`)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "取消迁移失败")
    } finally {
      setCancelingId(null)
    }
  }

  const selectedItems = useMemo(() => {
    const set = new Set(validSelected)
    return selectableItems.filter((item) => set.has(migrationKey(item)))
  }, [selectableItems, validSelected])

  return (
    <Card className="flex flex-col">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ArrowRightLeft className="size-4 text-muted-foreground" />
          历史数据迁移与空间释放
        </CardTitle>
        <CardDescription>
          将早期保存在其他节点上的数据集与已生成的数据版本在线迁移至当前存储节点
          {activeNode && (
            <>
              （<strong className="font-medium text-foreground">{activeNode.name}</strong>）
            </>
          )}
          ，自动逐文件核对字节数并同步元数据。
        </CardDescription>
      </CardHeader>

      <CardContent className="grid gap-5">
        {migratablePending ? (
          <Skeleton className="h-32 w-full rounded-lg" />
        ) : migratable.length === 0 ? (
          <div className="flex items-center gap-3 rounded-lg border bg-muted/25 px-4 py-3.5 text-sm">
            <CheckCircle2 className="size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
            <div className="grid gap-0.5">
              <p className="font-medium">所有数据集与已就绪数据版本均已在当前存储节点上</p>
              <p className="text-xs text-muted-foreground">
                切换到新的存储节点后，留在旧节点上的历史数据集与数据版本会自动列在这里供一键迁移。
              </p>
            </div>
          </div>
        ) : (
          <div className="grid gap-3">
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-10 pl-3">
                      <Checkbox
                        checked={allSelected}
                        onCheckedChange={(v) => toggleAll(v === true)}
                        disabled={selectableItems.length === 0 || starting}
                        aria-label="全选待迁移项"
                      />
                    </TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead className="hidden sm:table-cell">类型</TableHead>
                    <TableHead>当前节点</TableHead>
                    <TableHead className="text-right pr-3">规模</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {migratable.map((item) => {
                    const key = migrationKey(item)
                    const running = activeMigrationKeys.has(key)
                    const checked = validSelected.includes(key)
                    return (
                      <TableRow key={key}>
                        <TableCell className="pl-3">
                          <Checkbox
                            checked={checked && !running}
                            onCheckedChange={(v) => toggleOne(key, v === true)}
                            disabled={running || starting}
                            aria-label={`选择 ${item.name}`}
                          />
                        </TableCell>
                        <TableCell className="max-w-0 font-medium sm:max-w-none">
                          <div className="flex min-w-0 flex-col">
                            <Link
                              to={
                                item.target_type === "dataset"
                                  ? `/datasets/${item.target_id}`
                                  : dataVersionDetailPath(item.target_id)
                              }
                              className="truncate hover:underline"
                            >
                              {item.project_name
                                ? `${item.project_name} · ${item.name}`
                                : item.name}
                            </Link>
                            {running && (
                              <span className="text-xs text-sky-600 dark:text-sky-400">
                                正在后台迁移…
                              </span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="hidden sm:table-cell">
                          <Badge variant="secondary" className="font-normal">
                            {item.target_type === "dataset"
                              ? "数据集"
                              : "数据版本"}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <StorageNodeBadge node={item.storage_node} />
                        </TableCell>
                        <TableCell className="pr-3 text-right text-xs text-muted-foreground tabular-nums">
                          {formatNumber(item.image_count)} 张 ·{" "}
                          {formatBytes(item.total_bytes)}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>

            <div className="flex flex-col gap-3 rounded-lg border bg-muted/30 p-3 sm:flex-row sm:items-center sm:justify-between">
              <label className="flex cursor-pointer items-start gap-2.5 text-xs">
                <Checkbox
                  checked={deleteSource}
                  onCheckedChange={(v) => setDeleteSource(v === true)}
                  disabled={starting}
                  className="mt-0.5"
                />
                <span className="grid gap-0.5">
                  <span className="font-medium text-foreground">
                    迁移校验通过后删除原节点文件以释放磁盘空间（推荐）
                  </span>
                  <span className="text-muted-foreground">
                    本机数据集与它生成的数据版本共享硬链接时，将两者一并迁移并勾选此项可彻底释放本机占用的物理空间。
                  </span>
                </span>
              </label>

              <div className="flex shrink-0 flex-wrap items-center gap-2">
                {selectedItems.length > 0 ? (
                  <Button
                    size="sm"
                    onClick={() => void startMigration(selectedItems)}
                    disabled={starting}
                  >
                    {starting && <Spinner aria-hidden />}
                    迁移选中项（{selectedItems.length}）
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    onClick={() => void startMigration(selectableItems)}
                    disabled={starting || selectableItems.length === 0}
                  >
                    {starting && <Spinner aria-hidden />}
                    一键全部迁移（{selectableItems.length}）
                  </Button>
                )}
              </div>
            </div>
          </div>
        )}

        {!migrationsPending && migrations.length > 0 && (
          <div className="grid gap-2.5">
            <div className="flex items-center justify-between text-xs font-medium text-muted-foreground">
              <span>迁移任务记录</span>
              <span>保留最近 {migrations.length} 条</span>
            </div>
            <ul className="divide-y rounded-lg border">
              {migrations.slice(0, 8).map((m) => {
                const active = isActiveMigration(m)
                const pct =
                  m.bytes_total > 0
                    ? percentOf(m.bytes_done, m.bytes_total)
                    : m.files_total > 0
                      ? percentOf(m.files_done, m.files_total)
                      : 0
                return (
                  <li key={m.id} className="grid gap-2 px-3.5 py-2.5 text-xs">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <Badge variant="outline" className="font-normal">
                          {m.target_type === "dataset" ? "数据集" : "数据版本"}
                        </Badge>
                        <span className="truncate font-medium text-sm text-foreground">
                          {m.target_name}
                        </span>
                      </div>
                      <div className="flex items-center gap-2">
                        <MigrationStatusBadge status={m.status} />
                        {active && (
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => void handleCancel(m)}
                            disabled={cancelingId === m.id}
                            aria-label="取消迁移"
                          >
                            <X />
                          </Button>
                        )}
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-muted-foreground">
                      <span className="inline-flex items-center gap-1">
                        <span>{m.from_node.name}</span>
                        <ArrowRight className="size-3" />
                        <span className="text-foreground">{m.to_node.name}</span>
                        {m.delete_source && (
                          <span className="text-muted-foreground">
                            · 校验后清理源文件
                          </span>
                        )}
                      </span>
                      <span className="tabular-nums">
                        {m.files_total > 0 && (
                          <>
                            {formatNumber(m.files_done)} /{" "}
                            {formatNumber(m.files_total)} 个文件 ·{" "}
                          </>
                        )}
                        {formatBytes(m.bytes_done)}
                        {m.bytes_total > 0 && ` / ${formatBytes(m.bytes_total)}`}
                      </span>
                    </div>

                    {active && <Progress value={pct} className="h-1.5" />}

                    {m.status === "failed" && m.error && (
                      <p className="text-destructive">{m.error}</p>
                    )}
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function MigrationStatusBadge({
  status,
}: {
  status: StorageMigration["status"]
}) {
  if (status === "succeeded") {
    return (
      <Badge variant="outline" className="gap-1 font-normal">
        <span className="size-1.5 rounded-full bg-emerald-500" />
        {MIGRATION_STATUS_LABELS[status]}
      </Badge>
    )
  }
  if (status === "failed") {
    return <Badge variant="destructive">{MIGRATION_STATUS_LABELS[status]}</Badge>
  }
  if (status === "running" || status === "queued") {
    return (
      <Badge variant="secondary" className="gap-1 font-normal">
        <Spinner className="size-3" aria-hidden />
        {MIGRATION_STATUS_LABELS[status]}
      </Badge>
    )
  }
  return (
    <Badge variant="outline" className="font-normal text-muted-foreground">
      {MIGRATION_STATUS_LABELS[status]}
    </Badge>
  )
}

function TrainerCachePolicyCard({
  policy,
  isPending,
}: {
  policy: TrainerCachePolicy | undefined
  isPending: boolean
}) {
  const id = useId()
  const [draft, setDraft] = useState<TrainerCachePolicy | null>(null)
  const [saving, setSaving] = useState(false)

  const current: TrainerCachePolicy = draft ??
    policy ?? { enabled: false, max_versions: 2, ttl_hours: 24 }

  const dirty =
    policy !== undefined &&
    draft !== null &&
    (draft.enabled !== policy.enabled ||
      draft.max_versions !== policy.max_versions ||
      draft.ttl_hours !== policy.ttl_hours)

  const save = async () => {
    if (saving) return
    const maxVersions = Math.min(
      50,
      Math.max(1, Math.round(current.max_versions || 2))
    )
    const ttlHours = Math.min(
      720,
      Math.max(1, Math.round(current.ttl_hours || 24))
    )
    setSaving(true)
    try {
      const saved = await updateTrainerCachePolicy({
        enabled: current.enabled,
        max_versions: maxVersions,
        ttl_hours: ttlHours,
      })
      setDraft(null)
      await refreshStorageQueries()
      toast.success(
        saved.enabled
          ? `已启用训练机短期缓存（最多 ${saved.max_versions} 个版本 / ${saved.ttl_hours} 小时）`
          : "已设为训练结束后立即清理训练机数据集副本"
      )
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存缓存策略失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="flex flex-col justify-between">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Cpu className="size-4 text-muted-foreground" />
          训练机短期数据集缓存策略
        </CardTitle>
        <CardDescription>
          控制训练服务器（Compute Node）拉取数据集后的本地生命周期，通过心跳与任务指令自动下发给全部训练节点。
        </CardDescription>
      </CardHeader>

      <CardContent className="grid gap-4">
        {isPending ? (
          <Skeleton className="h-36 w-full rounded-lg" />
        ) : (
          <>
            <div className="flex items-start justify-between gap-3 rounded-lg border bg-muted/30 p-3.5">
              <div className="grid gap-1">
                <Label htmlFor={`${id}-enabled`} className="text-sm font-medium">
                  启用训练机短期数据集缓存
                </Label>
                <p className="text-xs text-muted-foreground">
                  {current.enabled
                    ? "开启中：同一数据版本连续调参训练时直接硬链接复用本地缓存，过期或空间不足时自动淘汰。"
                    : "已关闭（默认）：每次训练任务完成、失败或取消后，训练机立即删除解压的图片副本，保持训练机零数据残留。"}
                </p>
              </div>
              <Switch
                id={`${id}-enabled`}
                checked={current.enabled}
                onCheckedChange={(enabled) =>
                  setDraft({ ...current, enabled })
                }
                disabled={saving}
              />
            </div>

            <div
              className={cn(
                "grid gap-3 sm:grid-cols-2",
                !current.enabled && "opacity-55"
              )}
            >
              <div className="grid gap-1.5">
                <Label htmlFor={`${id}-max`} className="text-xs">
                  最多保留数据版本数（LRU）
                </Label>
                <Input
                  id={`${id}-max`}
                  type="number"
                  min={1}
                  max={50}
                  value={current.max_versions}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      max_versions: Number.parseInt(e.target.value || "1", 10),
                    })
                  }
                  disabled={!current.enabled || saving}
                />
                <span className="text-[11px] text-muted-foreground">
                  范围 1–50 个版本，超出后优先淘汰最久未使用的版本
                </span>
              </div>

              <div className="grid gap-1.5">
                <Label htmlFor={`${id}-ttl`} className="text-xs">
                  缓存最长保留时间（小时）
                </Label>
                <Input
                  id={`${id}-ttl`}
                  type="number"
                  min={1}
                  max={720}
                  value={current.ttl_hours}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      ttl_hours: Number.parseInt(e.target.value || "1", 10),
                    })
                  }
                  disabled={!current.enabled || saving}
                />
                <span className="text-[11px] text-muted-foreground">
                  范围 1–720 小时，空闲心跳期间到期自动清理
                </span>
              </div>
            </div>

            <div className="flex items-center justify-between gap-3 border-t pt-3">
              <span className="text-xs text-muted-foreground">
                {current.enabled
                  ? `当前策略：缓存最近 ${current.max_versions || 2} 个版本，保留 ${current.ttl_hours || 24} 小时`
                  : "当前策略：训练结束立即删除本地数据集"}
              </span>
              <Button
                size="sm"
                variant={dirty ? "default" : "outline"}
                onClick={() => void save()}
                disabled={saving || !dirty}
              >
                {saving && <Spinner aria-hidden />}
                保存策略
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function StorageDirectoriesCard() {
  const { data, error, isPending, isFetching, refetch } = useSystemInfo()

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <HardDrive className="size-4 text-muted-foreground" />
          平台本机系统目录
        </CardTitle>
        <CardDescription>
          模型权重、训练运行日志图表、审图工作区与缩略图缓存始终保存在平台本机目录
          {data && (
            <>
              {" "}
              <code className="font-mono text-foreground">
                {data.storage.root}
              </code>
              （宿主机 <code className="font-mono">TM_HOST_STORAGE_DIR</code>）
            </>
          )}
          ，保证网页缩略图浏览与高频小文件读写毫秒级响应。
        </CardDescription>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => void refetch()}
            disabled={isFetching}
          >
            <RefreshCw className={cn(isFetching && "animate-spin")} />
            <span className="sr-only">刷新本机目录状态</span>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-6">
        {isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-2 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : error ? (
          <SystemInfoError
            message={error.message}
            retrying={isFetching}
            onRetry={() => void refetch()}
          />
        ) : (
          <>
            <StorageUsage storage={data.storage} />
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>目录</TableHead>
                  <TableHead className="hidden sm:table-cell">职责说明</TableHead>
                  <TableHead>容器内路径</TableHead>
                  <TableHead className="text-right">状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.storage.directories.map((directory) => (
                  <TableRow key={directory.key}>
                    <TableCell className="font-medium">
                      {directory.key}
                    </TableCell>
                    <TableCell className="hidden text-muted-foreground sm:table-cell">
                      {directoryPurposes[directory.key] ?? "—"}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {directory.path}
                    </TableCell>
                    <TableCell className="text-right">
                      {directory.writable ? (
                        <Badge variant="outline">可写</Badge>
                      ) : (
                        <Badge variant="destructive">不可写</Badge>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </>
        )}
      </CardContent>
    </Card>
  )
}
