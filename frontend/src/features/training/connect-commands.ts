/** “接入节点”说明中的命令（纯函数，便于测试）。 */
import type { TrainerMode } from "@/features/training/compute-api"

/** 命令中的令牌占位符：用户需要换成部署机 .env 中的 TM_NODE_TOKEN。 */
export const TOKEN_PLACEHOLDER = "<令牌>"

/** 平台附带的 Windows 安装脚本（TM_TRAINER_PACKAGE_DIR 中的 scripts/install.ps1）。 */
const WINDOWS_INSTALLER_PATH = "/api/v1/compute/windows/install.ps1"

export type ConnectCommands = {
  /**
   * 本机：检测显卡并配置 Docker，然后启动全部服务（含训练节点）。本机的训练节点
   * 用 --mode off 关闭过时恢复自动选择（off 保存在 .env 中，否则 make up 不启动它）。
   */
  local: string
  /**
   * 其它机器上的 Docker 部署：make setup 写入平台地址与令牌（没有 .env 时从
   * .env.example 创建）、检测显卡并选择 CUDA 版本。
   */
  remoteSetup: string
  /**
   * 其它机器上的 Docker 部署：只启动训练节点（make setup 选择 CPU 时为
   * trainer-cpu，按它最后输出的命令执行）。不能用 make up 或不带服务名的
   * docker compose up -d：它们会在那台机器上再启动一套 postgres 与 app。
   */
  remoteStart: string
  /** 不用 Docker，直接安装到 Python 虚拟环境（安装到 /opt 并注册 systemd 服务，需要 root）。 */
  bareMetal: string
  /**
   * Windows 电脑：在管理员 PowerShell 中下载平台附带的安装脚本并运行（安装到
   * C:\ProgramData\trainMaster\trainer，-Service 注册开机自启的计划任务）。
   * https 地址先启用 TLS 1.2：Windows PowerShell 5.1 默认不启用它。
   */
  windows: string
  /** 平台没有附带 Windows 安装脚本时：在 Windows 电脑上的仓库目录中运行。 */
  windowsFromRepo: string
}

/** PowerShell 的单引号字符串：其中的单引号写成两个，其它字符原样保留。 */
function powershellQuote(value: string) {
  return `'${value.replaceAll("'", "''")}'`
}

export function connectCommands({
  serverUrl,
  tokenRequired,
  localTrainerMode = null,
}: {
  serverUrl: string
  tokenRequired: boolean
  /** 平台服务器上训练节点的模式（node_registration.local_trainer_mode）。 */
  localTrainerMode?: TrainerMode | null
}): ConnectCommands {
  const args = [`--server-url ${serverUrl}`]
  if (tokenRequired) {
    args.push(`--token ${TOKEN_PLACEHOLDER}`)
  }

  const windowsArgs = [`-ServerUrl ${powershellQuote(serverUrl)}`]
  if (tokenRequired) {
    windowsArgs.push(`-Token ${powershellQuote(TOKEN_PLACEHOLDER)}`)
  }
  windowsArgs.push("-InstallPython", "-Service")
  const installer = powershellQuote(`${serverUrl}${WINDOWS_INSTALLER_PATH}`)
  const tls12 = /^https:\/\//i.test(serverUrl)
    ? "[Net.ServicePointManager]::SecurityProtocol = 'Tls12'; "
    : ""

  return {
    local:
      localTrainerMode === "off"
        ? 'make up SETUP_ARGS="--mode auto"'
        : "make setup && make up",
    remoteSetup: `make setup SETUP_ARGS="${args.join(" ")}"`,
    remoteStart: "docker compose up -d --build trainer",
    bareMetal: `sudo trainer/scripts/install.sh ${args.join(" ")} --systemd`,
    windows: `${tls12}& ([scriptblock]::Create((irm ${installer}))) ${windowsArgs.join(" ")}`,
    windowsFromRepo: `powershell -ExecutionPolicy Bypass -File trainer\\scripts\\install.ps1 ${windowsArgs.join(" ")}`,
  }
}
