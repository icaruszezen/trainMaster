import { useId, useMemo, useRef, useState, type FormEvent } from "react"
import { useQuery } from "@tanstack/react-query"
import { FileArchive, FolderOpen, FolderUp } from "lucide-react"
import { useNavigate } from "react-router"
import { toast } from "sonner"

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
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import {
  createDataset,
  createImport,
  datasetKeys,
  datasetsQuery,
  deleteDataset,
  directoriesQuery,
  type Dataset,
  type DatasetDirectory,
} from "@/features/datasets/api"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import {
  DirectoryPicker,
  type DirectoryRowState,
} from "@/features/datasets/directory-picker"
import {
  ImportFormatFields,
  useImportFormat,
} from "@/features/datasets/import-format-fields"
import { watchImport } from "@/features/datasets/import-watch"
import {
  archiveBaseName,
  isNameTaken,
  suggestDatasetName,
  validateDatasetName,
  validateDescription,
} from "@/features/datasets/naming"
import {
  ArchivePicker,
  FolderPicker,
  useFolderSummary,
  type FolderSelection,
} from "@/features/datasets/source-pickers"
import {
  startArchiveUpload,
  startFolderUpload,
} from "@/features/datasets/upload-manager"
import { useDatasetsRoot } from "@/features/datasets/use-datasets-root"
import { ApiError } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type CreateTab = "archive" | "folder" | "directory"

type CreateDatasetDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  initialTab?: CreateTab
}

/**
 * 新建数据集：上传压缩包、上传文件夹，或把 datasets/ 下已有的文件夹添加为数据集。
 * 每次打开时由调用方更换 key，表单状态随之重置（关闭动画期间保持不变）。
 */
