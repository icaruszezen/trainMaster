import type { ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  Globe,
  KeyRound,
  Monitor,
  Network,
  Terminal,
  type LucideIcon,
} from "lucide-react"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { CommandBlock } from "@/features/training/command-block"
import { computeServerQuery } from "@/features/training/compute-api"
import {
  SERVER_HOST_PLACEHOLDER,
  suggestServerUrl,
} from "@/features/training/compute-format"
import {
  connectCommands,
  TOKEN_PLACEHOLDER,
} from "@/features/training/connect-commands"

/** 接入说明需要的平台信息；服务器信息还没加载时按“需要令牌”显示（默认部署会生成令牌）。 */
function useConnectInfo() {
  const server = useQuery(computeServerQuery)
  const tokenRequired = server.data?.node_registration.token_required ?? true
  const serverUrl = suggestServerUrl(window.location)
  return {
    tokenRequired,
    serverUrl,
    /** 通过 localhost 访问页面：命令中的平台地址是占位符。 */
    serverUrlIsPlaceholder: serverUrl.includes(SERVER_HOST_PLACEHOLDER),
    commands: connectCommands({ serverUrl, tokenRequired }),
    heartbeatSeconds: server.data?.node_registration.heartbeat_interval_seconds,
  }
}

function Code({ children }: { children: ReactNode }) {
  return (
    <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.8125rem] break-words text-foreground">
      {children}
    </code>
  )
}

function Method({
  icon: Icon,
  title,
  children,
}: {
  icon: LucideIcon
  title: string
  children: ReactNode
}) {
  return (
    <li className="grid min-w-0 gap-2">
      <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
          <Icon className="size-3.5" />
        </span>
        {title}
      </h3>
      <div className="grid min-w-0 gap-2 text-sm text-muted-foreground">
        {children}
      </div>
    </li>
  )
}

function TokenNote({ tokenRequired }: { tokenRequired: boolean }) {
  return (
    <p className="flex items-start gap-2 rounded-md border bg-muted/40 px-3 py-2 text-left text-xs text-muted-foreground">
      <KeyRound className="mt-px size-3.5 shrink-0" />
      {tokenRequired ? (
        <span>
          平台已启用节点令牌：把命令中的 <Code>{TOKEN_PLACEHOLDER}</Code>{" "}
          换成部署机（运行平台的机器）<Code>.env</Code> 中{" "}
          <Code>TM_NODE_TOKEN</Code> 的值，<Code>make setup</Code>{" "}
          会自动生成它。同一台机器上用 <Code>make up</Code>{" "}
          启动的节点会自动带上令牌。
        </span>
      ) : (
        <span>
          平台没有设置节点令牌，能访问平台的任何机器都可以注册为节点。建议在部署机的{" "}
          <Code>.env</Code> 中设置 <Code>TM_NODE_TOKEN</Code>（或运行{" "}
          <Code>make setup</Code> 自动生成）后重新启动平台。
        </span>
      )}
    </p>
  )
}

function ServerUrlNote() {
  return (
    <p className="flex items-start gap-2 rounded-md border bg-muted/40 px-3 py-2 text-left text-xs text-muted-foreground">
      <Globe className="mt-px size-3.5 shrink-0" />
      <span>
        当前通过本机地址访问平台，其它机器不能使用：把命令中的{" "}
        <Code>{SERVER_HOST_PLACEHOLDER}</Code> 换成平台服务器在局域网中的 IP
        或域名，端口按实际填写（部署机 <Code>.env</Code> 中的{" "}
        <Code>TM_HTTP_PORT</Code>）。
      </span>
    </p>
  )
}

/** 其它机器上的训练节点按 make setup 的结果启动。 */
function RemoteStartNote() {
  return (
    <>
      按 <Code>make setup</Code> 最后输出的服务名启动（选择 CPU 时为{" "}
      <Code>trainer-cpu</Code>）；Docker 还不能使用显卡时在{" "}
      <Code>SETUP_ARGS</Code> 中加上 <Code>--install-toolkit</Code> 让脚本安装
      NVIDIA Container Toolkit（需要 sudo）。
    </>
  )
}

/** 空状态中的简要说明：三种接入方式各自的命令。 */
export function ConnectMethodsBrief() {
  const { commands, tokenRequired, serverUrlIsPlaceholder } = useConnectInfo()

  return (
    <div className="grid w-full min-w-0 gap-3 text-left">
      <ol className="grid min-w-0 gap-3">
        <BriefMethod
          icon={Monitor}
          title="本机（Docker）"
          description="在平台的部署目录中运行"
          commands={[commands.local]}
        />
        <BriefMethod
          icon={Network}
          title="其它 GPU 机器（Docker）"
          description="在那台机器上的仓库目录中依次运行，只启动训练节点"
          // 两条命令分开复制：make setup 选择 CPU 时第二条要换成 trainer-cpu，
          // 一起粘贴会立刻启动 GPU 的 trainer 服务。
          commands={[commands.remoteSetup, commands.remoteStart]}
          note={<RemoteStartNote />}
        />
        <BriefMethod
          icon={Terminal}
          title="不用 Docker"
          description="在 GPU 机器上的仓库目录中运行"
          commands={[commands.bareMetal]}
        />
      </ol>
      <div className="grid gap-1 text-xs text-muted-foreground">
        <p>
          {serverUrlIsPlaceholder ? (
            <>
              把 <Code>{SERVER_HOST_PLACEHOLDER}</Code>{" "}
              换成其它机器能访问的平台服务器 IP 或域名，端口按实际填写。
            </>
          ) : (
            "平台地址取自当前页面；其它机器访问平台的地址或端口不同时按实际填写。"
          )}
        </p>
        {tokenRequired && (
          <p>
            <Code>{TOKEN_PLACEHOLDER}</Code> 是部署机 <Code>.env</Code> 中{" "}
            <Code>TM_NODE_TOKEN</Code> 的值。
          </p>
        )}
      </div>
    </div>
  )
}

