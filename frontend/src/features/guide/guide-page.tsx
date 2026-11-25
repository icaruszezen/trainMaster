import { useEffect, useMemo, useState } from "react"
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  BookOpen,
  Info,
  Keyboard,
  Lightbulb,
  List,
  Search,
  SearchX,
  X,
} from "lucide-react"
import { Link, useLocation, useParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Separator } from "@/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { getNavItem } from "@/config/navigation"
import { AnnotatedScreenshot } from "@/features/guide/annotated-screenshot"
import {
  guideChapters,
  guideGroups,
  getGuideChapter,
} from "@/features/guide/chapters"
import {
  DEFAULT_GUIDE_CHAPTER,
  GUIDE_PATH,
  guideChapterPath,
  isGuideChapterId,
} from "@/features/guide/paths"
import {
  searchGuideChapters,
  type GuideSearchMatch,
} from "@/features/guide/search"
import type {
  GuideChapter,
  GuideChapterId,
  GuideSection,
  GuideTip,
} from "@/features/guide/types"
import { usePageTitle } from "@/hooks/use-page-title"
import { cn } from "@/lib/utils"

const WORKFLOW_STAGES: {
  step: string
  title: string
  desc: string
  chapterId: GuideChapterId
}[] = [
  {
    step: "01",
    title: "建项目与导数据",
    desc: "定义 YOLO 类别顺序，批量导入图片与 VOC/YOLO/X-AnyLabeling 标注",
    chapterId: "datasets",
  },
  {
    step: "02",
    title: "在线标注与审图",
    desc: "在标注工作台画框，或用已有模型跑审图筛查难例并合并审图版本",
    chapterId: "annotation",
  },
  {
    step: "03",
    title: "冻结数据版本",
    desc: "叠加基础标注与审图版本，稳定划分 train/val/test 并按需离线增强",
    chapterId: "data-versions",
  },
  {
    step: "04",
    title: "发起模型训练",
    desc: "加载参数模板启动 YOLO 训练，实时追踪 Loss/mAP 曲线与训练日志",
    chapterId: "training-jobs",
  },
  {
    step: "05",
    title: "模型评估与对比",
    desc: "实时拖动 Conf/IoU 阈值看漏杀过杀，下钻混淆矩阵与多模型逐图对比",
    chapterId: "evaluations",
  },
  {
    step: "06",
    title: "仓库发布与导出",
    desc: "管理项目版本迭代，下载原始 PT 或由训练节点自动转换缓存 ONNX",
    chapterId: "models",
  },
]

