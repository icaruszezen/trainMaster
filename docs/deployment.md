# 部署指南

trainMaster 以 Docker Compose 部署：`postgres`（PostgreSQL 17）+ `app`（Go 服务，内含前端）。所有数据绑定挂载到宿主机目录。

## 1. 环境要求

- Linux 服务器（推荐），或 Windows / macOS 上的 Docker Desktop（见第 8 节）
- Docker Engine 24+，Docker Compose v2（`docker compose version` 可查看）
- 构建镜像时需要能访问 npm 源、Go 模块代理和 Alpine 软件源（均可替换为国内或内网镜像，见第 7 节）
- 磁盘：图片与模型会持续增长，建议把存储目录放在容量充足的数据盘上

## 2. 部署步骤

```bash
git clone https://github.com/icaruszezen/trainMaster.git && cd trainMaster
cp .env.example .env
vi .env                       # 至少修改 TM_DB_PASSWORD；按需修改端口、数据目录、PUID/PGID
docker compose up -d --build
docker compose ps             # postgres 与 app 都应为 healthy
```

浏览器访问 `http://<服务器IP>:<TM_HTTP_PORT>`（默认 8080）。

> **数据目录放在哪里**：默认的 `./data/storage`、`./data/postgres` 位于 git 检出目录内，适合试用。正式部署建议在 `.env` 中把 `TM_HOST_STORAGE_DIR`、`TM_HOST_POSTGRES_DIR` 改为仓库之外的绝对路径（如 `/srv/trainmaster/storage`、`/srv/trainmaster/postgres`）。数据留在仓库内时，`git clean -x`（或 `-X`）这类清理命令会把 `./data` 连同所有图片一起删除，且没有任何提示。

首次启动时：

1. postgres 用 `TM_DB_USER` / `TM_DB_PASSWORD` / `TM_DB_NAME` 初始化数据库，数据写入 `TM_HOST_POSTGRES_DIR`。
2. app 等待 postgres 健康后启动，容器入口脚本创建存储目录并修正属主，然后以 `PUID:PGID` 运行服务。
3. 服务自动执行数据库迁移，并在 8080 端口提供服务。

常用命令（也可用 `make up` / `make down` / `make logs` / `make ps`）：

```bash
docker compose logs -f app     # 应用日志（JSON 格式）
docker compose restart app     # 重启应用
docker compose down            # 停止并删除容器；宿主机上的数据目录不受影响
```

## 3. 环境变量

### 3.1 部署配置（`.env`，供 docker compose 使用）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `TM_HTTP_PORT` | `8080` | 宿主机对外端口 |
| `TM_HOST_STORAGE_DIR` | `./data/storage` | 文件存储目录（图片、数据集、模型、运行产物），挂载为容器内 `/data` |
| `TM_HOST_POSTGRES_DIR` | `./data/postgres` | PostgreSQL 数据目录 |
| `TM_DB_USER` | `trainmaster` | 数据库用户（同时用于初始化 postgres 与应用连接） |
| `TM_DB_PASSWORD` | `trainmaster` | 数据库密码，**生产环境务必修改** |
| `TM_DB_NAME` | `trainmaster` | 数据库名 |
| `PUID` / `PGID` | `1000` / `1000` | 应用进程的 UID / GID，见第 4 节 |
| `TZ` | `Asia/Shanghai` | 时区（app 与 postgres） |
| `TM_LOG_LEVEL` | `info` | 日志级别：debug / info / warn / error |
| `TM_TRUSTED_PROXIES` | 空 | 反向代理的 IP 或网段（逗号分隔），见第 9 节 |
| `TM_IMAGE_TAG` | `latest` | 构建出的镜像标签 `trainmaster:<TM_IMAGE_TAG>` |
| `NPM_REGISTRY` | `https://registry.npmjs.org` | 构建参数：npm 源 |
| `GOPROXY` | `https://proxy.golang.org,direct` | 构建参数：Go 模块代理 |
| `ALPINE_MIRROR` | 空（官方源） | 构建参数：Alpine 软件源，如 `https://mirrors.aliyun.com/alpine` |
| `NODE_IMAGE` / `GO_IMAGE` / `RUNTIME_IMAGE` | `node:22-alpine` / `golang:1.24-alpine` / `alpine:3.23` | 构建参数：应用镜像的基础镜像，可换成内网镜像仓库地址（见第 7 节） |
| `POSTGRES_IMAGE` | `postgres:17-alpine` | 数据库镜像 |
| `TM_DEV_DB_PORT` | `5432` | 仅开发：`docker-compose.dev.yml` 把 postgres 暴露到 `127.0.0.1` 的端口 |
| `TM_VERSION` / `TM_COMMIT` / `TM_BUILD_TIME` | 空 | 可选：写入镜像的版本信息；`make up` / `make docker-build` 会自动填入 git 提交号和构建时间 |

