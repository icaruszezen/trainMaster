/**
 * 新建模型评估页（`/evaluation/new`）：
 * - 支持 URL 预填 `?project_id=&model_id=&data_version_id=&dataset_id=`
 * - 支持批量多选同数据源的多个检测框模型（一次下发多条评估任务）
 * - 支持两种数据源：冻结「数据版本」（多选 val/test/train 划分）或「多数据集叠加」（可选基础标注 + 审图版本 + 图片范围）
 * - 右侧实时防抖调用 `POST /api/v1/evaluations/preview`，展示 OK/NG 分布、标签统计、风险告警与各模型类别对齐徽章
 */
import { useEffect, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Database,
  Gauge,
  Layers,
  Plus,
  Trash2,
} from "lucide-react"
import { Link, useNavigate, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { dataVersionsQuery } from "@/features/data-versions/api"
import {
  datasetsQuery,
  datasetVersionsQuery,
  type Dataset,
} from "@/features/datasets/api"
import {
  createEvaluations,
  previewEvaluation,
  refreshEvaluations,
  type DatasetSourceInput,
  type EvaluationSourceType,
  type ImageMode,
  type PreviewResult,
  type SourceInput,
} from "@/features/evaluation/api"
import {
  evaluationModelBlocker,
  IMAGE_MODE_LABELS,
  SPLIT_LABELS,
} from "@/features/evaluation/format"
import {
  evaluationDetailPath,
  EVALUATIONS_PATH,
  parsePositiveId,
} from "@/features/evaluation/paths"
import { modelsQuery, type Model } from "@/features/models/api"
import { modelTitle } from "@/features/models/model-info"
import { yoloVersionLabel } from "@/features/models/yolo-versions"
import { projectsQuery } from "@/features/projects/api"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type DatasetDraft = {
  dataset_id: number
  include_base: boolean
  version_ids: number[]
  image_mode: ImageMode
  include_empty: boolean
}

export function NewEvaluationPage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  const prefillProjectId = parsePositiveId(searchParams.get("project_id"))
  const prefillModelId = parsePositiveId(searchParams.get("model_id"))
  const prefillDvId = parsePositiveId(searchParams.get("data_version_id"))
  const prefillDatasetId = parsePositiveId(searchParams.get("dataset_id"))

  // undefined 表示未手动覆盖，允许按 prefillModelId 自动带出所属项目
  const [projectFilterOverride, setProjectFilterOverride] = useState<
    number | null | undefined
  >(prefillProjectId ?? undefined)
  const [selectedModelIds, setSelectedModelIds] = useState<number[]>(
    prefillModelId ? [prefillModelId] : []
  )
  const [sourceType, setSourceType] = useState<EvaluationSourceType>(
    prefillDatasetId && !prefillDvId ? "datasets" : "data_version"
  )
  const [dataVersionIdOverride, setDataVersionIdOverride] = useState<
    number | null
  >(prefillDvId)
  const [splitsOverride, setSplitsOverride] = useState<string[] | null>(null)
  const [datasetDrafts, setDatasetDrafts] = useState<DatasetDraft[]>(
    prefillDatasetId
      ? [
          {
            dataset_id: prefillDatasetId,
            include_base: true,
            version_ids: [],
            image_mode: "annotated",
            include_empty: true,
          },
        ]
      : []
  )

  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [conf, setConf] = useState("0.25")
  const [iou, setIou] = useState("0.50")
  const [imgsz, setImgsz] = useState("")
  const [batch, setBatch] = useState("16")
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [nmsIou, setNmsIou] = useState("0.70")
  const [maxDet, setMaxDet] = useState("300")
  const [half, setHalf] = useState(false)
  const [device, setDevice] = useState("")

  const projects = useQuery(projectsQuery())
  const models = useQuery(modelsQuery())

  // 根据预填的 modelId 自动推导其项目筛选
  const inferredProjectId = useMemo(() => {
    if (!prefillModelId || !models.data) {
      return null
    }
    return (
      models.data.items.find((x) => x.id === prefillModelId)?.project.id ?? null
    )
  }, [prefillModelId, models.data])

  const projectFilter =
    projectFilterOverride !== undefined
      ? projectFilterOverride
      : inferredProjectId

  const dataVersions = useQuery(dataVersionsQuery(projectFilter))
  const datasets = useQuery(datasetsQuery())

  const filteredModels = useMemo(() => {
    const all = models.data?.items ?? []
    if (!projectFilter) {
      return all
    }
    return all.filter((m) => m.project.id === projectFilter)
  }, [models.data, projectFilter])

  const readyDataVersions = useMemo(
    () =>
      (dataVersions.data?.items ?? []).filter((dv) => dv.status === "ready"),
    [dataVersions.data]
  )

  // 未手动选择时默认选中最新一个就绪的数据版本
  const dataVersionId = useMemo(() => {
    if (dataVersionIdOverride !== null) {
      return dataVersionIdOverride
    }
    if (sourceType === "data_version" && readyDataVersions.length > 0) {
      return readyDataVersions[0].id
    }
    return null
  }, [dataVersionIdOverride, sourceType, readyDataVersions])

  const selectedDataVersion = useMemo(
    () => readyDataVersions.find((dv) => dv.id === dataVersionId) ?? null,
    [readyDataVersions, dataVersionId]
  )

  const splits = useMemo(() => {
    if (splitsOverride !== null) {
      return splitsOverride
    }
    if (!selectedDataVersion) {
      return ["val"]
    }
    if (selectedDataVersion.val_count > 0) {
      return ["val"]
    }
    if (selectedDataVersion.test_count > 0) {
      return ["test"]
    }
    return ["train"]
  }, [splitsOverride, selectedDataVersion])

  const datasetMap = useMemo(() => {
    const m = new Map<number, Dataset>()
    for (const d of datasets.data?.items ?? []) {
      m.set(d.id, d)
    }
    return m
  }, [datasets.data])

  const availableDatasetsToAdd = useMemo(() => {
    const chosen = new Set(datasetDrafts.map((d) => d.dataset_id))
    return (datasets.data?.items ?? []).filter((d) => {
      if (chosen.has(d.id) || d.directory_missing) {
        return false
      }
      if (!projectFilter) {
        return true
      }
      return d.projects.some((p) => p.id === projectFilter)
    })
  }, [datasets.data, datasetDrafts, projectFilter])

  // 组装 SourceInput（合法时非 null）
  const sourceInput = useMemo<SourceInput | null>(() => {
    if (sourceType === "data_version") {
      if (!dataVersionId || splits.length === 0) {
        return null
      }
      return {
        type: "data_version",
        data_version_id: dataVersionId,
        splits,
      }
    }
    if (datasetDrafts.length === 0) {
      return null
    }
    const dsInputs: DatasetSourceInput[] = datasetDrafts.map((d) => ({
      dataset_id: d.dataset_id,
      include_base: d.include_base,
      version_ids: d.version_ids,
      image_mode: d.image_mode,
      include_empty: d.include_empty,
    }))
    return {
      type: "datasets",
      datasets: dsInputs,
    }
  }, [sourceType, dataVersionId, splits, datasetDrafts])

  // 防抖预览对齐结果（仅在异步回调中写入状态）
  const [previewResult, setPreviewResult] = useState<{
    key: string
    data: PreviewResult | null
    error: string | null
  }>({ key: "", data: null, error: null })

  const previewKey = useMemo(
    () =>
      sourceInput
        ? JSON.stringify({ model_ids: selectedModelIds, source: sourceInput })
        : "",
    [selectedModelIds, sourceInput]
  )

  useEffect(() => {
    if (!sourceInput || !previewKey) {
      return
    }
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const res = await previewEvaluation(
          { model_ids: selectedModelIds, source: sourceInput },
          controller.signal
        )
        if (!controller.signal.aborted) {
          setPreviewResult({ key: previewKey, data: res, error: null })
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          setPreviewResult({
            key: previewKey,
            data: null,
            error: err instanceof Error ? err.message : "预览数据源失败",
          })
        }
      }
    }, 260)

    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [previewKey, selectedModelIds, sourceInput])

  const preview =
    previewKey && previewResult.key === previewKey ? previewResult.data : null
  const previewError =
    previewKey && previewResult.key === previewKey ? previewResult.error : null
  const previewLoading = Boolean(previewKey) && previewResult.key !== previewKey

  const createMut = useMutation({
    mutationFn: () => {
      if (!sourceInput) {
        throw new Error("请先配置评估数据源")
      }
      if (selectedModelIds.length === 0) {
        throw new Error("请至少选择一个模型")
      }
      const parsedConf = Number(conf)
      const parsedIou = Number(iou)
      const parsedNms = Number(nmsIou)
      const parsedBatch = Number(batch)
      const parsedMaxDet = Number(maxDet)
      const imgszTrim = imgsz.trim()
      const parsedImgsz = imgszTrim
        ? imgszTrim
            .split(/[,xX\s]+/)
            .map((s) => Number(s.trim()))
            .filter((n) => Number.isFinite(n) && n > 0)
        : undefined

      return createEvaluations({
        model_ids: selectedModelIds,
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        source: sourceInput,
        options: {
          conf: Number.isFinite(parsedConf) ? parsedConf : 0.25,
          iou: Number.isFinite(parsedIou) ? parsedIou : 0.5,
          nms_iou: Number.isFinite(parsedNms) ? parsedNms : 0.7,
          batch: Number.isFinite(parsedBatch) ? parsedBatch : 16,
          max_det: Number.isFinite(parsedMaxDet) ? parsedMaxDet : 300,
          imgsz: parsedImgsz && parsedImgsz.length > 0 ? parsedImgsz : undefined,
          half,
          device: device.trim() || undefined,
        },
      })
    },
    onSuccess: (res) => {
      refreshEvaluations()
      if (res.items.length === 1) {
        toast.success("已创建评估任务，等待训练节点执行")
        navigate(evaluationDetailPath(res.items[0].id))
      } else {
        toast.success(`已批量创建 ${res.items.length} 个同源评估任务`)
        navigate(EVALUATIONS_PATH)
      }
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const toggleModel = (model: Model) => {
    if (evaluationModelBlocker(model) !== null) {
      return
    }
    setSelectedModelIds((prev) => {
      if (prev.includes(model.id)) {
        return prev.filter((id) => id !== model.id)
      }
      if (prev.length >= 12) {
        toast.info("单次最多批量创建 12 个模型的评估任务")
        return prev
      }
      return [...prev, model.id]
    })
  }

  const toggleSplit = (split: string) => {
    setSplitsOverride((prev) => {
      const cur = prev ?? splits
      return cur.includes(split)
        ? cur.length > 1
          ? cur.filter((s) => s !== split)
          : cur
        : [...cur, split]
    })
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    createMut.mutate()
  }

  const canSubmit =
    selectedModelIds.length > 0 &&
    sourceInput !== null &&
    (preview === null || preview.source.total_images > 0) &&
    !createMut.isPending

  return (
    <form onSubmit={handleSubmit} className="grid min-w-0 gap-6 pb-10">
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={EVALUATIONS_PATH}>
            <ArrowLeft />
            返回模型评估列表
          </Link>
        </Button>
        <PageHeader
          title="新建模型评估"
          description="选择一个或多个检测模型与验证数据源；右侧实时预览样本规模、OK/NG 比例及模型类别对齐状态。"
        />
      </div>

      <div className="grid items-start gap-6 xl:grid-cols-[1fr_24rem]">
        {/* 左栏：配置表单 */}
        <div className="grid min-w-0 gap-5">
          {/* 1. 选择模型 */}
          <Card className="py-4">
            <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 px-5 pb-3">
              <div>
                <CardTitle className="text-base">
                  1. 选择待评估模型（支持批量多选）
                </CardTitle>
                <CardDescription>
                  一次勾选多个模型将在同一评估数据源上批量下发，完成后可直接开启「同源逐图差异对比」。
                </CardDescription>
              </div>
              <Select
                value={projectFilter ? String(projectFilter) : "__all__"}
                onValueChange={(v) => {
                  const next = v === "__all__" ? null : Number(v)
                  setProjectFilterOverride(next)
                  setDataVersionIdOverride(null)
                  setSplitsOverride(null)
                }}
              >
                <SelectTrigger className="h-8 w-44 text-xs">
                  <SelectValue placeholder="按项目筛选" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__all__">全部项目</SelectItem>
                  {projects.data?.items.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </CardHeader>
            <CardContent className="px-5">
              {models.isPending ? (
                <div className="grid gap-2 sm:grid-cols-2">
                  {[0, 1, 2, 3].map((i) => (
                    <Skeleton key={i} className="h-16 rounded-lg" />
                  ))}
                </div>
              ) : filteredModels.length === 0 ? (
                <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                  当前筛选范围内没有可用模型，请先在模型仓库上传或通过训练任务入库模型。
                </div>
              ) : (
                <div className="grid max-h-72 gap-2 overflow-y-auto pr-1 sm:grid-cols-2">
                  {filteredModels.map((m) => {
                    const blocker = evaluationModelBlocker(m)
                    const checked = selectedModelIds.includes(m.id)
                    return (
                      <label
                        key={m.id}
                        title={blocker ?? undefined}
                        className={cn(
                          "flex cursor-pointer items-start gap-2.5 rounded-lg border p-3 text-xs transition-colors",
                          blocker
                            ? "cursor-not-allowed bg-muted/40 opacity-55"
                            : checked
                              ? "border-primary bg-primary/[0.04]"
                              : "hover:bg-muted/30"
                        )}
                      >
                        <Checkbox
                          checked={checked}
                          disabled={blocker !== null}
                          onCheckedChange={() => toggleModel(m)}
                          className="mt-0.5"
                        />
                        <div className="min-w-0 flex-1 space-y-1">
                          <div className="flex items-center justify-between gap-1">
                            <span className="truncate font-semibold text-foreground">
                              {modelTitle(m)}
                            </span>
                            <Badge
                              variant="outline"
                              className="h-4 shrink-0 px-1 text-[10px]"
                            >
                              {yoloVersionLabel(m.yolo_version)}
                              {m.info.scale || ""}
                            </Badge>
                          </div>
                          <div className="truncate text-muted-foreground">
                            {blocker
                              ? blocker
                              : `${m.info.class_names.length} 个类别${m.info.class_names.length > 0 ? `：${m.info.class_names.slice(0, 3).join("、")}${m.info.class_names.length > 3 ? " 等" : ""}` : ""}`}
                          </div>
                        </div>
                      </label>
                    )
                  })}
                </div>
              )}
            </CardContent>
          </Card>

          {/* 2. 选择评估数据源 */}
          <Card className="py-4">
            <CardHeader className="px-5 pb-3">
              <CardTitle className="text-base">2. 选择评估数据源</CardTitle>
              <CardDescription>
                推荐使用冻结的「数据版本」验证集/测试集以保证跨迭代可复现；也可直接组合多个数据集及审图版本。
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 px-5">
              <div className="grid grid-cols-2 gap-2.5">
                <button
                  type="button"
                  onClick={() => setSourceType("data_version")}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg border p-3 text-left transition-colors",
                    sourceType === "data_version"
                      ? "border-primary bg-primary/[0.05] text-foreground"
                      : "text-muted-foreground hover:bg-muted/30"
                  )}
                >
                  <Layers className="size-4 shrink-0" />
                  <div>
                    <div className="text-sm font-medium">冻结数据版本</div>
                    <div className="text-xs text-muted-foreground">
                      零拷贝高速读取，划分严格固定
                    </div>
                  </div>
                </button>
                <button
                  type="button"
                  onClick={() => setSourceType("datasets")}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg border p-3 text-left transition-colors",
                    sourceType === "datasets"
                      ? "border-primary bg-primary/[0.05] text-foreground"
                      : "text-muted-foreground hover:bg-muted/30"
                  )}
                >
                  <Database className="size-4 shrink-0" />
                  <div>
                    <div className="text-sm font-medium">标注数据集（可多选）</div>
                    <div className="text-xs text-muted-foreground">
                      灵活叠加基础标注与审图版本
                    </div>
                  </div>
                </button>
              </div>

              {sourceType === "data_version" ? (
                <div className="grid gap-4 rounded-lg border bg-muted/20 p-4">
                  <div className="grid gap-1.5">
                    <Label>选择数据版本（仅显示已就绪版本）</Label>
                    {readyDataVersions.length === 0 ? (
                      <p className="text-xs text-muted-foreground">
                        当前项目筛选下没有已就绪的数据版本，可切换到「标注数据集」或更改上方项目筛选。
                      </p>
                    ) : (
                      <Select
                        value={dataVersionId ? String(dataVersionId) : ""}
                        onValueChange={(v) => {
                          setDataVersionIdOverride(Number(v))
                          setSplitsOverride(null)
                        }}
                      >
                        <SelectTrigger>
                          <SelectValue placeholder="请选择数据版本" />
                        </SelectTrigger>
                        <SelectContent>
                          {readyDataVersions.map((dv) => (
                            <SelectItem key={dv.id} value={String(dv.id)}>
                              {dv.project_name} · 数据版本 v{dv.version}（训练{" "}
                              {dv.train_count} / 验证 {dv.val_count} / 测试{" "}
                              {dv.test_count}）
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  </div>

                  {selectedDataVersion && (
                    <div className="grid gap-2">
                      <Label>纳入的数据划分 (Splits)</Label>
                      <div className="flex flex-wrap gap-3">
                        {(["val", "test", "train"] as const).map((sp) => {
                          const count =
                            sp === "val"
                              ? selectedDataVersion.val_count
                              : sp === "test"
                                ? selectedDataVersion.test_count
                                : selectedDataVersion.train_count
                          const checked = splits.includes(sp)
                          return (
                            <label
                              key={sp}
                              className={cn(
                                "flex cursor-pointer items-center gap-2 rounded-md border bg-background px-3 py-2 text-xs",
                                count === 0 && "opacity-50",
                                checked && "border-primary bg-primary/[0.04]"
                              )}
                            >
                              <Checkbox
                                checked={checked}
                                disabled={count === 0}
                                onCheckedChange={() => toggleSplit(sp)}
                              />
                              <span className="font-medium">
                                {SPLIT_LABELS[sp]}
                              </span>
                              <span className="text-muted-foreground tabular-nums">
                                {formatNumber(count)} 张
                              </span>
                            </label>
                          )
                        })}
                      </div>
                    </div>
                  )}
                </div>
              ) : (
                <div className="grid gap-3 rounded-lg border bg-muted/20 p-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <Select
                      value=""
                      onValueChange={(v) => {
                        const id = Number(v)
                        if (
                          id > 0 &&
                          !datasetDrafts.some((d) => d.dataset_id === id)
                        ) {
                          setDatasetDrafts((prev) => [
                            ...prev,
                            {
                              dataset_id: id,
                              include_base: true,
                              version_ids: [],
                              image_mode: "annotated",
                              include_empty: true,
                            },
                          ])
                        }
                      }}
                    >
                      <SelectTrigger className="flex-1 bg-background">
                        <SelectValue
                          placeholder={
                            availableDatasetsToAdd.length > 0
                              ? "添加数据集到评估范围…"
                              : "没有更多可添加的数据集"
                          }
                        />
                      </SelectTrigger>
                      <SelectContent>
                        {availableDatasetsToAdd.map((ds) => (
                          <SelectItem key={ds.id} value={String(ds.id)}>
                            {ds.name}（{formatNumber(ds.image_count)} 张 · 已标{" "}
                            {formatNumber(ds.annotated_count)}）
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>

                  {datasetDrafts.length === 0 ? (
                    <div className="rounded-lg border border-dashed py-6 text-center text-xs text-muted-foreground">
                      请从上方下拉框添加一个或多个数据集作为评估真值来源。
                    </div>
                  ) : (
                    <div className="grid gap-2.5">
                      {datasetDrafts.map((draft) => {
                        const ds = datasetMap.get(draft.dataset_id)
                        return (
                          <DatasetDraftCard
                            key={draft.dataset_id}
                            dataset={ds}
                            draft={draft}
                            onChange={(updated) =>
                              setDatasetDrafts((prev) =>
                                prev.map((d) =>
                                  d.dataset_id === updated.dataset_id
                                    ? updated
                                    : d
                                )
                              )
                            }
                            onRemove={() =>
                              setDatasetDrafts((prev) =>
                                prev.filter(
                                  (d) => d.dataset_id !== draft.dataset_id
                                )
                              )
                            }
                          />
                        )
                      })}
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>

          {/* 3. 评估参数与元信息 */}
          <Card className="py-4">
            <CardHeader className="px-5 pb-3">
              <CardTitle className="text-base">3. 评估参数与名称</CardTitle>
              <CardDescription>
                默认展示的 Conf / IoU 阈值在评估完成后仍可在详情页通过滑块毫秒级实时重算，无需重跑推理。
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 px-5">
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-name">评估名称（可选）</Label>
                  <Input
                    id="eval-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={
                      selectedModelIds.length > 1
                        ? "批量创建时作为名称前缀，留空自动命名"
                        : "如：产线 A 验证集基线评估"
                    }
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-desc">备注说明（可选）</Label>
                  <Textarea
                    id="eval-desc"
                    rows={1}
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder="记录本次评估的背景或调参意图"
                    className="min-h-9"
                  />
                </div>
              </div>

              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-conf">默认置信度 Conf</Label>
                  <Input
                    id="eval-conf"
                    type="number"
                    min="0.05"
                    max="0.99"
                    step="0.05"
                    value={conf}
                    onChange={(e) => setConf(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-iou">匹配 IoU 阈值</Label>
                  <Input
                    id="eval-iou"
                    type="number"
                    min="0.05"
                    max="0.99"
                    step="0.05"
                    value={iou}
                    onChange={(e) => setIou(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-imgsz">推理尺寸 (imgsz)</Label>
                  <Input
                    id="eval-imgsz"
                    value={imgsz}
                    onChange={(e) => setImgsz(e.target.value)}
                    placeholder="自动 (如 640)"
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="eval-batch">推理 Batch</Label>
                  <Input
                    id="eval-batch"
                    type="number"
                    min="1"
                    max="256"
                    value={batch}
                    onChange={(e) => setBatch(e.target.value)}
                  />
                </div>
              </div>

              <div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="-ml-2 text-xs text-muted-foreground"
                  onClick={() => setShowAdvanced((v) => !v)}
                >
                  {showAdvanced ? (
                    <ChevronUp className="size-3.5" />
                  ) : (
                    <ChevronDown className="size-3.5" />
                  )}
                  高级推理选项（NMS IoU、最大检测数、FP16 半精度、设备）
                </Button>

                {showAdvanced && (
                  <div className="mt-2 grid grid-cols-2 gap-3 rounded-lg border bg-muted/20 p-3.5 sm:grid-cols-4">
                    <div className="grid gap-1.5">
                      <Label htmlFor="eval-nms" className="text-xs">
                        NMS IoU
                      </Label>
                      <Input
                        id="eval-nms"
                        type="number"
                        min="0.1"
                        max="0.99"
                        step="0.05"
                        value={nmsIou}
                        onChange={(e) => setNmsIou(e.target.value)}
                      />
                    </div>
                    <div className="grid gap-1.5">
                      <Label htmlFor="eval-maxdet" className="text-xs">
                        每图最大框数 (max_det)
                      </Label>
                      <Input
                        id="eval-maxdet"
                        type="number"
                        min="1"
                        max="3000"
                        value={maxDet}
                        onChange={(e) => setMaxDet(e.target.value)}
                      />
                    </div>
                    <div className="grid gap-1.5">
                      <Label htmlFor="eval-device" className="text-xs">
                        计算设备 (device)
                      </Label>
                      <Input
                        id="eval-device"
                        value={device}
                        onChange={(e) => setDevice(e.target.value)}
                        placeholder="自动 (如 0 / cpu)"
                      />
                    </div>
                    <div className="flex items-end pb-1.5">
                      <label className="flex cursor-pointer items-center gap-2 text-xs">
                        <Switch checked={half} onCheckedChange={setHalf} />
                        启用 FP16 半精度
                      </label>
                    </div>
                  </div>
                )}
              </div>
            </CardContent>
          </Card>
        </div>

        {/* 右栏：实时预览与类别对齐体检面板 */}
        <div className="grid gap-4 xl:sticky xl:top-20">
          <Card className="py-4">
            <CardHeader className="px-5 pb-3">
              <CardTitle className="flex items-center gap-2 text-base">
                <Gauge className="size-4 text-primary" />
                样本预览与类别对齐检查
              </CardTitle>
              <CardDescription>
                按类别名称取并集对齐，跨版本与不同类别顺序的模型均可安全评估。
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 px-5 text-xs">
              {previewLoading && !preview ? (
                <div className="grid gap-2">
                  <Skeleton className="h-16 w-full rounded-lg" />
                  <Skeleton className="h-24 w-full rounded-lg" />
                </div>
              ) : previewError ? (
                <Alert variant="destructive">
                  <AlertTriangle className="size-4" />
                  <AlertTitle>无法预览数据源</AlertTitle>
                  <AlertDescription>{previewError}</AlertDescription>
                </Alert>
              ) : !preview ? (
                <div className="rounded-lg border border-dashed py-8 text-center text-muted-foreground">
                  请选择数据源与模型以实时预览样本分布与类别对齐情况。
                </div>
              ) : (
                <>
                  {/* 样本统计格子 */}
                  <div className="grid grid-cols-2 gap-2 tabular-nums">
                    <div className="rounded-lg border bg-muted/30 p-2.5">
                      <div className="text-muted-foreground">评估总图片</div>
                      <div className="mt-0.5 text-lg font-bold text-foreground">
                        {formatNumber(preview.source.total_images)}
                      </div>
                    </div>
                    <div className="rounded-lg border bg-muted/30 p-2.5">
                      <div className="text-muted-foreground">真实目标框</div>
                      <div className="mt-0.5 text-lg font-bold text-foreground">
                        {formatNumber(preview.source.total_objects)}
                      </div>
                    </div>
                    <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-2.5">
                      <div className="text-emerald-700 dark:text-emerald-400">
                        OK 背景图
                      </div>
                      <div className="mt-0.5 text-base font-semibold text-emerald-700 dark:text-emerald-400">
                        {formatNumber(preview.source.ok_images)} 张
                      </div>
                    </div>
                    <div className="rounded-lg border border-blue-500/30 bg-blue-500/5 p-2.5">
                      <div className="text-blue-700 dark:text-blue-400">
                        NG 有缺陷图
                      </div>
                      <div className="mt-0.5 text-base font-semibold text-blue-700 dark:text-blue-400">
                        {formatNumber(preview.source.ng_images)} 张
                      </div>
                    </div>
                  </div>

                  {/* 告警提示 */}
                  {preview.warnings.length > 0 && (
                    <Alert className="border-amber-500/40 bg-amber-500/5 [&>svg]:text-amber-600">
                      <AlertTriangle className="size-4" />
                      <AlertTitle className="text-xs">评估前提醒</AlertTitle>
                      <AlertDescription className="text-xs">
                        <ul className="list-disc space-y-1 pl-4">
                          {preview.warnings.map((w, idx) => (
                            <li key={idx}>{w}</li>
                          ))}
                        </ul>
                      </AlertDescription>
                    </Alert>
                  )}

                  {/* GT 标签分布 */}
                  {preview.gt_labels.length > 0 && (
                    <div className="grid gap-1.5">
                      <div className="font-medium text-foreground">
                        数据源标注类别分布（{preview.gt_labels.length} 类）
                      </div>
                      <div className="grid max-h-36 gap-1.5 overflow-y-auto pr-1">
                        {preview.gt_labels.map((lbl) => {
                          const maxCnt = Math.max(
                            1,
                            preview.gt_labels[0]?.object_count ?? 1
                          )
                          const pct = Math.round(
                            (lbl.object_count / maxCnt) * 100
                          )
                          return (
                            <div key={lbl.name} className="grid gap-0.5">
                              <div className="flex justify-between tabular-nums">
                                <span className="truncate font-medium">
                                  {lbl.name}
                                </span>
                                <span className="text-muted-foreground">
                                  {formatNumber(lbl.object_count)} 框 /{" "}
                                  {formatNumber(lbl.image_count)} 图
                                </span>
                              </div>
                              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                                <div
                                  className="h-full rounded-full bg-primary/80"
                                  style={{ width: `${pct}%` }}
                                />
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )}

                  {/* 各模型类别对齐明细 */}
                  {preview.models.length > 0 && (
                    <div className="grid gap-2">
                      <div className="font-medium text-foreground">
                        模型类别对齐体检（已选 {preview.models.length} 个）
                      </div>
                      <div className="grid max-h-64 gap-2 overflow-y-auto pr-1">
                        {preview.models.map((ma) => (
                          <div
                            key={ma.model.id}
                            className="grid gap-1.5 rounded-lg border bg-background p-2.5"
                          >
                            <div className="flex items-center justify-between gap-2">
                              <span className="truncate font-semibold">
                                {ma.model.project_name} v{ma.model.version}
                              </span>
                              <span className="inline-flex items-center gap-1 text-[11px] text-emerald-700 tabular-nums dark:text-emerald-400">
                                <CheckCircle2 className="size-3" />
                                对齐 {ma.matched_count}
                              </span>
                            </div>
                            <div className="flex flex-wrap gap-1">
                              {ma.classes.map((cls) => (
                                <Badge
                                  key={cls.name}
                                  variant="outline"
                                  className={cn(
                                    "h-5 px-1.5 text-[10px] font-normal",
                                    cls.in_model && cls.in_gt
                                      ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
                                      : cls.in_model
                                        ? "border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-400"
                                        : "border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-400"
                                  )}
                                >
                                  {cls.name}
                                  {cls.in_model && cls.in_gt
                                    ? ""
                                    : cls.in_model
                                      ? " (仅模型有)"
                                      : " (仅标注有)"}
                                </Badge>
                              ))}
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </>
              )}

              <Button type="submit" className="w-full" disabled={!canSubmit}>
                {createMut.isPending ? (
                  <Spinner aria-hidden />
                ) : (
                  <Plus className="size-4" />
                )}
                {selectedModelIds.length > 1
                  ? `批量发起 ${selectedModelIds.length} 个模型评估`
                  : "开始模型评估"}
              </Button>
            </CardContent>
          </Card>
        </div>
      </div>
    </form>
  )
}

function DatasetDraftCard({
  dataset,
  draft,
  onChange,
  onRemove,
}: {
  dataset?: Dataset
  draft: DatasetDraft
  onChange: (next: DatasetDraft) => void
  onRemove: () => void
}) {
  const versions = useQuery(datasetVersionsQuery(draft.dataset_id))
  const reviewVersions = versions.data?.items ?? []

  return (
    <div className="grid gap-2.5 rounded-lg border bg-background p-3 text-xs">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate font-semibold text-foreground">
            {dataset?.name ?? `数据集 #${draft.dataset_id}`}
          </div>
          {dataset && (
            <div className="text-muted-foreground tabular-nums">
              共 {formatNumber(dataset.image_count)} 张 · 已标{" "}
              {formatNumber(dataset.annotated_count)} · 空标注{" "}
              {formatNumber(dataset.empty_count)}
            </div>
          )}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          onClick={onRemove}
          aria-label="移除数据集"
        >
          <Trash2 className="size-3.5 text-muted-foreground" />
        </Button>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="grid gap-1">
          <span className="text-muted-foreground">图片纳入范围</span>
          <Select
            value={draft.image_mode}
            onValueChange={(v) =>
              onChange({ ...draft, image_mode: v as ImageMode })
            }
          >
            <SelectTrigger className="h-8 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(IMAGE_MODE_LABELS) as ImageMode[]).map((m) => (
                <SelectItem key={m} value={m}>
                  {IMAGE_MODE_LABELS[m]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="flex flex-wrap items-end gap-3 pb-1">
          <label className="flex cursor-pointer items-center gap-1.5">
            <Checkbox
              checked={draft.include_base}
              onCheckedChange={(v) =>
                onChange({ ...draft, include_base: v === true })
              }
            />
            <span>包含基础标注</span>
          </label>
          {draft.image_mode === "annotated" && (
            <label className="flex cursor-pointer items-center gap-1.5">
              <Checkbox
                checked={draft.include_empty}
                onCheckedChange={(v) =>
                  onChange({ ...draft, include_empty: v === true })
                }
              />
              <span>纳入空标注 (OK)</span>
            </label>
          )}
        </div>
      </div>

      {reviewVersions.length > 0 && (
        <div className="grid gap-1">
          <span className="text-muted-foreground">叠加审图版本（可选）</span>
          <div className="flex flex-wrap gap-2">
            {reviewVersions.map((ver) => {
              const checked = draft.version_ids.includes(ver.id)
              return (
                <label
                  key={ver.id}
                  className={cn(
                    "flex cursor-pointer items-center gap-1.5 rounded border px-2 py-1",
                    checked && "border-primary bg-primary/[0.05]"
                  )}
                >
                  <Checkbox
                    checked={checked}
                    onCheckedChange={(v) => {
                      const next =
                        v === true
                          ? [...draft.version_ids, ver.id]
                          : draft.version_ids.filter((id) => id !== ver.id)
                      onChange({ ...draft, version_ids: next })
                    }}
                  />
                  <span>{ver.name}</span>
                </label>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