export function GuidePage() {
  const { chapterId: rawChapterId } = useParams()
  const { hash } = useLocation()
  const [query, setQuery] = useState("")
  const [drawerOpen, setDrawerOpen] = useState(false)

  const resolvedId: GuideChapterId | null =
    rawChapterId === undefined
      ? DEFAULT_GUIDE_CHAPTER
      : isGuideChapterId(rawChapterId)
        ? rawChapterId
        : null

  const chapter = resolvedId ? getGuideChapter(resolvedId) : undefined

  usePageTitle(
    rawChapterId === undefined
      ? "操作指南"
      : chapter
        ? chapter.shortTitle
        : "章节不存在"
  )

  const matches = useMemo(
    () => searchGuideChapters(guideChapters, query),
    [query]
  )

  // 切换章节或带锚点跳转时定位到对应位置。
  useEffect(() => {
    if (hash) {
      const targetId = hash.slice(1)
      const el = document.getElementById(targetId)
      if (el) {
        el.scrollIntoView({ behavior: "smooth", block: "start" })
        return
      }
    }
    window.scrollTo({ top: 0, behavior: "instant" })
  }, [resolvedId, hash])

  if (!chapter) {
    return (
      <Empty className="min-h-[60svh]">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <BookOpen />
          </EmptyMedia>
          <EmptyTitle>操作指南章节不存在</EmptyTitle>
          <EmptyDescription>
            链接中的章节标识无效，请从操作指南目录重新选择。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button asChild>
            <Link to={GUIDE_PATH}>
              <ArrowLeft />
              返回操作指南首页
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  }

  const chapterIndex = guideChapters.findIndex((c) => c.id === chapter.id)
  const prevChapter = chapterIndex > 0 ? guideChapters[chapterIndex - 1] : null
  const nextChapter =
    chapterIndex >= 0 && chapterIndex < guideChapters.length - 1
      ? guideChapters[chapterIndex + 1]
      : null
  const currentGroup = guideGroups.find((g) => g.id === chapter.groupId)

  return (
    <div className="space-y-6">
      <PageHeader
        title="操作指南"
        description="按工业视觉质检工作流整理的界面操作手册，涵盖全部 13 个已上线模块，配真实界面截图与交互式编号标注。"
        actions={
          <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
            <SheetTrigger asChild>
              <Button variant="outline" size="sm" className="lg:hidden">
                <List className="size-4" />
                章节目录
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="w-80 p-4">
              <SheetHeader className="px-0 pb-2 text-left">
                <SheetTitle>操作指南目录</SheetTitle>
                <SheetDescription>
                  共 {guideChapters.length} 个章节，支持关键词搜索快速定位。
                </SheetDescription>
              </SheetHeader>
              <GuideDirectory
                query={query}
                onQueryChange={setQuery}
                matches={matches}
                activeChapterId={chapter.id}
                onSelect={() => setDrawerOpen(false)}
              />
            </SheetContent>
          </Sheet>
        }
      />

      <div className="grid items-start gap-6 lg:grid-cols-[264px_minmax(0,1fr)] xl:grid-cols-[280px_minmax(0,1fr)]">
        <aside
          aria-label="操作指南章节目录"
          className="hidden lg:sticky lg:top-20 lg:flex lg:max-h-[calc(100svh-6.5rem)] lg:flex-col lg:rounded-xl lg:border lg:bg-card lg:p-3"
        >
          <GuideDirectory
            query={query}
            onQueryChange={setQuery}
            matches={matches}
            activeChapterId={chapter.id}
          />
        </aside>

        <article className="min-w-0 space-y-8">
          <header className="space-y-4 rounded-xl border bg-card p-5 shadow-2xs sm:p-6">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="min-w-0 space-y-2">
                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <Badge variant="secondary">
                    {currentGroup?.title ?? "操作指南"}
                  </Badge>
                  <span className="tabular-nums">
                    第 {String(chapterIndex + 1).padStart(2, "0")} /{" "}
                    {String(guideChapters.length).padStart(2, "0")} 章
                  </span>
                </div>
                <h2 className="text-xl font-semibold tracking-tight sm:text-2xl">
                  {chapter.title}
                </h2>
                <p className="max-w-3xl text-sm leading-relaxed text-muted-foreground">
                  {chapter.summary}
                </p>
              </div>

              <Button asChild size="sm" className="shrink-0">
                <Link to={chapter.targetPath}>
                  前往「{chapter.targetLabel}」
                  <ArrowRight className="size-4" />
                </Link>
              </Button>
            </div>

            {chapter.sections.length > 1 && (
              <>
                <Separator />
                <nav
                  aria-label="本页小节导航"
                  className="flex flex-wrap items-center gap-1.5"
                >
                  <span className="mr-1 text-xs font-medium text-muted-foreground">
                    本页小节：
                  </span>
                  {chapter.sections.map((section, idx) => (
                    <a
                      key={section.id}
                      href={`#${section.id}`}
                      className="inline-flex items-center gap-1 rounded-md border border-transparent bg-muted/60 px-2.5 py-1 text-xs text-foreground transition-colors hover:border-border hover:bg-accent"
                    >
                      <span className="text-[11px] text-muted-foreground tabular-nums">
                        {idx + 1}.
                      </span>
                      {section.title}
                    </a>
                  ))}
                </nav>
              </>
            )}
          </header>

          {chapter.id === "quickstart" && <QuickWorkflowStrip />}

          <div className="space-y-10">
            {chapter.sections.map((section, index) => (
              <GuideSectionView
                key={section.id}
                section={section}
                index={index + 1}
              />
            ))}
          </div>

          <ChapterPagination prev={prevChapter} next={nextChapter} />
        </article>
      </div>
    </div>
  )
}

function GuideDirectory({
  query,
  onQueryChange,
  matches,
  activeChapterId,
  onSelect,
}: {
  query: string
  onQueryChange: (value: string) => void
  matches: GuideSearchMatch[]
  activeChapterId: GuideChapterId
  onSelect?: () => void
}) {
  const hasQuery = query.trim().length > 0
  const matchMap = useMemo(
    () => new Map(matches.map((m) => [m.chapter.id, m])),
    [matches]
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          placeholder="搜索章节、按钮或快捷键…"
          aria-label="搜索操作指南"
          className="h-8 pr-7 pl-8 text-xs"
        />
        {hasQuery && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            onClick={() => onQueryChange("")}
            aria-label="清空搜索"
            className="absolute top-1/2 right-1 -translate-y-1/2 text-muted-foreground"
          >
            <X className="size-3.5" />
          </Button>
        )}
      </div>

      {hasQuery && (
        <div className="flex items-center justify-between px-1 text-[11px] text-muted-foreground">
          <span>匹配 {matches.length} 个章节</span>
          <button
            type="button"
            onClick={() => onQueryChange("")}
            className="text-foreground underline-offset-4 hover:underline"
          >
            重置
          </button>
        </div>
      )}

      <nav className="min-h-0 flex-1 scrollbar-auto-hide space-y-4 overflow-y-auto pr-1">
        {matches.length === 0 ? (
          <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-3 py-8 text-center">
            <SearchX className="size-5 text-muted-foreground" />
            <p className="text-xs text-muted-foreground">
              未找到与“{query.trim()}”相关的章节
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 text-xs"
              onClick={() => onQueryChange("")}
            >
              清空搜索
            </Button>
          </div>
        ) : (
          guideGroups.map((group) => {
            const items = guideChapters
              .filter((c) => c.groupId === group.id && matchMap.has(c.id))
              .map((c) => matchMap.get(c.id)!)
            if (items.length === 0) {
              return null
            }
            return (
              <div key={group.id} className="space-y-1">
                <div className="px-2 text-[11px] font-medium text-muted-foreground">
                  {group.title}
                </div>
                <ul className="space-y-0.5">
                  {items.map(
                    ({ chapter, matchedSectionId, matchedSectionTitle }) => {
                      const navItem = getNavItem(chapter.moduleId)
                      const Icon = navItem.icon
                      const isActive = chapter.id === activeChapterId
                      const to =
                        hasQuery && matchedSectionId
                          ? `${guideChapterPath(chapter.id)}#${matchedSectionId}`
                          : guideChapterPath(chapter.id)
                      return (
                        <li key={chapter.id}>
                          <Link
                            to={to}
                            onClick={onSelect}
                            aria-current={isActive ? "page" : undefined}
                            className={cn(
                              "flex items-start gap-2 rounded-lg px-2 py-1.5 text-sm transition-colors",
                              isActive
                                ? "bg-accent font-medium text-accent-foreground"
                                : "text-muted-foreground hover:bg-accent/60 hover:text-foreground"
                            )}
                          >
                            <Icon className="mt-0.5 size-4 shrink-0" />
                            <span className="min-w-0 flex-1">
                              <span className="block truncate leading-snug">
                                {chapter.shortTitle}
                              </span>
                              {hasQuery && matchedSectionTitle && (
                                <span className="block truncate text-[11px] font-normal text-muted-foreground">
                                  命中：{matchedSectionTitle}
                                </span>
                              )}
                            </span>
                          </Link>
                        </li>
                      )
                    }
                  )}
                </ul>
              </div>
            )
          })
        )}
      </nav>
    </div>
  )
}

