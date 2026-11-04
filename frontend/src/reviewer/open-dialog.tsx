import { useState, type FormEvent } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  ArrowUp,
  Download,
  FileArchive,
  FileCheck2,
  Folder,
  FolderCheck,
  FolderOpen,
  HardDrive,
  House,
  Monitor,
  FileText,
  RefreshCw,
  type LucideIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { formatBytes, formatDateTime, formatNumber } from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"
import { cn } from "@/lib/utils"
import {
  browseQuery,
  type BrowseEntry,
  type BrowseEntryKind,
  type Place,
} from "@/reviewer/api"
import { isOpenable, typedPath } from "@/reviewer/format"

const LAST_FOLDER_KEY = "trainmaster.reviewer.folder"

const kindIcons: Record<BrowseEntryKind, LucideIcon> = {
  folder: Folder,
  package: FolderCheck,
  zip: FileArchive,
  results: FileCheck2,
  home: House,
  downloads: Download,
  desktop: Monitor,
  documents: FileText,
  drive: HardDrive,
  root: HardDrive,
}

/**
 * 选择要打开的审图包：浏览本机的文件夹（只列出文件夹与 zip 文件，审图包文件夹
 * 标出），也可以直接粘贴路径。浏览器不提供文件的完整路径，所以不用系统的“打开
 * 文件”对话框。
 */