修改 `.env` 后执行 `docker compose up -d` 使其生效（构建参数需加 `--build`）。

> `.env` 由 docker compose 解析，值中可以包含 `#`、`$` 等字符；含空格时用引号括起来。
>
> `TM_DB_*` 只在 postgres **首次初始化**时生效。之后要改密码，需先在数据库中修改，再同步修改 `.env`：
>
> ```bash
> docker compose exec postgres psql -U trainmaster -d trainmaster -c "ALTER USER trainmaster WITH PASSWORD '新密码';"
> ```

### 3.2 应用配置（`TM_*`，容器内由 compose 和镜像设置）

服务本身只读取下列变量；空值视为未设置。通常无需手动设置，本地开发或自定义部署时参考：

| 变量 | 默认值 | 容器内取值 | 说明 |
|---|---|---|---|
| `TM_HTTP_ADDR` | `:8080` | `:8080` | 监听地址 |
| `TM_DATA_DIR` | `./data` | `/data` | 文件存储根目录 |
| `TM_WEB_DIR` | 空 | `/app/web` | 前端构建产物目录；为空则不托管前端 |
| `TM_DATABASE_URL` | 空 | — | 完整连接串（URL 或 `key=value` 形式），设置后忽略下面的 `TM_DB_*` |
| `TM_DB_HOST` | `localhost` | `postgres` | 数据库主机 |
| `TM_DB_PORT` | `5432` | `5432` | 数据库端口 |
| `TM_DB_USER` | `trainmaster` | 来自 `.env` | 用户名（自动转义特殊字符） |
| `TM_DB_PASSWORD` | `trainmaster` | 来自 `.env` | 密码（自动转义特殊字符） |
| `TM_DB_NAME` | `trainmaster` | 来自 `.env` | 数据库名 |
| `TM_DB_SSLMODE` | `disable` | `disable` | disable / allow / prefer / require / verify-ca / verify-full |
| `TM_DB_AUTO_MIGRATE` | `true` | `true` | 启动时自动执行数据库迁移 |
| `TM_LOG_LEVEL` | `info` | 来自 `.env` | debug / info / warn / error |
| `TM_LOG_FORMAT` | `text` | `json` | text / json |
| `TM_TRUSTED_PROXIES` | 空 | 来自 `.env` | 受信任的反向代理（IP 或 CIDR，逗号分隔）；为空时忽略 `X-Forwarded-For` |

非法取值（如端口不是数字、未知的日志级别）会在启动时报错退出，并列出所有有问题的变量。

## 4. 目录权限（PUID / PGID）

- app 容器以 root 启动，入口脚本 `deploy/docker/entrypoint.sh` 会创建 `/data` 及 6 个子目录，并在属主不一致时把**这几个目录本身**（非递归）的属主改为 `PUID:PGID`，然后用 `su-exec` 降权，以 `PUID:PGID` 运行服务。
- 不做递归 `chown`，避免存储目录里有海量图片时启动缓慢。
- 建议把 `PUID` / `PGID` 设为宿主机上管理这些数据的用户（执行 `id` 查看），这样在宿主机上可以直接拷贝、整理图片文件。
- **修改了 PUID / PGID**，或者从其他机器拷贝来的数据属主不同，需要手动修正一次：

  ```bash
  sudo chown -R 1000:1000 ./data/storage   # 换成你的 PUID:PGID 和实际的 TM_HOST_STORAGE_DIR
  ```