export function CreateDatasetDialog({
  open,
  onOpenChange,
  initialTab = "archive",
}: CreateDatasetDialogProps) {
  const navigate = useNavigate()
  const root = useDatasetsRoot()
  const nameId = useId()
  const descriptionId = useId()
  const nameInputRef = useRef<HTMLInputElement>(null)
  const classFieldRef = useRef<HTMLTextAreaElement>(null)

  const [tab, setTab] = useState<CreateTab>(initialTab)
  const [archive, setArchive] = useState<File | null>(null)
  const [folder, setFolder] = useState<FolderSelection | null>(null)
  const [selectedNames, setSelectedNames] = useState<string[]>([])
  // 用户编辑过的名称（按标签页分开）；null 表示沿用由来源自动生成的名称。
  const [nameDrafts, setNameDrafts] = useState<
    Record<CreateTab, string | null>
  >({ archive: null, folder: null, directory: null })
  const [description, setDescription] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  // 服务端对某个名称的报错（如 409 重名）；名称改动后不再显示。
  const [serverNameError, setServerNameError] = useState<{
    name: string
    message: string
  } | null>(null)
  const [rowStates, setRowStates] = useState<
    ReadonlyMap<string, DirectoryRowState>
  >(new Map())
  // 上传内容的标注格式；“已有目录”原地登记文件夹，始终按 VOC 扫描。
  const annotation = useImportFormat()
  const folderSummary = useFolderSummary(folder, annotation.format)

  const datasets = useQuery(datasetsQuery())
  const directories = useQuery({ ...directoriesQuery(), enabled: open })

  const existingNames = useMemo(
    () => datasets.data?.items.map((dataset) => dataset.name) ?? [],
    [datasets.data]
  )
  const directoryList = useMemo(
    () => directories.data?.items ?? [],
    [directories.data]
  )

  // 已被添加（例如上一次部分成功的提交）的目录自动从选择中去掉。
  const selectedDirectories = useMemo(
    () =>
      selectedNames
        .map((name) => directoryList.find((d) => d.name === name))
        .filter((d): d is DatasetDirectory => d !== undefined && !d.registered),
    [selectedNames, directoryList]
  )
  const selectedSet = useMemo(
    () => new Set(selectedDirectories.map((d) => d.name)),
    [selectedDirectories]
  )
  const singleDirectory =
    selectedDirectories.length === 1 ? selectedDirectories[0] : null

  const autoName =
    tab === "archive"
      ? archive
        ? suggestDatasetName(archiveBaseName(archive.name))
        : ""
      : tab === "folder"
        ? suggestDatasetName(folder?.name ?? "")
        : suggestDatasetName(singleDirectory?.name ?? "")
  const name = nameDrafts[tab] ?? autoName
  const isUpload = tab !== "directory"
  const showNameFields = isUpload || singleDirectory !== null

  const sameFolder = isUpload
    ? directoryList.find(
        (d) => d.name.toLowerCase() === name.trim().toLowerCase()
      )
    : undefined
  const nameProblem =
    validateDatasetName(name) ??
    (isNameTaken(name, existingNames) ? "已存在同名数据集" : null) ??
    (sameFolder
      ? sameFolder.registered
        ? `数据目录中已存在同名文件夹“${sameFolder.name}”`
        : `数据目录中已存在同名文件夹“${sameFolder.name}”，可在“已有目录”中直接添加为数据集`
      : null)
  const visibleNameError =
    (submitted || name !== "" ? nameProblem : null) ??
    (serverNameError?.name === name ? serverNameError.message : null)
  const descriptionProblem = validateDescription(description)

  // 多选目录时每个目录以文件夹名作为数据集名称。
  const directoryProblems = useMemo(() => {
    const problems = new Map<string, string>()
    if (selectedDirectories.length < 2) {
      return problems
    }
    const seen = new Set<string>()
    for (const directory of selectedDirectories) {
      const key = directory.name.trim().toLowerCase()
      const problem =
        validateDatasetName(directory.name) ??
        (isNameTaken(directory.name, existingNames)
          ? "已存在同名数据集，请单独选择该目录并修改名称"
          : null) ??
        (seen.has(key) ? "与其他选中的目录重名" : null)
      seen.add(key)
      if (problem) {
        problems.set(directory.name, problem)
      }
    }
    for (const [dirName, state] of rowStates) {
      if (state.status === "error" && !problems.has(dirName)) {
        problems.set(dirName, state.message)
      }
    }
    return problems
  }, [selectedDirectories, existingNames, rowStates])

  const hasSource =
    tab === "archive"
      ? archive !== null
      : tab === "folder"
        ? folderSummary !== null && folderSummary.images > 0
        : selectedDirectories.length > 0
  const dirty =
    archive !== null ||
    folder !== null ||
    selectedNames.length > 0 ||
    annotation.dirty

  const selectFolder = (selection: FolderSelection | null) => {
    setFolder(selection)
    annotation.prefillFromFolder(selection)
  }

  const changeTab = (next: CreateTab) => {
    setTab(next)
    // 文件夹里的 classes.txt 只对上传文件夹有效。
    annotation.prefillFromFolder(next === "folder" ? folder : null)
  }

  const setName = (value: string) => {
    setNameDrafts((drafts) => ({ ...drafts, [tab]: value }))
  }

  const toggleDirectory = (directory: DatasetDirectory, checked: boolean) => {
    setSelectedNames((names) =>
      checked
        ? [...names, directory.name]
        : names.filter((n) => n !== directory.name)
    )
    // 名称跟随选中的目录：选择变化后重新使用文件夹名。
    setNameDrafts((drafts) => ({ ...drafts, directory: null }))
    setRowStates((states) => {
      if (!states.has(directory.name)) {
        return states
      }
      const next = new Map(states)
      next.delete(directory.name)
      return next
    })
  }

  const close = () => onOpenChange(false)

  const reportCreateError = (error: unknown, forName: string) => {
    const message = error instanceof Error ? error.message : "创建失败"
    if (
      error instanceof ApiError &&
      (error.code === "conflict" || error.code === "validation_failed") &&
      !message.includes("描述")
    ) {
      setServerNameError({ name: forName, message })
      nameInputRef.current?.focus()
    } else {
      setFormError(message)
    }
  }

  const submitUpload = async () => {
    const trimmed = name.trim()
    let dataset: Dataset
    try {
      dataset = await createDataset({
        name: trimmed,
        description: description.trim() || undefined,
      })
    } catch (error) {
      reportCreateError(error, name)
      return
    }

    try {
      if (tab === "archive" && archive) {
        const job = await createImport(dataset.id, {
          source: "archive",
          file_name: archive.name,
          file_size: archive.size,
          ...annotation.importFields,
        })
        startArchiveUpload({ dataset, job, file: archive })
      } else if (folder && folderSummary) {
        const { uploads, uploadBytes } = folderSummary
        const job = await createImport(dataset.id, {
          source: "folder",
          file_name: folder.name || trimmed,
          file_size: uploadBytes,
          file_count: uploads.length,
          ...annotation.importFields,
        })
        startFolderUpload({ dataset, job, files: uploads })
      }
    } catch (error) {
      // 导入任务没建起来（如磁盘空间不足）：撤销刚创建的空数据集，
      // 用户修正后可以用同一个名称重试。
      const rollbackProblem = await deleteDataset(dataset.id, true).then(
        (result) => (result?.files_removed === false ? result.message : null),
        () =>
          `已创建的空数据集“${dataset.name}”未能自动删除，可在列表中手动删除`
      )
      void queryClient.invalidateQueries({ queryKey: datasetKeys.all })
      const message =
        error instanceof Error ? error.message : "创建导入任务失败"
      setFormError(
        rollbackProblem ? `${message}（${rollbackProblem}）` : message
      )
      return
    }

    void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
    void queryClient.invalidateQueries({ queryKey: datasetKeys.directories() })
    toast.success(`已创建数据集“${dataset.name}”`, {
      description: "正在上传，可在上传面板中查看进度；上传期间请不要关闭页面。",
    })
    close()
    navigate(`/datasets/${dataset.id}`)
  }

  const submitDirectories = async () => {
    const targets = singleDirectory
      ? [{ directory: singleDirectory.name, name: name.trim() }]
      : selectedDirectories.map((d) => ({
          directory: d.name,
          name: d.name.trim(),
        }))

    const created: Dataset[] = []
    let failed = 0
    for (const target of targets) {
      setRowStates((states) =>
        new Map(states).set(target.directory, { status: "pending" })
      )
      try {
        const dataset = await createDataset({
          name: target.name,
          directory: target.directory,
          description:
            singleDirectory && description.trim()
              ? description.trim()
              : undefined,
        })
        created.push(dataset)
        if (dataset.active_import) {
          watchImport(dataset.active_import, dataset.name)
        }
        setRowStates((states) =>
          new Map(states).set(target.directory, { status: "done" })
        )
      } catch (error) {
        failed += 1
        const message = error instanceof Error ? error.message : "添加失败"
        setRowStates((states) =>
          new Map(states).set(target.directory, { status: "error", message })
        )
        if (singleDirectory) {
          reportCreateError(error, name)
        }
      }
    }

    void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
    void queryClient.invalidateQueries({ queryKey: datasetKeys.directories() })

    if (created.length > 0) {
      toast.success(
        created.length === 1
          ? `已添加数据集“${created[0].name}”`
          : `已添加 ${created.length} 个数据集`,
        { description: "正在扫描文件夹中的图片与标注。" }
      )
    }
    if (failed > 0) {
      if (!singleDirectory) {
        setFormError(`${failed} 个目录添加失败，请查看列表中的提示`)
      }
      return
    }
    close()
    if (created.length === 1) {
      navigate(`/datasets/${created[0].id}`)
    }
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (submitting) {
      return
    }
    setSubmitted(true)
    setFormError(null)
    if (!hasSource) {
      return
    }
    if (showNameFields && (nameProblem || descriptionProblem)) {
      nameInputRef.current?.focus()
      return
    }
    if (isUpload && annotation.invalid) {
      classFieldRef.current?.focus()
      return
    }
    if (!showNameFields && directoryProblems.size > 0) {
      return
    }

    setSubmitting(true)
    try {
      if (isUpload) {
        await submitUpload()
      } else {
        await submitDirectories()
      }
    } finally {
      setSubmitting(false)
    }
  }

  const submitLabel = isUpload
    ? "创建并上传"
    : selectedDirectories.length > 1
      ? `添加 ${selectedDirectories.length} 个数据集`
      : "添加数据集"

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!submitting) {
          onOpenChange(next)
        }
      }}
    >
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        // 已选择了文件时，点到对话框外面不关闭，避免误操作丢失选择。
        onInteractOutside={(event) => {
          if (dirty || submitting) {
            event.preventDefault()
          }
        }}
        onEscapeKeyDown={(event) => {
          if (submitting) {
            event.preventDefault()
          }
        }}
        // 拖放落在拖放区之外时，阻止浏览器直接打开文件。
        onDragOver={(event) => event.preventDefault()}
        onDrop={(event) => event.preventDefault()}
      >
        <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
          <DialogHeader>
            <DialogTitle>新建数据集</DialogTitle>
            <DialogDescription>
              上传图片压缩包或整个文件夹（标注可以是 VOC、X-AnyLabeling 或 YOLO
              格式，统一转换为 VOC 保存），也可以把服务器上已有的 VOC
              数据集文件夹直接添加为数据集。
            </DialogDescription>
          </DialogHeader>

          <Tabs
            value={tab}
            onValueChange={(value) => {
              changeTab(value as CreateTab)
              setSubmitted(false)
              setFormError(null)
            }}
            className="min-w-0 gap-4"
          >
            <TabsList className="grid w-full grid-cols-3">
              <TabsTrigger value="archive" disabled={submitting}>
                <FileArchive className="hidden sm:block" />
                上传压缩包
              </TabsTrigger>
              <TabsTrigger value="folder" disabled={submitting}>
                <FolderUp className="hidden sm:block" />
                上传文件夹
              </TabsTrigger>
              <TabsTrigger value="directory" disabled={submitting}>
                <FolderOpen className="hidden sm:block" />
                已有目录
              </TabsTrigger>
            </TabsList>
            <TabsContent value="archive">
              <ArchivePicker
                file={archive}
                onChange={setArchive}
                format={annotation.format}
                disabled={submitting}
              />
            </TabsContent>
            <TabsContent value="folder">
              <FolderPicker
                selection={folder}
                summary={folderSummary}
                onChange={selectFolder}
                format={annotation.format}
                onFormatChange={annotation.setFormat}
                disabled={submitting}
              />
            </TabsContent>
            <TabsContent value="directory">
              <DirectoryPicker
                root={root}
                selected={selectedSet}
                onToggle={toggleDirectory}
                problems={directoryProblems}
                rowStates={rowStates}
                disabled={submitting}
              />
            </TabsContent>
          </Tabs>

          {isUpload && (
            <ImportFormatFields
              state={annotation}
              showErrors={submitted}
              disabled={submitting}
              classFieldRef={classFieldRef}
            />
          )}

          {showNameFields && (
            <FieldGroup className="gap-5">
              <Field data-invalid={visibleNameError ? true : undefined}>
                <FieldLabel htmlFor={nameId}>数据集名称</FieldLabel>
                <Input
                  ref={nameInputRef}
                  id={nameId}
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder={
                    isUpload ? "如：划痕检测-2026 批次 1" : "默认使用文件夹名"
                  }
                  aria-invalid={visibleNameError ? true : undefined}
                  disabled={submitting}
                  autoComplete="off"
                />
                {visibleNameError ? (
                  <FieldError>
                    {visibleNameError}
                    {sameFolder && !sameFolder.registered && (
                      <Button
                        type="button"
                        variant="link"
                        size="sm"
                        className="h-auto px-1 py-0 align-baseline text-xs underline"
                        onClick={() => {
                          changeTab("directory")
                          setSelectedNames([sameFolder.name])
                        }}
                      >
                        去添加
                      </Button>
                    )}
                  </FieldError>
                ) : (
                  <FieldDescription className="text-xs">
                    {isUpload ? (
                      <>
                        将创建文件夹{" "}
                        <code className="font-mono text-foreground">
                          datasets/{name.trim() || "名称"}/
                        </code>
                        ，导入的文件保持原有的子目录结构
                      </>
                    ) : (
                      <>
                        直接使用已有文件夹{" "}
                        <code className="font-mono text-foreground">
                          datasets/{singleDirectory?.name}/
                        </code>
                        ，文件不会被移动或复制
                      </>
                    )}
                  </FieldDescription>
                )}
              </Field>
              <Field data-invalid={descriptionProblem ? true : undefined}>
                <FieldLabel htmlFor={descriptionId}>
                  描述
                  <span className="font-normal text-muted-foreground">
                    （可选）
                  </span>
                </FieldLabel>
                <Textarea
                  id={descriptionId}
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder="数据来源、产线、工位、批次等"
                  aria-invalid={descriptionProblem ? true : undefined}
                  disabled={submitting}
                  className="min-h-20"
                />
                {descriptionProblem ? (
                  <FieldError>{descriptionProblem}</FieldError>
                ) : (
                  <DescriptionCounter value={description} />
                )}
              </Field>
            </FieldGroup>
          )}

          {formError && (
            <p role="alert" className="text-sm text-destructive">
              {formError}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={close}
              disabled={submitting}
            >
              取消
            </Button>
            <Button type="submit" disabled={submitting || !hasSource}>
              {submitting && <Spinner aria-hidden />}
              {submitting ? "正在创建…" : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
