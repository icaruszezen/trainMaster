import { useId, useState, type FormEvent } from "react"
import {
  CheckCircle2,
  CircleAlert,
  Info,
  KeyRound,
  PlugZap,
  Sparkles,
  Terminal,
} from "lucide-react"
import { toast } from "sonner"

import { StorageKindIcon } from "@/components/storage-node-badge"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  createStorageNode,
  getDockerConfig,
  refreshStorageQueries,
  storageKindLabel,
  testStorageNode,
  updateStorageNode,
  type DockerStorageConfig,
  type LocalStorageConfig,
  type S3StorageConfig,
  type SMBStorageConfig,
  type StorageNode,
  type StorageNodeKind,
  type TestStorageNodeResult,
} from "@/features/system/storage-api"
import {
  generateStorageToken,
  storageConnectCommands,
} from "@/features/system/storage-connect-commands"
import { CommandBlock } from "@/features/training/command-block"
import { formatBytes } from "@/lib/format"
import { cn } from "@/lib/utils"

type StorageNodeDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** null 表示新建存储节点；非 null 表示编辑已有节点。 */
  node: StorageNode | null
}

const KIND_OPTIONS: {
  kind: StorageNodeKind
  title: string
  hint: string
  recommended?: boolean
}[] = [
  {
    kind: "docker",
    title: "存储服务容器 (Docker)",
    hint: "独立存储容器，支持先打包压缩、传输后自动解压",
    recommended: true,
  },
  {
    kind: "s3",
    title: "S3 对象存储",
    hint: "MinIO / RustFS / 云 S3（兼容直传）",
  },
  {
    kind: "smb",
    title: "SMB 共享文件夹",
    hint: "Windows 共享 / NAS（兼容直传）",
  },
  {
    kind: "local",
    title: "本机挂载目录",
    hint: "NFS / 独立磁盘目录（兼容直传）",
  },
]

