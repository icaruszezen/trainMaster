/**
 * 下载数据集：选择导出格式，由浏览器直接下载服务端生成的 zip。
 *
 * YOLO 需要类别顺序（类别编号）。数据集属于项目时使用项目的类别列表的顺序，
 * 与用这个项目训练时一致；属于多个项目且各项目的顺序不同时，由用户选择用
 * 哪个项目的。不属于任何项目时手动指定（文本框或 classes.txt）。
 */
import {
  useId,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type Ref,
} from "react"
import {
  useMutation,
  useQueries,
  useQuery,
  type UseQueryResult,
} from "@tanstack/react-query"
import {
  Download,
  Info,
  Pencil,
  RefreshCw,
  Shapes,
  TriangleAlert,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import {
  Field,
  FieldDescription,
  FieldError,
  FieldTitle,
} from "@/components/ui/field"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import {
  createExport,
  labelsQuery,
  lastYoloImportQuery,
  liveDatasetQuery,
  type AnnotationFormat,
  type CreateExportInput,
  type Dataset,
  type DatasetLabel,
  type DatasetProjectRef,
} from "@/features/datasets/api"
import { AnnotationFormatPicker } from "@/features/datasets/annotation-format-picker"
import {
  compareClassesWithLabels,
  defaultClassOrder,
  defaultOrderIndex,
  parseClassList,
  toClassListText,
} from "@/features/datasets/class-list"
import {
  ClassIdPreview,
  ClassListField,
} from "@/features/datasets/class-list-field"
import {
  projectClassesQuery,
  refreshMembership,
  type ProjectClassList,
} from "@/features/projects/api"
import { isPermanentError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import { formatBytes, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 每种格式下载后的目录结构（与服务端的打包方式一致）。 */
const exportDescriptions: Record<AnnotationFormat, string> = {
  voc: "图片与配对的 .xml 按原有目录结构打包",
  xanylabeling:
    "图片按原有目录结构，有标注的图片旁生成同名 .json（X-AnyLabeling 4.x）",
  yolo: "Ultralytics 布局：images/、labels/（同名 .txt），附 classes.txt 与 data.yaml",
}

/** 警告中最多列出的标签数。 */
const LIST_LIMIT = 30

const PROJECT_REQUIRED_MESSAGE = "请选择使用哪个项目的类别顺序"

/** 数据集能否下载：文件夹存在且至少有一张图片。 */
export function canExportDataset(dataset: Dataset) {
  return !dataset.directory_missing && dataset.image_count > 0
}

/** 项目的类别设置（类别列表）所在的页面。 */
const projectClassesPath = (projectId: number) =>
  `/projects/${projectId}?tab=classes`

const classNamesOf = (list: UseQueryResult<ProjectClassList>) =>
  list.data?.items.map((cls) => cls.name)

type ExportDialogProps = {
  dataset: Dataset | null
  onOpenChange: (open: boolean) => void
}

export function ExportDialog({ dataset, onOpenChange }: ExportDialogProps) {
  return (
    <Dialog open={dataset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        {dataset && (
          <ExportForm dataset={dataset} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ExportForm({
  dataset: opened,
  onDone,
}: {
  dataset: Dataset
  onDone: () => void
}) {
  // 读取最新的数据集，而不是只用打开时传入的：所属项目决定使用哪个项目的
  // 类别顺序还是手动指定。列表页传入的是打开时列表项的副本，下载失败后
  // refreshMembership 刷新的结果要能反映到对话框中（见 liveDatasetQuery）。
  const { data: dataset } = useQuery(liveDatasetQuery(opened))
  const [format, setFormat] = useState<AnnotationFormat>("voc")
  const [submitted, setSubmitted] = useState(false)
  const isYolo = format === "yolo"
  const { projects } = dataset
  const inProjects = projects.length > 0

  const labels = useQuery(labelsQuery(dataset.id))
  const labelItems = labels.data?.items

  // ---- 属于项目：使用（所选）项目的类别列表 ----
  const projectGroupRef = useRef<HTMLDivElement>(null)
  // 用户选择的项目；null（或它已不在数据集的项目中）时按 defaultOrderIndex。
  const [chosenProjectId, setChosenProjectId] = useState<number | null>(null)
  // 各项目的类别列表都要读取：选项中显示类别数，而且只有各项目的顺序都相同
  // 时才能替用户选择。
  const projectLists = useQueries({
    queries: projects.map((project) => projectClassesQuery(project.id)),
  })
  const chosenIndex = projects.findIndex(
    (project) => project.id === chosenProjectId
  )
  const projectIndex =
    chosenIndex >= 0
      ? chosenIndex
      : defaultOrderIndex(projectLists.map(classNamesOf))
  const project = projectIndex >= 0 ? projects[projectIndex] : null
  const projectList = projectIndex >= 0 ? projectLists[projectIndex] : null
  const projectData = projectList?.data
  const projectClasses = useMemo(
    () => projectData?.items.map((cls) => cls.name),
    [projectData]
  )

  // ---- 不属于项目：手动指定 ----
  const classFieldRef = useRef<HTMLTextAreaElement>(null)
  // 用户编辑过的类别顺序；null 表示沿用由数据集标签生成的默认顺序。
  const [classesDraft, setClassesDraft] = useState<string | null>(null)
  // 默认顺序：数据集由 YOLO 导入时，先沿用最近一次导入的类别顺序（类别编号
  // 保持不变），其余标签按名称排在后面。
  const lastYolo = useQuery({
    ...lastYoloImportQuery(dataset.id),
    enabled: !inProjects,
  })
  const yoloImport = lastYolo.data ?? null
  const importedClasses = yoloImport?.classes
  // 请求都有结果后才有默认顺序；在此之前不能按默认顺序下载。
  const defaultsPending = labels.isPending || lastYolo.isPending
  const defaultText = useMemo(
    () =>
      defaultsPending
        ? ""
        : toClassListText(defaultClassOrder(labelItems ?? [], importedClasses)),
    [defaultsPending, labelItems, importedClasses]
  )
  const classesText = classesDraft ?? defaultText
  const classList = useMemo(() => parseClassList(classesText), [classesText])

  // 将要使用的类别顺序（还不知道或无效时为 undefined），与标签对照。
  const orderClasses = inProjects
    ? projectClasses
    : classList.error === null
      ? classList.classes
      : undefined
  const coverage = useMemo(
    () =>
      labelItems && orderClasses && orderClasses.length > 0
        ? compareClassesWithLabels(orderClasses, labelItems)
        : null,
    [labelItems, orderClasses]
  )

  // 类别顺序还没确定（所选项目的类别列表、默认选择哪个项目、默认的手动顺序）
  // 时不能下载：页面上还看不到将要使用的顺序。读取失败时可以下载，由服务端
  // 按项目当前的类别列表生成。
  const waiting =
    isYolo &&
    (inProjects
      ? projectList
        ? projectList.isPending
        : projectLists.some((list) => list.isPending)
      : classesDraft === null && defaultsPending)
  // 所选项目还没有类别列表：服务端也会拒绝，页面上提示去项目中设置。
  const noClasses = isYolo && inProjects && projectClasses?.length === 0

  const mutation = useMutation({
    mutationFn: (input: CreateExportInput) => createExport(dataset.id, input),
    onSuccess: (result) => {
      startDownload(result.url, result.file_name)
      toast.success("已开始下载", {
        description:
          "压缩包在下载时实时生成，数据集较大时需要一些时间，可在浏览器的下载列表中查看进度。",
      })
      onDone()
    },
    onError: (error, input) => {
      // 多半是项目已删除、数据集已移出项目或类别列表已被清空：刷新项目与
      // 数据集的信息，对话框中显示最新的选项与类别顺序（或改为手动指定）。
      if (input.project_id !== undefined && isPermanentError(error)) {
        refreshMembership()
      }
    },
  })

  const changeFormat = (next: AnnotationFormat) => {
    setFormat(next)
    mutation.reset()
  }
  const chooseProject = (id: number) => {
    setChosenProjectId(id)
    mutation.reset()
  }
  const changeClasses = (text: string) => {
    setClassesDraft(text)
    mutation.reset()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (mutation.isPending || waiting || noClasses) {
      return
    }
    setSubmitted(true)
    let input: CreateExportInput = { format }
    if (isYolo && inProjects) {
      if (!project) {
        projectGroupRef.current
          ?.querySelector<HTMLElement>('[role="radio"]')
          ?.focus()
        return
      }
      input = { format, project_id: project.id }
    } else if (isYolo) {
      if (classList.error) {
        classFieldRef.current?.focus()
        return
      }
      input = { format, classes: classList.classes }
    }
    mutation.mutate(input)
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>下载数据集</DialogTitle>
        <DialogDescription>
          将“{dataset.name}”的 {formatNumber(dataset.image_count)} 张图片（
          {formatBytes(dataset.total_bytes)}
          ）与标注打包为 zip。数据集在服务器上以 VOC
          格式保存，其它格式在下载时转换。
        </DialogDescription>
      </DialogHeader>

      <AnnotationFormatPicker
        label="下载格式"
        value={format}
        onChange={changeFormat}
        descriptions={exportDescriptions}
        disabled={mutation.isPending}
      />

      {isYolo && (
        <div className="grid gap-4">
          {inProjects ? (
            <ProjectClassOrder
              projects={projects}
              lists={projectLists}
              index={projectIndex}
              chosen={chosenIndex >= 0}
              classes={projectClasses}
              onChoose={chooseProject}
              showRequired={submitted}
              disabled={mutation.isPending}
              groupRef={projectGroupRef}
            />
          ) : (
            <ClassListField
              value={classesText}
              onChange={changeClasses}
              result={classList}
              showRequired={submitted}
              description={
                <>
                  数据集还没有纳入项目，需要手动指定类别顺序（纳入项目后可使用项目的类别顺序）。
                  {defaultsPending
                    ? "正在读取数据集中的标签…"
                    : labels.error
                      ? `无法读取数据集中的标签（${labels.error.message}），请手动填写。`
                      : lastYolo.error
                        ? `无法读取导入记录（${lastYolo.error.message}），默认按标签名称排序；请确认与训练时使用的 classes.txt 一致。`
                        : yoloImport
                          ? `默认沿用导入“${yoloImport.file_name}”时的类别顺序（类别编号不变），其余标签按名称排在后面；可直接编辑，或读取训练时使用的 classes.txt。`
                          : "默认按数据集中的标签名称排序；可直接编辑，或读取训练时使用的 classes.txt 以保持类别编号一致。"}
                </>
              }
              onReset={
                classesDraft !== null && classesDraft !== defaultText
                  ? () => setClassesDraft(null)
                  : undefined
              }
              disabled={mutation.isPending}
              textareaRef={classFieldRef}
            />
          )}

          {coverage && <CoverageNotes {...coverage} />}
        </div>
      )}

      {format !== "voc" && dataset.invalid_count > 0 && (
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <TriangleAlert className="mt-px size-3.5 shrink-0 text-amber-600 dark:text-amber-500" />
          {formatNumber(dataset.invalid_count)}{" "}
          张图片的标注无法解析，导出时只包含图片。
        </p>
      )}

      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onDone}
          disabled={mutation.isPending}
        >
          取消
        </Button>
        <Button
          type="submit"
          disabled={mutation.isPending || waiting || noClasses}
        >
          {mutation.isPending ? <Spinner aria-hidden /> : <Download />}
          下载
        </Button>
      </DialogFooter>
    </form>
  )
}

type ProjectClassOrderProps = {
  /** 数据集所属的项目（至少一个），按名称排序。 */
  projects: readonly DatasetProjectRef[]
  /** 各项目的类别列表查询，与 `projects` 一一对应。 */
  lists: readonly UseQueryResult<ProjectClassList>[]
  /** 使用的项目在 `projects` 中的下标；还没有选择时为 -1。 */
  index: number
  /** `index` 是用户选的，而不是默认选中的。 */
  chosen: boolean
  /** 所用项目的类别名称（类别列表还没读到时为 undefined）。 */
  classes: readonly string[] | undefined
  onChoose: (projectId: number) => void
  /** 没有选择项目时是否提示，一般在尝试提交之后。 */
  showRequired: boolean
  disabled: boolean
  groupRef: Ref<HTMLDivElement>
}

/**
 * 按项目的类别列表生成类别编号：只属于一个项目时直接使用它；属于多个项目时
 * 列出各项目（及类别数）供选择。下面是所用项目的类别编号预览，或读取中 /
 * 读取失败 / 还没有类别列表的提示。
 */
function ProjectClassOrder({
  projects,
  lists,
  index,
  chosen,
  classes,
  onChoose,
  showRequired,
  disabled,
  groupRef,
}: ProjectClassOrderProps) {
  const groupId = useId()
  const several = projects.length > 1
  const project = index >= 0 ? projects[index] : null
  const list = index >= 0 ? lists[index] : null
  const error = showRequired && !project ? PROJECT_REQUIRED_MESSAGE : null

  // 多个项目时默认选中的原因（见 defaultOrderIndex）。
  let defaultNote: string | null = null
  if (several && project && !chosen) {
    const withClasses = lists.filter((item) => item.data?.items.length)
    defaultNote =
      withClasses.length === 1
        ? `只有项目“${project.name}”有类别列表，已默认选择它。`
        : withClasses.length === lists.length
          ? `各项目的类别顺序相同，选择哪个下载结果都一样，已默认选择“${project.name}”。`
          : `有类别列表的项目类别顺序都相同，已默认选择“${project.name}”。`
  }

  return (
    <Field data-invalid={error ? true : undefined} className="gap-2">
      <FieldTitle id={groupId}>类别顺序</FieldTitle>
      <FieldDescription className="text-xs">
        {several
          ? `数据集属于 ${formatNumber(projects.length)} 个项目，类别编号按所选项目的类别列表的顺序（第一个为 0），请选择训练时要使用的项目。`
          : `类别编号按项目“${projects[0].name}”的类别列表的顺序（第一个为 0），与使用这个项目训练时一致。`}
      </FieldDescription>

      {several && (
        <RadioGroup
          ref={groupRef}
          value={project ? String(project.id) : null}
          onValueChange={(value) => onChoose(Number(value))}
          aria-labelledby={groupId}
          aria-invalid={error ? true : undefined}
          className="grid gap-2 sm:grid-cols-2"
          disabled={disabled}
        >
          {projects.map((option, optionIndex) => {
            const itemId = `${groupId}-${option.id}`
            return (
              <label
                key={option.id}
                htmlFor={itemId}
                className={cn(
                  // 字段无效时文字会变成红色，选项的名称不跟着变。
                  "flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 text-foreground transition-colors hover:bg-muted/40",
                  optionIndex === index && "border-primary/60 bg-muted/40"
                )}
              >
                <RadioGroupItem
                  id={itemId}
                  value={String(option.id)}
                  className="mt-0.5"
                />
                <span className="grid min-w-0 gap-1">
                  <span className="truncate text-sm font-medium">
                    {option.name}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {listSummary(lists[optionIndex])}
                  </span>
                </span>
              </label>
            )
          })}
        </RadioGroup>
      )}

      {error && <FieldError>{error}</FieldError>}
      {defaultNote && (
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <Info className="mt-px size-3.5 shrink-0" />
          {defaultNote}
        </p>
      )}

      {project && list && (
        <ProjectClassState project={project} list={list} classes={classes} />
      )}
    </Field>
  )
}

/** 选项中的类别列表概况。 */
function listSummary(list: UseQueryResult<ProjectClassList>) {
  if (list.data) {
    const count = list.data.items.length
    return count > 0 ? `${formatNumber(count)} 个类别` : "还没有类别列表"
  }
  return list.isPending ? "正在读取类别列表…" : "无法读取类别列表"
}

/** 所用项目的类别编号预览；读取中、读取失败（可重试）或还没有类别列表时的提示。 */
function ProjectClassState({
  project,
  list,
  classes,
}: {
  project: DatasetProjectRef
  list: UseQueryResult<ProjectClassList>
  classes: readonly string[] | undefined
}) {
  const path = projectClassesPath(project.id)

  if (classes && classes.length > 0) {
    return (
      <ClassIdPreview
        classes={classes}
        action={
          <Button
            asChild
            variant="link"
            size="xs"
            className="h-auto px-0 has-[>svg]:px-0"
          >
            <Link to={path}>
              <Pencil />
              在项目中修改
            </Link>
          </Button>
        }
      />
    )
  }
  if (classes) {
    return (
      <Alert>
        <Shapes />
        <AlertTitle>项目“{project.name}”还没有类别列表</AlertTitle>
        <AlertDescription>
          <p>
            YOLO
            的类别编号来自项目的类别列表，请先在项目中设置类别（可以从项目中数据集的标签生成），再下载。
          </p>
          <Button asChild variant="outline" size="sm" className="mt-1">
            <Link to={path}>去项目中设置类别</Link>
          </Button>
        </AlertDescription>
      </Alert>
    )
  }
  if (list.isPending) {
    return (
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Spinner className="size-3.5" aria-hidden />
        正在读取项目“{project.name}”的类别列表…
      </p>
    )
  }
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-destructive"
    >
      <span className="min-w-0 break-words">
        无法读取项目“{project.name}”的类别列表：{list.error?.message}
      </span>
      <Button
        type="button"
        variant="outline"
        size="xs"
        className="text-foreground"
        onClick={() => void list.refetch()}
        disabled={list.isFetching}
      >
        <RefreshCw className={cn(list.isFetching && "animate-spin")} />
        {list.isFetching ? "正在重试…" : "重试"}
      </Button>
    </div>
  )
}

/**
 * 类别顺序与数据集标签的对照：不在列表中的标签（它们的目标不会导出），以及
 * 数据集中没有目标、但仍会写入 classes.txt 的类别。
 */
function CoverageNotes({
  missing,
  unused,
}: {
  missing: readonly DatasetLabel[]
  unused: readonly string[]
}) {
  const missingObjects = missing.reduce(
    (sum, label) => sum + label.object_count,
    0
  )
  return (
    <>
      {missing.length > 0 && (
        <Alert>
          <TriangleAlert className="text-amber-600 dark:text-amber-500" />
          <AlertTitle>
            {formatNumber(missing.length)} 个标签不在类别列表中
          </AlertTitle>
          <AlertDescription>
            <p>
              这些标签的目标不会导出（共 {formatNumber(missingObjects)}{" "}
              个目标）：
            </p>
            <ul className="flex flex-wrap gap-1.5">
              {missing.slice(0, LIST_LIMIT).map((label) => (
                <li key={label.name} className="max-w-full">
                  <Badge
                    variant="outline"
                    className="max-w-56 font-normal"
                    title={label.name}
                  >
                    <span className="truncate">{label.name}</span>
                    <span className="text-muted-foreground tabular-nums">
                      {formatNumber(label.object_count)}
                    </span>
                  </Badge>
                </li>
              ))}
              {missing.length > LIST_LIMIT && (
                <li>
                  <Badge variant="secondary" className="font-normal">
                    还有 {formatNumber(missing.length - LIST_LIMIT)} 个
                  </Badge>
                </li>
              )}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {unused.length > 0 && (
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <Info className="mt-px size-3.5 shrink-0" />
          <span>
            数据集中没有这些类别的目标：
            {unused.slice(0, LIST_LIMIT).join("、")}
            {unused.length > LIST_LIMIT &&
              ` 等 ${formatNumber(unused.length)} 个`}
            。它们仍会按顺序写入 classes.txt。
          </span>
        </p>
      )}
    </>
  )
}