- 服务启动时会对每个子目录做写入探测，不可写时启动失败并在日志中提示检查挂载目录权限。
- PostgreSQL 数据目录由 postgres 镜像自行管理，属主为容器内的 postgres 用户（uid 70），不要修改。

## 5. 升级

```bash
# 1. 先备份（见第 6 节）
# 2. 保留当前镜像用于回退：重新构建会覆盖 trainmaster:latest（TM_IMAGE_TAG 不是 latest 时相应替换）
docker tag trainmaster:latest trainmaster:rollback
# 3. 更新代码并重新构建、启动
git pull
docker compose up -d --build     # 或 make up（会写入提交号与构建时间）
docker compose logs -f app       # 确认迁移执行成功、服务正常
```

- 数据库迁移在应用启动时自动执行，多实例同时启动也只会执行一次。
- 迁移只向前，不提供自动回滚。需要回退版本时，先恢复升级前的数据库备份（第 6 节），再用保留的旧镜像启动，不要加 `--build`：

  ```bash
  TM_IMAGE_TAG=rollback docker compose up -d
  ```
- 升级 PostgreSQL 主版本（如 17 → 18）时数据目录格式不兼容，需要用 `pg_dump` 导出后在新版本中导入。

## 6. 备份与恢复

需要备份两部分：**数据库**（元数据）和**存储目录**（图片等文件）。两者应在同一时间点附近备份。

下面的命令在仓库目录（`docker-compose.yml` 所在目录）中执行：

- `STORAGE_DIR` / `POSTGRES_DIR` 是 `.env` 中 `TM_HOST_STORAGE_DIR` / `TM_HOST_POSTGRES_DIR` 的值（默认 `./data/storage`、`./data/postgres`），**改过这两个变量时务必换成实际目录**，否则备份到的是空目录或旧目录。
- `BACKUP` 是备份目录，应放在仓库之外（最好在另一块磁盘或另一台机器上），不要把数据库导出和图片打包放进 git 检出目录。

```bash
STORAGE_DIR=./data/storage
POSTGRES_DIR=./data/postgres
BACKUP=/backup/trainmaster
```

### 备份

```bash
mkdir -p "$BACKUP"

# 数据库：在线备份，使用 PostgreSQL 自定义格式
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' \
  > "$BACKUP/db-$(date +%F).dump"

# 存储目录：rsync 增量同步（适合大量图片）
rsync -a --delete "$STORAGE_DIR/" "$BACKUP/storage/"
# 或打包成一个文件
tar -C "$STORAGE_DIR" -czf "$BACKUP/storage-$(date +%F).tar.gz" .
```

可以把以上命令放入 cron 定时执行，例如每天凌晨（`/opt/trainMaster` 为仓库目录，存储目录按实际修改）：

```cron
0 3 * * * cd /opt/trainMaster && docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > /backup/trainmaster/db-$(date +\%F).dump && rsync -a --delete ./data/storage/ /backup/trainmaster/storage/
```

### 恢复

```bash
docker compose stop app

# 数据库
docker compose exec -T postgres sh -c \
  'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner' \
  < "$BACKUP/db-2026-10-30.dump"

# 存储目录
rsync -a --delete "$BACKUP/storage/" "$STORAGE_DIR/"
# 或：tar -C "$STORAGE_DIR" -xzf "$BACKUP/storage-2026-10-30.tar.gz"

docker compose start app
```

### 整体冷备份 / 迁移到新服务器

停止服务后，把 `.env` 和**两个数据目录**一起打包：

