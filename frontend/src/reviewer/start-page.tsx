import { useEffect, useState } from "react"
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query"
import {
  ArrowRight,
  CircleAlert,
  FileArchive,
  FolderOpen,
  Power,
  ScanEye,
  X,
} from "lucide-react"
import { Navigate, useNavigate } from "react-router"
import { toast } from "sonner"

import { ModeToggle } from "@/components/mode-toggle"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { TaskProgress } from "@/features/datasets/task-progress"
import { LoadError } from "@/features/datasets/load-error"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import {
  cancelOpening,
  closePackage,
  openPackage,
  removeRecent,
  revealInFolder,
  sessionKey,
  sessionQuery,
  type Opening,
  type PackageInfo,
  type RecentPackage,
  type Session,
} from "@/reviewer/api"
import { useExportResults } from "@/reviewer/export-dialog"
import {
  baseName,
  openingProgress,
  openingStageLabels,
  openOutcome,
  pageTitle,
  progressSummary,
} from "@/reviewer/format"
import { OpenDialog } from "@/reviewer/open-dialog"
import { useQuit } from "@/reviewer/quit"

/** 开始页：打开审图包、继续正在审的审图包、最近打开过的审图包。 */
export function StartPage() {
  const query = useQuery(sessionQuery())
  usePageTitle(pageTitle(null))
  if (query.data === undefined) {
    return (
      <Shell version="">
        {query.error ? (
          <LoadError
            title="无法连接本地审图程序"
            error={query.error}
            retrying={query.isFetching}
            onRetry={() => void query.refetch()}
          />
        ) : (
          <div className="grid gap-4">
            <Skeleton className="h-40" />
            <Skeleton className="h-56" />
          </div>
        )}
      </Shell>
    )
  }
  return <Start session={query.data} />
}

async function refreshSession(queryClient: QueryClient) {
  await queryClient.refetchQueries({ queryKey: sessionKey, exact: true })
}

function Start({ session }: { session: Session }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  // 等待打开完成的审图包（命令行 / 拖到图标上打开的审图包也算），完成后进入审图。
  const [following, setFollowing] = useState<string | null>(
    () => session.opening?.path ?? null
  )
  const [dialogOpen, setDialogOpen] = useState(false)
  const [dismissed, setDismissed] = useState<Session["open_error"]>(null)
  const outcome = openOutcome(session, following)

  const open = async (path: string) => {
    try {
      await openPackage(path)
    } catch (error) {
      toast.error("无法打开审图包", {
        description: error instanceof Error ? error.message : undefined,
      })
      return false
    }
    await refreshSession(queryClient)
    setFollowing(path)
    return true
  }
  const cancel = useMutation({
    mutationFn: cancelOpening,
    onSuccess: async () => {
      setFollowing(null)
      await refreshSession(queryClient)
    },
    onError: (error) =>
      toast.error("无法取消打开", { description: error.message }),
  })
  const close = useMutation({
    mutationFn: closePackage,
    onSuccess: () => refreshSession(queryClient),
    onError: (error) =>
      toast.error("无法关闭审图包", { description: error.message }),
  })

  if (outcome.done) {
    return <Navigate to="/review" />
  }
  const failure = session.opening ? null : session.open_error
  const error =
    failure &&
    !(dismissed?.path === failure.path && dismissed.message === failure.message)
      ? failure
      : null

  return (
    <Shell version={session.version}>
      {session.opening && (
        <OpeningCard
          opening={session.opening}
          canceling={cancel.isPending}
          onCancel={() => cancel.mutate()}
        />
      )}
      {error && (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>无法打开 {baseName(error.path)}</AlertTitle>
          <AlertDescription>
            <p className="break-all">{error.message}</p>
            <p className="font-mono text-xs break-all opacity-80">
              {error.path}
            </p>
          </AlertDescription>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="关闭提示"
            className="absolute top-2 right-2"
            onClick={() => setDismissed(error)}
          >
            <X />
          </Button>
        </Alert>
      )}
      {session.package && (
        <CurrentPackage
          pkg={session.package}
          opening={session.opening !== null}
          closing={close.isPending}
          onContinue={() => navigate("/review")}
          onClose={() => close.mutate()}
        />
      )}
      <OpenCard
        hasPackage={session.package !== null}
        busy={session.opening !== null}
        onOpen={() => setDialogOpen(true)}
      />
      <RecentList
        recent={session.recent}
        current={session.package?.path ?? null}
        busy={session.opening !== null}
        onOpen={(path) => void open(path)}
      />
      <OpenDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onOpen={open}
      />
    </Shell>
  )
}

