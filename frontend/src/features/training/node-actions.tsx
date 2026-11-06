import { useRef, type ReactNode } from "react"
import { useMutation } from "@tanstack/react-query"
import { Ellipsis, FileSearch, RefreshCw, Trash2 } from "lucide-react"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Spinner } from "@/components/ui/spinner"
import { CommandBlock } from "@/features/training/command-block"
import {
  cacheNode,
  deleteNode,
  forgetNode,
  probeNode,
  refreshNodes,
  type ComputeNode,
} from "@/features/training/compute-api"
import {
  isOnline,
  isProbing,
  isWindowsNode,
} from "@/features/training/compute-status"
import { ApiError } from "@/lib/api"

/** 永久停止 Windows 节点：停止并删除开机自启的计划任务（需要管理员）。 */
const WINDOWS_STOP_COMMAND =
  "Stop-ScheduledTask -TaskName trainmaster-trainer; Unregister-ScheduledTask -TaskName trainmaster-trainer -Confirm:$false"

/** 请求节点重新检测硬件与 CUDA 环境；节点在下一次心跳时开始检测。 */
export function useProbeNode() {
  return useMutation({
    mutationFn: (node: ComputeNode) => probeNode(node.id),
    onSuccess: (updated) => {
      cacheNode(updated)
      toast(`已请求重新检测“${updated.name}”`, {
        description: "节点会在下一次心跳时重新检测显卡与 CUDA 环境并运行自检。",
      })
    },
    onError: (error, node) => {
      // 多半是节点刚刚离线（409）或已被删除（404）：刷新列表让状态与按钮同步。
      refreshNodes()
      toast.error(`无法重新检测“${node.name}”`, {
        description: error.message,
      })
    },
  })
}

type NodeActionsMenuProps = {
  node: ComputeNode
  /** 参数为菜单按钮：对话框关闭后把焦点还给它（菜单项此时已经不在页面上）。 */
  onShowDetails: (trigger: HTMLElement | null) => void
  onDelete: (trigger: HTMLElement | null) => void
}

export function NodeActionsMenu({
  node,
  onShowDetails,
  onDelete,
}: NodeActionsMenuProps) {
  const probe = useProbeNode()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const online = isOnline(node)
  const probing = probe.isPending || isProbing(node)

  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          ref={triggerRef}
          variant="ghost"
          size="icon-sm"
          className="shrink-0"
          aria-label={`更多操作：${node.name}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuItem
          onSelect={() => probe.mutate(node)}
          disabled={!online || probing}
        >
          {probing ? <Spinner aria-hidden /> : <RefreshCw />}
          <span className="grid">
            {probing ? "正在检测…" : "重新检测"}
            {!online && (
              <span className="text-xs text-muted-foreground">
                节点离线，无法检测
              </span>
            )}
          </span>
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onShowDetails(triggerRef.current)}>
          <FileSearch />
          查看详情
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          onSelect={() => onDelete(triggerRef.current)}
          disabled={online}
        >
          <Trash2 />
          <span className="grid">
            删除节点
            {online && (
              <span className="text-xs text-muted-foreground">
                在线节点不能删除，先停止它的 trainer 服务
              </span>
            )}
          </span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 删除离线节点的确认对话框。`node` 为列表中的最新数据：确认前节点重新上线时
 * 禁用删除并说明原因（服务端同样会拒绝）。
 */
export function DeleteNodeDialog({
  node,
  onOpenChange,
  onCloseAutoFocus,
}: {
  node: ComputeNode | null
  onOpenChange: (open: boolean) => void
  /** 关闭后焦点去哪里（对话框没有触发按钮，见 returnFocus）。 */
  onCloseAutoFocus?: (event: Event) => void
}) {
  return (
    <AlertDialog open={node !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent onCloseAutoFocus={onCloseAutoFocus}>
        {node && (
          <DeleteNodeForm node={node} onDeleted={() => onOpenChange(false)} />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteNodeForm({
  node,
  onDeleted,
}: {
  node: ComputeNode
  onDeleted: () => void
}) {
  const mutation = useMutation({
    mutationFn: () => deleteNode(node.id),
    onSuccess: () => {
      forgetNode(node.id)
      toast.success(`已删除节点“${node.name}”`)
      onDeleted()
    },
    onError: (error) => {
      // 404：已被别人删除，结果与删除成功相同。
      if (error instanceof ApiError && error.status === 404) {
        forgetNode(node.id)
        toast.success(`节点“${node.name}”已不存在`)
        onDeleted()
        return
      }
      // 409：节点刚刚重新上线；刷新后对话框中会说明原因。
      refreshNodes()
    },
  })
  const online = isOnline(node)
  const error =
    mutation.error instanceof ApiError && mutation.error.status === 404
      ? null
      : mutation.error

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="break-all">
          删除节点“{node.name}”？
        </AlertDialogTitle>
        {isWindowsNode(node) ? (
          // Windows 节点以计划任务运行，没有 Docker 部署。命令较长，与以命令
          // 结尾的修复建议一样单独一行显示，可以复制。
          <AlertDialogDescription asChild>
            <div className="grid w-full min-w-0 gap-2">
              <p>
                只删除平台中的节点记录与最近的指标历史，不影响那台电脑上的文件。只要那台电脑上的
                trainer
                服务还在运行，恢复连接后节点就会自动重新注册。要永久移除，先在那台电脑上以管理员身份在
                PowerShell 中停止并删除计划任务：
              </p>
              <CommandBlock command={WINDOWS_STOP_COMMAND} />
            </div>
          </AlertDialogDescription>
        ) : (
          <AlertDialogDescription>
            只删除平台中的节点记录与最近的指标历史，不影响那台机器上的文件。只要那台机器上的
            trainer
            服务还在运行，恢复连接后节点就会自动重新注册。要永久移除，先在那台机器上停止训练节点：Docker
            部署运行 <Command>docker compose stop trainer</Command>（CPU 节点为{" "}
            <Command>trainer-cpu</Command>；平台所在的机器上之后的{" "}
            <Command>make up</Command> 会再次启动它，改用{" "}
            <Command>make up SETUP_ARGS="--mode off"</Command>
            ，以后要恢复时运行{" "}
            <Command>make up SETUP_ARGS="--mode auto"</Command>
            ），直接安装的运行{" "}
            <Command>sudo systemctl disable --now trainmaster-trainer</Command>
            。
          </AlertDialogDescription>
        )}
      </AlertDialogHeader>
      {online && (
        <p role="alert" className="text-sm text-destructive">
          节点已重新上线，先停止该节点上的 trainer 服务再删除。
        </p>
      )}
      {error && !online && (
        <p role="alert" className="text-sm text-destructive">
          {error.message}
        </p>
      )}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={mutation.isPending}>
          取消
        </AlertDialogCancel>
        <Button
          variant="destructive"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending || online}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          删除节点
        </Button>
      </AlertDialogFooter>
    </>
  )
}

function Command({ children }: { children: ReactNode }) {
  return (
    <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.8125rem] wrap-anywhere text-foreground">
      {children}
    </code>
  )
}