```bash
docker compose down
sudo tar --numeric-owner -P -czf "$BACKUP/full-$(date +%F).tar.gz" .env "$STORAGE_DIR" "$POSTGRES_DIR"
# 在新服务器上：克隆仓库，在仓库目录中解压，然后启动
sudo tar --numeric-owner -P -xzf full-2026-10-30.tar.gz
docker compose up -d --build
```

- `--numeric-owner` 保留文件的数字属主（postgres 数据目录的 uid 70 等）。
- `-P` 保留绝对路径：数据目录在仓库之外（如 `/srv/trainmaster/...`）时，解压后回到新服务器上的相同位置。
- 直接拷贝 PostgreSQL 数据目录要求主版本一致（当前为 17）。数据库以 PostgreSQL 内置的 `C.UTF-8` 排序规则初始化（`POSTGRES_INITDB_ARGS`），不依赖镜像的 libc，因此可以在 alpine 版与 Debian 版 postgres 镜像之间直接切换；更早用其它方式初始化的数据目录，换镜像系列前请先用 `pg_dump` 导出。

## 7. 国内网络 / 内网镜像加速

构建镜像会下载三类依赖，都可以通过 `.env` 换成国内或公司内网镜像：

```dotenv
NPM_REGISTRY=https://registry.npmmirror.com
GOPROXY=https://goproxy.cn,direct
ALPINE_MIRROR=https://mirrors.aliyun.com/alpine
```

基础镜像（`node:22-alpine`、`golang:1.24-alpine`、`alpine:3.23`、`postgres:17-alpine`）从 Docker Hub 拉取，拉取慢时：

- 在 Docker 守护进程配置 `/etc/docker/daemon.json` 中设置 `registry-mirrors`（使用公司或云厂商提供的镜像加速地址），然后 `sudo systemctl restart docker`；
- 或者使用内网镜像仓库，在 `.env` 中替换基础镜像。`docker compose up -d --build`、`make up` 和以后的升级都会使用这些地址：

  ```dotenv
  NODE_IMAGE=registry.example.com/library/node:22-alpine
  GO_IMAGE=registry.example.com/library/golang:1.24-alpine
  RUNTIME_IMAGE=registry.example.com/library/alpine:3.23
  POSTGRES_IMAGE=registry.example.com/library/postgres:17-alpine
  ```

**离线服务器**：在能联网的机器上构建并导出镜像，再拷贝到服务器导入：

```bash
docker compose build app && docker pull postgres:17-alpine
docker save trainmaster:latest postgres:17-alpine | gzip > trainmaster-images.tar.gz
# 服务器上
gunzip -c trainmaster-images.tar.gz | docker load
docker compose up -d          # 不加 --build，直接使用导入的镜像
```

## 8. Windows / Docker Desktop 注意事项

- 使用 Docker Desktop 的 **WSL 2 后端**。推荐把仓库克隆到 WSL 发行版的文件系统中（如 `\\wsl$\Ubuntu\home\<用户>\trainMaster`），在 WSL 终端里执行 `docker compose`：文件读写更快，权限行为与 Linux 一致。
- 绑定挂载 Windows 盘符路径时，在 `.env` 中使用正斜杠，例如：

  ```dotenv
  TM_HOST_STORAGE_DIR=D:/trainmaster-data/storage
  TM_HOST_POSTGRES_DIR=D:/trainmaster-data/postgres
  ```

  并在 Docker Desktop 的 Settings → Resources → File sharing 中允许该盘符（WSL 2 后端默认可访问）。