function QuickWorkflowStrip() {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">端到端工业视觉闭环路线</CardTitle>
        <CardDescription>
          按顺序点击任意阶段，直接查看对应模块的图文操作指南
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {WORKFLOW_STAGES.map((stage) => (
            <li key={stage.step}>
              <Link
                to={guideChapterPath(stage.chapterId)}
                className="group flex h-full flex-col gap-1.5 rounded-lg border bg-background p-3.5 transition-colors hover:border-foreground/25 hover:bg-accent/50"
              >
                <div className="flex items-center gap-2">
                  <span className="inline-flex h-5 items-center rounded bg-muted px-1.5 text-[11px] font-semibold text-muted-foreground tabular-nums">
                    {stage.step}
                  </span>
                  <span className="text-sm font-medium text-foreground">
                    {stage.title}
                  </span>
                  <ArrowRight className="ml-auto size-3.5 text-muted-foreground opacity-60 transition-transform group-hover:translate-x-0.5 group-hover:opacity-100" />
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {stage.desc}
                </p>
              </Link>
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  )
}

function GuideSectionView({
  section,
  index,
}: {
  section: GuideSection
  index: number
}) {
  return (
    <section id={section.id} className="scroll-mt-20 space-y-4">
      <div className="space-y-1">
        <h3 className="flex items-center gap-2 text-lg font-semibold tracking-tight">
          <span className="inline-flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground tabular-nums">
            {index}
          </span>
          {section.title}
        </h3>
        {section.summary && (
          <p className="text-sm leading-relaxed text-muted-foreground">
            {section.summary}
          </p>
        )}
      </div>

      {section.paragraphs && section.paragraphs.length > 0 && (
        <div className="space-y-2.5 text-sm leading-relaxed text-foreground/90">
          {section.paragraphs.map((p, i) => (
            <p key={i}>{p}</p>
          ))}
        </div>
      )}

      {section.figures && section.figures.length > 0 && (
        <div className="space-y-5">
          {section.figures.map((figure) => (
            <AnnotatedScreenshot key={figure.id} figure={figure} />
          ))}
        </div>
      )}

      {section.steps && section.steps.length > 0 && (
        <ol className="space-y-2.5">
          {section.steps.map((step, idx) => (
            <li
              key={step.title}
              className="flex items-start gap-3 rounded-lg border bg-card/60 p-3.5"
            >
              <span className="mt-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground tabular-nums">
                {idx + 1}
              </span>
              <div className="min-w-0 flex-1 space-y-1">
                <div className="text-sm font-medium text-foreground">
                  {step.title}
                </div>
                <p className="text-sm leading-relaxed text-muted-foreground">
                  {step.description}
                </p>
                {step.tip && (
                  <p className="pt-0.5 text-xs text-muted-foreground">
                    提示：{step.tip}
                  </p>
                )}
              </div>
            </li>
          ))}
        </ol>
      )}

      {section.table && (
        <div className="overflow-hidden rounded-lg border bg-card">
          {section.table.caption && (
            <div className="border-b bg-muted/40 px-4 py-2 text-xs font-medium text-muted-foreground">
              {section.table.caption}
            </div>
          )}
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  {section.table.headers.map((header) => (
                    <TableHead key={header} className="whitespace-nowrap">
                      {header}
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {section.table.rows.map((row, rowIdx) => (
                  <TableRow key={rowIdx}>
                    {row.map((cell, cellIdx) => (
                      <TableCell
                        key={cellIdx}
                        className={cn(
                          "align-top text-sm whitespace-normal",
                          cellIdx === 0 && "font-medium whitespace-nowrap"
                        )}
                      >
                        {cell}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      )}

      {section.shortcuts && section.shortcuts.length > 0 && (
        <div className="grid gap-4 md:grid-cols-2">
          {section.shortcuts.map((group) => (
            <Card key={group.title} className="gap-3 py-4">
              <CardHeader className="px-4 py-0">
                <CardTitle className="flex items-center gap-1.5 text-sm">
                  <Keyboard className="size-3.5 text-muted-foreground" />
                  {group.title}
                </CardTitle>
              </CardHeader>
              <CardContent className="px-4 py-0">
                <dl className="divide-y">
                  {group.items.map((item) => (
                    <div
                      key={item.description}
                      className="flex items-start justify-between gap-3 py-2 text-xs first:pt-0 last:pb-0"
                    >
                      <dt className="flex shrink-0 flex-wrap gap-1">
                        {item.keys.map((k) => (
                          <Kbd key={k}>{k}</Kbd>
                        ))}
                      </dt>
                      <dd className="text-right leading-relaxed text-muted-foreground">
                        {item.description}
                      </dd>
                    </div>
                  ))}
                </dl>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {section.tips && section.tips.length > 0 && (
        <div className="space-y-2.5">
          {section.tips.map((tip) => (
            <TipCallout key={tip.title} tip={tip} />
          ))}
        </div>
      )}
    </section>
  )
}

function TipCallout({ tip }: { tip: GuideTip }) {
  const styles =
    tip.variant === "warning"
      ? {
          box: "border-amber-500/35 bg-amber-50/70 dark:border-amber-400/30 dark:bg-amber-950/30",
          icon: AlertTriangle,
          iconColor: "text-amber-600 dark:text-amber-400",
        }
      : tip.variant === "tip"
        ? {
            box: "border-emerald-500/35 bg-emerald-50/65 dark:border-emerald-400/30 dark:bg-emerald-950/30",
            icon: Lightbulb,
            iconColor: "text-emerald-600 dark:text-emerald-400",
          }
        : {
            box: "border-border bg-muted/45",
            icon: Info,
            iconColor: "text-muted-foreground",
          }

  const Icon = styles.icon
  return (
    <div
      className={cn(
        "flex items-start gap-3 rounded-lg border px-4 py-3 text-sm",
        styles.box
      )}
    >
      <Icon className={cn("mt-0.5 size-4 shrink-0", styles.iconColor)} />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="font-medium text-foreground">{tip.title}</div>
        <p className="text-xs leading-relaxed text-muted-foreground sm:text-sm">
          {tip.content}
        </p>
      </div>
    </div>
  )
}

function ChapterPagination({
  prev,
  next,
}: {
  prev: GuideChapter | null
  next: GuideChapter | null
}) {
  return (
    <nav
      aria-label="章节切换"
      className="grid gap-3 border-t pt-6 sm:grid-cols-2"
    >
      {prev ? (
        <Link
          to={guideChapterPath(prev.id)}
          className="group flex items-center gap-3 rounded-xl border bg-card p-4 transition-colors hover:bg-accent/60"
        >
          <ArrowLeft className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:-translate-x-0.5" />
          <div className="min-w-0">
            <div className="text-xs text-muted-foreground">上一章</div>
            <div className="truncate text-sm font-medium text-foreground">
              {prev.title}
            </div>
          </div>
        </Link>
      ) : (
        <div />
      )}

      {next ? (
        <Link
          to={guideChapterPath(next.id)}
          className="group flex items-center justify-end gap-3 rounded-xl border bg-card p-4 text-right transition-colors hover:bg-accent/60"
        >
          <div className="min-w-0">
            <div className="text-xs text-muted-foreground">下一章</div>
            <div className="truncate text-sm font-medium text-foreground">
              {next.title}
            </div>
          </div>
          <ArrowRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
        </Link>
      ) : (
        <div />
      )}
    </nav>
  )
}