function Shell({
  version,
  children,
}: {
  version: string
  children: React.ReactNode
}) {
  const quit = useQuit()
  return (
    <div className="flex min-h-svh flex-col bg-background">
      <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-3 border-b bg-background/95 px-4 backdrop-blur">
        <span className="flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <ScanEye className="size-4" />
        </span>
        <div className="min-w-0">
          <h1 className="truncate text-sm leading-tight font-semibold">
            trainMaster 本地审图
          </h1>
          {version && (
            <p className="truncate text-xs leading-tight text-muted-foreground">
              版本 {version}
            </p>
          )}
        </div>
        <div className="ml-auto flex items-center gap-1">
          <ModeToggle />
          <Button variant="ghost" size="sm" onClick={quit}>
            <Power />
            退出
          </Button>
        </div>
      </header>
      <main className="mx-auto grid w-full max-w-4xl grid-cols-1 content-start gap-4 p-4 md:gap-6 md:p-6">
        {children}
      </main>
    </div>
  )
}

function OpeningCard({
  opening,
  canceling,
  onCancel,
}: {
  opening: Opening
  canceling: boolean
  onCancel: () => void
}) {
  const { percent, detail } = openingProgress(opening)
  return (
    <Card aria-live="polite">
      <CardHeader>
        <CardTitle className="text-base">
          {openingStageLabels[opening.stage]}…
        </CardTitle>
        <CardDescription className="font-mono text-xs break-all">
          {opening.path}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-1.5">
        <TaskProgress value={percent} label="打开进度" />
        <p className="text-xs text-muted-foreground tabular-nums">
          {detail || "请稍候"}
          {opening.stage === "extracting" &&
            " · 第一次打开时需要解压，之后再打开会直接继续"}
        </p>
      </CardContent>
      <CardFooter>
        <Button variant="outline" onClick={onCancel} disabled={canceling}>
          取消
        </Button>
      </CardFooter>
    </Card>
  )
}

/**
 * 正在审的审图包。正在打开另一个审图包时不能继续审它：打开完成时程序关闭它、
 * 换成新的审图包，这时还没有保存的修改会写进新的审图包。
 */
