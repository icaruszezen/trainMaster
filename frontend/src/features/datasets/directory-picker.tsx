import { useId } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  CircleAlert,
  CircleCheck,
  Folder,
  FolderOpen,
  RefreshCw,
} from "lucide-react"
import { Link } from "react-router"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  directoriesQuery,
  type DatasetDirectory,
} from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import { formatBytes, formatNumber, formatRelativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 数据集根目录在服务器与宿主机上的位置说明。 */
export function DatasetsRootHint({
  root,
  className,
}: {
  root: string | null
  className?: string
}) {
  return (
    <p className={cn("text-xs text-muted-foreground", className)}>
      服务器目录{" "}
      <code className="rounded bg-muted px-1 py-0.5 font-mono text-foreground">
        {root ? `${root}/` : "datasets/"}
      </code>
      {/* 宿主机路径只对部署人员有用，窄屏上省略以免挤占空间。 */}
      <span className="hidden sm:inline">
        ，即宿主机上{" "}
        <code className="font-mono">TM_HOST_STORAGE_DIR/datasets/</code>
        （默认 <code className="font-mono">./data/storage/datasets/</code>）
      </span>
    </p>
  )
}

export type DirectoryRowState =
  | { status: "pending" }
  | { status: "done" }
  | { status: "error"; message: string }

type DirectoryPickerProps = {
  root: string | null
  selected: ReadonlySet<string>
  onToggle: (directory: DatasetDirectory, checked: boolean) => void
  /** 选中目录的问题（名称不合法、重名、服务端报错）。 */
  problems: ReadonlyMap<string, string>
  rowStates: ReadonlyMap<string, DirectoryRowState>
  disabled?: boolean
}

/** 选择 datasets/ 下已有的文件夹（可多选）。已添加的显示为不可选。 */
export function DirectoryPicker({
  root,
  selected,
  onToggle,
  problems,
  rowStates,
  disabled,
}: DirectoryPickerProps) {
  const { data, error, isPending, isFetching, refetch } =
    useQuery(directoriesQuery())

  return (
    <div className="grid gap-3">
      <div className="flex items-start justify-between gap-3">
        <DatasetsRootHint root={root} className="pt-1" />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => refetch()}
          disabled={isFetching}
          className="shrink-0"
        >
          <RefreshCw className={cn(isFetching && "animate-spin")} />
          刷新
        </Button>
      </div>

      {isPending ? (
        <div className="grid gap-px overflow-hidden rounded-lg border">
          {[0, 1, 2].map((row) => (
            <div key={row} className="flex items-center gap-3 px-3 py-3">
              <Skeleton className="size-4" />
              <Skeleton className="size-8" />
              <div className="grid flex-1 gap-1.5">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-3 w-56" />
              </div>
            </div>
          ))}
        </div>
      ) : error && data === undefined ? (
        <LoadError
          title="无法读取数据目录"
          error={error}
          retrying={isFetching}
          onRetry={() => refetch()}
          className="p-6 md:p-8"
        />
      ) : data.items.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-10 text-center">
          <FolderOpen className="size-8 text-muted-foreground" />
          <p className="text-sm font-medium">数据目录下还没有文件夹</p>
          <p className="max-w-sm text-xs text-muted-foreground">
            把图片（以及同名的 VOC 标注
            .xml）按文件夹拷贝到上面的目录中，然后点击“刷新”。
          </p>
        </div>
      ) : (
        <ul className="max-h-80 divide-y overflow-y-auto rounded-lg border">
          {data.items.map((directory) => (
            <DirectoryRow
              key={directory.name}
              directory={directory}
              checked={selected.has(directory.name)}
              onToggle={onToggle}
              problem={problems.get(directory.name)}
              state={rowStates.get(directory.name)}
              disabled={disabled}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function DirectoryRow({
  directory,
  checked,
  onToggle,
  problem,
  state,
  disabled,
}: {
  directory: DatasetDirectory
  checked: boolean
  onToggle: DirectoryPickerProps["onToggle"]
  problem: string | undefined
  state: DirectoryRowState | undefined
  disabled?: boolean
}) {
  const id = useId()
  const prefix = directory.approximate ? "约 " : ""
  const unavailable = directory.registered

  return (
    <li
      className={cn(
        "flex items-center gap-3 px-3 transition-colors",
        !unavailable && "hover:bg-muted/50",
        checked && "bg-muted/40"
      )}
    >
      <Checkbox
        id={id}
        checked={checked && !unavailable}
        disabled={unavailable || disabled}
        onCheckedChange={(value) => onToggle(directory, value === true)}
        aria-describedby={`${id}-meta`}
      />
      <label
        htmlFor={id}
        className={cn(
          "flex min-w-0 flex-1 cursor-pointer items-center gap-3 py-2.5",
          unavailable && "cursor-default opacity-60"
        )}
      >
        <div className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
          <Folder className="size-4" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium" title={directory.name}>
            {directory.name}
          </p>
          <p
            id={`${id}-meta`}
            className="text-xs text-muted-foreground tabular-nums"
          >
            {prefix}
            {formatNumber(directory.image_count)} 张图片 · {prefix}
            {formatNumber(directory.annotation_count)} 个标注 ·{" "}
            {formatBytes(directory.total_bytes)} ·{" "}
            {formatRelativeTime(directory.modified_at)}修改
          </p>
          {problem && checked && (
            <p className="mt-0.5 text-xs text-destructive">{problem}</p>
          )}
        </div>
      </label>
      <div className="flex shrink-0 items-center gap-2">
        {state?.status === "pending" && <Spinner />}
        {state?.status === "done" && (
          <CircleCheck className="size-4 text-emerald-600 dark:text-emerald-500" />
        )}
        {state?.status === "error" && (
          <CircleAlert className="size-4 text-destructive" />
        )}
        {unavailable && (
          <>
            <Badge variant="secondary">已添加</Badge>
            {directory.dataset_id !== null && (
              <Link
                to={`/datasets/${directory.dataset_id}`}
                className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
              >
                查看
              </Link>
            )}
          </>
        )}
      </div>
    </li>
  )
}
