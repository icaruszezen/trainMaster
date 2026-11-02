/** “接入节点”说明中的命令（纯函数，便于测试）。 */

/** 命令中的令牌占位符：用户需要换成部署机 .env 中的 TM_NODE_TOKEN。 */
export const TOKEN_PLACEHOLDER = "<令牌>"

export type ConnectCommands = {
  /** 本机：检测显卡并配置 Docker，然后启动全部服务（含训练节点）。 */
  local: string
  /** 其它机器上的 Docker 部署：写入 .env 的内容。 */
  remoteEnv: string
  /** 其它机器上的 Docker 部署：选择 CUDA 版本后只启动训练节点。 */
  remoteStart: string
  /** 不用 Docker，直接安装到 Python 虚拟环境。 */
  bareMetal: string
}

export function connectCommands({
  serverUrl,
  tokenRequired,
}: {
  serverUrl: string
  tokenRequired: boolean
}): ConnectCommands {
  const envLines = [`TM_SERVER_URL=${serverUrl}`]
  const installArgs = [`--server-url ${serverUrl}`]
  if (tokenRequired) {
    envLines.push(`TM_NODE_TOKEN=${TOKEN_PLACEHOLDER}`)
    installArgs.push(`--token ${TOKEN_PLACEHOLDER}`)
  }
  installArgs.push("--systemd")

  return {
    local: "make setup && make up",
    remoteEnv: envLines.join("\n"),
    remoteStart: "make setup\ndocker compose up -d trainer",
    bareMetal: `trainer/scripts/install.sh ${installArgs.join(" ")}`,
  }
}
