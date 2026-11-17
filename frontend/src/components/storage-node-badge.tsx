import { Cloud, FolderSync, HardDrive } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import {
  STORAGE_KIND_SHORT_LABELS,
  type StorageNodeKind,
  type StorageNodeRef,
} from "@/features/system/storage-api"
import { cn } from "@/lib/utils"

export function StorageKindIcon({
  kind,
  className,
}: {
  kind: StorageNodeKind
  className?: string
}) {
  switch (kind) {
    case "s3":
      return <Cloud className={className} aria-hidden />
    case "smb":
      return <FolderSync className={className} aria-hidden />
    default:
      return <HardDrive className={className} aria-hidden />
  }
}

export function StorageNodeBadge({
  node,
  hideBuiltin = false,
  className,
}: {
  node?: StorageNodeRef | null
  /** 列表卡片等紧凑位置可在需要时隐藏默认本机存储。 */
  hideBuiltin?: boolean
  className?: string
}) {
  if (!node || (hideBuiltin && node.is_builtin)) {
    return null
  }
  const remote = !node.is_builtin
  return (
    <Badge
      variant="outline"
      title={`存储节点：${node.name}（${STORAGE_KIND_SHORT_LABELS[node.kind]}）`}
      className={cn(
        "max-w-44 gap-1 font-normal",
        remote
          ? "border-sky-500/35 bg-sky-500/8 text-sky-700 dark:border-sky-400/35 dark:bg-sky-400/10 dark:text-sky-300"
          : "text-muted-foreground",
        className
      )}
    >
      <StorageKindIcon kind={node.kind} className="size-3 shrink-0" />
      <span className="truncate">{node.name}</span>
    </Badge>
  )
}
