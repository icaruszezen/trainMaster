/**
 * 生成数据版本（`/datasets/versions/new?project=<id>&from=<versionId>`）：选择
 * 项目、数据集与标注来源、划分、过滤与增强，右侧（手机为底部）实时预览。
 *
 * 默认以项目最新的数据版本为模板（`?from=` 指定模板，`?from=none` 不用模板）：
 * 沿用它的来源、比例、随机种子、分组、过滤与增强，新的审图版本单独标出。
 * 预览请求防抖 300 毫秒，设置无效时不发送；创建时带上预览返回的类别列表版本，
 * 类别列表在此之后被修改时服务端返回 409。
 */
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type Ref,
} from "react"
import {
  useMutation,
  useQueries,
  useQuery,
  type UseQueryResult,
} from "@tanstack/react-query"
import {
  ArrowLeft,
  ChevronDown,
  CircleAlert,
  FolderKanban,
  FolderX,
  Info,
  Pencil,
  Plus,
  RefreshCw,
  Shapes,
  Shuffle,
  Sparkles,
  TriangleAlert,
} from "lucide-react"
import { Link, useNavigate, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  datasetKeys,
  datasetVersionsQuery,
  type Dataset,
  type DatasetVersion,
} from "@/features/datasets/api"
import { ClassIdPreview } from "@/features/datasets/class-list-field"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import { datasetStatusLabels } from "@/features/datasets/dataset-status"
import { DatasetStatusBadge } from "@/features/datasets/dataset-status-picker"
import { unannotatedCount } from "@/features/datasets/images"
import { LoadError } from "@/features/datasets/load-error"
import { versionCountsText } from "@/features/datasets/versions"
import {
  createDataVersion,
  dataVersionPreviewQuery,
  dataVersionQuery,
  dataVersionsQuery,
  type AugmentMethod,
  type CreateDataVersionInput,
  type DataVersion,
  type DataVersionPreview,
  type DataVersionPreviewInput,
  type GroupBy,
  type TrainingScope,
} from "@/features/data-versions/api"
import {
  AUGMENT_METHODS,
  copiesLimit,
  maxCopies,
} from "@/features/data-versions/augmentations"
import { watchDataVersionBuild } from "@/features/data-versions/build-watch"
import { cacheDataVersion } from "@/features/data-versions/data-version-actions"
import {
  blockingWarning,
  buildPreviewInput,
  CUSTOM_RATIO,
  defaultUnlistedChoice,
  effectiveCopies,
  effectiveScope,
  effectiveSelection,
  fieldOfServerError,
  ignoreLabels,
  initialDraft,
  isClassListConflict,
  isNewReviewVersion,
  isSelected,
  mergeKnownUnlisted,
  missingTemplateReviewVersions,
  missingTemplateSources,
  NO_SOURCE,
  noteError,
  notInTemplate,
  parseRatio,
  previewWarnings,
  readyReviewVersions,
  scopeOverride,
  selectionOnScopeChange,
  selectionToInclude,
  sourceOverride,
  sourceDisabledReason,
  sourceInputs,
  toggleVersion,
  type FormDraft,
  type KnownUnlisted,
  type PreviewWarning,
  type ScopeOverride,
  type SourceChange,
  type SourceSelection,
  type UnlistedChoice,
} from "@/features/data-versions/form-logic"
import {
  dataVersionFullTitle,
  dataVersionTitle,
  GROUP_BY_DESCRIPTIONS,
  GROUP_BY_LABELS,
  parseSeed,
  previewSourceText,
  queueText,
  randomSeed,
  RATIO_PRESETS,
  ratioPresetLabel,
  selectionHint,
  TRAINING_SCOPE_HINTS,
  TRAINING_SCOPE_LABELS,
  TRAINING_SCOPES,
} from "@/features/data-versions/format"
import {
  dataVersionDetailPath,
  dataVersionsPath,
  parsePositiveId,
  projectClassesPath,
  projectDatasetsPath,
} from "@/features/data-versions/paths"
import {
  PreviewBar,
  PreviewPanel,
  type PreviewState,
} from "@/features/data-versions/preview-panel"
import { latestDataVersion } from "@/features/data-versions/project-data-versions"
import { useBuildWatchNavigate } from "@/features/data-versions/use-build-watch-navigate"
import {
  projectClassesQuery,
  projectDatasetsQuery,
  projectKeys,
  projectQuery,
  projectsQuery,
  type ProjectClassList,
} from "@/features/projects/api"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError, type ListResponse } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

/** 预览查询在没有有效输入时用的占位输入（enabled 为 false，不会发送）。 */
const PLACEHOLDER_INPUT: DataVersionPreviewInput = {
  project_id: 0,
  sources: [],
  split: {
    train: 80,
    val: 20,
    test: 0,
    seed: 0,
    use_folders: true,
    group_by: "stem",
  },
  scope: "annotated",
  include_empty: true,
  ignore_labels: [],
  augmentation: null,
}

export function NewDataVersionPage() {
  usePageTitle("生成数据版本")
  useBuildWatchNavigate()
  const [params, setParams] = useSearchParams()
  const projects = useQuery(projectsQuery())

  const fromParam = params.get("from")
  const noTemplate = fromParam === "none"
  const fromId = noTemplate ? null : parsePositiveId(fromParam)
  const from = useQuery({
    ...dataVersionQuery(fromId ?? 0),
    enabled: fromId !== null,
  })
  const projectParam = parsePositiveId(params.get("project"))
  // 只带模板（?from=）时，项目就是模板的项目。
  const projectId = projectParam ?? from.data?.project_id ?? null

  const selectProject = (id: number) =>
    setParams(new URLSearchParams({ project: String(id) }), { replace: true })
  const dropTemplate = () =>
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        if (projectId !== null) {
          next.set("project", String(projectId))
        }
        next.set("from", "none")
        return next
      },
      { replace: true }
    )

  let content: ReactNode
  if (projectId === null && fromId !== null && from.isPending) {
    content = <FormSkeleton />
  } else if (projectId === null) {
    content = (
      <ChooseProject
        noProjects={projects.data?.items.length === 0}
        fromMissing={fromId !== null && from.isError}
      />
    )
  } else {
    content = (
      <ProjectForm
        // 换项目或模板时表单从头开始。
        key={`${projectId}:${fromParam ?? ""}`}
        projectId={projectId}
        fromId={fromId}
        noTemplate={noTemplate}
        onDropTemplate={dropTemplate}
      />
    )
  }

  return (
    <>
      <PageHeader
        title="生成数据版本"
        description="从项目的数据集生成冻结的训练数据：类别编号按项目的类别列表，图片按固定的随机种子划分训练 / 验证 / 测试集，可选离线增强。生成后不再变化，可以直接训练或下载。"
        actions={
          <Button variant="outline" asChild>
            <Link to={dataVersionsPath(projectId)}>
              <ArrowLeft />
              返回数据版本
            </Link>
          </Button>
        }
      />
      <Card className="gap-3 py-4">
        <CardHeader className="px-4 sm:px-6">
          <CardTitle className="text-base">项目</CardTitle>
          <CardDescription>
            数据版本属于一个项目：使用项目的类别列表与项目中的数据集，版本号在项目内递增（v1、v2…）。
          </CardDescription>
        </CardHeader>
        <CardContent className="px-4 sm:px-6">
          <ProjectSelect
            projects={projects.data?.items}
            loading={projects.isPending}
            value={projectId}
            onChange={selectProject}
          />
        </CardContent>
      </Card>
      {content}
    </>
  )
}