- **PostgreSQL 数据目录不能放在 Windows 盘符（NTFS）上**：NTFS 绑定挂载上的 `chown` / `chmod` 不生效，postgres 会因数据目录属主或权限不对而拒绝启动。在 PowerShell 中直接克隆到 `C:\...` 并使用默认的 `./data/postgres` 就属于这种情况。二选一：
  1. 在 WSL 2 的文件系统中克隆仓库并执行 `docker compose`（推荐，见上一条）；
  2. 改用命名卷：在仓库目录中新建 `docker-compose.override.yml`（compose 会自动读取；挂载点相同的条目会替换原来的绑定挂载）：

     ```yaml
     services:
       postgres:
         volumes:
           - pgdata:/var/lib/postgresql/data
     volumes:
       pgdata:
     ```

     命名卷的数据不在宿主机目录中，备份请使用 `pg_dump`。图片等文件仍可放在 Windows 盘符上（`TM_HOST_STORAGE_DIR`）。
- Docker Desktop 下绑定挂载的属主由 Docker 映射，`PUID` / `PGID` 一般保持默认即可。
- 仓库的 `.gitattributes` 固定使用 LF 换行，Windows 上的默认 `core.autocrlf` 设置不影响检出结果；Dockerfile 也会再去除入口脚本中可能的 CRLF。
- macOS 的 Docker Desktop 同样适用以上关于绑定挂载路径的说明（路径如 `/Users/<用户>/trainmaster-data/storage`）。

## 9. 反向代理（nginx 示例）

需要通过域名、HTTPS 或统一入口访问时，在 trainMaster 前面加一层 nginx。后续上传图片、下载模型可能是大文件、长连接，注意 `client_max_body_size` 与超时设置：

```nginx
server {
    listen 80;
    server_name trainmaster.example.internal;

    # 允许上传大文件（批量图片、模型权重）；0 表示不限制
    client_max_body_size 2g;
    # 大文件直接流式转发给后端，不在 nginx 落盘
    proxy_request_buffering off;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-ID $request_id;

        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        # 训练日志等实时推送（SSE）不缓冲
        proxy_buffering off;
    }
}
```

- 默认不信任 `X-Forwarded-For`（否则内网中任何人都能伪造自己的 IP），访问日志中的 `client_ip` 是 TCP 对端地址。经 nginx 访问时，它是 nginx 的地址——同机部署、经 Docker 端口映射时通常是 Docker 网络的网关（如 `172.18.0.1`）。把这个地址填入 `.env` 的 `TM_TRUSTED_PROXIES`（可写多个 IP 或网段，逗号分隔）并 `docker compose up -d` 后，日志就会记录 nginx 转发的真实客户端 IP。只填写真正的代理地址。
- nginx 与 trainMaster 在同一台机器时，可以让应用端口只监听本机，避免绕过 nginx 直接访问：在 `docker-compose.override.yml` 中为 `app` 设置 `ports: !override ["127.0.0.1:8080:8080"]`。

## 10. 运维与排障

| 现象 | 排查 |
|---|---|
| app 一直不 healthy / 反复重启 | `docker compose logs app` 查看启动日志 |
| 日志提示 `is not writable` / 请检查挂载目录权限 | 存储目录属主与 `PUID:PGID` 不一致，见第 4 节 |
| 日志反复出现 `database not ready, retrying` 后退出 | postgres 未启动或账号密码不一致（修改过 `.env` 中的密码？见 3.1 的提示） |
| 端口被占用 | 修改 `.env` 中的 `TM_HTTP_PORT` |
| 页面能打开但工作台显示后端不可达 | `curl http://localhost:8080/api/readyz` 查看数据库与存储检查结果 |
| 日志提示 `migration versions must never be reused or renamed` | 两个不同的迁移文件用了同一个编号（如不同分支各自新增了 `0002_*.sql`），或改了已执行文件的名字，见[架构说明 · 数据库迁移约定](architecture.md#6-数据库迁移约定) |

容器日志按 10MB × 5 个文件轮转（`docker-compose.yml` 中的 `x-logging`），`docker compose logs` 只能看到最近的日志，需要长期保存时请接入日志系统。

健康检查接口：`/api/healthz`（存活，Docker HEALTHCHECK 使用）、`/api/readyz`（就绪，含数据库与存储检查，可接入监控）。
