/**
 * 独立存储服务容器（`kind === "docker"`）的部署引导命令（纯函数，便于测试）。
 */

export const DEFAULT_STORAGE_PORT = 8090
export const DEFAULT_STORAGE_HOST_DIR = "/srv/trainmaster-storage"
export const STORAGE_TOKEN_PLACEHOLDER = "<访问令牌>"
export const SAVED_STORAGE_TOKEN_PLACEHOLDER = "<已保存的访问令牌>"

export type StorageConnectCommands = {
  port: number
  /** 在存储服务器仓库目录下使用独立 compose 文件一键启动。 */
  dockerCompose: string
  /** 无需完整仓库时直接使用 docker run 启动存储容器。 */
  dockerRun: string
}

/** 从输入的 Endpoint URL 中提取存储容器宿主机端口；显式 http:// 无端口返回 80，裸主机名返回默认 8090。 */
export function parseStoragePort(endpoint: string): number {
  const raw = endpoint.trim()
  if (!raw) {
    return DEFAULT_STORAGE_PORT
  }
  const hasExplicitScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw)
  const hasExplicitPort80 = /:80(?:\/|$|\?|#)/.test(raw)
  const withScheme = hasExplicitScheme ? raw : `http://${raw}`
  try {
    const parsed = new URL(withScheme)
    if (parsed.port) {
      const port = Number.parseInt(parsed.port, 10)
      if (Number.isInteger(port) && port >= 1 && port <= 65535) {
        return port
      }
    }
    if (parsed.protocol === "https:") {
      return 443
    }
    if (
      parsed.protocol === "http:" &&
      (hasExplicitPort80 || (hasExplicitScheme && !/:\s*$/.test(raw)))
    ) {
      return 80
    }
  } catch {
    // 忽略半输入状态的 URL，回退默认端口
  }
  return DEFAULT_STORAGE_PORT
}

function shellToken(value: string): string {
  if (/^[A-Za-z0-9_./:@-]+$/.test(value)) {
    return value
  }
  return `'${value.replaceAll("'", "'\\''")}'`
}

/** 生成高熵十六进制随机令牌，方便管理员一键填入并复制到存储容器启动命令。 */
export function generateStorageToken(byteLength = 24): string {
  const count = Math.max(16, Math.min(64, byteLength))
  const bytes = new Uint8Array(count)
  if (typeof globalThis.crypto?.getRandomValues === "function") {
    globalThis.crypto.getRandomValues(bytes)
  } else {
    for (let i = 0; i < count; i += 1) {
      bytes[i] = Math.floor(Math.random() * 256)
    }
  }
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
}

export function storageConnectCommands({
  endpoint,
  token,
  hasSavedSecret = false,
  hostDataDir = DEFAULT_STORAGE_HOST_DIR,
}: {
  endpoint: string
  token: string
  hasSavedSecret?: boolean
  hostDataDir?: string
}): StorageConnectCommands {
  const port = parseStoragePort(endpoint)
  const trimmedToken = token.trim()
  const effectiveToken = shellToken(
    trimmedToken
      ? trimmedToken
      : hasSavedSecret
        ? SAVED_STORAGE_TOKEN_PLACEHOLDER
        : STORAGE_TOKEN_PLACEHOLDER
  )
  const dir = shellToken(hostDataDir.trim() || DEFAULT_STORAGE_HOST_DIR)

  return {
    port,
    dockerCompose:
      `TM_STORAGE_PORT=${port} TM_STORAGE_TOKEN=${effectiveToken} ` +
      `TM_STORAGE_HOST_DIR=${dir} docker compose -f docker-compose.storage.yml up -d`,
    dockerRun:
      `docker run -d --name trainmaster-storage --restart unless-stopped ` +
      `-p ${port}:8090 -e TM_STORAGE_TOKEN=${effectiveToken} ` +
      `-v ${dir}:/data trainmaster-storage:latest`,
  }
}