function ProjectSelect({
  projects,
  loading,
  value,
  onChange,
}: {
  projects: { id: number; name: string; dataset_count: number }[] | undefined
  loading: boolean
  value: number | null
  onChange: (id: number) => void
}) {
  const sorted = useMemo(
    () =>
      [...(projects ?? [])].sort((a, b) =>
        nameCollator.compare(a.name, b.name)
      ),
    [projects]
  )
  const pending =
    value !== null && !sorted.some((project) => project.id === value)
      ? value
      : null
  return (
    <Select
      value={value === null ? undefined : String(value)}
      onValueChange={(next) => onChange(Number(next))}
    >
      <SelectTrigger className="w-full sm:w-80" aria-label="项目">
        <FolderKanban className="text-muted-foreground" />
        <SelectValue placeholder={loading ? "正在加载项目…" : "选择项目"} />
      </SelectTrigger>
      <SelectContent>
        {pending !== null && (
          <SelectItem value={String(pending)}>
            {loading ? "加载中…" : `项目 #${pending}`}
          </SelectItem>
        )}
        {sorted.map((project) => (
          <SelectItem key={project.id} value={String(project.id)}>
            <span className="max-w-56 truncate">{project.name}</span>
            <span className="text-muted-foreground tabular-nums">
              {formatNumber(project.dataset_count)} 个数据集
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function ChooseProject({
  noProjects,
  fromMissing,
}: {
  noProjects: boolean
  fromMissing: boolean
}) {
  return (
    <Empty className="border">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <FolderKanban />
        </EmptyMedia>
        <EmptyTitle className="text-base">
          {noProjects ? "还没有项目" : "选择一个项目"}
        </EmptyTitle>
        <EmptyDescription>
          {fromMissing
            ? "作为模板的数据版本不存在（可能已被删除），请选择项目后重新设置。"
            : noProjects
              ? "数据版本属于项目：先新建一个项目，把数据集加入项目并设置类别列表。"
              : "在上方选择项目后，设置数据集、划分与过滤。"}
        </EmptyDescription>
      </EmptyHeader>
      {noProjects && (
        <EmptyContent>
          <Button asChild>
            <Link to="/projects">
              <Plus />
              新建项目
            </Link>
          </Button>
        </EmptyContent>
      )}
    </Empty>
  )
}

/** 模板的来源：?from= 指定的、项目最新的，或没有。 */
type TemplateKind = "from" | "latest"

/** 读取项目、数据集、类别列表与模板，都读到后再显示表单（模板决定初始值）。 */
function ProjectForm({
  projectId,
  fromId,
  noTemplate,
  onDropTemplate,
}: {
  projectId: number
  fromId: number | null
  noTemplate: boolean
  onDropTemplate: () => void
}) {
  const project = useQuery(projectQuery(projectId))
  const datasets = useQuery(projectDatasetsQuery(projectId))
  const classes = useQuery(projectClassesQuery(projectId))
  const from = useQuery({
    ...dataVersionQuery(fromId ?? 0),
    enabled: fromId !== null,
  })
  const latest = useQuery({
    ...dataVersionsQuery(projectId),
    enabled: fromId === null && !noTemplate,
  })

  if (
    project.isPending ||
    datasets.isPending ||
    classes.isPending ||
    (fromId !== null && from.isPending) ||
    (fromId === null && !noTemplate && latest.isPending)
  ) {
    return <FormSkeleton />
  }
  if (project.error instanceof ApiError && project.error.status === 404) {
    return <ProjectMissing />
  }
  for (const query of [project, datasets, classes] as const) {
    if (query.data === undefined) {
      return (
        <LoadError
          title="无法读取项目"
          error={query.error ?? new Error("加载失败")}
          retrying={query.isFetching}
          onRetry={() => void query.refetch()}
        />
      )
    }
  }
  if (!project.data || !datasets.data || !classes.data) {
    return null
  }
  if (classes.data.items.length === 0) {
    return <NoClasses projectId={projectId} projectName={project.data.name} />
  }

  let template: DataVersion | null = null
  let kind: TemplateKind | null = null
  let templateNote: string | null = null
  if (fromId !== null) {
    if (from.data && from.data.project_id === projectId) {
      template = from.data
      kind = "from"
    } else if (from.data) {
      templateNote = `作为模板的“${dataVersionFullTitle(from.data)}”属于另一个项目，没有使用。`
    } else if (from.error instanceof ApiError && from.error.status === 404) {
      templateNote =
        "作为模板的数据版本不存在（可能已被删除），已使用默认设置。"
    } else {
      templateNote = `无法读取作为模板的数据版本（${from.error?.message ?? "未知错误"}），已使用默认设置。`
    }
  } else if (!noTemplate && latest.data) {
    template = latestDataVersion(latest.data.items)
    kind = template ? "latest" : null
  }

  return (
    <NewDataVersionForm
      projectId={projectId}
      projectName={project.data.name}
      datasets={datasets.data.items}
      classList={classes.data}
      initialTemplate={template}
      templateKind={kind}
      templateNote={templateNote}
      onDropTemplate={onDropTemplate}
    />
  )
}

function ProjectMissing() {
  return (
    <Empty className="border">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FolderX />
        </EmptyMedia>
        <EmptyTitle className="text-base">项目不存在</EmptyTitle>
        <EmptyDescription>
          该项目可能已被删除，或者链接有误。请在上方选择其他项目。
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

function NoClasses({
  projectId,
  projectName,
}: {
  projectId: number
  projectName: string
}) {
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <Shapes />
        </EmptyMedia>
        <EmptyTitle className="text-base">
          项目“{projectName}”还没有类别
        </EmptyTitle>
        <EmptyDescription>
          数据版本会冻结项目的类别列表，列表的顺序就是训练时的类别编号（第一个为
          0）。请先在项目中设置类别（可以从项目中数据集的标签生成），再生成数据版本。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to={projectClassesPath(projectId)}>
            <Pencil />
            去项目中设置类别
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

type SourceRowData = {
  dataset: Dataset
  query: UseQueryResult<ListResponse<DatasetVersion>>
  disabled: string | null
  versions: DatasetVersion[]
  /** 有正在生成的审图版本（完成后才能选择）。 */
  building: boolean
  selection: SourceSelection
  /** 单独设置的参与训练的图片，`""` 使用默认的。 */
  scopeSetting: ScopeOverride
  /** 实际使用的参与训练的图片。 */
  scope: TrainingScope
}

type ServerError = {
  field: ReturnType<typeof fieldOfServerError> | "classes"
  message: string
}

function NewDataVersionForm({
  projectId,
  projectName,
  datasets,
  classList,
  initialTemplate,
  templateKind,
  templateNote,
  onDropTemplate,
}: {
  projectId: number
  projectName: string
  datasets: Dataset[]
  classList: ProjectClassList
  initialTemplate: DataVersion | null
  templateKind: TemplateKind | null
  templateNote: string | null
  onDropTemplate: () => void
}) {
  const navigate = useNavigate()
  const formId = useId()
  const previewId = useId()
  const noteRef = useRef<HTMLTextAreaElement>(null)
  // 模板在打开页面时确定，之后（如最新版本变化）不再改变。
  const [template] = useState(initialTemplate)
  const [draft, setDraft] = useState(() => initialDraft(initialTemplate))
  const [known, setKnown] = useState<KnownUnlisted>({})
  const [lastPreview, setLastPreview] = useState<DataVersionPreview>()
  const [submitted, setSubmitted] = useState(false)
  const [serverError, setServerError] = useState<ServerError | null>(null)

  const versionQueries = useQueries({
    queries: datasets.map((dataset) => ({
      ...datasetVersionsQuery(dataset.id),
      enabled: sourceDisabledReason(dataset) === null,
    })),
  })

  const rows: SourceRowData[] = datasets.map((dataset, index) => {
    const query = versionQueries[index]
    const versions = readyReviewVersions(query.data?.items ?? [])
    const scope = effectiveScope(draft, template, dataset.id)
    return {
      dataset,
      query,
      disabled: sourceDisabledReason(dataset),
      versions,
      building: query.data?.items.some((v) => v.status === "building") ?? false,
      selection: effectiveSelection({
        dataset,
        versions,
        template,
        override: draft.overrides[dataset.id],
        scope,
      }),
      scopeSetting: scopeOverride(draft, template, dataset.id),
      scope,
    }
  })
  const versionsLoaded = rows.every(
    (row) => row.disabled !== null || !row.query.isPending
  )
  const sources = sourceInputs(
    rows.map((row) => ({
      datasetId: row.dataset.id,
      selection: row.selection,
      scope: row.scopeSetting,
    }))
  )
  // 空标注的开关只影响“仅有标注的图片”的数据集。
  // 还没有选中数据集时按默认的设置。
  const selectedRows = rows.filter((row) => isSelected(row.selection))
  const annotatedSelected =
    selectedRows.length === 0
      ? draft.scope === "annotated"
      : selectedRows.some((row) => row.scope === "annotated")

  const classes = lastPreview?.classes ?? classList.items
  // 忽略的标签不能是类别：两份列表都算（项目的类别列表可能比上次的预览新）。
  const classNames = useMemo(
    () => new Set([...classes, ...classList.items].map((cls) => cls.name)),
    [classes, classList.items]
  )
  const templateIgnore = template?.config.ignore_labels ?? null
  const ignore = ignoreLabels({
    known,
    choices: draft.choices,
    templateIgnore,
    classNames,
  })
  const { input, errors } = buildPreviewInput({
    projectId,
    draft,
    sources,
    ignore,
  })
  const ratio = parseRatio(draft.ratio)
  const testPercent = ratio.ok ? ratio.test : 0

  // 防抖：比较序列化后的输入（对象每次渲染都是新的）。
  const inputKey = input && versionsLoaded ? JSON.stringify(input) : null
  const debouncedKey = useDebouncedValue(inputKey, 300)
  const previewInput = useMemo(
    () =>
      debouncedKey
        ? (JSON.parse(debouncedKey) as DataVersionPreviewInput)
        : null,
    [debouncedKey]
  )
  const preview = useQuery({
    ...dataVersionPreviewQuery(previewInput ?? PLACEHOLDER_INPUT),
    // 输入暂时无效时保持上一次的查询（不显示它），恢复有效时不重新请求旧的输入。
    enabled: previewInput !== null,
  })

  // 记住每次预览中不在类别列表中的标签（之后的预览中可能不再出现）。
  if (
    preview.data &&
    !preview.isPlaceholderData &&
    preview.data !== lastPreview
  ) {
    setLastPreview(preview.data)
    setKnown((previous) =>
      mergeKnownUnlisted(previous, preview.data.unlisted_labels)
    )
  }

  // 类别列表在打开页面后变了（预览中的版本不同，或预览说类别列表变了 / 为空）：
  // 刷新项目的类别列表，空列表时整个表单换成提示。
  const previewError = preview.error
  const classVersion = lastPreview?.class_version
  useEffect(() => {
    if (
      (classVersion !== undefined && classVersion !== classList.version) ||
      (previewError instanceof ApiError &&
        (isClassListConflict(previewError.message) ||
          (previewError.message.startsWith("忽略的标签“") &&
            previewError.message.endsWith("在类别列表中"))))
    ) {
      void queryClient.invalidateQueries({
        queryKey: projectKeys.classes(projectId),
      })
    }
  }, [classVersion, classList.version, previewError, projectId])

  const fresh = inputKey !== null && inputKey === debouncedKey
  let previewState: PreviewState
  if (!input) {
    previewState = {
      kind: "invalid",
      reasons: Object.values(errors).filter((e): e is string => !!e),
    }
  } else if (!versionsLoaded) {
    previewState = { kind: "loading" }
  } else if (preview.isError) {
    previewState = {
      kind: "error",
      error: preview.error,
      retrying: preview.isFetching,
      onRetry: () => void preview.refetch(),
    }
  } else if (preview.data && !preview.isPlaceholderData) {
    previewState = {
      kind: "ready",
      preview: preview.data,
      stale: !fresh || preview.isFetching,
    }
  } else {
    previewState = { kind: "loading" }
  }
  const shown = previewState.kind === "ready" ? previewState.preview : undefined
  const warnings: PreviewWarning[] = [
    ...(shown ? previewWarnings(shown, testPercent) : []),
    ...rows
      .filter((row) => isSelected(row.selection) && row.dataset.active_import)
      .map((row): PreviewWarning => ({
        key: `import:${row.dataset.id}`,
        text: `数据集“${row.dataset.name}”正在导入数据，生成时以那时的文件为准`,
        tone: "warning",
      })),
  ]

  const create = useMutation({
    mutationFn: (body: CreateDataVersionInput) => createDataVersion(body),
    onSuccess: (version) => {
      cacheDataVersion(version)
      watchDataVersionBuild(version)
      toast.success(`已开始生成“${dataVersionFullTitle(version)}”`, {
        description:
          version.status === "queued"
            ? `排队中（${queueText(version.queue_position)}），完成后会提示。`
            : "完成后会提示。",
      })
      navigate(dataVersionDetailPath(version.id))
    },
    onError: (error) => {
      const { message } = error
      const status = error instanceof ApiError ? error.status : 0
      if (status === 409 && isClassListConflict(message)) {
        void queryClient.invalidateQueries({
          queryKey: projectKeys.classes(projectId),
        })
        setServerError({ field: "classes", message })
        void preview.refetch()
      } else if (status === 422) {
        const field = fieldOfServerError(message)
        setServerError({ field, message })
        if (field === "sources") {
          // 数据集或审图版本可能刚被删除或移出项目。
          void queryClient.invalidateQueries({
            queryKey: projectKeys.datasets(projectId),
          })
          for (const row of rows) {
            void queryClient.invalidateQueries({
              queryKey: datasetKeys.versions(row.dataset.id),
            })
          }
        }
      } else {
        setServerError({ field: null, message })
        if (status === 409) {
          void preview.refetch()
        }
      }
      toast.error("无法生成数据版本", { description: message })
    },
  })

  const update = (changes: Partial<FormDraft>) => {
    setDraft((previous) => ({ ...previous, ...changes }))
    setServerError(null)
    create.reset()
  }
  const selectionArgs = (row: SourceRowData, scope = row.scope) => ({
    dataset: row.dataset,
    versions: row.versions,
    template,
    override: draft.overrides[row.dataset.id],
    scope,
  })
  const setSource = (
    row: SourceRowData,
    selection: SourceSelection,
    change: SourceChange
  ) => {
    const override = sourceOverride(selectionArgs(row), selection, change)
    setDraft((previous) => ({
      ...previous,
      overrides: { ...previous.overrides, [row.dataset.id]: override },
    }))
    setServerError(null)
    create.reset()
  }
  /**
   * 修改一个数据集参与训练的图片；取背景图时没有选中的数据集一并选中（审图
   * 版本还没有读到时不选：模板的来源还不完整）。
   */
  const setSourceScope = (row: SourceRowData, value: ScopeOverride) => {
    const args = selectionArgs(row, value || draft.scope)
    const include =
      row.disabled || row.query.data === undefined
        ? null
        : selectionOnScopeChange({
            ...args,
            selection: effectiveSelection(args),
          })
    setDraft((previous) => ({
      ...previous,
      scopes: { ...previous.scopes, [row.dataset.id]: value },
      overrides: include
        ? {
            ...previous.overrides,
            [row.dataset.id]: sourceOverride(args, include, "dataset"),
          }
        : previous.overrides,
    }))
    setServerError(null)
    create.reset()
  }

  const noteMessage = noteError(draft.note)
  const showNoteError =
    noteMessage !== null && (submitted || draft.note.trim() !== "")

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (create.isPending) {
      return
    }
    if (!input) {
      toast.error("请先完善设置", {
        description: Object.values(errors).find(Boolean),
      })
      return
    }
    if (noteMessage) {
      noteRef.current?.focus()
      return
    }
    if (!lastPreview) {
      if (previewState.kind === "error") {
        toast.error("无法计算预览，暂时不能生成", {
          description: previewState.error.message,
        })
      } else {
        toast.info("正在计算预览，请稍候再生成")
      }
      return
    }
    if (previewState.kind === "ready" && !previewState.stale) {
      const blocking = blockingWarning(warnings)
      if (blocking) {
        toast.error("无法生成数据版本", { description: blocking.text })
        return
      }
    }
    create.mutate({
      ...input,
      note: draft.note.trim(),
      class_version: lastPreview.class_version,
    })
  }

  const showPreview = () =>
    document
      .getElementById(previewId)
      ?.scrollIntoView({ behavior: "smooth", block: "start" })

  // 没有对应位置的错误显示在表单底部（增强关闭时增强设置不显示）。
  const generalError =
    serverError &&
    (serverError.field === null ||
      serverError.field === "ratio" ||
      (serverError.field === "augment" && !draft.augment.enabled))
      ? serverError.message
      : null

  return (
    <>
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem] xl:items-start">
        <form
          id={formId}
          onSubmit={submit}
          className="grid min-w-0 gap-6"
          noValidate
        >
          {(template || templateNote) && (
            <TemplateNotice
              template={template}
              kind={templateKind}
              note={templateNote}
              onDrop={onDropTemplate}
            />
          )}
          <ClassesSection
            projectId={projectId}
            names={classes.map((cls) => cls.name)}
            error={
              serverError?.field === "classes" ? serverError.message : null
            }
          />
          <SourcesSection
            projectId={projectId}
            rows={rows}
            template={template}
            scope={draft.scope}
            onScopeChange={(scope) => update({ scope })}
            onSourceScopeChange={setSourceScope}
            previewSources={lastPreview?.sources}
            error={
              serverError?.field === "sources"
                ? serverError.message
                : submitted
                  ? (errors.sources ?? null)
                  : null
            }
            onChange={setSource}
          />
          <SplitSection
            draft={draft}
            update={update}
            errors={errors}
            byFolder={
              lastPreview
                ? lastPreview.sources.reduce((sum, s) => sum + s.by_folder, 0)
                : 0
            }
            imageCount={lastPreview?.image_count ?? 0}
            serverError={
              serverError?.field === "seed" ? serverError.message : null
            }
          />
          <FilterSection
            projectId={projectId}
            annotatedSelected={annotatedSelected}
            draft={draft}
            update={update}
            preview={lastPreview}
            ignore={ignore}
            templateIgnore={templateIgnore}
            known={known}
            serverError={
              serverError?.field === "labels" ? serverError.message : null
            }
          />
          <AugmentSection
            draft={draft}
            update={update}
            error={
              serverError?.field === "augment"
                ? serverError.message
                : (errors.augment ?? null)
            }
            estimate={lastPreview?.augmented_count ?? null}
          />
          <NoteSection
            ref={noteRef}
            projectName={projectName}
            value={draft.note}
            onChange={(note) => update({ note })}
            error={
              serverError?.field === "note"
                ? serverError.message
                : showNoteError
                  ? noteMessage
                  : null
            }
            disabled={create.isPending}
          />
          {generalError && (
            <Alert variant="destructive">
              <CircleAlert />
              <AlertTitle>无法生成数据版本</AlertTitle>
              <AlertDescription>
                <p className="break-words">{generalError}</p>
              </AlertDescription>
            </Alert>
          )}
        </form>

        <aside className="min-w-0 xl:sticky xl:top-20 xl:max-h-[calc(100dvh-6.5rem)] xl:overflow-y-auto xl:rounded-xl">
          <PreviewPanel
            id={previewId}
            state={previewState}
            last={lastPreview}
            testPercent={testPercent}
            warnings={warnings}
            footer={
              <div className="hidden gap-2 border-t pt-4 xl:grid">
                <Button
                  type="submit"
                  form={formId}
                  disabled={create.isPending}
                  className="w-full"
                >
                  {create.isPending ? <Spinner aria-hidden /> : <Sparkles />}
                  生成数据版本
                </Button>
                <p className="text-xs text-pretty text-muted-foreground">
                  生成在后台排队进行，完成后会提示；之后可以在详情页查看与下载。
                </p>
              </div>
            }
          />
        </aside>
      </div>
      <PreviewBar
        className="xl:hidden"
        state={previewState}
        last={lastPreview}
        warnings={warnings}
        pending={create.isPending}
        formId={formId}
        onShowPreview={showPreview}
      />
    </>
  )
}

function Section({
  title,
  description,
  action,
  children,
}: {
  title: string
  description?: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-4 sm:px-6">
        <CardTitle className="text-base">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
        {action && <CardAction>{action}</CardAction>}
      </CardHeader>
      <CardContent className="grid min-w-0 gap-4 px-4 sm:px-6">
        {children}
      </CardContent>
    </Card>
  )
}

function TemplateNotice({
  template,
  kind,
  note,
  onDrop,
}: {
  template: DataVersion | null
  kind: TemplateKind | null
  note: string | null
  onDrop: () => void
}) {
  return (
    <Alert>
      <Info />
      <AlertTitle>
        {template
          ? kind === "from"
            ? `以“${dataVersionTitle(template)}”为模板`
            : `沿用最新的“${dataVersionTitle(template)}”`
          : "没有使用模板"}
      </AlertTitle>
      <AlertDescription>
        {template ? (
          <>
            <p>
              数据集与标注来源、划分比例、随机种子、分组、过滤与增强设置与它相同；比例与种子不变时，同一张图片的划分也不变。标为“新”的是它之后生成的审图版本。
            </p>
            <div className="mt-1 flex flex-wrap gap-2">
              <Button variant="outline" size="sm" asChild>
                <Link to={dataVersionDetailPath(template.id)}>
                  查看{dataVersionTitle(template)}
                </Link>
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={onDrop}>
                改用默认设置
              </Button>
            </div>
          </>
        ) : (
          <p>{note}</p>
        )}
      </AlertDescription>
    </Alert>
  )
}

function ClassesSection({
  projectId,
  names,
  error,
}: {
  projectId: number
  names: string[]
  error: string | null
}) {
  return (
    <Section
      title="类别"
      description="版本会冻结项目当前的类别列表，顺序就是 YOLO 的类别编号。不在列表中的标签在“过滤”中处理。"
    >
      {error && (
        <p role="alert" className="text-sm break-words text-destructive">
          {error}
        </p>
      )}
      <ClassIdPreview
        classes={names}
        action={
          <Button
            asChild
            variant="link"
            size="xs"
            className="h-auto px-0 has-[>svg]:px-0"
          >
            <Link to={projectClassesPath(projectId)}>
              <Pencil />
              在项目中修改
            </Link>
          </Button>
        }
      />
    </Section>
  )
}

function SourcesSection({
  projectId,
  rows,
  template,
  scope,
  onScopeChange,
  onSourceScopeChange,
  previewSources,
  error,
  onChange,
}: {
  projectId: number
  rows: SourceRowData[]
  template: DataVersion | null
  /** 默认的参与训练的图片。 */
  scope: TrainingScope
  onScopeChange: (scope: TrainingScope) => void
  onSourceScopeChange: (row: SourceRowData, scope: ScopeOverride) => void
  previewSources: DataVersionPreview["sources"] | undefined
  error: string | null
  onChange: (
    row: SourceRowData,
    selection: SourceSelection,
    change: SourceChange
  ) => void
}) {
  const newVersions = rows.flatMap((row) =>
    row.disabled
      ? []
      : row.versions
          .filter(
            (version) =>
              isNewReviewVersion(version, row.dataset.id, template) &&
              !row.selection.version_ids.includes(version.id)
          )
          .map((version) => ({ row, version }))
  )
  const missing = missingTemplateSources(
    template,
    rows.map((row) => row.dataset)
  )
  const missingVersions = rows.flatMap((row) =>
    row.disabled || !row.query.isSuccess
      ? []
      : missingTemplateReviewVersions(
          template,
          row.dataset.id,
          row.versions
        ).map((name) => ({ dataset: row.dataset.name, name }))
  )

  const selectNew = () => {
    for (const row of rows) {
      const ids = newVersions
        .filter((item) => item.row === row)
        .map((item) => item.version.id)
      if (ids.length === 0) {
        continue
      }
      let selection = row.selection
      for (const id of ids) {
        selection = toggleVersion(selection, id, true, row.versions)
      }
      onChange(row, selection, "versions")
    }
  }

  const selectedCount = rows.filter((row) => isSelected(row.selection)).length

  return (
    <Section
      title="数据集与标注来源"
      description="每个数据集可以选择基础标注（数据集自己的标注文件）与审图版本。同一张图片在多个来源中时，新的审图版本优先；不选基础标注时只纳入审图版本中标为需加样的图片。"
    >
      {rows.length > 0 && (
        <ScopeChoice
          value={scope}
          onChange={onScopeChange}
          overridden={
            rows.filter(
              (row) => !row.disabled && row.scopeSetting && row.scope !== scope
            ).length
          }
        />
      )}
      {template &&
        (newVersions.length > 0 ||
          missing.length > 0 ||
          missingVersions.length > 0) && (
          <div className="grid gap-2">
            {newVersions.length > 0 && (
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
                <Sparkles className="size-4 shrink-0 text-amber-600 dark:text-amber-500" />
                <span className="min-w-0 flex-1">
                  {dataVersionTitle(template)} 之后有{" "}
                  {formatNumber(newVersions.length)}{" "}
                  个新的审图版本（已标为“新”），默认没有选中。
                </span>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={selectNew}
                >
                  全部选中
                </Button>
              </div>
            )}
            {missing.length > 0 && (
              <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
                <Info className="mt-px size-3.5 shrink-0" />
                <span className="min-w-0 break-words">
                  {dataVersionTitle(template)} 的数据集“{missing.join("”“")}
                  ”已不在项目中或不能使用，没有选择。
                </span>
              </p>
            )}
            {missingVersions.length > 0 && (
              <p className="flex items-start gap-1.5 text-xs text-pretty text-amber-700 dark:text-amber-500">
                <TriangleAlert className="mt-px size-3.5 shrink-0" />
                <span className="min-w-0 break-words">
                  {dataVersionTitle(template)} 用到的审图版本
                  {missingVersions
                    .map((item) => `“${item.dataset} · ${item.name}”`)
                    .join("、")}
                  已在回收站中或已被删除，没有选择：这些图片的标注会与模板不同。
                </span>
              </p>
            )}
          </div>
        )}
      {rows.length === 0 ? (
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle className="text-base">项目中还没有数据集</EmptyTitle>
            <EmptyDescription>
              先把数据集加入项目，再生成数据版本。
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button size="sm" asChild>
              <Link to={projectDatasetsPath(projectId)}>
                <Plus />
                在项目中添加数据集
              </Link>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <ul className="grid grid-cols-[minmax(0,1fr)] gap-2">
          {rows.map((row) => (
            <SourceRow
              key={row.dataset.id}
              row={row}
              template={template}
              defaultScope={scope}
              onScopeChange={(value) => onSourceScopeChange(row, value)}
              previewSource={previewSources?.find(
                (s) => s.dataset_id === row.dataset.id
              )}
              onChange={(selection, change) => onChange(row, selection, change)}
            />
          ))}
        </ul>
      )}
      {rows.length > 0 && (
        <p className="text-xs text-muted-foreground tabular-nums">
          已选 {formatNumber(selectedCount)} / {formatNumber(rows.length)}{" "}
          个数据集
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm break-words text-destructive">
          {error}
        </p>
      )}
    </Section>
  )
}

function SourceRow({
  row,
  template,
  defaultScope,
  onScopeChange,
  previewSource,
  onChange,
}: {
  row: SourceRowData
  template: DataVersion | null
  defaultScope: TrainingScope
  onScopeChange: (scope: ScopeOverride) => void
  previewSource: DataVersionPreview["sources"][number] | undefined
  onChange: (selection: SourceSelection, change: SourceChange) => void
}) {
  const id = useId()
  const { dataset, disabled, versions, selection, query, scope } = row
  const selected = isSelected(selection)
  // 审图版本读到之前不能修改来源：模板与默认的来源还不完整。
  const locked = disabled !== null || query.data === undefined
  const newDataset = !disabled && notInTemplate(dataset.id, template)
  const unannotated = unannotatedCount(dataset)
  const background = unannotated + dataset.empty_count

  const toggleDataset = (checked: boolean) => {
    onChange(
      checked ? selectionToInclude(dataset, versions, scope) : NO_SOURCE,
      "dataset"
    )
  }

  return (
    <li
      className={cn(
        "grid min-w-0 gap-2.5 rounded-lg border p-3 transition-colors",
        selected && "border-primary/60 bg-muted/40",
        disabled && "bg-muted/20"
      )}
    >
      <div className="flex min-w-0 items-start gap-3">
        <Checkbox
          id={id}
          checked={selected}
          onCheckedChange={(checked) => toggleDataset(checked === true)}
          disabled={locked}
          className="mt-0.5"
        />
        <label
          htmlFor={id}
          className={cn(
            "grid min-w-0 flex-1 gap-0.5",
            disabled ? "opacity-70" : !locked && "cursor-pointer"
          )}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="truncate text-sm font-medium" title={dataset.name}>
              {dataset.name}
            </span>
            <DatasetStatusBadge status={dataset.status} />
            {newDataset && template && (
              <Badge variant="outline" className="font-normal">
                {dataVersionTitle(template)} 中没有
              </Badge>
            )}
          </span>
          <span className="text-xs text-muted-foreground tabular-nums">
            {formatNumber(dataset.image_count)} 张图片 · 已标注{" "}
            {formatNumber(dataset.annotated_count)} 张
            {dataset.empty_count > 0 &&
              ` · 负样本 ${formatNumber(dataset.empty_count)} 张`}
            {unannotated > 0 && ` · 未标注 ${formatNumber(unannotated)} 张`}
          </span>
        </label>
      </div>

      {disabled && (
        <p className="flex items-start gap-1.5 pl-7 text-xs text-destructive">
          <FolderX className="mt-px size-3.5 shrink-0" />
          <span className="min-w-0">{disabled}</span>
        </p>
      )}
      {!disabled && dataset.active_import && (
        <p className="flex items-start gap-1.5 pl-7 text-xs text-amber-700 dark:text-amber-500">
          <TriangleAlert className="mt-px size-3.5 shrink-0" />
          <span className="min-w-0">
            正在导入数据：预览按当前的索引统计，生成时以那时的文件为准。
          </span>
        </p>
      )}

      {!disabled &&
        selected &&
        dataset.status !== "" &&
        dataset.status !== "trainable" && (
          <p className="flex items-start gap-1.5 pl-7 text-xs text-muted-foreground">
            <Info className="mt-px size-3.5 shrink-0" />
            <span className="min-w-0">
              数据集的状态是“{datasetStatusLabels[dataset.status]}
              ”，还没有标记为可训练。
            </span>
          </p>
        )}

      {!disabled && (
        <div className="grid min-w-0 gap-1.5 pl-7">
          <SourceScopeSelect
            datasetName={dataset.name}
            value={row.scopeSetting}
            defaultScope={defaultScope}
            onChange={onScopeChange}
          />
          <SourceOption
            checked={selection.include_base}
            onCheckedChange={(checked) =>
              onChange({ ...selection, include_base: checked }, "base")
            }
            disabled={locked}
            title="基础标注"
            detail={baseDetail(scope, dataset.annotated_count, background)}
          />
          {query.isPending ? (
            <p className="flex items-center gap-1.5 py-1 text-xs text-muted-foreground">
              <Spinner className="size-3.5" aria-hidden />
              正在读取审图版本…
            </p>
          ) : query.isError && query.data === undefined ? (
            <div
              role="alert"
              className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-destructive"
            >
              <span className="min-w-0 break-words">
                无法读取审图版本：{query.error.message}
              </span>
              <Button
                type="button"
                variant="outline"
                size="xs"
                className="text-foreground"
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
              >
                <RefreshCw className={cn(query.isFetching && "animate-spin")} />
                重试
              </Button>
            </div>
          ) : (
            versions.map((version) => (
              <SourceOption
                key={version.id}
                checked={selection.version_ids.includes(version.id)}
                onCheckedChange={(checked) =>
                  onChange(
                    toggleVersion(selection, version.id, checked, versions),
                    "versions"
                  )
                }
                title={`${version.name}（需加样 ${formatNumber(version.needs_sample_count)}）`}
                isNew={isNewReviewVersion(version, dataset.id, template)}
                note={version.note}
                detail={versionCountsText(version)}
              />
            ))
          )}
          {row.building && (
            <p className="text-xs text-muted-foreground">
              有审图版本正在生成，完成后可以选择。
            </p>
          )}
          {selected && (
            <p className="text-xs text-pretty text-muted-foreground">
              {selectionHint(selection)}
            </p>
          )}
          {selected && previewSource && (
            <p className="text-xs font-medium tabular-nums">
              {previewSourceText(previewSource)}
            </p>
          )}
        </div>
      )}
    </li>
  )
}

/** 基础标注的说明：按参与训练的图片说明会纳入哪些图片。 */
function baseDetail(
  scope: TrainingScope,
  annotated: number,
  background: number
) {
  switch (scope) {
    case "background":
      return `数据集自己的图片：没有目标的 ${formatNumber(background)} 张作为负样本`
    case "all":
      return `数据集自己的全部图片：有目标 ${formatNumber(annotated)} 张，没有目标 ${formatNumber(background)} 张`
    default:
      return annotated > 0
        ? `数据集自己的标注文件：已标注 ${formatNumber(annotated)} 张`
        : "数据集自己的标注文件：还没有已标注的图片"
  }
}

/** 默认的参与训练的图片（所有数据集），每个数据集可以在列表中单独修改。 */
function ScopeChoice({
  value,
  onChange,
  overridden,
}: {
  value: TrainingScope
  onChange: (scope: TrainingScope) => void
  /** 单独设置了、与默认不同的数据集数。 */
  overridden: number
}) {
  const groupId = useId()
  const hintId = useId()
  return (
    <div className="grid gap-2">
      <div className="grid gap-0.5">
        <Label id={groupId}>参与训练的图片</Label>
        <p id={hintId} className="text-xs text-pretty text-muted-foreground">
          所有数据集默认使用这个设置，也可以在下面为单个数据集修改
          {overridden > 0 && `（已单独修改 ${formatNumber(overridden)} 个）`}。
        </p>
      </div>
      <RadioGroup
        value={value}
        onValueChange={(next) => onChange(next as TrainingScope)}
        aria-labelledby={groupId}
        aria-describedby={hintId}
        className="grid gap-2 sm:grid-cols-3"
      >
        {TRAINING_SCOPES.map((scope) => {
          const itemId = `${groupId}-${scope}`
          return (
            <label
              key={scope}
              htmlFor={itemId}
              className={cn(
                "flex cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2.5 text-sm transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
                value === scope && "border-primary/60 bg-muted/40"
              )}
            >
              <RadioGroupItem id={itemId} value={scope} className="mt-0.5" />
              <span className="grid min-w-0 gap-0.5">
                <span className="font-medium">
                  {TRAINING_SCOPE_LABELS[scope]}
                </span>
                <span className="text-xs text-pretty text-muted-foreground">
                  {TRAINING_SCOPE_HINTS[scope]}
                </span>
              </span>
            </label>
          )
        })}
      </RadioGroup>
    </div>
  )
}

/** 单个数据集参与训练的图片：使用默认的，或单独设置。 */
function SourceScopeSelect({
  datasetName,
  value,
  defaultScope,
  onChange,
}: {
  datasetName: string
  value: ScopeOverride
  defaultScope: TrainingScope
  onChange: (value: ScopeOverride) => void
}) {
  const custom = value !== "" && value !== defaultScope
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 px-1 py-0.5">
      <span className="text-xs text-muted-foreground">参与训练的图片</span>
      <Select
        value={value || "default"}
        onValueChange={(next) =>
          onChange(next === "default" ? "" : (next as TrainingScope))
        }
      >
        <SelectTrigger
          size="sm"
          className={cn(
            "h-7 max-w-full min-w-0 gap-1.5 px-2 text-xs",
            custom && "border-primary/60 bg-primary/5 font-medium"
          )}
          aria-label={`数据集“${datasetName}”参与训练的图片`}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">
            使用默认（{TRAINING_SCOPE_LABELS[defaultScope]}）
          </SelectItem>
          <SelectSeparator />
          {TRAINING_SCOPES.map((scope) => (
            <SelectItem key={scope} value={scope}>
              {TRAINING_SCOPE_LABELS[scope]}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/** 标注来源的选项：名称、新标记、备注与图片数。 */
function SourceOption({
  checked,
  onCheckedChange,
  disabled = false,
  title,
  isNew = false,
  note,
  detail,
}: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
  title: string
  isNew?: boolean
  note?: string
  detail: string
}) {
  const id = useId()
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex min-w-0 items-start gap-2.5 rounded-md px-1 py-1 transition-colors has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
        !disabled && "cursor-pointer hover:bg-muted/60"
      )}
    >
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={(next) => onCheckedChange(next === true)}
        disabled={disabled}
        className="mt-0.5"
      />
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <span className="min-w-0 break-words">{title}</span>
          {isNew && (
            <Badge className="h-5 bg-amber-500/15 px-1.5 font-normal text-amber-700 dark:text-amber-400">
              新
            </Badge>
          )}
        </span>
        {note && (
          <span
            className="line-clamp-2 text-xs break-words whitespace-pre-line"
            title={note}
          >
            {note}
          </span>
        )}
        <span className="text-xs text-muted-foreground tabular-nums">
          {detail}
        </span>
      </span>
    </label>
  )
}

type SectionProps = {
  draft: FormDraft
  update: (changes: Partial<FormDraft>) => void
}

function SplitSection({
  draft,
  update,
  errors,
  byFolder,
  imageCount,
  serverError,
}: SectionProps & {
  errors: { ratio?: string; seed?: string }
  byFolder: number
  imageCount: number
  serverError: string | null
}) {
  const groupId = useId()
  const trainId = useId()
  const valId = useId()
  const foldersId = useId()
  const seedId = useId()
  const groupById = useId()
  const advancedId = useId()
  const [advanced, setAdvanced] = useState(
    draft.seed !== "42" || draft.group_by !== "stem"
  )
  const custom = draft.ratio.preset === CUSTOM_RATIO
  const ratio = parseRatio(draft.ratio)
  const seedError = serverError ?? errors.seed ?? null
  // 关闭后预览不再统计按文件夹划分的图片，开关仍然保留，方便重新打开。
  const showFolders = byFolder > 0 || !draft.use_folders

  return (
    <Section
      title="划分"
      description="按固定的随机种子把图片分到训练 / 验证 / 测试集；比例与种子不变时，同一张图片在各个数据版本中的划分也不变，验证结果才能对比。"
    >
      <div className="grid gap-2">
        <Label id={groupId}>比例（训练 / 验证 / 测试）</Label>
        <RadioGroup
          value={draft.ratio.preset}
          onValueChange={(preset) => {
            const presetValue = RATIO_PRESETS.find((p) => p.id === preset)
            update({
              ratio: presetValue
                ? {
                    preset,
                    train: String(presetValue.train),
                    val: String(presetValue.val),
                  }
                : { ...draft.ratio, preset },
            })
          }}
          aria-labelledby={groupId}
          className="grid grid-cols-2 gap-2 sm:grid-cols-4"
        >
          {[
            ...RATIO_PRESETS.map((preset) => ({
              id: preset.id,
              label: ratioPresetLabel(preset),
            })),
            { id: CUSTOM_RATIO, label: "自定义" },
          ].map((option) => {
            const itemId = `${groupId}-${option.id}`
            return (
              <label
                key={option.id}
                htmlFor={itemId}
                className={cn(
                  "flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
                  draft.ratio.preset === option.id &&
                    "border-primary/60 bg-muted/40"
                )}
              >
                <RadioGroupItem id={itemId} value={option.id} />
                <span className="tabular-nums">{option.label}</span>
              </label>
            )
          })}
        </RadioGroup>
      </div>

      {custom && (
        <Field data-invalid={errors.ratio ? true : undefined}>
          <div className="flex flex-wrap items-end gap-3">
            <div className="grid gap-1.5">
              <FieldLabel htmlFor={trainId}>训练 %</FieldLabel>
              <Input
                id={trainId}
                value={draft.ratio.train}
                onChange={(event) =>
                  update({
                    ratio: { ...draft.ratio, train: event.target.value },
                  })
                }
                inputMode="numeric"
                className="w-20 tabular-nums"
                aria-invalid={errors.ratio ? true : undefined}
              />
            </div>
            <div className="grid gap-1.5">
              <FieldLabel htmlFor={valId}>验证 %</FieldLabel>
              <Input
                id={valId}
                value={draft.ratio.val}
                onChange={(event) =>
                  update({ ratio: { ...draft.ratio, val: event.target.value } })
                }
                inputMode="numeric"
                className="w-20 tabular-nums"
                aria-invalid={errors.ratio ? true : undefined}
              />
            </div>
            <p className="pb-2 text-sm text-muted-foreground tabular-nums">
              测试 {ratio.ok ? `${ratio.test}%` : "—"}
            </p>
          </div>
          {errors.ratio ? (
            <FieldError>{errors.ratio}</FieldError>
          ) : (
            <FieldDescription>
              训练与验证至少 1%，测试 = 100 − 训练 − 验证。
            </FieldDescription>
          )}
        </Field>
      )}

      {showFolders && (
        <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
          <div className="grid min-w-0 gap-1">
            <Label htmlFor={foldersId}>沿用 train / val / test 文件夹</Label>
            <p className="text-xs text-pretty text-muted-foreground">
              {draft.use_folders
                ? `${formatNumber(byFolder)} 张图片位于数据集的 train / val / test 文件夹中（如 images/train），沿用文件夹的划分，其余按比例。${
                    imageCount > 0 && byFolder >= imageCount
                      ? "全部图片都按文件夹划分，比例不起作用。"
                      : ""
                  }`
                : "关闭时全部图片按比例划分。数据集同时有 train 与 val 文件夹时，打开可以沿用它们原来的划分。"}
            </p>
          </div>
          <Switch
            id={foldersId}
            checked={draft.use_folders}
            onCheckedChange={(use_folders) => update({ use_folders })}
          />
        </div>
      )}

      <div className="grid gap-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="-ml-2 w-fit text-muted-foreground"
          aria-expanded={advanced}
          aria-controls={advancedId}
          onClick={() => setAdvanced((open) => !open)}
        >
          <ChevronDown
            className={cn("transition-transform", !advanced && "-rotate-90")}
          />
          高级：随机种子与分组
        </Button>
        {advanced && (
          <div
            id={advancedId}
            className="grid min-w-0 gap-5 rounded-lg border p-4"
          >
            <Field data-invalid={seedError ? true : undefined}>
              <FieldLabel htmlFor={seedId}>随机种子</FieldLabel>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  id={seedId}
                  value={draft.seed}
                  onChange={(event) => update({ seed: event.target.value })}
                  inputMode="numeric"
                  className="w-36 tabular-nums"
                  aria-invalid={seedError ? true : undefined}
                />
                <Button
                  type="button"
                  variant="outline"
                  onClick={() =>
                    update({
                      seed: String(randomSeed(parseSeed(draft.seed))),
                    })
                  }
                >
                  <Shuffle />
                  换一种划分
                </Button>
              </div>
              {seedError ? (
                <FieldError>{seedError}</FieldError>
              ) : (
                <FieldDescription>
                  0–2147483647。换一种划分会换一个随机种子：与之前用原来的种子生成的数据版本相比，很多图片会换到别的划分，两个版本的验证结果不再能直接对比。某个类别在验证集中没有目标时可以试试。
                </FieldDescription>
              )}
            </Field>
            <div className="grid gap-2">
              <Label id={groupById}>防止数据泄漏的分组</Label>
              <RadioGroup
                value={draft.group_by}
                onValueChange={(value) =>
                  update({ group_by: value as GroupBy })
                }
                aria-labelledby={groupById}
                className="grid gap-2"
              >
                {(["stem", "folder"] as const).map((value) => {
                  const itemId = `${groupById}-${value}`
                  return (
                    <label
                      key={value}
                      htmlFor={itemId}
                      className={cn(
                        "flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
                        draft.group_by === value &&
                          "border-primary/60 bg-muted/40"
                      )}
                    >
                      <RadioGroupItem
                        id={itemId}
                        value={value}
                        className="mt-0.5"
                      />
                      <span className="grid min-w-0 gap-1">
                        <span className="text-sm font-medium">
                          {GROUP_BY_LABELS[value]}
                        </span>
                        <span className="text-xs text-pretty text-muted-foreground">
                          {GROUP_BY_DESCRIPTIONS[value]}
                        </span>
                      </span>
                    </label>
                  )
                })}
              </RadioGroup>
              <p className="text-xs text-pretty text-muted-foreground">
                另外，不同数据集或文件夹中文件名与大小都相同的图片（同一个文件）也总在同一个划分中。
              </p>
            </div>
          </div>
        )}
      </div>
    </Section>
  )
}

/** 不在类别列表中的标签的一行。 */
type UnlistedRow = {
  name: string
  objects: number | null
  images: number | null
  other_project: boolean
}

function FilterSection({
  projectId,
  annotatedSelected,
  draft,
  update,
  preview,
  ignore,
  templateIgnore,
  known,
  serverError,
}: SectionProps & {
  projectId: number
  /** 有选中的数据集参与训练的图片是“仅有标注的图片”（空标注的开关只影响它们）。 */
  annotatedSelected: boolean
  preview: DataVersionPreview | undefined
  ignore: string[]
  templateIgnore: readonly string[] | null
  known: KnownUnlisted
  serverError: string | null
}) {
  const emptyId = useId()
  const listed = preview?.unlisted_labels ?? []
  // 预览中不再出现、但选择了忽略的标签也列出来，可以改回去。
  const rows: UnlistedRow[] = [
    ...listed,
    ...ignore
      .filter((name) => !listed.some((label) => label.name === name))
      .map((name) => ({
        name,
        objects: null,
        images: null,
        other_project: known[name]?.other_project ?? false,
      })),
  ]
  const choiceOf = (row: UnlistedRow): UnlistedChoice =>
    draft.choices[row.name] ?? defaultUnlistedChoice(row, templateIgnore)
  const skippedImages = preview?.skipped.unlisted ?? 0

  return (
    <Section
      title="过滤"
      description="标注文件无法读取的图片不会纳入；没有标注的图片是否纳入由数据集参与训练的图片决定。"
    >
      <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
        <div className="grid min-w-0 gap-1">
          <Label
            htmlFor={emptyId}
            className={cn(!annotatedSelected && "text-muted-foreground")}
          >
            纳入空标注图片（负样本）
          </Label>
          <p className="text-xs text-pretty text-muted-foreground">
            标注文件中没有目标的图片作为背景参与训练，可以减少误检。
            {annotatedSelected
              ? `只影响参与训练的图片为“${TRAINING_SCOPE_LABELS.annotated}”的数据集，其它数据集总是纳入没有目标的图片。`
              : `所选数据集参与训练的图片都不是“${TRAINING_SCOPE_LABELS.annotated}”，总是纳入没有目标的图片。`}
            {annotatedSelected &&
            preview &&
            preview.skipped.empty > 0 &&
            !draft.include_empty
              ? `将有 ${formatNumber(preview.skipped.empty)} 张空标注图片不纳入。`
              : ""}
          </p>
        </div>
        <Switch
          id={emptyId}
          checked={draft.include_empty}
          onCheckedChange={(include_empty) => update({ include_empty })}
          disabled={!annotatedSelected}
        />
      </div>

      <div className="grid gap-3">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
          <h3 className="text-sm font-medium">不在类别列表中的标签</h3>
          <Button
            asChild
            variant="link"
            size="xs"
            className="h-auto px-0 has-[>svg]:px-0"
          >
            <Link to={projectClassesPath(projectId)}>
              <Pencil />
              在项目中修改类别
            </Link>
          </Button>
        </div>
        {rows.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {preview
              ? "所选图片的标签都在类别列表中。"
              : "预览后列出所选图片中不在类别列表中的标签。"}
          </p>
        ) : (
          <>
            <p className="text-xs text-pretty text-muted-foreground">
              忽略这些框：去掉这些目标，图片照常纳入（适合共用数据集的其他项目的类别）；跳过这些图片：含有这个标签的图片不纳入（它可能是还没加入类别列表的真实缺陷，纳入后会被当作背景）。
            </p>
            <ul className="grid grid-cols-[minmax(0,1fr)] gap-2">
              {rows.map((row) => (
                <UnlistedLabelRow
                  key={row.name}
                  row={row}
                  choice={choiceOf(row)}
                  onChange={(choice) =>
                    update({
                      choices: { ...draft.choices, [row.name]: choice },
                    })
                  }
                />
              ))}
            </ul>
          </>
        )}
        {skippedImages > 0 && (
          <Alert className="border-amber-500/40 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-500">
            <TriangleAlert />
            <AlertTitle>
              {formatNumber(skippedImages)} 张图片不会纳入
            </AlertTitle>
            <AlertDescription>
              <p>
                它们含有选择“跳过”的标签。如果这些标签是需要检测的缺陷，请先把它们加入项目的类别列表。
              </p>
              <Button asChild variant="outline" size="sm" className="mt-1">
                <Link to={projectClassesPath(projectId)}>去项目中设置类别</Link>
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {preview && preview.dropped_objects > 0 && (
          <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
            <Info className="mt-px size-3.5 shrink-0" />
            将去掉 {formatNumber(preview.dropped_objects)} 个忽略的标签的框。
          </p>
        )}
        {serverError && (
          <p role="alert" className="text-sm break-words text-destructive">
            {serverError}
          </p>
        )}
      </div>
    </Section>
  )
}

function UnlistedLabelRow({
  row,
  choice,
  onChange,
}: {
  row: UnlistedRow
  choice: UnlistedChoice
  onChange: (choice: UnlistedChoice) => void
}) {
  const id = useId()
  return (
    <li className="flex min-w-0 flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg border p-3">
      <div className="grid min-w-0 gap-0.5">
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span
            id={id}
            className="max-w-full truncate text-sm font-medium"
            title={row.name}
          >
            {row.name}
          </span>
          {row.other_project && (
            <Badge variant="outline" className="font-normal">
              其他项目的类别
            </Badge>
          )}
        </span>
        <span className="text-xs text-muted-foreground tabular-nums">
          {row.objects === null || row.images === null
            ? "当前所选的图片中没有"
            : `${formatNumber(row.objects)} 个目标 · ${formatNumber(row.images)} 张图片`}
        </span>
      </div>
      <RadioGroup
        value={choice}
        onValueChange={(value) => onChange(value as UnlistedChoice)}
        aria-labelledby={id}
        className="flex flex-wrap gap-x-4 gap-y-2"
      >
        {(
          [
            ["ignore", "忽略这些框"],
            ["skip", "跳过这些图片"],
          ] as const
        ).map(([value, label]) => (
          <label
            key={value}
            htmlFor={`${id}-${value}`}
            className="flex cursor-pointer items-center gap-2 text-sm"
          >
            <RadioGroupItem id={`${id}-${value}`} value={value} />
            {label}
          </label>
        ))}
      </RadioGroup>
    </li>
  )
}

function AugmentSection({
  draft,
  update,
  error,
  estimate,
}: SectionProps & { error: string | null; estimate: number | null }) {
  const enabledId = useId()
  const copiesId = useId()
  const objectsId = useId()
  const { augment } = draft
  const setAugment = (changes: Partial<FormDraft["augment"]>) =>
    update({ augment: { ...augment, ...changes } })
  const limit = copiesLimit(augment.methods)
  const copies = effectiveCopies(augment)
  const toggleMethod = (method: AugmentMethod, checked: boolean) =>
    setAugment({
      methods: checked
        ? [...augment.methods, method]
        : augment.methods.filter((m) => m !== method),
    })

  return (
    <Section
      title="增强"
      description="Ultralytics 训练时已经会在线随机翻转、调整颜色与拼接（mosaic）；离线增强只建议用于样本很少的缺陷。只增强训练集，验证集与测试集保持真实图片。"
    >
      <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
        <div className="grid min-w-0 gap-1">
          <Label htmlFor={enabledId}>生成增强图片</Label>
          <p className="text-xs text-pretty text-muted-foreground">
            为训练集图片生成增强副本，与原图一起存放在版本中；下载时可以不包含它们。
          </p>
        </div>
        <Switch
          id={enabledId}
          checked={augment.enabled}
          onCheckedChange={(enabled) => setAugment({ enabled })}
        />
      </div>

      {augment.enabled && (
        <>
          <Field data-invalid={error ? true : undefined}>
            <FieldTitle id={`${enabledId}-methods`}>增强方式</FieldTitle>
            <ul
              className="grid gap-2 sm:grid-cols-2"
              aria-labelledby={`${enabledId}-methods`}
            >
              {AUGMENT_METHODS.map((method) => {
                const itemId = `${enabledId}-${method.id}`
                const checked = augment.methods.includes(method.id)
                return (
                  <li key={method.id} className="min-w-0">
                    <label
                      htmlFor={itemId}
                      className={cn(
                        "flex h-full min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
                        checked && "border-primary/60 bg-muted/40"
                      )}
                    >
                      <Checkbox
                        id={itemId}
                        checked={checked}
                        onCheckedChange={(next) =>
                          toggleMethod(method.id, next === true)
                        }
                        className="mt-0.5"
                      />
                      <span className="grid min-w-0 gap-1">
                        <span className="text-sm font-medium">
                          {method.label}
                          <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                            {method.geometric ? "几何" : "像素"}
                          </span>
                        </span>
                        <span className="text-xs text-pretty text-muted-foreground">
                          {method.description}
                        </span>
                      </span>
                    </label>
                  </li>
                )
              })}
            </ul>
            {error && <FieldError>{error}</FieldError>}
          </Field>

          <div className="flex flex-wrap items-start gap-x-6 gap-y-4">
            <Field className="w-auto">
              <FieldLabel htmlFor={copiesId}>每张训练集图片生成</FieldLabel>
              <Select
                value={String(copies)}
                onValueChange={(value) => setAugment({ copies: Number(value) })}
                disabled={limit === 0}
              >
                <SelectTrigger id={copiesId} className="w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {Array.from(
                    { length: Math.max(limit, 1) },
                    (_, i) => i + 1
                  ).map((value) => (
                    <SelectItem key={value} value={String(value)}>
                      {value} 份
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FieldDescription>
                {limit > 0 && maxCopies(augment.methods) < 5
                  ? `只选翻转 / 旋转时最多 ${limit} 份互不相同的副本。`
                  : "最多 5 份；每份随机组合所选的方式，不会与原图或其它副本相同。"}
              </FieldDescription>
            </Field>
            <div className="flex min-w-0 flex-1 items-start justify-between gap-4 rounded-lg border p-3 sm:min-w-64">
              <div className="grid min-w-0 gap-1">
                <Label htmlFor={objectsId}>只增强有目标的图片</Label>
                <p className="text-xs text-pretty text-muted-foreground">
                  负样本（没有目标的图片）通常已经很多，不必再增强。
                </p>
              </div>
              <Switch
                id={objectsId}
                checked={augment.only_with_objects}
                onCheckedChange={(only_with_objects) =>
                  setAugment({ only_with_objects })
                }
              />
            </div>
          </div>

          {estimate !== null && (
            <p className="flex items-start gap-1.5 text-xs text-muted-foreground tabular-nums">
              <Info className="mt-px size-3.5 shrink-0" />
              预计生成约 {formatNumber(estimate)}{" "}
              张增强图片（全部在训练集中，生成时无法解码或太大的图片会跳过）。
            </p>
          )}
        </>
      )}
    </Section>
  )
}

function NoteSection({
  ref,
  projectName,
  value,
  onChange,
  error,
  disabled,
}: {
  ref: Ref<HTMLTextAreaElement>
  projectName: string
  value: string
  onChange: (value: string) => void
  error: string | null
  disabled: boolean
}) {
  const id = useId()
  return (
    <Section title="备注">
      <Field data-invalid={error ? true : undefined}>
        <FieldLabel htmlFor={id}>版本备注</FieldLabel>
        <Textarea
          ref={ref}
          id={id}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={`如：${projectName}，加入 11 月夜班数据与审图1.3 中需加样的图片`}
          aria-invalid={error ? true : undefined}
          aria-required
          disabled={disabled}
          className="min-h-24"
        />
        {error ? (
          <FieldError>{error}</FieldError>
        ) : (
          <div className="flex items-start justify-between gap-3">
            <FieldDescription className="text-xs">
              必填：说明这个版本的数据与用途，列表与训练时选择版本可以看到。
            </FieldDescription>
            <DescriptionCounter value={value} />
          </div>
        )}
      </Field>
    </Section>
  )
}

function FormSkeleton() {
  return (
    <div
      className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]"
      aria-busy="true"
      aria-label="正在加载"
    >
      <div className="grid gap-6">
        <Skeleton className="h-32 rounded-xl" />
        <Skeleton className="h-64 rounded-xl" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
      <Skeleton className="h-96 rounded-xl" />
    </div>
  )
}
