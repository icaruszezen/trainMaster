/**
 * 训练任务的操作（列表页、项目详情页与训练详情页共用）：
 * - 查看详情
 * - 停止训练（排队中 / 训练中）
 * - 重新训练（失败 / 已取消）
 * - 注册模型到模型仓库（best.pt / last.pt）
 * - 复用全套配置新建训练任务
 * - 修改任务名称与说明
 * - 删除训练任务（进行中时先停止再删除；已注册到模型仓库的模型不受影响）
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import {
  CopyPlus,
  Ellipsis,
  Info,
  OctagonX,
  PackagePlus,
  Pencil,
  RotateCcw,
  Trash2,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  cacheTrainingJob,
  cancelTrainingJob,
  deleteTrainingJob,
  isActiveTrainingJob,
  refreshAfterModelRegistered,
  registerTrainedModel,
  retryTrainingJob,
  trainingKeys,
  updateTrainingJob,
  type TrainingJob,
  type WeightVariant,
} from "@/features/training/api"
import { trainingJobTitle } from "@/features/training/format"
import {
  newTrainingJobPath,
  trainingJobDetailPath,
} from "@/features/training/paths"
import { queryClient } from "@/lib/query-client"

export type TrainingJobActions = {
  edit: (job: TrainingJob) => void
  cancel: (job: TrainingJob) => void
  retry: (job: TrainingJob) => void
  registerModel: (job: TrainingJob, defaultVariant?: WeightVariant) => void
  remove: (job: TrainingJob) => void
}

export function canRegisterAnyWeight(job: TrainingJob) {
  const hasBest =
    job.artifacts.some((a) => a.rel_path === "weights/best.pt") &&
    job.best_model === null
  const hasLast =
    job.artifacts.some((a) => a.rel_path === "weights/last.pt") &&
    job.last_model === null
  return hasBest || hasLast
}

export function TrainingJobActionsMenu({
  job,
  actions,
  showDetails = true,
  className,
}: {
  job: TrainingJob
  actions: TrainingJobActions
  showDetails?: boolean
  className?: string
}) {
  const active = isActiveTrainingJob(job)
  const canRetry = job.status === "failed" || job.status === "canceled"
  const canRegister = canRegisterAnyWeight(job)
  const title = trainingJobTitle(job)

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${title}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {showDetails && (
          <DropdownMenuItem asChild>
            <Link to={trainingJobDetailPath(job.id)}>
              <Info />
              查看详情
            </Link>
          </DropdownMenuItem>
        )}

        {active && (
          <DropdownMenuItem onSelect={() => actions.cancel(job)}>
            <OctagonX />
            停止训练
          </DropdownMenuItem>
        )}

        {canRetry && (
          <DropdownMenuItem onSelect={() => actions.retry(job)}>
            <RotateCcw />
            重新训练
          </DropdownMenuItem>
        )}

        {canRegister && (
          <DropdownMenuItem onSelect={() => actions.registerModel(job)}>
            <PackagePlus />
            注册模型入库…
          </DropdownMenuItem>
        )}

        <DropdownMenuItem onSelect={() => actions.edit(job)}>
          <Pencil />
          修改名称与说明
        </DropdownMenuItem>

        <DropdownMenuItem asChild>
          <Link
            to={newTrainingJobPath({
              projectId: job.project_id,
              from: job.id,
            })}
          >
            <CopyPlus />
            复用参数新建训练
          </Link>
        </DropdownMenuItem>

        <DropdownMenuSeparator />

        <DropdownMenuItem
          variant="destructive"
          onSelect={() => actions.remove(job)}
        >
          <Trash2 />
          {active ? "停止并删除" : "删除任务"}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function useTrainingJobActions({
  onDeleted,
}: {
  onDeleted?: (job: TrainingJob) => void
} = {}) {
  const [editing, setEditing] = useState<TrainingJob | null>(null)
  const [canceling, setCanceling] = useState<TrainingJob | null>(null)
  const [deleting, setDeleting] = useState<TrainingJob | null>(null)
  const [registerTarget, setRegisterTarget] = useState<{
    job: TrainingJob
    variant: WeightVariant
  } | null>(null)

  const retry = useMutation({
    mutationFn: (job: TrainingJob) => retryTrainingJob(job.id),
    onSuccess: (queued) => {
      cacheTrainingJob(queued)
      toast.success(`已重新提交“${trainingJobTitle(queued)}”`, {
        description: "任务已进入训练队列，等待在线训练节点领取执行。",
      })
    },
    onError: (error, job) => {
      void queryClient.invalidateQueries({ queryKey: trainingKeys.all })
      toast.error(`无法重新训练“${trainingJobTitle(job)}”`, {
        description: error.message,
      })
    },
  })

  const actions: TrainingJobActions = {
    edit: setEditing,
    cancel: setCanceling,
    retry: (job) => {
      if (!retry.isPending) {
        retry.mutate(job)
      }
    },
    registerModel: (job, defaultVariant) => {
      const hasBest =
        job.artifacts.some((a) => a.rel_path === "weights/best.pt") &&
        job.best_model === null
      const variant = defaultVariant ?? (hasBest ? "best" : "last")
      setRegisterTarget({ job, variant })
    },
    remove: setDeleting,
  }

  const dialogs = (
    <>
      <EditTrainingJobDialog
        job={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <CancelTrainingJobDialog
        job={canceling}
        onOpenChange={(open) => !open && setCanceling(null)}
      />
      <RegisterTrainedModelDialog
        target={registerTarget}
        onOpenChange={(open) => !open && setRegisterTarget(null)}
      />
      <DeleteTrainingJobDialog
        job={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={onDeleted}
      />
    </>
  )

  return { actions, dialogs, retrying: retry.isPending }
}

function EditTrainingJobDialog({
  job,
  onOpenChange,
}: {
  job: TrainingJob | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={job !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {job && (
          <EditTrainingJobForm
            key={job.id}
            job={job}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditTrainingJobForm({
  job,
  onDone,
}: {
  job: TrainingJob
  onDone: () => void
}) {
  const nameId = useId()
  const descId = useId()
  const [name, setName] = useState(job.name)
  const [description, setDescription] = useState(job.description)

  const mutation = useMutation({
    mutationFn: () =>
      updateTrainingJob(job.id, {
        name: name.trim(),
        description: description.trim(),
      }),
    onSuccess: (updated) => {
      cacheTrainingJob(updated)
      toast.success("已更新训练任务信息")
      onDone()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (mutation.isPending) {
      return
    }
    mutation.mutate()
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-5" noValidate>
      <DialogHeader>
        <DialogTitle>编辑训练任务信息</DialogTitle>
        <DialogDescription>
          修改训练任务的名称与备注说明（不影响已锁定的数据版本与训练超参数）。
        </DialogDescription>
      </DialogHeader>

      <Field>
        <FieldLabel htmlFor={nameId}>任务名称（可选）</FieldLabel>
        <Input
          id={nameId}
          value={name}
          maxLength={120}
          placeholder={`留空默认显示“训练 #${job.id}”`}
          disabled={mutation.isPending}
          onChange={(event) => {
            setName(event.target.value)
            mutation.reset()
          }}
        />
      </Field>

      <Field>
        <FieldLabel htmlFor={descId}>备注说明（可选）</FieldLabel>
        <Textarea
          id={descId}
          value={description}
          maxLength={1000}
          placeholder="记录实验目的、调参思路或对比结论…"
          disabled={mutation.isPending}
          className="min-h-24"
          onChange={(event) => {
            setDescription(event.target.value)
            mutation.reset()
          }}
        />
      </Field>

      {mutation.error && (
        <p role="alert" className="text-sm break-words text-destructive">
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
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <Spinner aria-hidden />}
          保存
        </Button>
      </DialogFooter>
    </form>
  )
}

function CancelTrainingJobDialog({
  job,
  onOpenChange,
}: {
  job: TrainingJob | null
  onOpenChange: (open: boolean) => void
}) {
  const mutation = useMutation({
    mutationFn: (id: number) => cancelTrainingJob(id),
    onSuccess: (updated) => {
      cacheTrainingJob(updated)
      toast.success(`已停止“${trainingJobTitle(updated)}”`)
      onOpenChange(false)
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: trainingKeys.all })
    },
  })

  return (
    <AlertDialog open={job !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {job && (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle className="break-words">
                停止“{trainingJobTitle(job)}”？
              </AlertDialogTitle>
              <AlertDialogDescription>
                {job.status === "queued"
                  ? "任务将从排队队列中移出并标记为已取消；之后可以随时点击“重新训练”。"
                  : "训练节点收到信号后会立即终止当前训练子进程，任务标记为已取消；已输出的训练日志会保留，之后可点击“重新训练”。"}
              </AlertDialogDescription>
            </AlertDialogHeader>
            {mutation.error && (
              <p role="alert" className="text-sm break-words text-destructive">
                {mutation.error.message}
              </p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={mutation.isPending}>
                继续训练
              </AlertDialogCancel>
              <Button
                variant="destructive"
                disabled={mutation.isPending}
                onClick={() => mutation.mutate(job.id)}
              >
                {mutation.isPending && <Spinner aria-hidden />}
                停止训练
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function RegisterTrainedModelDialog({
  target,
  onOpenChange,
}: {
  target: { job: TrainingJob; variant: WeightVariant } | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={target !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {target && (
          <RegisterTrainedModelForm
            key={`${target.job.id}-${target.variant}`}
            job={target.job}
            initialVariant={target.variant}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function RegisterTrainedModelForm({
  job,
  initialVariant,
  onDone,
}: {
  job: TrainingJob
  initialVariant: WeightVariant
  onDone: () => void
}) {
  const variantId = useId()
  const descId = useId()

  const canBest =
    job.artifacts.some((a) => a.rel_path === "weights/best.pt") &&
    job.best_model === null
  const canLast =
    job.artifacts.some((a) => a.rel_path === "weights/last.pt") &&
    job.last_model === null

  const [variant, setVariant] = useState<WeightVariant>(
    initialVariant === "best" && canBest ? "best" : canLast ? "last" : "best"
  )
  const [description, setDescription] = useState("")

  const mutation = useMutation({
    mutationFn: () =>
      registerTrainedModel(job.id, {
        variant,
        description: description.trim(),
      }),
    onSuccess: (registered) => {
      refreshAfterModelRegistered(job.id, job.project_id)
      toast.success(`已注册到模型仓库：模型 v${registered.version}`, {
        description:
          variant === "last"
            ? "该检查点包含优化器状态，后续可用于断点续训或微调。"
            : "可在模型仓库中直接导出 ONNX、发起模型审图或继续微调。",
      })
      onDone()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (mutation.isPending) {
      return
    }
    mutation.mutate()
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-5" noValidate>
      <DialogHeader>
        <DialogTitle>注册训练权重到模型仓库</DialogTitle>
        <DialogDescription>
          将“{trainingJobTitle(job)}”产出的 PyTorch 权重注册到项目“
          {job.project_name}”的模型仓库中。
        </DialogDescription>
      </DialogHeader>

      <Field>
        <FieldLabel htmlFor={variantId}>选择权重文件</FieldLabel>
        <Select
          value={variant}
          disabled={mutation.isPending}
          onValueChange={(v) => setVariant(v as WeightVariant)}
        >
          <SelectTrigger id={variantId} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {canBest && (
              <SelectItem value="best">
                best.pt（验证集最佳指标权重，推荐用于推理与部署）
              </SelectItem>
            )}
            {canLast && (
              <SelectItem value="last">
                last.pt（最后一轮完整检查点，支持断点续训）
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </Field>

      <Field>
        <FieldLabel htmlFor={descId}>模型说明（可选）</FieldLabel>
        <Textarea
          id={descId}
          value={description}
          maxLength={500}
          disabled={mutation.isPending}
          placeholder="留空将自动填充训练任务、数据版本与验证集最佳指标摘要"
          className="min-h-24"
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>

      {mutation.error && (
        <p role="alert" className="text-sm break-words text-destructive">
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
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <Spinner aria-hidden />}
          注册入库
        </Button>
      </DialogFooter>
    </form>
  )
}

function DeleteTrainingJobDialog({
  job,
  onOpenChange,
  onDeleted,
}: {
  job: TrainingJob | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (job: TrainingJob) => void
}) {
  const mutation = useMutation({
    mutationFn: (target: TrainingJob) => deleteTrainingJob(target.id),
    onSuccess: (_, target) => {
      queryClient.removeQueries({ queryKey: trainingKeys.detail(target.id) })
      void queryClient.invalidateQueries({ queryKey: trainingKeys.lists() })
      toast.success(`已删除“${trainingJobTitle(target)}”`)
      onOpenChange(false)
      onDeleted?.(target)
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: trainingKeys.all })
    },
  })

  const active = isActiveTrainingJob(job)

  return (
    <AlertDialog open={job !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {job && (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle className="break-words">
                {active
                  ? `停止并删除“${trainingJobTitle(job)}”？`
                  : `删除“${trainingJobTitle(job)}”？`}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {active
                  ? "正在排队或运行中的训练会立即终止，训练日志与任务目录下的临时产物一并永久删除，不可恢复。"
                  : "训练记录、日志与任务目录下的未入库临时权重将被永久删除，不可恢复。"}
                {(job.best_model || job.last_model) &&
                  " 已经注册到模型仓库的模型版本仍会完整保留，不受影响。"}
              </AlertDialogDescription>
            </AlertDialogHeader>
            {mutation.error && (
              <p role="alert" className="text-sm break-words text-destructive">
                {mutation.error.message}
              </p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={mutation.isPending}>
                取消
              </AlertDialogCancel>
              <Button
                variant="destructive"
                disabled={mutation.isPending}
                onClick={() => mutation.mutate(job)}
              >
                {mutation.isPending && <Spinner aria-hidden />}
                {active ? "停止并删除" : "删除任务"}
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}
