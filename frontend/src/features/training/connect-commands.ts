/** “接入节点”说明中的命令（纯函数，便于测试）。 */

/** 命令中的令牌占位符：用户需要换成部署机 .env 中的 TM_NODE_TOKEN。 */
export const TOKEN_PLACEHOLDER = "<令牌>"

export type ConnectCommands = {
  /** 本机：检测显卡并配置 Docker，然后启动全部服务（含训练节点）。 */
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
}

export function connectCommands({
  serverUrl,
  tokenRequired,
}: {
  serverUrl: string
  tokenRequired: boolean
}): ConnectCommands {
  const args = [`--server-url ${serverUrl}`]
  if (tokenRequired) {
    args.push(`--token ${TOKEN_PLACEHOLDER}`)
  }

  return {
    local: "make setup && make up",
    remoteSetup: `make setup SETUP_ARGS="${args.join(" ")}"`,
    remoteStart: "docker compose up -d --build trainer",
    bareMetal: `sudo trainer/scripts/install.sh ${args.join(" ")} --systemd`,
  }
}