export function StorageNodeDialog({
  open,
  onOpenChange,
  node,
}: StorageNodeDialogProps) {
  const editing = node !== null
  const formId = useId()
  const initialDocker = getDockerConfig(node?.config)

  const [kind, setKind] = useState<StorageNodeKind>(node?.kind ?? "docker")
  const [name, setName] = useState(node?.name ?? "")
  const [activate, setActivate] = useState(true)

  // Docker storage container
  const [dockerEndpoint, setDockerEndpoint] = useState(
    initialDocker?.endpoint ?? "http://192.168.1.50:8090"
  )
  const [dockerToken, setDockerToken] = useState("")
  const [dockerInsecureTLS, setDockerInsecureTLS] = useState(
    initialDocker?.insecure_skip_verify ?? false
  )

  // Local
  const [localPath, setLocalPath] = useState(node?.config.local?.path ?? "")

  // S3
  const [s3Endpoint, setS3Endpoint] = useState(
    node?.config.s3?.endpoint ?? "http://192.168.10.15:9000"
  )
  const [s3Region, setS3Region] = useState(
    node?.config.s3?.region ?? "us-east-1"
  )
  const [s3Bucket, setS3Bucket] = useState(node?.config.s3?.bucket ?? "")
  const [s3Prefix, setS3Prefix] = useState(
    node?.config.s3?.prefix ?? "trainmaster"
  )
  const [s3AccessKey, setS3AccessKey] = useState(
    node?.config.s3?.access_key_id ?? ""
  )
  const [s3SecretKey, setS3SecretKey] = useState("")
  const [s3PathStyle, setS3PathStyle] = useState(
    node?.config.s3?.force_path_style ?? true
  )
  const [s3InsecureTLS, setS3InsecureTLS] = useState(
    node?.config.s3?.insecure_skip_verify ?? false
  )

  // SMB
  const [smbHost, setSmbHost] = useState(node?.config.smb?.host ?? "")
  const [smbPort, setSmbPort] = useState(String(node?.config.smb?.port ?? 445))
  const [smbShare, setSmbShare] = useState(node?.config.smb?.share ?? "")
  const [smbBasePath, setSmbBasePath] = useState(
    node?.config.smb?.base_path ?? "trainmaster"
  )
  const [smbUsername, setSmbUsername] = useState(
    node?.config.smb?.username ?? ""
  )
  const [smbPassword, setSmbPassword] = useState("")
  const [smbDomain, setSmbDomain] = useState(node?.config.smb?.domain ?? "")

  const [testing, setTesting] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [probeResult, setProbeResult] = useState<TestStorageNodeResult | null>(
    null
  )
  const [formError, setFormError] = useState<string | null>(null)

  const busy = testing || submitting

  const dockerCommands = storageConnectCommands({
    endpoint: dockerEndpoint,
    token: dockerToken,
    hasSavedSecret: Boolean(editing && node.has_secret),
  })

  const buildConfigs = (): {
    local?: LocalStorageConfig
    docker?: DockerStorageConfig
    config?: DockerStorageConfig
    s3?: S3StorageConfig
    smb?: SMBStorageConfig
  } => {
    if (kind === "docker") {
      const dockerCfg: DockerStorageConfig = {
        endpoint: dockerEndpoint.trim(),
        insecure_skip_verify: dockerInsecureTLS,
      }
      return { docker: dockerCfg, config: dockerCfg }
    }
    if (kind === "local") {
      return { local: { path: localPath.trim() } }
    }
    if (kind === "s3") {
      return {
        s3: {
          endpoint: s3Endpoint.trim(),
          region: s3Region.trim() || "us-east-1",
          bucket: s3Bucket.trim(),
          prefix: s3Prefix.trim(),
          access_key_id: s3AccessKey.trim(),
          force_path_style: s3PathStyle,
          insecure_skip_verify: s3InsecureTLS,
        },
      }
    }
    const parsedPort = Number.parseInt(smbPort.trim() || "445", 10)
    return {
      smb: {
        host: smbHost.trim(),
        port: Number.isFinite(parsedPort) && parsedPort > 0 ? parsedPort : 445,
        share: smbShare.trim(),
        base_path: smbBasePath.trim(),
        username: smbUsername.trim(),
        domain: smbDomain.trim(),
      },
    }
  }

  const clearFeedback = () => {
    setProbeResult(null)
    setFormError(null)
  }

  const handleTest = async () => {
    if (busy) return
    clearFeedback()
    setTesting(true)
    try {
      const configs = buildConfigs()
      const res = await testStorageNode({
        node_id: editing ? node.id : undefined,
        kind,
        ...configs,
        token: kind === "docker" ? dockerToken || undefined : undefined,
        secret_access_key: kind === "s3" ? s3SecretKey || undefined : undefined,
        password: kind === "smb" ? smbPassword || undefined : undefined,
      })
      setProbeResult(res)
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "连接测试失败")
    } finally {
      setTesting(false)
    }
  }

  const handleSubmit = async (event: FormEvent) => {
    event.preventDefault()
    if (busy) return
    setFormError(null)

    const trimmedName = name.trim()
    if (!trimmedName) {
      setFormError("请填写存储节点名称")
      return
    }

    setSubmitting(true)
    try {
      const configs = buildConfigs()
      if (editing) {
        await updateStorageNode(node.id, {
          name: trimmedName,
          ...configs,
          token:
            kind === "docker" && dockerToken !== "" ? dockerToken : undefined,
          secret_access_key:
            kind === "s3" && s3SecretKey !== "" ? s3SecretKey : undefined,
          password:
            kind === "smb" && smbPassword !== "" ? smbPassword : undefined,
        })
        await refreshStorageQueries()
        toast.success(`已更新存储节点“${trimmedName}”`)
      } else {
        const created = await createStorageNode({
          name: trimmedName,
          kind,
          ...configs,
          token: kind === "docker" ? dockerToken || undefined : undefined,
          secret_access_key:
            kind === "s3" ? s3SecretKey || undefined : undefined,
          password: kind === "smb" ? smbPassword || undefined : undefined,
          activate,
        })
        await refreshStorageQueries()
        toast.success(
          created.is_active
            ? `已添加并切换当前存储为“${created.name}”`
            : `已添加存储节点“${created.name}”`
        )
      }
      onOpenChange(false)
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "保存存储节点失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) {
          onOpenChange(next)
        }
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={handleSubmit} className="grid gap-5" noValidate>
          <DialogHeader>
            <DialogTitle>
              {editing ? `编辑存储节点：${node.name}` : "新增存储节点"}
            </DialogTitle>
            <DialogDescription>
              推荐使用独立存储服务容器（Docker），在大批量数据集迁移与训练拷贝前自动打包压缩、传输完成后自动校验解压；同时完全兼容按现有方式接入的
              S3、SMB 与挂载目录存储服务器。
            </DialogDescription>
          </DialogHeader>

          {!editing ? (
            <div className="grid gap-2">
              <Label>存储协议类型</Label>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {KIND_OPTIONS.map((option) => {
                  const active = kind === option.kind
                  return (
                    <button
                      key={option.kind}
                      type="button"
                      disabled={busy}
                      onClick={() => {
                        setKind(option.kind)
                        clearFeedback()
                      }}
                      className={cn(
                        "flex flex-col items-start gap-1 rounded-lg border p-3 text-left transition-colors",
                        active
                          ? "border-primary bg-primary/5 ring-1 ring-primary/30"
                          : "hover:bg-muted/60"
                      )}
                    >
                      <span className="flex w-full items-center justify-between gap-1.5 text-sm font-medium">
                        <span className="flex items-center gap-1.5">
                          <StorageKindIcon
                            kind={option.kind}
                            className="size-4 text-muted-foreground"
                          />
                          {option.title}
                        </span>
                        {option.recommended && (
                          <Badge
                            variant="secondary"
                            className="h-5 px-1.5 text-[11px] font-normal"
                          >
                            支持打包加速
                          </Badge>
                        )}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {option.hint}
                      </span>
                    </button>
                  )
                })}
              </div>
            </div>
          ) : (
            <div className="rounded-lg border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
              协议类型：
              <span className="font-medium text-foreground">
                {storageKindLabel(kind)}
              </span>
            </div>
          )}

          <FieldGroup className="gap-4">
            <Field>
              <FieldLabel htmlFor={`${formId}-name`}>节点名称</FieldLabel>
              <Input
                id={`${formId}-name`}
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  clearFeedback()
                }}
                placeholder={
                  kind === "docker"
                    ? "如：机房专用存储容器节点 #1"
                    : kind === "s3"
                      ? "如：机房存储服务器 MinIO"
                      : kind === "smb"
                        ? "如：NAS 共享存储柜"
                        : "如：大容量数据盘 /mnt/data"
                }
                disabled={busy}
                autoComplete="off"
              />
            </Field>

            {kind === "docker" && (
              <>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-docker-endpoint`}>
                      存储容器服务地址（Endpoint）
                    </FieldLabel>
                    <Input
                      id={`${formId}-docker-endpoint`}
                      value={dockerEndpoint}
                      onChange={(e) => {
                        setDockerEndpoint(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="http://192.168.1.50:8090"
                      disabled={busy}
                      autoComplete="off"
                    />
                    <FieldDescription className="text-xs">
                      填写存储服务器可被平台访问的 HTTP(S) 地址与端口（默认
                      8090）。
                    </FieldDescription>
                  </Field>

                  <Field>
                    <div className="flex items-center justify-between gap-2">
                      <FieldLabel htmlFor={`${formId}-docker-token`}>
                        访问令牌（Bearer Token）
                      </FieldLabel>
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        className="h-6 px-2 text-xs"
                        disabled={busy}
                        onClick={() => {
                          setDockerToken(generateStorageToken())
                          clearFeedback()
                        }}
                      >
                        <Sparkles className="size-3" />
                        随机生成
                      </Button>
                    </div>
                    <Input
                      id={`${formId}-docker-token`}
                      type="password"
                      value={dockerToken}
                      onChange={(e) => {
                        setDockerToken(e.target.value)
                        clearFeedback()
                      }}
                      placeholder={
                        editing && node.has_secret
                          ? "已加密保存，留空保持不变"
                          : "与存储容器 TM_STORAGE_TOKEN 一致"
                      }
                      disabled={busy}
                      autoComplete="new-password"
                    />
                    <FieldDescription className="flex items-center gap-1 text-xs">
                      <KeyRound className="size-3 shrink-0" />
                      {editing && node.has_secret && !dockerToken
                        ? "当前使用已加密保存的令牌；如需轮换请填写新令牌。"
                        : dockerToken
                          ? "已填入访问令牌，可直接复制下方命令在存储服务器上启动容器。"
                          : "建议点击“随机生成”自动创建高强度令牌，服务端将使用 AES-256-GCM 加密存储。"}
                    </FieldDescription>
                  </Field>
                </div>

                <div className="flex items-center justify-between gap-3 rounded-lg border bg-muted/30 p-3">
                  <div className="grid gap-0.5">
                    <Label htmlFor={`${formId}-docker-tls`}>
                      跳过 TLS 证书校验
                    </Label>
                    <span className="text-xs text-muted-foreground">
                      仅当存储容器通过内网自签名 HTTPS 反向代理暴露时开启
                    </span>
                  </div>
                  <Switch
                    id={`${formId}-docker-tls`}
                    checked={dockerInsecureTLS}
                    onCheckedChange={(v) => {
                      setDockerInsecureTLS(v)
                      clearFeedback()
                    }}
                    disabled={busy}
                  />
                </div>

                <div className="grid gap-2.5 rounded-lg border bg-muted/25 p-3.5 text-xs">
                  <div className="flex items-center gap-1.5 font-medium text-foreground">
                    <Terminal className="size-3.5 text-muted-foreground" />
                    在存储服务器上启动独立存储 Docker 容器
                  </div>
                  <p className="text-muted-foreground">
                    复制以下任一命令在目标存储机上运行（已自动带入端口{" "}
                    <code className="font-mono text-foreground">
                      {dockerCommands.port}
                    </code>{" "}
                    与上方访问令牌），启动后再点击左下角“测试连接”即可验证连通与打包能力：
                  </p>
                  <div className="grid gap-1.5">
                    <span className="font-medium text-muted-foreground">
                      方式一：使用仓库独立 Compose 配置启动（推荐）
                    </span>
                    <CommandBlock command={dockerCommands.dockerCompose} />
                  </div>
                  <div className="grid gap-1.5">
                    <span className="font-medium text-muted-foreground">
                      方式二：直接使用 Docker 镜像启动
                    </span>
                    <CommandBlock command={dockerCommands.dockerRun} />
                  </div>
                </div>
              </>
            )}

            {kind !== "docker" && (
              <div className="flex items-start gap-2 rounded-lg border bg-muted/30 px-3 py-2.5 text-xs text-muted-foreground">
                <Info className="mt-0.5 size-3.5 shrink-0 text-sky-600 dark:text-sky-400" />
                <span>
                  按现有方式接入的存储服务器保持兼容直传，不提供打包操作；已保存的数据集、数据版本与训练拉取均按原有直传流程无缝运行。
                </span>
              </div>
            )}

            {kind === "s3" && (
              <>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-endpoint`}>
                      Endpoint 服务地址
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-endpoint`}
                      value={s3Endpoint}
                      onChange={(e) => {
                        setS3Endpoint(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="http://192.168.10.15:9000"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-region`}>
                      Region 区域
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-region`}
                      value={s3Region}
                      onChange={(e) => {
                        setS3Region(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="us-east-1"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-bucket`}>
                      Bucket 存储桶
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-bucket`}
                      value={s3Bucket}
                      onChange={(e) => {
                        setS3Bucket(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="vision-datasets"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-prefix`}>
                      对象前缀（可选）
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-prefix`}
                      value={s3Prefix}
                      onChange={(e) => {
                        setS3Prefix(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="trainmaster"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-ak`}>
                      Access Key ID
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-ak`}
                      value={s3AccessKey}
                      onChange={(e) => {
                        setS3AccessKey(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="minioadmin"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-s3-sk`}>
                      Secret Access Key
                    </FieldLabel>
                    <Input
                      id={`${formId}-s3-sk`}
                      type="password"
                      value={s3SecretKey}
                      onChange={(e) => {
                        setS3SecretKey(e.target.value)
                        clearFeedback()
                      }}
                      placeholder={
                        editing && node.has_secret
                          ? "已加密保存，留空保持原密钥不变"
                          : "填写访问密钥"
                      }
                      disabled={busy}
                      autoComplete="new-password"
                    />
                  </Field>
                </div>

                <div className="grid gap-3 rounded-lg border bg-muted/30 p-3 sm:grid-cols-2">
                  <div className="flex items-center justify-between gap-3">
                    <div className="grid gap-0.5">
                      <Label htmlFor={`${formId}-s3-path-style`}>
                        Path-Style 路径寻址
                      </Label>
                      <span className="text-xs text-muted-foreground">
                        MinIO、RustFS、Ceph 等自建存储请开启
                      </span>
                    </div>
                    <Switch
                      id={`${formId}-s3-path-style`}
                      checked={s3PathStyle}
                      onCheckedChange={(v) => {
                        setS3PathStyle(v)
                        clearFeedback()
                      }}
                      disabled={busy}
                    />
                  </div>
                  <div className="flex items-center justify-between gap-3">
                    <div className="grid gap-0.5">
                      <Label htmlFor={`${formId}-s3-tls`}>
                        跳过 TLS 证书校验
                      </Label>
                      <span className="text-xs text-muted-foreground">
                        仅内网自签名 HTTPS 证书时开启
                      </span>
                    </div>
                    <Switch
                      id={`${formId}-s3-tls`}
                      checked={s3InsecureTLS}
                      onCheckedChange={(v) => {
                        setS3InsecureTLS(v)
                        clearFeedback()
                      }}
                      disabled={busy}
                    />
                  </div>
                </div>
              </>
            )}

            {kind === "smb" && (
              <>
                <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-host`}>
                      SMB 服务器地址 / IP
                    </FieldLabel>
                    <Input
                      id={`${formId}-smb-host`}
                      value={smbHost}
                      onChange={(e) => {
                        setSmbHost(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="192.168.10.20"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-port`}>端口</FieldLabel>
                    <Input
                      id={`${formId}-smb-port`}
                      type="number"
                      min={1}
                      max={65535}
                      value={smbPort}
                      onChange={(e) => {
                        setSmbPort(e.target.value)
                        clearFeedback()
                      }}
                      disabled={busy}
                    />
                  </Field>
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-share`}>
                      共享名称（Share）
                    </FieldLabel>
                    <Input
                      id={`${formId}-smb-share`}
                      value={smbShare}
                      onChange={(e) => {
                        setSmbShare(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="ai-datasets"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-base`}>
                      子目录路径（可选）
                    </FieldLabel>
                    <Input
                      id={`${formId}-smb-base`}
                      value={smbBasePath}
                      onChange={(e) => {
                        setSmbBasePath(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="trainmaster"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                </div>

                <div className="grid gap-4 sm:grid-cols-3">
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-user`}>
                      用户名
                    </FieldLabel>
                    <Input
                      id={`${formId}-smb-user`}
                      value={smbUsername}
                      onChange={(e) => {
                        setSmbUsername(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="nas_admin（匿名可留空）"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-pass`}>密码</FieldLabel>
                    <Input
                      id={`${formId}-smb-pass`}
                      type="password"
                      value={smbPassword}
                      onChange={(e) => {
                        setSmbPassword(e.target.value)
                        clearFeedback()
                      }}
                      placeholder={
                        editing && node.has_secret
                          ? "已保存密码，留空不变"
                          : "共享访问密码"
                      }
                      disabled={busy}
                      autoComplete="new-password"
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${formId}-smb-domain`}>
                      域 / 工作组（可选）
                    </FieldLabel>
                    <Input
                      id={`${formId}-smb-domain`}
                      value={smbDomain}
                      onChange={(e) => {
                        setSmbDomain(e.target.value)
                        clearFeedback()
                      }}
                      placeholder="WORKGROUP"
                      disabled={busy}
                      autoComplete="off"
                    />
                  </Field>
                </div>
              </>
            )}

            {kind === "local" && (
              <Field>
                <FieldLabel htmlFor={`${formId}-local-path`}>
                  平台服务器本地绝对路径
                </FieldLabel>
                <Input
                  id={`${formId}-local-path`}
                  value={localPath}
                  onChange={(e) => {
                    setLocalPath(e.target.value)
                    clearFeedback()
                  }}
                  placeholder="/mnt/nas-datasets/trainmaster"
                  disabled={busy}
                  autoComplete="off"
                />
                <FieldDescription className="text-xs">
                  适用于已通过 NFS /
                  大容量数据盘挂载到平台服务器（容器内可见）的独立目录。
                </FieldDescription>
              </Field>
            )}
          </FieldGroup>

          {!editing && (
            <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border bg-muted/30 p-3 text-sm">
              <Checkbox
                checked={activate}
                onCheckedChange={(v) => setActivate(v === true)}
                disabled={busy}
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="font-medium">
                  创建后立即设为当前使用的存储节点
                </span>
                <span className="text-xs text-muted-foreground">
                  开启后会先验证该节点可正常读写；之后新建的数据集与数据版本自动写入此节点，已有历史数据不受影响。
                </span>
              </span>
            </label>
          )}

          {probeResult && (
            <Alert variant={probeResult.ok ? "default" : "destructive"}>
              {probeResult.ok ? (
                <CheckCircle2 className="text-emerald-600 dark:text-emerald-400" />
              ) : (
                <CircleAlert />
              )}
              <AlertTitle>
                {probeResult.ok
                  ? `连接与读写探针通过（${probeResult.latency_ms.toFixed(1)} ms）`
                  : "连接检测未通过"}
              </AlertTitle>
              <AlertDescription className="grid gap-1 text-xs">
                <p>{probeResult.message}</p>
                {probeResult.display_target && (
                  <p className="font-mono break-all text-muted-foreground">
                    目标：{probeResult.display_target}
                  </p>
                )}
                {probeResult.usage && probeResult.usage.total_bytes > 0 && (
                  <p className="text-muted-foreground tabular-nums">
                    可用空间：{formatBytes(probeResult.usage.free_bytes)} /
                    总容量 {formatBytes(probeResult.usage.total_bytes)}
                  </p>
                )}
              </AlertDescription>
            </Alert>
          )}

          {formError && (
            <Alert variant="destructive">
              <CircleAlert />
              <AlertTitle>操作失败</AlertTitle>
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}

          <DialogFooter className="gap-2 sm:justify-between">
            <Button
              type="button"
              variant="secondary"
              onClick={() => void handleTest()}
              disabled={busy}
            >
              {testing ? <Spinner aria-hidden /> : <PlugZap />}
              {testing ? "正在检测读写…" : "测试连接"}
            </Button>
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={busy}
              >
                取消
              </Button>
              <Button type="submit" disabled={busy}>
                {submitting && <Spinner aria-hidden />}
                {submitting
                  ? "正在保存…"
                  : editing
                    ? "保存修改"
                    : activate
                      ? "保存并启用"
                      : "保存节点"}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