export function CurrentPackage({
  pkg,
  opening,
  closing,
  onContinue,
  onClose,
}: {
  pkg: PackageInfo
  /** 正在打开另一个审图包。 */
  opening: boolean
  closing: boolean
  onContinue: () => void
  onClose: () => void
}) {
  const exporter = useExportResults(pkg.review.name)
  const reveal = useMutation({
    mutationFn: () => revealInFolder("folder"),
    onError: (error) =>
      toast.error("无法打开文件夹", { description: error.message }),
  })
  const { counts } = pkg
  return (
    <Card>
      <CardHeader>
        <CardDescription>正在审图</CardDescription>
        <CardTitle className="text-lg break-all">{pkg.review.name}</CardTitle>
        <CardDescription className="font-mono text-xs break-all">
          {pkg.path}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        <div className="flex flex-wrap gap-2">
          <Stat label="待审核" value={counts.pending} />
          <Stat label="已审核" value={counts.approved} />
          <Stat label="需加样" value={counts.needs_sample} />
          <Stat label="改过标注" value={counts.edited} />
        </div>
        <p className="text-muted-foreground">
          {progressSummary(counts)} · 模型 {pkg.model.project_name} v
          {pkg.model.version} · {pkg.datasets.join("、")}
        </p>
        {pkg.missing_images > 0 && (
          <p className="text-amber-600 dark:text-amber-400">
            {formatNumber(pkg.missing_images)}{" "}
            张图片的文件不在审图包中（下载时缺失或被移走），只能查看和修改它们的审核状态与标注。
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {pkg.last_export
            ? `上次导出：${formatDateTime(pkg.last_export.exported_at)}（${pkg.last_export.file_name}）`
            : "还没有导出过审图结果。"}
        </p>
        {opening && (
          <p className="text-amber-600 dark:text-amber-400">
            正在打开另一个审图包，打开后这个审图包会被关闭；取消打开可以继续审它。
          </p>
        )}
      </CardContent>
      <CardFooter className="flex-wrap gap-2">
        <Button onClick={onContinue} disabled={opening}>
          继续审图
          <ArrowRight />
        </Button>
        <Button
          variant="outline"
          onClick={exporter.run}
          disabled={exporter.pending}
        >
          <FileArchive />
          导出审图结果
        </Button>
        <Button
          variant="ghost"
          onClick={() => reveal.mutate()}
          disabled={reveal.isPending}
        >
          <FolderOpen />
          打开文件夹
        </Button>
        <Button
          variant="ghost"
          className="sm:ml-auto"
          onClick={onClose}
          disabled={closing}
        >
          <X />
          关闭审图包
        </Button>
      </CardFooter>
      {exporter.dialog}
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-24 rounded-md border px-3 py-2">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="text-lg font-semibold tabular-nums">
        {formatNumber(value)}
      </p>
    </div>
  )
}

function OpenCard({
  hasPackage,
  busy,
  onOpen,
}: {
  hasPackage: boolean
  busy: boolean
  onOpen: () => void
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">
          {hasPackage ? "打开其他审图包" : "打开审图包"}
        </CardTitle>
        <CardDescription>
          在本机离线审图：审核模型的识别结果，修改标注框，标记“已审核”或“需加样”。
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <ol className="grid gap-3 text-sm sm:grid-cols-3">
          <Step n={1} title="下载审图包">
            在平台的审图任务页面点“下载审图包”，得到一个 .zip 文件。
          </Step>
          <Step n={2} title="在这里审图">
            打开审图包后逐张审核。修改随时自动保存在审图包文件夹中，关掉程序下次打开会继续。
          </Step>
          <Step n={3} title="导出并上传">
            点“导出审图结果”，再到平台的审图任务页面点“上传审图结果”上传导出的文件。
          </Step>
        </ol>
        <div className="flex flex-wrap items-center gap-3">
          <Button size="lg" onClick={onOpen} disabled={busy}>
            <FolderOpen />
            打开审图包…
          </Button>
          <p className="text-xs text-muted-foreground">
            也可以把审图包（.zip）拖到本地审图程序的图标上直接打开。
          </p>
        </div>
      </CardContent>
    </Card>
  )
}

function Step({
  n,
  title,
  children,
}: {
  n: number
  title: string
  children: React.ReactNode
}) {
  return (
    <li className="flex gap-3 rounded-md bg-muted/50 p-3">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">
        {n}
      </span>
      <div className="grid gap-1">
        <p className="font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{children}</p>
      </div>
    </li>
  )
}

function RecentList({
  recent,
  current,
  busy,
  onOpen,
}: {
  recent: RecentPackage[]
  current: string | null
  busy: boolean
  onOpen: (path: string) => void
}) {
  const queryClient = useQueryClient()
  const remove = useMutation({
    mutationFn: removeRecent,
    onSuccess: () => refreshSession(queryClient),
    onError: (error) => toast.error("无法移除", { description: error.message }),
  })
  const items = recent.filter((item) => item.path !== current)
  if (items.length === 0) {
    return null
  }
  return (
    <section aria-labelledby="recent-title" className="grid grid-cols-1 gap-2">
      <h2 id="recent-title" className="text-sm font-medium">
        最近打开
      </h2>
      <ul className="divide-y rounded-lg border">
        {items.map((item) => (
          <li key={item.path} className="flex items-center gap-3 p-3">
            <div className="min-w-0 flex-1">
              <p className="flex items-center gap-2 text-sm font-medium">
                <span className="truncate">{item.review_name}</span>
                {!item.exists && (
                  <Badge variant="outline" className="shrink-0">
                    文件夹已不存在
                  </Badge>
                )}
              </p>
              {/* 路径太长时省略开头，留下文件夹的名字 */}
              <p
                dir="rtl"
                className="truncate text-left font-mono text-xs text-muted-foreground"
                title={item.path}
              >
                <bdi>{item.path}</bdi>
              </p>
              <p className="text-xs text-muted-foreground">
                {progressSummary(item.counts)} ·{" "}
                {formatRelativeTime(item.opened_at)}打开
              </p>
            </div>
            <Button
              variant="outline"
              size="sm"
              disabled={!item.exists || busy}
              onClick={() => onOpen(item.path)}
            >
              打开
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`从列表中移除 ${item.review_name}`}
              title="从列表中移除（不删除文件）"
              disabled={remove.isPending}
              onClick={() => remove.mutate(item.path)}
            >
              <X />
            </Button>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** 窗口 / 标签页的标题。 */
export function usePageTitle(title: string) {
  useEffect(() => {
    document.title = title
  }, [title])
}