export function OpenDialog({
  open,
  onOpenChange,
  onOpen,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 开始打开；成功开始时返回 true（对话框随之关闭）。 */
  onOpen: (path: string) => Promise<boolean>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[min(44rem,calc(100svh-2rem))] flex-col gap-3 sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>打开审图包</DialogTitle>
          <DialogDescription>
            选择从平台下载的审图包（.zip），或者解压后的审图包文件夹（其中有
            review.json）。zip 会解压到它旁边的同名文件夹中。
          </DialogDescription>
        </DialogHeader>
        {open && (
          <Browser onOpen={onOpen} onCancel={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Browser({
  onOpen,
  onCancel,
}: {
  onOpen: (path: string) => Promise<boolean>
  onCancel: () => void
}) {
  const [folder, setFolder] = useState(
    () => readPreference(LAST_FOLDER_KEY) ?? ""
  )
  const [selected, setSelected] = useState<BrowseEntry | null>(null)
  const [typed, setTyped] = useState("")
  const [opening, setOpening] = useState(false)
  const query = useQuery({
    ...browseQuery(folder),
    placeholderData: keepPreviousData,
  })
  const data = query.data
  // 记住的文件夹已不存在（或无法打开）：回到常用位置。
  const failed = query.isError && !query.isFetching && folder !== ""

  const go = (path: string) => {
    setFolder(path)
    setSelected(null)
    writePreference(LAST_FOLDER_KEY, path)
  }
  const start = async (text: string) => {
    const path = typedPath(text)
    if (!path || opening) {
      return
    }
    setOpening(true)
    const ok = await onOpen(path)
    setOpening(false)
    if (ok) {
      onCancel()
    }
  }
  const activate = (entry: BrowseEntry) => {
    if (isOpenable(entry)) {
      void start(entry.path)
    } else if (entry.kind !== "results") {
      go(entry.path)
    }
  }
  const submitTyped = (event: FormEvent) => {
    event.preventDefault()
    void start(typed)
  }

  const places = data?.places ?? []
  const entries = data?.entries ?? []
  return (
    <>
      <form onSubmit={submitTyped} className="flex gap-2">
        <Input
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          placeholder="或粘贴路径，如 C:\Users\张三\Downloads\审图-12-外观.zip"
          aria-label="审图包的路径"
          spellCheck={false}
          className="font-mono text-xs"
        />
        <Button
          type="submit"
          variant="outline"
          disabled={!typed.trim() || opening}
        >
          打开
        </Button>
      </form>

      <div className="flex min-h-0 flex-1 flex-col gap-2 sm:flex-row">
        {/* 常用位置：宽屏时在左侧，窄屏时是一行可横向滚动的按钮 */}
        <nav
          aria-label="常用位置"
          className="flex shrink-0 gap-1 overflow-x-auto pb-1 sm:w-40 sm:flex-col sm:overflow-x-visible sm:overflow-y-auto sm:pb-0"
        >
          {places.length === 0 && query.isPending
            ? Array.from({ length: 4 }, (_, i) => (
                <Skeleton key={i} className="h-8 w-24 sm:w-full" />
              ))
            : places.map((place) => (
                <PlaceButton
                  key={place.path}
                  place={place}
                  active={folder === place.path}
                  onClick={() => go(place.path)}
                />
              ))}
        </nav>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col rounded-md border">
          <div className="flex items-center gap-1 border-b p-1.5">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="上一级"
              title="上一级"
              disabled={folder === ""}
              onClick={() => go(data?.parent ?? "")}
            >
              <ArrowUp />
            </Button>
            {/* 路径太长时省略开头，留下当前文件夹的名字 */}
            <p
              dir="rtl"
              className="min-w-0 flex-1 truncate text-left font-mono text-xs text-muted-foreground"
              title={folder}
            >
              <bdi>{folder || "常用位置"}</bdi>
            </p>
            {query.isFetching && <Spinner className="size-4" />}
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="刷新"
              title="刷新"
              onClick={() => void query.refetch()}
            >
              <RefreshCw />
            </Button>
          </div>
          <div
            role="listbox"
            aria-label="文件夹中的项目"
            className="min-h-48 flex-1 overflow-y-auto p-1"
          >
            {failed ? (
              <div className="flex flex-col items-center gap-3 p-6 text-center text-sm">
                <p className="text-destructive">{query.error.message}</p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => go("")}
                >
                  回到常用位置
                </Button>
              </div>
            ) : query.isPending ? (
              <div className="grid gap-1 p-1">
                {Array.from({ length: 6 }, (_, i) => (
                  <Skeleton key={i} className="h-9" />
                ))}
              </div>
            ) : entries.length === 0 ? (
              <p className="p-6 text-center text-sm text-muted-foreground">
                这个文件夹中没有子文件夹或 zip 文件。
              </p>
            ) : (
              entries.map((entry) => (
                <EntryRow
                  key={entry.path}
                  entry={entry}
                  selected={selected?.path === entry.path}
                  onSelect={() => setSelected(entry)}
                  onActivate={() => activate(entry)}
                />
              ))
            )}
            {data?.truncated && (
              <p className="p-2 text-center text-xs text-muted-foreground">
                只列出了前 {formatNumber(entries.length)} 项。
              </p>
            )}
          </div>
        </div>
      </div>

      <DialogFooter className="items-center gap-2 sm:justify-between">
        <p className="min-w-0 truncate text-xs text-muted-foreground sm:flex-1">
          {selected
            ? isOpenable(selected)
              ? `将打开：${selected.name}`
              : selected.kind === "results"
                ? "这是导出的审图结果：请在平台上审图任务的页面点“上传审图结果”上传它"
                : "双击文件夹进入；选中审图包（.zip 或审图包文件夹）后打开"
            : "选中审图包（.zip 或带 review.json 的文件夹），双击直接打开"}
        </p>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button
            type="button"
            disabled={!selected || selected.kind === "results" || opening}
            onClick={() => selected && activate(selected)}
          >
            {opening ? <Spinner /> : <FolderOpen />}
            {selected && !isOpenable(selected) ? "进入文件夹" : "打开"}
          </Button>
        </div>
      </DialogFooter>
    </>
  )
}

function PlaceButton({
  place,
  active,
  onClick,
}: {
  place: Place
  active: boolean
  onClick: () => void
}) {
  const Icon = kindIcons[place.kind]
  return (
    <Button
      type="button"
      variant={active ? "secondary" : "ghost"}
      size="sm"
      className="shrink-0 justify-start sm:w-full"
      title={place.path}
      onClick={onClick}
    >
      <Icon />
      <span className="truncate">{place.name}</span>
    </Button>
  )
}

function EntryRow({
  entry,
  selected,
  onSelect,
  onActivate,
}: {
  entry: BrowseEntry
  selected: boolean
  onSelect: () => void
  onActivate: () => void
}) {
  const Icon = kindIcons[entry.kind]
  const openable = isOpenable(entry)
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      onDoubleClick={onActivate}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault()
          onActivate()
        }
      }}
      className={cn(
        "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50",
        selected && "bg-accent"
      )}
    >
      <Icon
        className={cn(
          "size-4 shrink-0",
          openable ? "text-primary" : "text-muted-foreground"
        )}
      />
      <span
        className={cn(
          "min-w-0 flex-1 truncate",
          entry.kind === "results" && "text-muted-foreground"
        )}
        title={entry.name}
      >
        {entry.name}
      </span>
      {entry.kind === "package" && (
        <Badge variant="secondary" className="shrink-0">
          审图包
        </Badge>
      )}
      {entry.kind === "results" && (
        <Badge variant="outline" className="shrink-0">
          审图结果
        </Badge>
      )}
      {(entry.kind === "zip" || entry.kind === "results") && (
        <span className="hidden shrink-0 text-xs text-muted-foreground tabular-nums sm:inline">
          {formatBytes(entry.size_bytes)}
        </span>
      )}
      {entry.modified_at && (
        <span className="hidden w-32 shrink-0 text-right text-xs text-muted-foreground tabular-nums md:inline">
          {formatDateTime(entry.modified_at)}
        </span>
      )}
    </button>
  )
}