function BriefMethod({
  icon: Icon,
  title,
  description,
  commands,
  note,
}: {
  icon: LucideIcon
  title: string
  description: ReactNode
  /** 每条命令一个代码块，各自复制。 */
  commands: readonly string[]
  /** 命令下方的补充说明。 */
  note?: ReactNode
}) {
  return (
    <li className="grid min-w-0 gap-2 rounded-lg border bg-background p-3 md:grid-cols-[15rem_minmax(0,1fr)] md:items-center md:gap-4">
      <div className="grid min-w-0 gap-1">
        <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
          <Icon className="size-4 shrink-0 text-muted-foreground" />
          {title}
        </h3>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
      <div className="grid min-w-0 gap-1.5">
        {commands.map((command) => (
          <CommandBlock key={command} command={command} />
        ))}
        {note && <p className="text-xs text-muted-foreground">{note}</p>}
      </div>
    </li>
  )
}

/** “接入节点”对话框：三种方式的完整步骤。 */
export function ConnectNodeDialog({
  open,
  onOpenChange,
  onCloseAutoFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 关闭后焦点去哪里（对话框没有触发按钮，见 returnFocus）。 */
  onCloseAutoFocus?: (event: Event) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        {open && <ConnectNodeContent />}
      </DialogContent>
    </Dialog>
  )
}

function ConnectNodeContent() {
  const { commands, tokenRequired, heartbeatSeconds, serverUrlIsPlaceholder } =
    useConnectInfo()

  return (
    <>
      <DialogHeader>
        <DialogTitle>接入训练节点</DialogTitle>
        <DialogDescription>
          训练节点是运行在 GPU 机器上的 trainer
          服务：自动识别显卡、CPU、内存与磁盘，按驱动选择 PyTorch 的 CUDA
          版本，检测 CUDA / YOLO 环境并定时向平台上报。节点启动后约{" "}
          {heartbeatSeconds ?? 10} 秒内出现在本页，首次检测环境需要几十秒。
        </DialogDescription>
      </DialogHeader>

      <ol className="grid min-w-0 gap-6">
        <Method icon={Monitor} title="1. 本机（与平台在同一台机器，Docker）">
          <p>
            在平台的部署目录中运行。<Code>make setup</Code> 检测显卡与驱动、检查
            Docker 能否使用显卡（NVIDIA Container Toolkit）、按驱动选择 PyTorch
            的 CUDA 版本并写入 <Code>.env</Code>；没有可用的显卡时改用 CPU
            训练节点。
            <Code>make up</Code> 随后构建并启动全部服务。
          </p>
          <CommandBlock command={commands.local} />
          <p className="text-xs">
            Docker 还不能使用显卡时，加上{" "}
            <Code>SETUP_ARGS="--install-toolkit"</Code> 自动安装 NVIDIA
            Container Toolkit（需要 sudo）。没有 make（如 Windows）时运行{" "}
            <Code>deploy/scripts/setup-compute.sh</Code> 或按部署文档手动修改{" "}
            <Code>.env</Code>。
          </p>
        </Method>

        <Method icon={Network} title="2. 其它 GPU 机器（Docker）">
          <p>
            在 GPU 机器上克隆本仓库，在仓库目录中运行 <Code>make setup</Code>
            ：把平台地址{tokenRequired && "与节点令牌"}写入 <Code>.env</Code>
            （没有时从 <Code>.env.example</Code> 创建）、检测显卡与驱动、检查
            Docker 能否使用显卡，并按驱动选择 PyTorch 的 CUDA
            版本。平台地址按其它机器实际能访问的地址与端口填写。
          </p>
          <CommandBlock command={commands.remoteSetup} />
          <p>然后只启动训练节点，构建与驱动匹配的镜像：</p>
          <CommandBlock command={commands.remoteStart} />
          <p className="text-xs">
            <RemoteStartNote />
            不要在这台机器上运行 <Code>make up</Code> 或不带服务名的{" "}
            <Code>docker compose up -d</Code>：它们会再启动一套 postgres 与
            app。
          </p>
        </Method>

        <Method icon={Terminal} title="3. 不用 Docker（直接安装）">
          <p>
            需要 NVIDIA 驱动与 Python 3.10 及以上。在 GPU 机器上的仓库目录中以
            root 权限运行：自动选择 CUDA 版本，安装到{" "}
            <Code>/opt/trainmaster-trainer</Code> 的虚拟环境中，
            <Code>--systemd</Code>{" "}
            注册为开机自启的系统服务。平台地址按其它机器实际能访问的地址与端口填写。
          </p>
          <CommandBlock command={commands.bareMetal} />
          <p className="text-xs">
            加 <Code>--dry-run</Code> 只打印将要执行的步骤；
            <Code>--variant</Code> 可以手动指定 CUDA 版本（如 <Code>cu126</Code>
            ）；使用 pip 镜像时这样传入：
            <Code>sudo env PIP_INDEX_URL=… trainer/scripts/install.sh …</Code>。
          </p>
        </Method>
      </ol>

      <div className="grid gap-2">
        {serverUrlIsPlaceholder && <ServerUrlNote />}
        <TokenNote tokenRequired={tokenRequired} />
      </div>
    </>
  )
}
