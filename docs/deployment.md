# 部署指南

trainMaster 以 Docker Compose 部署：`postgres`（PostgreSQL 17）+ `app`（Go 服务，内含前端），以及可选的训练节点 `trainer`（GPU）或 `trainer-cpu`（见第 10 节）。所有数据绑定挂载到宿主机目录。

## 1. 环境要求

- Linux 服务器（推荐），或 Windows / macOS 上的 Docker Desktop（见第 8 节）
- Docker Engine 24+，Docker Compose v2（`docker compose version` 可查看）
- 构建镜像时需要能访问 npm 源、Go 模块代理和 Alpine 软件源（均可替换为国内或内网镜像，见第 7 节）
- 磁盘：图片与模型会持续增长，建议把存储目录放在容量充足的数据盘上
- 使用 GPU 训练时（可选，见第 10 节）：
  - NVIDIA 显卡与 NVIDIA 驱动，`nvidia-smi` 能正常输出。推荐驱动 580 或更新（可使用最新的 CUDA 13 版 PyTorch），最低 450.80.02；不需要安装 CUDA Toolkit，CUDA 运行库随 PyTorch 装在训练节点镜像中。
  - Linux 上的 Docker 还需要 [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)，`make up SETUP_ARGS="--install-toolkit"` 可自动安装与配置（见第 10.2 节）。
  - Windows 上的 Docker Desktop（WSL 2 后端）自带 GPU 支持：在 Windows 中安装 NVIDIA 驱动即可，不需要（也不能）在 WSL 中安装驱动或 NVIDIA Container Toolkit。macOS 没有 NVIDIA GPU 支持，只能运行 CPU 训练节点。
  - 训练节点镜像包含 PyTorch 与 CUDA 运行库，第一次构建需要下载数 GB（可换成国内镜像，见第 7 节）。
- `make setup` / `make up` 需要 bash 与 make（Linux、macOS、WSL 中都有）；没有时可以手动配置（见第 10.4 节）

## 2. 部署步骤

```bash
git clone https://github.com/icaruszezen/trainMaster.git && cd trainMaster
cp .env.example .env
vi .env                       # 至少修改 TM_DB_PASSWORD；按需修改端口、数据目录、PUID/PGID
make setup                    # 检测显卡、驱动与 Docker 的 GPU 支持，把训练节点配置写入 .env（见第 10 节）
make up                       # 再执行一次 make setup，然后先构建并启动 postgres 与 app，再构建并启动训练节点
docker compose ps             # postgres 与 app 都应为 healthy；启用训练节点时还有 trainer 或 trainer-cpu
```

浏览器访问 `http://<服务器IP>:<TM_HTTP_PORT>`（默认 8080），训练节点在“模型训练 → 计算资源”页面中显示。

- `make setup` 的输出说明检测到的显卡、选择的 GPU / CPU 模式与 PyTorch 版本，以及不能使用 GPU 的原因与修复方法（如 `make up SETUP_ARGS="--install-toolkit"`）。
- `make up` 分两步启动：先 `docker compose up -d --build postgres app`，再构建并启动启用的训练节点。训练节点镜像要下载数 GB 的 PyTorch，下载失败时 `make up` 只打印警告，平台照常启动；在 `.env` 中配置镜像源（第 7 节）后重新运行 `make up` 即可。
- 不需要训练节点时：`make setup SETUP_ARGS="--mode off"`，之后的 `make up` 只启动 postgres 与 app。
- 没有 make 时可以直接 `docker compose up -d --build`：`.env.example` 中没有启用训练节点，只启动 postgres 与 app；需要训练节点时按第 10.4 节手动配置。注意它一次构建全部镜像，其中一个构建失败时不会启动或更新任何容器。

> **数据目录放在哪里**：默认的 `./data/storage`、`./data/postgres` 位于 git 检出目录内，适合试用。正式部署建议在 `.env` 中把 `TM_HOST_STORAGE_DIR`、`TM_HOST_POSTGRES_DIR` 改为仓库之外的绝对路径（如 `/srv/trainmaster/storage`、`/srv/trainmaster/postgres`）。数据留在仓库内时，`git clean -x`（或 `-X`）这类清理命令会把 `./data` 连同所有图片一起删除，且没有任何提示。

首次启动时：

1. postgres 用 `TM_DB_USER` / `TM_DB_PASSWORD` / `TM_DB_NAME` 初始化数据库，数据写入 `TM_HOST_POSTGRES_DIR`。
2. app 等待 postgres 健康后启动，容器入口脚本创建存储目录并修正属主，然后以 `PUID:PGID` 运行服务。
3. 服务自动执行数据库迁移，并在 8080 端口提供服务。
4. 启用了训练节点时，它向 app 注册（app 还没就绪时自动重试），随后在后台检测 CUDA 环境并自检（一两分钟），结果出现在“计算资源”页面。

常用命令（也可用 `make up` / `make down` / `make logs` / `make ps`；`make up` 每次都会先执行 `make setup`）：

```bash
docker compose logs -f app     # 应用日志（JSON 格式）
docker compose restart app     # 重启应用
docker compose logs -f trainer # 训练节点日志（CPU 训练节点为 trainer-cpu）
docker compose --profile '*' down   # 停止并删除全部容器（即 make down）；宿主机上的数据目录不受影响
```

`docker compose down` 只删除当前 `COMPOSE_PROFILES` 中的服务：手动切换过 `COMPOSE_PROFILES` 时，另一个训练节点的容器会留下。`make down` 带上全部 profile（`--profile '*'`），把它们一起删除。

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
| `TZ` | `Asia/Shanghai` | 时区（app、postgres 与训练节点） |
| `TM_LOG_LEVEL` | `info` | 日志级别：debug / info / warn / error（app 与训练节点） |
| `TM_TRUSTED_PROXIES` | 空 | 反向代理的 IP 或网段（逗号分隔），见第 9 节 |
| `TM_IMAGE_TAG` | `latest` | 构建出的镜像标签 `trainmaster:<TM_IMAGE_TAG>`；训练节点镜像为 `trainmaster-trainer:<TM_IMAGE_TAG>-<TM_TORCH_VARIANT>` |
| `NPM_REGISTRY` | `https://registry.npmjs.org` | 构建参数：npm 源 |
| `GOPROXY` | `https://proxy.golang.org,direct` | 构建参数：Go 模块代理 |
| `ALPINE_MIRROR` | 空（官方源） | 构建参数：Alpine 软件源，如 `https://mirrors.aliyun.com/alpine` |
| `NODE_IMAGE` / `GO_IMAGE` / `RUNTIME_IMAGE` | `node:22-alpine` / `golang:1.24-alpine` / `alpine:3.23` | 构建参数：应用镜像的基础镜像，可换成内网镜像仓库地址（见第 7 节） |
| `POSTGRES_IMAGE` | `postgres:17-alpine` | 数据库镜像 |
| `TM_DEV_DB_PORT` | `5432` | 仅开发：`docker-compose.dev.yml` 把 postgres 暴露到 `127.0.0.1` 的端口 |
| `TM_NODE_TOKEN` | 空 | 节点令牌（16–256 个可见 ASCII 字符），平台与本机训练节点共用。平台设置后只接受携带它的训练节点；为空时 `make setup` 生成 32 个字符的随机令牌。其它机器上的训练节点填写同一个值 |
| `TM_NODE_HEARTBEAT_INTERVAL` | `10s` | 平台要求训练节点发送心跳的间隔（2s–5m）；超过 3 × 间隔 + 5 秒没有心跳的节点显示为离线 |
| `TM_TRAINER_MODE` | `auto` | 训练节点模式，`make setup` 读取：`auto`（显卡、驱动与 Docker 的 GPU 支持都正常时用 GPU，否则用 CPU）、`gpu` / `cpu`（强制）、`off`（不运行），见第 10 节。也传给训练节点容器：为 `cpu` 时节点知道是有意选择了 CPU |
| `TM_TRAINER_VARIANT` | `auto` | 固定训练节点的 PyTorch 变体（`cu130` / `cu126` / `cu128` / `cu118`）；`auto` 时 `make setup` 按驱动与显卡选择。用 `make setup SETUP_ARGS="--variant cu126"` 写入 |
| `COMPOSE_PROFILES` | 空 | docker compose 启用的 profile：`gpu` → `trainer` 服务，`cpu` → `trainer-cpu` 服务，都没有时不运行训练节点。由 `make setup` 写入，其中的其它 profile 会保留 |
| `TM_TORCH_VARIANT` | `cu130`（`trainer-cpu` 为 `cpu`） | 训练节点镜像中的 PyTorch 变体（`cu130` / `cu126` / `cu128` / `cu118` / `cpu`，见第 10.3 节）。**`make setup` 每次运行（包括 `make up`）都按检测结果重新写入**，手动修改会被覆盖：要换变体请用 `--variant`（写入 `TM_TRAINER_VARIANT`），如 `make up SETUP_ARGS="--variant cu128"`。只有不用 `make setup` 的手动配置（第 10.4 节）才直接修改它，然后 `docker compose up -d --build` |
| `TM_NODE_NAME` | 空 | 训练节点在平台上显示的名称；`make setup` 写入本机主机名，为空时为主机名 |
| `TM_HOST_HOSTNAME` | 空 | 本机的主机名，`make setup` 每次写入。app 与训练节点容器以它作为主机名（compose 中的 `hostname`），页面上显示的服务器与节点主机名就是真实的机器名，重建容器后也不变；为空时 app 为 `trainmaster`、训练节点为 `trainmaster-trainer` |
| `TM_SERVER_URL` | `http://app:8080` | 训练节点连接的平台地址；只在其它机器上运行训练节点时设置，如 `http://192.168.1.10:8080` |
| `TM_HOST_TRAINER_DIR` | `./data/trainer` | 训练节点的状态目录（节点标识 `node-key`、心跳文件、Ultralytics 配置），挂载为容器内 `/var/lib/trainmaster`。训练节点还把 `TM_HOST_STORAGE_DIR` 挂载为 `/data`。不要拷贝到其它机器（见第 6 节） |
| `TM_TRAINER_SHM_SIZE` | `8gb` | 训练节点容器的 `/dev/shm` 大小（PyTorch DataLoader 多进程加载数据使用） |
| `TRAINER_PYTHON_IMAGE` | `python:3.12-slim-bookworm` | 构建参数：训练节点的基础镜像，可换成内网镜像仓库地址；`make setup` 也用它验证 Docker 能否使用显卡 |
| `DEBIAN_MIRROR` | 空（官方源） | 构建参数：训练节点镜像的 Debian 软件源，如 `https://mirrors.aliyun.com/debian` |
| `PIP_INDEX_URL` | `https://pypi.org/simple` | 构建参数：PyPI 源（Ultralytics 等依赖，以及 `cu130` 版 PyTorch） |
| `TORCH_INDEX_URL` | 空（`https://download.pytorch.org/whl`） | 构建参数：PyTorch 官方索引（`cu126` / `cu128` / `cu118` / `cpu` 变体），自动拼接 `/<变体>` |
| `TORCH_FIND_LINKS` | 空 | 构建参数：扁平目录形式的 PyTorch 镜像（如 `https://mirrors.aliyun.com/pytorch-wheels`），设置后代替 `TORCH_INDEX_URL`，自动拼接 `/<变体>/` |
| `NVIDIA_TOOLKIT_REPO` | `https://nvidia.github.io/libnvidia-container` | `--install-toolkit` 使用的 NVIDIA Container Toolkit 软件源，可换成镜像站（见第 7 节） |
| `TM_VERSION` / `TM_COMMIT` / `TM_BUILD_TIME` | 空 | 可选：写入镜像的版本信息；`make up` / `make docker-build` 会自动填入 git 提交号和构建时间 |

修改 `.env` 后执行 `make up`（或 `docker compose up -d`，构建参数需加 `--build`；其它机器上只运行训练节点时见第 10.6 节）使其生效。docker compose 优先使用 shell 中的同名环境变量，其次才是 `.env`；`make setup` 发现 `COMPOSE_PROFILES`、`TM_TORCH_VARIANT` 等在 shell 中设置了不同的值时会提醒。

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
| `TM_NODE_TOKEN` | 空 | 来自 `.env` | 训练节点令牌：设置后 `/api/v1/compute/agent/*` 必须带 `Authorization: Bearer <令牌>`。16–256 个可见 ASCII 字符，首尾不能有空白 |
| `TM_NODE_HEARTBEAT_INTERVAL` | `10s` | 来自 `.env` | 训练节点的心跳间隔（Go duration，2s–5m）；离线判定为 3 × 间隔 + 5 秒 |

非法取值（如端口不是数字、未知的日志级别、过短的令牌）会在启动时报错退出，并列出所有有问题的变量。

### 3.3 训练节点配置（容器内由 compose 和镜像设置）

训练节点（`python -m trainmaster_trainer`）读取下列变量；裸机安装时写在配置文件 `<安装目录>/trainer.env` 中（见第 10.7 节）。空值视为未设置。

| 变量 | 默认值 | 容器内取值 | 说明 |
|---|---|---|---|
| `TM_SERVER_URL` | — | 来自 `.env`，默认 `http://app:8080` | 平台地址（运行时必填） |
| `TM_NODE_TOKEN` | 空 | 来自 `.env` | 与平台的 `TM_NODE_TOKEN` 相同 |
| `TM_NODE_NAME` | 主机名 | 来自 `.env` | 显示名称 |
| `TM_TRAINER_SERVICE` | 空（按 `trainer` 处理） | `trainer` / `trainer-cpu` | 节点所在的 compose 服务，修复建议中的命令用它（如 `docker compose up -d --build trainer-cpu`）；裸机安装不设置 |
| `TM_TRAINER_MODE` | `auto` | 来自 `.env` | `make setup` 的模式。为 `cpu` 时节点看到没有传入容器的显卡，给出“已选择 CPU 模式”的提示，而不是“显卡没有传入容器”的警告 |
| `TM_STATE_DIR` | `/var/lib/trainmaster` | `/var/lib/trainmaster` | 状态目录：节点标识 `node-key`、心跳文件、Ultralytics 配置；不可写时退回 `~/.local/state/trainmaster`（节点标识不能持久保存，日志中有提示） |
| `TM_DATA_DIR` | `/data` | `/data` | 数据目录，上报其磁盘空间；不存在时使用状态目录 |
| `TM_TORCH_VARIANT` | 空 | 镜像构建时的变体 | 已安装的 PyTorch 变体，用于诊断 |
| `TM_SELF_TEST` | `true` | `true` | 检测 CUDA 环境时是否执行自检（矩阵乘法与 YOLO 前向推理） |
| `TM_SELF_TEST_TIMEOUT` | `300` | `300` | 环境检测子进程的超时秒数 |
| `TM_LOG_LEVEL` | `info` | 来自 `.env` | debug / info / warn / error |

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
- 训练节点容器同样以 root 启动：入口脚本 `trainer/docker/entrypoint.sh` 创建状态目录 `/var/lib/trainmaster`（宿主机 `TM_HOST_TRAINER_DIR`），把它本身（非递归）的属主改为 `PUID:PGID`，然后用 `setpriv` 降权运行。存储目录 `/data` 由 app 负责创建与修正属主。

### 直接放到服务器上的数据集

除了在网页上上传，也可以把整理好的数据集文件夹（图片 + 同目录的 VOC `.xml`）直接拷贝到存储目录的 `datasets/` 下，然后在“数据集 → 添加已有目录”中选择它：

```bash
STORAGE_DIR=./data/storage                         # 改过 TM_HOST_STORAGE_DIR 时换成实际目录
rsync -a /path/to/产线A-划痕/ "$STORAGE_DIR/datasets/产线A-划痕/"
sudo chown -R 1000:1000 "$STORAGE_DIR/datasets/产线A-划痕"   # 换成你的 PUID:PGID
```

- 文件夹需要对 `PUID:PGID` 可读（浏览、扫描）；以后要往里导入文件或在页面上删除它时还需要可写。以 `PUID` 对应的用户身份拷贝时不需要 `chown`。
- 只列出 `datasets/` 下的一级文件夹；以 `.` 开头的隐藏文件夹和符号链接不会列出。文件夹内的子目录会递归扫描，但其中的符号链接会被跳过，需要引用其他磁盘上的数据时请直接把那块磁盘挂载进来（或把 `TM_HOST_STORAGE_DIR` 设到那块磁盘上）。
- 之后在服务器上增删、修改了文件，在数据集详情页点“重新扫描”即可同步。

## 5. 升级

```bash
# 1. 先备份（见第 6 节）
# 2. 保留当前镜像用于回退：重新构建会覆盖 trainmaster:latest（TM_IMAGE_TAG 不是 latest 时相应替换）
docker tag trainmaster:latest trainmaster:rollback
# 3. 更新代码并重新构建、启动
git pull
make up                          # 或 docker compose up -d --build（见下面的说明）
docker compose logs -f app       # 确认迁移执行成功、服务正常
```

- 数据库迁移在应用启动时自动执行，多实例同时启动也只会执行一次。
- `make up` 会写入提交号与构建时间，并先重新检测计算资源（驱动升级后可能换用更新的 PyTorch 变体）。它先构建并启动 postgres 与 app，再构建训练节点镜像：PyTorch 变体不变时，体积最大的 PyTorch 层使用构建缓存；训练节点镜像构建失败时只警告，平台的升级不受影响。直接 `docker compose up -d --build` 则一次构建全部镜像，训练节点镜像构建失败时 app 也不会更新。
- **从没有训练节点的版本升级**：`make up` 现在会运行 `make setup`，在 `.env` 中启用训练节点（有可用的显卡时为 GPU 版 `trainer`，否则为 `trainer-cpu`）并构建它的镜像，第一次需要下载 PyTorch（GPU 版数 GB）。国内网络先按第 7 节配置镜像源；不需要训练节点时先执行 `make setup SETUP_ARGS="--mode off"`。
- 新旧版本的平台与训练节点可以混用一段时间（上报的数据允许未知字段），其它机器上的训练节点按第 10.6 节分别升级。
- 迁移只向前，不提供自动回滚。需要回退版本时，先恢复升级前的数据库备份（第 6 节），再用保留的旧镜像启动，不要加 `--build`：

  ```bash
  TM_IMAGE_TAG=rollback docker compose up -d
  ```
- 升级 PostgreSQL 主版本（如 17 → 18）时数据目录格式不兼容，需要用 `pg_dump` 导出后在新版本中导入。

## 6. 备份与恢复

需要备份两部分：**数据库**（元数据）和**存储目录**（图片等文件）。两者应在同一时间点附近备份。训练节点的状态目录（`TM_HOST_TRAINER_DIR`）只保存节点标识，不需要备份：丢失后训练节点会作为新节点注册，旧的节点记录显示为离线，可以在页面上删除。

> **不要把训练节点的状态目录拷贝到其它机器**：`node-key` 就是节点的身份。把整个部署目录（包括 `./data/trainer`）拷贝到另一台 GPU 机器，或克隆装好训练节点的虚拟机，两台机器会使用同一个节点标识，在页面上只显示为一个节点，主机名、显卡与指标在两台机器之间来回变化（平台日志中有“两台机器使用了同一个节点标识”的警告）。在其中一台机器上删除状态目录中的 `node-key`（Docker 为 `./data/trainer/node-key`，裸机默认为 `/var/lib/trainmaster/node-key`）并重启训练节点，它会作为新节点注册。

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

# 存储目录：rsync 增量同步（适合大量图片）。cache/（缩略图）与 tmp/ 可以再生，不需要备份
rsync -a --delete --exclude /cache/ --exclude /tmp/ "$STORAGE_DIR/" "$BACKUP/storage/"
# 或打包成一个文件
tar -C "$STORAGE_DIR" --exclude ./cache --exclude ./tmp -czf "$BACKUP/storage-$(date +%F).tar.gz" .
```

可以把以上命令放入 cron 定时执行，例如每天凌晨（`/opt/trainMaster` 为仓库目录，存储目录按实际修改）：

```cron
0 3 * * * cd /opt/trainMaster && docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > /backup/trainmaster/db-$(date +\%F).dump && rsync -a --delete --exclude /cache/ --exclude /tmp/ ./data/storage/ /backup/trainmaster/storage/
```

### 恢复

```bash
docker compose stop app

# 数据库
docker compose exec -T postgres sh -c \
  'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner' \
  < "$BACKUP/db-2026-10-30.dump"

# 存储目录（cache/、tmp/ 不在备份中，--exclude 保证它们不被删除）
rsync -a --delete --exclude /cache/ --exclude /tmp/ "$BACKUP/storage/" "$STORAGE_DIR/"
# 或：tar -C "$STORAGE_DIR" -xzf "$BACKUP/storage-2026-10-30.tar.gz"

docker compose start app
```

### 整体冷备份 / 迁移到新服务器

停止服务后，把 `.env` 和**两个数据目录**一起打包：

```bash
make down                        # 即 docker compose --profile '*' down，训练节点也一起停止
sudo tar --numeric-owner -P -czf "$BACKUP/full-$(date +%F).tar.gz" .env "$STORAGE_DIR" "$POSTGRES_DIR"
# 在新服务器上：克隆仓库，在仓库目录中解压，然后启动
sudo tar --numeric-owner -P -xzf full-2026-10-30.tar.gz
make up                          # 按新服务器重新检测显卡，更新 .env 中的主机名等；没有 make 时 docker compose up -d --build
```

- 打包中没有训练节点的状态目录（`TM_HOST_TRAINER_DIR`）：新服务器上的训练节点作为新节点注册，旧服务器的节点记录显示为离线，可以在页面上删除。

- `--numeric-owner` 保留文件的数字属主（postgres 数据目录的 uid 70 等）。
- `-P` 保留绝对路径：数据目录在仓库之外（如 `/srv/trainmaster/...`）时，解压后回到新服务器上的相同位置。
- 直接拷贝 PostgreSQL 数据目录要求主版本一致（当前为 17）。数据库以 PostgreSQL 内置的 `C.UTF-8` 排序规则初始化（`POSTGRES_INITDB_ARGS`），不依赖镜像的 libc，因此可以在 alpine 版与 Debian 版 postgres 镜像之间直接切换；更早用其它方式初始化的数据目录，换镜像系列前请先用 `pg_dump` 导出。

## 7. 国内网络 / 内网镜像加速

构建应用镜像会下载三类依赖，都可以通过 `.env` 换成国内或公司内网镜像：

```dotenv
NPM_REGISTRY=https://registry.npmmirror.com
GOPROXY=https://goproxy.cn,direct
ALPINE_MIRROR=https://mirrors.aliyun.com/alpine
```

训练节点镜像需要下载 Debian 软件包、PyPI 上的依赖与 PyTorch（数 GB），对应的设置：

```dotenv
DEBIAN_MIRROR=https://mirrors.aliyun.com/debian
PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple          # 或 https://pypi.tuna.tsinghua.edu.cn/simple
TORCH_FIND_LINKS=https://mirrors.aliyun.com/pytorch-wheels   # cu126 / cu128 / cu118 / cpu 变体
```

- `cu130` 变体的 PyTorch 来自 PyPI，设置 `PIP_INDEX_URL` 即可；`cu126`、`cu128`、`cu118`、`cpu` 来自 PyTorch 官方索引（`https://download.pytorch.org/whl/<变体>`），用 `TORCH_FIND_LINKS`（扁平目录形式的镜像，自动拼接 `/<变体>/`）或 `TORCH_INDEX_URL`（与官方索引结构相同的内网镜像，自动拼接 `/<变体>`）替换。
- 修改后执行 `make up`（或 `docker compose up -d --build`）重新构建。

`--install-toolkit` 从 `https://nvidia.github.io/libnvidia-container` 安装 NVIDIA Container Toolkit。访问慢时在 `.env` 中设置 `NVIDIA_TOOLKIT_REPO`，换成如 USTC 等镜像站提供的 libnvidia-container 镜像（填写与官方地址目录结构相同的地址，其中有 `gpgkey`、`stable/deb/`、`stable/rpm/`；具体地址见镜像站的说明）。脚本会把软件源列表中的官方地址一并替换为镜像地址；zypper 直接使用镜像中的 `.repo` 文件，需要该文件本身指向镜像。

基础镜像（`node:22-alpine`、`golang:1.24-alpine`、`alpine:3.23`、`postgres:17-alpine`，训练节点的 `python:3.12-slim-bookworm`）从 Docker Hub 拉取，拉取慢时：

- 在 Docker 守护进程配置 `/etc/docker/daemon.json` 中设置 `registry-mirrors`（使用公司或云厂商提供的镜像加速地址），然后 `sudo systemctl restart docker`；
- 或者使用内网镜像仓库，在 `.env` 中替换基础镜像。`docker compose up -d --build`、`make up` 和以后的升级都会使用这些地址：

  ```dotenv
  NODE_IMAGE=registry.example.com/library/node:22-alpine
  GO_IMAGE=registry.example.com/library/golang:1.24-alpine
  RUNTIME_IMAGE=registry.example.com/library/alpine:3.23
  POSTGRES_IMAGE=registry.example.com/library/postgres:17-alpine
  TRAINER_PYTHON_IMAGE=registry.example.com/library/python:3.12-slim-bookworm
  ```

**离线服务器**：在能联网的机器上构建并导出镜像，再拷贝到服务器导入：

```bash
docker compose build app && docker pull postgres:17-alpine
docker save trainmaster:latest postgres:17-alpine | gzip > trainmaster-images.tar.gz
# 服务器上
gunzip -c trainmaster-images.tar.gz | docker load
docker compose up -d          # 不加 --build，直接使用导入的镜像
```

训练节点镜像同理：在联网的机器上用与服务器相同的变体构建（`TM_TORCH_VARIANT=cu126 docker compose --profile gpu build trainer`，变体按第 10.3 节或服务器上 `make setup SETUP_ARGS=--dry-run` 的输出选择），再 `docker save trainmaster-trainer:latest-cu126 python:3.12-slim-bookworm` 一起导出（`make setup` 用后者验证 Docker 能否使用显卡）。服务器上导入后执行 `make setup`，再 `docker compose up -d`（不加 `--build`；`make up` 会重新构建）。

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
- **GPU 训练节点**：Docker Desktop 的 WSL 2 后端自带 GPU 支持，只需在 Windows 中安装 NVIDIA 驱动（WSL 中能运行 `nvidia-smi`）。在 WSL 终端中执行 `make setup` 即可识别 Docker Desktop 并验证；不能（也不需要）安装 NVIDIA Container Toolkit。在 PowerShell 中部署、没有 make 时，按第 10.4 节手动编辑 `.env`。不用 Docker Desktop、在 WSL 中直接安装 Docker Engine 时与 Linux 相同，见第 10.2 节。
- 仓库的 `.gitattributes` 固定使用 LF 换行，Windows 上的默认 `core.autocrlf` 设置不影响检出结果；Dockerfile 也会再去除入口脚本中可能的 CRLF。
- macOS 的 Docker Desktop 同样适用以上关于绑定挂载路径的说明（路径如 `/Users/<用户>/trainmaster-data/storage`）。

## 9. 反向代理（nginx 示例）

需要通过域名、HTTPS 或统一入口访问时，在 trainMaster 前面加一层 nginx。数据集导入会上传大量数据：压缩包按 8 MiB 分块上传（服务端单块上限 64 MiB），文件夹按批上传（服务端单批上限 256 MiB），因此 `client_max_body_size` 不能小于 `300m`（以后下载模型等可能更大，下例直接放宽到 2g）；同时注意超时设置：

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

## 10. GPU 训练节点

训练节点（`trainer` / `trainer-cpu` 服务）自动识别本机的显卡、CPU 与 CUDA 环境，向平台注册并定时上报心跳，在“计算资源”页面中显示；镜像中预装与驱动匹配的 PyTorch 与 Ultralytics（YOLO），启动后自检。原理见 [架构说明 · 训练节点](architecture.md#9-训练节点)。

### 10.1 make setup 做了什么

`make setup`（即 `bash deploy/scripts/setup-compute.sh`；`make up` 每次都会先执行它）默认只读取系统信息并修改 `.env`，不需要 root：

1. `.env` 不存在时从 `.env.example` 复制。
2. 确定模式：`--mode` 参数 > 环境变量 `TM_TRAINER_MODE` > `.env` 中的 `TM_TRAINER_MODE` > `auto`。
3. 检测显卡与驱动：`nvidia-smi --query-gpu=name,driver_version,compute_cap,memory.total`（旧版 `nvidia-smi` 查不到计算能力时按 7.5 估计并提示）；没有 `nvidia-smi` 时从 `/sys/bus/pci/devices` 判断有没有 NVIDIA 显卡，有则提示先安装驱动。
4. 按驱动与显卡选择 PyTorch 变体（见 10.3）。
5. 检查 Docker 能否使用显卡：Docker 的运行时中有没有 `nvidia`、是不是 Docker Desktop、有没有 NVIDIA 的 CDI 配置（`/etc/cdi/nvidia.yaml`、`/var/run/cdi/nvidia.yaml`），然后实际运行一次 `docker run --rm --gpus all python:3.12-slim-bookworm nvidia-smi -L`（超时 120 秒；镜像即 `TRAINER_PYTHON_IMAGE`，构建训练节点时本来就要用。拉取失败时视为“无法验证”，按 Docker 的配置判断；`--no-docker-test` 跳过这一步）。
6. 有显卡和驱动、但 Docker 因为缺少（或没有配置）NVIDIA Container Toolkit 不能使用显卡时：加了 `--install-toolkit` 则安装并配置（已安装时只配置并重启 Docker，见 10.2），否则打印要执行的命令。
7. 决定结果：`auto` 时显卡、驱动与 Docker 验证都通过才用 GPU，否则用 CPU，并说明原因与改用 GPU 的方法；`gpu` / `cpu` 强制使用（强制 GPU 而检查没有通过时只警告）；`off` 不运行训练节点。暂时无法判断时不改动原来的结果：
   - 无法连接 Docker（当前用户不在 `docker` 组、dockerd 正在重启等）：`.env` 中已有训练节点配置（`COMPOSE_PROFILES` 含 `gpu` 或 `cpu`）时沿用它；还没有时什么也不写，以退出码 1 结束，提示先解决 Docker 的连接问题（或用 `--mode gpu` / `--mode cpu` 直接指定）。
   - 驱动更新后还没有重启（`nvidia-smi` 报 `Driver/library version mismatch`）：原来是 GPU 时沿用 GPU 配置，不删除仍在运行的 GPU 训练节点，提示重启机器。
8. 写入 `.env`：只替换或追加 `COMPOSE_PROFILES`（保留其中的其它 profile，去掉 `gpu` / `cpu` 后按结果加入）、`TM_TORCH_VARIANT`、`TM_NODE_NAME`（为空时设为主机名）、`TM_HOST_HOSTNAME`（每次写入本机当前的主机名，app 与训练节点容器以它作为主机名）、`TM_NODE_TOKEN`（为空时生成 32 个字符的随机令牌；`TM_SERVER_URL` 指向其它机器时不生成，见 10.6），以及通过参数指定的 `TM_TRAINER_MODE`、`TM_TRAINER_VARIANT`、`TM_SERVER_URL`。其它内容、注释与顺序保持不变，文件权限不变；追加的键放在一个注释标题下。输出中的令牌只显示前 4 个字符。
9. 结果从 GPU 改为 CPU（或反过来，或改为 off）时，删除不再启用的那个训练节点容器，避免同一台机器上有两个节点。
10. 输出中文摘要：显卡（名称、显存、计算能力）、驱动、Docker 的 GPU 支持、模式与变体及原因、下一步命令。

参数通过 `SETUP_ARGS` 传入，如 `make setup SETUP_ARGS="--mode cpu"`（完整说明见 `bash deploy/scripts/setup-compute.sh --help`）：

| 参数 | 说明 |
|---|---|
| `--mode auto\|gpu\|cpu\|off` | 训练节点模式，同时写入 `TM_TRAINER_MODE`，之后的 `make up` 沿用 |
| `--install-toolkit` | Docker 不能使用显卡时安装并配置 NVIDIA Container Toolkit（需要 root 或 sudo）；已安装时只配置并重启 Docker；Docker 已经能使用显卡、没有显卡或不使用 GPU 时什么也不做，照常完成 |
| `--yes` | 安装前不再询问 |
| `--variant cu126` | 固定 PyTorch 变体，写入 `TM_TRAINER_VARIANT`，之后的 `make up` 沿用；`--variant auto` 恢复自动选择 |
| `--server-url URL`、`--token TOKEN` | 在其它机器上运行训练节点时的平台地址与令牌（见 10.6） |
| `--env-file PATH` | 修改其它位置的 `.env` |
| `--no-docker-test` | 不运行 `docker run --gpus all` 验证，只检查 Docker 的配置 |
| `--dry-run` | 只检测并打印将写入的内容：不修改任何文件，不安装任何软件 |

退出码：0 正常（包括强制 GPU 但检查没有通过、只有警告的情况）；1 执行失败（如安装失败、无法连接 Docker 而 `.env` 中还没有训练节点配置，此时不修改 `.env`）；2 参数错误。

`--install-toolkit` 可以直接加在 `make up` 上：`make up SETUP_ARGS="--install-toolkit"` 先安装 / 配置 Toolkit，再启动 GPU 训练节点。

### 10.2 NVIDIA Container Toolkit

Linux 上的 Docker 需要 NVIDIA Container Toolkit 才能把显卡传入容器。`make up SETUP_ARGS="--install-toolkit"`（只检测、不启动时用 `make setup SETUP_ARGS=--install-toolkit`）按 [NVIDIA 官方文档](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html) 的步骤安装：先列出所有命令，确认后逐条执行并显示（加 `--yes` 不再询问；不是 root 时通过 sudo 执行）：

- apt（Debian / Ubuntu）：导入 `https://nvidia.github.io/libnvidia-container/gpgkey`，添加 `…/stable/deb/nvidia-container-toolkit.list`，安装 `nvidia-container-toolkit`；
- dnf / yum（RHEL、Rocky、Fedora、CentOS 等）：添加 `…/stable/rpm/nvidia-container-toolkit.repo`，安装 `nvidia-container-toolkit`；
- zypper（openSUSE / SLES）：添加同一个 `.repo`，安装 `nvidia-container-toolkit`；
- 然后 `nvidia-ctk runtime configure --runtime=docker`（在 `/etc/docker/daemon.json` 中登记 nvidia 运行时）并重启 Docker（systemd 正在运行时用 `systemctl restart docker`，否则用 `service docker restart`），再重新验证。**重启 Docker 会重启正在运行的容器**（`restart: unless-stopped` 的服务会自动恢复）。

已经安装了 Toolkit、只是 Docker 还没有配置或重启时，只执行最后一步（配置并重启 Docker）。Docker 29.5 起，dockerd 启动时找不到 Toolkit 就会用 AMD 的 CDI 驱动处理 `--gpus`：先启动 Docker、后安装 Toolkit 时报 `AMD CDI spec not found`，`make setup` 也按这种情况处理。Docker 已经能使用显卡（或机器上没有可用的显卡、选择了 CPU 模式）时 `--install-toolkit` 什么也不做，照常完成。

以下几种 Docker 不使用 `/etc/docker/daemon.json` 或 `systemctl restart docker`，脚本不自动配置，只打印对应的手动步骤：

- **snap 版 Docker**（Ubuntu 安装程序可选）：安装 Toolkit 的软件包后 `sudo snap restart docker`，snap 启动时自动配置显卡；仍然不行时改用 Docker 官方的软件包。
- **rootless Docker**：配置写到当前用户的 `$HOME/.config/docker/daemon.json`，用 `systemctl --user restart docker` 重启，并执行 `sudo nvidia-ctk config --set nvidia-container-cli.no-cgroups --in-place`（NVIDIA 文档的 Rootless mode 一节）。
- **没有运行 systemd 的 WSL**（在 WSL 中直接安装 Docker Engine、未启用 systemd）：配置后用 `sudo service docker restart` 重启；或在 `/etc/wsl.conf` 中启用 systemd（`[boot]` 一节加 `systemd=true`），在 Windows 中 `wsl --shutdown` 后重新打开 WSL，再运行 `make setup SETUP_ARGS=--install-toolkit`。

安装失败时不修改 `.env`，可以按官方文档手动安装（以 Ubuntu 为例）：

```bash
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
  | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
  | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
  | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
sudo apt-get update && sudo apt-get install -y nvidia-container-toolkit
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
docker run --rm --gpus all python:3.12-slim-bookworm nvidia-smi -L   # 应列出全部显卡
make setup
```

Docker Desktop（Windows 的 WSL 2 后端）自带 GPU 支持，不需要安装。

### 10.3 驱动与 PyTorch 变体

PyTorch 的 CUDA 版本（变体）由 `make setup` 按驱动与显卡自动选择：按下表顺序，取第一个**驱动版本满足、且支持全部显卡**的变体。表格内容来自 `trainer/trainmaster_trainer/torch_variants.txt`（唯一的数据源，裸机安装与训练节点的诊断使用同一份）：

| 变体 | PyTorch / torchvision | CUDA | 最低驱动 | 编译的显卡架构（计算能力） | 安装来源 |
|---|---|---|---|---|---|
| `cu130` | 2.14.1 / 0.29.1 | 13.0 | 580 | 7.5、8.0、8.6、9.0、10.0、12.0 | PyPI（`PIP_INDEX_URL`） |
| `cu126` | 2.14.1 / 0.29.1 | 12.6 | 525.60.13 | 5.0、6.0、7.0、7.5、8.0、8.6、9.0 | PyTorch 索引 |
| `cu128` | 2.11.0 / 0.26.0 | 12.8 | 525.60.13 | 7.5、8.0、8.6、9.0、10.0、12.0 | PyTorch 索引 |
| `cu118` | 2.7.1 / 0.22.1 | 11.8 | 450.80.02 | 3.7、5.0、6.0、7.0、7.5、8.0、8.6、9.0 | PyTorch 索引 |
| `cpu` | 2.14.1 / 0.29.1 | — | — | — | PyTorch 索引 |

- 同一主版本中次版本不小于表中架构的显卡都能运行，如 8.6 也覆盖 RTX 40 系列的 8.9、12.0 也覆盖 12.1。常见显卡的计算能力：GTX 10 系列 6.1，V100 7.0，RTX 20 系列与 T4 7.5，A100 8.0，RTX 30 系列 8.6，RTX 40 系列与 L4 8.9，H100 9.0，B200 10.0，RTX 50 系列 12.0。
- 例子：驱动 550 + RTX 4090 → `cu126`；驱动 570 + RTX 5090 → `cu128`；驱动 580 + RTX 3090 → `cu130`；驱动 580 + GTX 1080 → `cu126`（`cu130` 不支持 6.1）；驱动 470 → `cu118`；驱动低于 450.80.02 → `cpu`。
- 多块显卡中有不受支持的型号时，选支持显卡最多的变体，并提示哪些显卡无法用于训练。
- ARM 服务器（aarch64，如 Grace Hopper）只能使用 `cu130`（需要驱动 ≥ 580 且显卡为 Ampere 或更新），否则为 `cpu`。
- 升级驱动后再次 `make up` 会自动换用更合适的变体并重新构建镜像（镜像标签带变体，不会误用旧镜像）。需要固定某个变体时用 `make setup SETUP_ARGS="--variant cu126"`。
- 训练节点也会按实际的驱动与显卡检查已安装的变体，不匹配时在“计算资源”页面上给出原因与推荐的变体。

### 10.4 手动配置（Windows / 没有 make 时）

没有 bash / make 时直接编辑 `.env`，再 `docker compose up -d --build`：

```dotenv
# GPU 训练节点：先确认 docker run --rm --gpus all python:3.12-slim-bookworm nvidia-smi -L 能列出显卡
COMPOSE_PROFILES=gpu
TM_TORCH_VARIANT=cu130          # 按 10.3 的表选择，驱动低于 580 时一般为 cu126（RTX 50 系列为 cu128）
TM_NODE_NAME=gpu-server-01
TM_HOST_HOSTNAME=gpu-server-01  # 本机的主机名（可选）：app 与训练节点容器使用它作为主机名
TM_NODE_TOKEN=<32 个字母或数字>  # 平台与训练节点共用，可用 openssl rand -hex 16 生成
```

没有可用的显卡时改为 `COMPOSE_PROFILES=cpu`、`TM_TORCH_VARIANT=cpu`（启动 `trainer-cpu`）。以后在有 bash 与 make 的环境中运行 `make setup` / `make up` 时，它们会按检测结果改写 `COMPOSE_PROFILES` 与 `TM_TORCH_VARIANT`；要保留手动的选择，同时设置 `TM_TRAINER_MODE=gpu`（或 `cpu`）与 `TM_TRAINER_VARIANT=<变体>`。

手动把 `COMPOSE_PROFILES` 从 `gpu` 改为 `cpu`（或反过来）时，先删除原来的训练节点容器，再 `docker compose up -d --build`：

```bash
docker compose rm -s -f trainer        # 从 cpu 改为 gpu 时为 trainer-cpu
```

两个训练节点共用状态目录，也就是同一个节点标识，同时运行会互相覆盖平台上的节点信息；`docker compose up` 不会删除没有启用的 profile 中的容器。`make setup` 切换模式时会自动删除，`make down` 也会把它们一起删除。

### 10.5 强制 CPU / 关闭训练节点

- `make setup SETUP_ARGS="--mode cpu"`：始终使用 CPU 训练节点（如显卡留给其它程序使用）；
- `make setup SETUP_ARGS="--mode off"`：不运行训练节点，`make up` 只启动 postgres 与 app；
- `make setup SETUP_ARGS="--mode gpu"`：检查没有通过也使用 GPU 训练节点（只警告），用于 `make setup` 误判的环境；
- `make setup SETUP_ARGS="--mode auto"`：恢复自动检测。

模式写入 `.env` 的 `TM_TRAINER_MODE`，之后的 `make up` 都沿用它（也可以直接 `make up SETUP_ARGS="--mode cpu"`）。模式同时传给训练节点（compose 中的 `TM_TRAINER_MODE`）：有显卡的机器选择了 CPU 模式时，“计算资源”页面显示“已选择 CPU 模式”的提示，不再警告显卡没有传入容器，服务器卡片也不再提示没有 GPU 训练节点；改回自动检测：`make up SETUP_ARGS="--mode auto"`。

### 10.6 在其它 GPU 机器上运行训练节点

平台所在机器之外的 GPU 机器（需要能访问平台的 `TM_HTTP_PORT`）也可以接入，只运行训练节点：

```bash
git clone https://github.com/icaruszezen/trainMaster.git && cd trainMaster
# 令牌在平台所在机器上查看：grep TM_NODE_TOKEN .env
make setup SETUP_ARGS="--server-url http://192.168.1.10:8080 --token <平台的 TM_NODE_TOKEN>"
docker compose up -d --build trainer
docker compose logs -f trainer
```

- 按 `make setup` 最后输出的服务名启动：它选择 CPU 时为 `trainer-cpu`（`docker compose up -d --build trainer-cpu`）。Docker 还不能使用显卡时，加 `--install-toolkit` 让脚本安装 NVIDIA Container Toolkit：`make setup SETUP_ARGS="--install-toolkit"`，再启动它输出的服务（通常是 `docker compose up -d --build trainer`）。`--server-url` 与 `--token` 已写入 `.env`，之后的 `make setup` 不需要再加。
- 只启动训练节点服务（指定服务名）：不要用 `make up` 或不带服务名的 `docker compose up -d`，它们会在这台机器上再启动一套 postgres 与 app。“计算资源”页面上给这类节点的修复建议也都是 `make setup …` 加 `docker compose up -d --build <服务>` 的形式（compose 通过 `TM_TRAINER_SERVICE` 告诉节点自己的服务名）。
- `TM_SERVER_URL` 指向其它机器时，`make setup` 不会生成令牌（必须与平台相同）；平台设置了令牌而这里没有填写时，训练节点注册会失败（日志中为 401 / “节点令牌不正确”）。
- `TM_SERVER_URL` 填平台的 IP 地址或平台机器的主机名，不要填这台机器自己的主机名：训练节点容器的主机名与本机相同（`TM_HOST_HOSTNAME`），在容器中这个名字指向容器自己（`make setup` 发现时会提醒）。
- 平台在 HTTPS 反向代理之后时，填写代理的地址（如 `https://trainmaster.example.internal`）。
- 用 `git clone` 接入新机器，不要从平台或其它节点拷贝整个部署目录：`./data/trainer` 中的节点标识会一起被拷贝，两台机器会成为同一个节点（见第 6 节）。
- 升级：`git pull` 后再次执行 `make setup` 与 `docker compose up -d --build trainer`（或 `make setup` 输出的服务）。修改 `.env` 中的 `TM_TRAINER_SHM_SIZE` 等设置后：`docker compose up -d trainer`。
- 训练节点把这台机器上的 `TM_HOST_STORAGE_DIR` 挂载为 `/data`，目前只用于上报磁盘空间；训练任务上线后需要读取数据集，届时需要通过 NFS 等共享存储挂载与平台相同的目录。

### 10.7 不用 Docker（裸机安装）

```bash
sudo trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --token <平台的 TM_NODE_TOKEN> --systemd
systemctl status trainmaster-trainer
journalctl -u trainmaster-trainer -f
```

- 按与 `make setup` 相同的规则选择 PyTorch 变体（`--variant` 可手动指定），在 `/opt/trainmaster-trainer`（`--prefix` 可改）中创建虚拟环境，安装 PyTorch、Ultralytics 与训练节点，写入配置文件 `<安装目录>/trainer.env`（权限 600，变量见第 3.3 节）并自检；`--systemd` 生成并启用 systemd 服务 `trainmaster-trainer`。
- 需要 Python 3.10 或更新（`--python` 指定解释器）与 NVIDIA 驱动，不需要 CUDA Toolkit 和 Docker。
- 可以重复运行：复用已有的虚拟环境，变体变化时重新安装 PyTorch。已有 `<安装目录>/trainer.env` 时沿用其中的 `TM_SERVER_URL`、`TM_NODE_TOKEN`、`TM_NODE_NAME` 与状态 / 数据目录（参数可以覆盖），之前安装过 systemd 服务时重新安装并重启它。因此升级驱动后换变体只需 `sudo trainer/scripts/install.sh --variant cu128`（在 trainMaster 仓库目录中执行；安装目录不是默认的 `/opt/trainmaster-trainer` 时加 `--prefix`），“计算资源”页面上给裸机节点的修复建议也是这种形式；不要对裸机节点运行 `make setup` / `make up`。`--dry-run` 只打印检测结果与将要执行的步骤；全部参数见 `trainer/scripts/install.sh --help`。
- 镜像加速同样使用 `PIP_INDEX_URL`、`TORCH_INDEX_URL`、`TORCH_FIND_LINKS`（见第 7 节），sudo 时这样传入：`sudo env PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple trainer/scripts/install.sh …`。

### 10.8 只使用部分显卡

`trainer` 默认使用本机的全部显卡。只使用其中几块时（如另外几块留给其它程序），在仓库目录中新建 `docker-compose.override.yml`（需要 Docker Compose v2.24.4 或更新，`!override` 表示替换而不是追加）：

```yaml
services:
  trainer:
    deploy:
      resources:
        reservations:
          devices: !override
            - driver: nvidia
              device_ids: ["0", "1"]   # nvidia-smi -L 中的序号，也可以写 GPU-… UUID
              capabilities: [gpu]
```

然后 `docker compose up -d trainer`，用 `docker compose exec trainer nvidia-smi -L` 确认容器中看到的显卡。

### 10.9 国内镜像

训练节点镜像的 Debian 软件源、PyPI、PyTorch 与 NVIDIA Container Toolkit 软件源都可以换成国内镜像（`DEBIAN_MIRROR`、`PIP_INDEX_URL`、`TORCH_FIND_LINKS`、`NVIDIA_TOOLKIT_REPO`），基础镜像 `python:3.12-slim-bookworm` 可以换成内网镜像仓库（`TRAINER_PYTHON_IMAGE`），见第 7 节。

### 10.10 常见问题

| 现象 | 原因与解决 |
|---|---|
| 启动 `trainer` 时报 `could not select device driver "nvidia" with capabilities: [[gpu]]`（较新的 Docker 为 `failed to discover GPU vendor from CDI: no known GPU vendor found`） | Docker 没有安装或没有配置 NVIDIA Container Toolkit：`make up SETUP_ARGS="--install-toolkit"`（其它机器上的训练节点：`make setup SETUP_ARGS="--install-toolkit"`，再 `docker compose up -d --build trainer`）。暂时不用 GPU：`make up SETUP_ARGS="--mode cpu"` |
| `docker run --gpus all …` 或启动 `trainer` 时报 `AMD CDI spec not found` | Docker 29.5 起，dockerd 启动时还没有安装 NVIDIA Container Toolkit（装好 Toolkit 后没有重启 Docker）就会这样报错，与 AMD 显卡无关：`make up SETUP_ARGS="--install-toolkit"`（已安装时只配置并重启 Docker），或手动 `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker` |
| `--install-toolkit` 只打印了手动步骤，没有安装 | snap 版或 rootless 模式的 Docker、没有运行 systemd 的 WSL：脚本不能可靠地配置它们，按打印的步骤执行后重新运行 `make setup`，见 10.2 |
| `make setup` 报“无法连接 Docker，无法判断训练节点能否使用显卡，没有写入 .env”并退出 | 当前用户不能访问 Docker：`sudo usermod -aG docker $USER` 后重新登录（或用 sudo 运行），确认 `docker info` 正常后重新运行。`.env` 中已有训练节点配置时 `make setup` 不报错，沿用原来的配置 |
| `Failed to initialize NVML: Driver/library version mismatch` | 升级了驱动，但内核中还是旧版的驱动模块：重启机器（或卸载并重新加载 nvidia 内核模块）。`make setup` 与“计算资源”页面会给出同样的提示；重启前 `make setup` 沿用原来的 GPU 配置 |
| 页面提示驱动过旧（`driver_too_old`），或 `make setup` 选择了 `cpu` 并说明驱动过旧 | 驱动低于镜像中 PyTorch 变体要求的最低版本（见 10.3）：升级驱动（推荐 580 或更新）后 `make up`（`make setup` 按当前驱动重新选择变体并重新构建）；或按页面建议换用驱动支持的变体：`make up SETUP_ARGS="--variant cu126"`（其它机器上：`make setup SETUP_ARGS="--variant cu126"`，再 `docker compose up -d --build trainer`）。不要直接修改 `.env` 中的 `TM_TORCH_VARIANT`，下次 `make up` 会把它改回去（见 3.1）。固定了变体（`TM_TRAINER_VARIANT`）时用 `--variant auto` 恢复自动选择 |
| RTX 50 系列（Blackwell，计算能力 12.0）提示显卡架构不受支持（`arch_unsupported`），或训练时报 `no kernel image is available for execution on the device` | 只有 `cu128` 与 `cu130` 支持 RTX 50 系列：驱动 ≥ 580 时为 `cu130`，否则为 `cu128`（RTX 50 系列本身需要 570 或更新的驱动）。`make setup` 会自动选择；固定为 `cu126` / `cu118` 时用 `make up SETUP_ARGS="--variant auto"` 恢复 |
| 训练时 DataLoader 报 `Bus error`、`unable to write to file </torch_…>` 或页面提示 `/dev/shm` 过小（`shm_small`） | 共享内存不足：调大 `.env` 中的 `TM_TRAINER_SHM_SIZE`（默认 `8gb`）后 `make up`（其它机器上：`docker compose up -d trainer`，CPU 训练节点为 `trainer-cpu`），或减少 DataLoader 的 workers |
| 宿主机有显卡，页面提示显卡没有传入容器（`gpu_not_passed_through`） | 运行的是 `trainer-cpu`（Docker 不能使用显卡时 `make setup` 选择了 CPU），或 `trainer` 没有拿到显卡：`make up SETUP_ARGS="--install-toolkit"` 安装 / 配置 NVIDIA Container Toolkit 并启动 GPU 训练节点（需要 sudo；其它机器上用 `make setup SETUP_ARGS="--install-toolkit"` 再启动它输出的服务）。有意选择了 CPU 模式（`--mode cpu`）时页面只显示“已选择 CPU 模式”的提示 |
| `make setup` 提示没能验证 Docker 能否使用显卡（离线或无法访问镜像仓库） | 拉取 `TRAINER_PYTHON_IMAGE` 失败，只能按 Docker 的配置判断：配置镜像加速或内网镜像仓库（第 7 节），或先 `docker load` 导入该镜像，再运行 `make setup` |
| 训练节点一直显示离线或没有出现 | `docker compose logs trainer` 查看：401 / “节点令牌不正确” → 平台与训练节点的 `TM_NODE_TOKEN` 不一致（修改 `.env` 后平台 `make up`，其它机器上的节点 `docker compose up -d trainer`）；连接失败 → 检查 `TM_SERVER_URL` 与防火墙 |
| 两台机器在页面上显示为同一个节点（主机名、显卡在两台机器之间来回变化），平台日志中有“两台机器使用了同一个节点标识” | 拷贝了 `./data/trainer`（或克隆了装好训练节点的虚拟机），两台机器的 `node-key` 相同：在其中一台上删除状态目录中的 `node-key` 并重启训练节点，见第 6 节 |
| `make up` 提示“警告：训练节点（…）没有构建或启动成功” | 平台已经启动，只是训练节点没有起来：按上面的错误信息处理。构建训练节点镜像时下载 PyTorch 失败或超时 → 在 `.env` 中设置 `PIP_INDEX_URL`、`TORCH_FIND_LINKS` 等国内镜像（第 7 节）后重新 `make up` |
| 构建训练节点镜像时下载 PyTorch 很慢或超时 | 设置 `PIP_INDEX_URL`、`TORCH_FIND_LINKS` 等国内镜像，见第 7 节 |

排障时可以在运行中的训练节点容器里打印完整的检测报告（JSON，不连接平台）：`docker compose exec trainer python -m trainmaster_trainer probe`；`docker compose exec trainer python -m trainmaster_trainer selftest` 打印中文的 CUDA 环境检测结果（CPU 训练节点把 `trainer` 换成 `trainer-cpu`）。不要用 `docker compose run --rm trainer …` 排查 CPU 训练节点：指定 `trainer` 会启用 gpu profile 并请求显卡。裸机安装时用安装目录中的解释器：`/opt/trainmaster-trainer/bin/python -m trainmaster_trainer selftest`。

## 11. 运维与排障

| 现象 | 排查 |
|---|---|
| app 一直不 healthy / 反复重启 | `docker compose logs app` 查看启动日志 |
| 日志提示 `is not writable` / 请检查挂载目录权限 | 存储目录属主与 `PUID:PGID` 不一致，见第 4 节 |
| 日志反复出现 `database not ready, retrying` 后退出 | postgres 未启动或账号密码不一致（修改过 `.env` 中的密码？见 3.1 的提示） |
| 端口被占用 | 修改 `.env` 中的 `TM_HTTP_PORT` |
| 页面能打开但工作台显示后端不可达 | `curl http://localhost:8080/api/readyz` 查看数据库与存储检查结果 |
| 导入时提示“存储空间不足” | 压缩包需要约 2 倍于压缩包大小的剩余空间（上传 + 解压）；清理磁盘或把 `TM_HOST_STORAGE_DIR` 换到更大的磁盘 |
| 经 nginx 上传失败，提示 HTTP 413 | `client_max_body_size` 太小，见第 9 节 |
| “添加已有目录”里看不到拷贝上去的文件夹 | 确认文件夹在 `TM_HOST_STORAGE_DIR/datasets/` 下一级、不是以 `.` 开头、不是符号链接，并且对 `PUID:PGID` 可读，见第 4 节；文件夹名不是 UTF-8 编码（如在 Linux 上用 `unzip` 解压 Windows 打包的 zip 得到的乱码名）时也不会列出，请重命名（或改用 `unzip -O GBK` / 7-Zip 解压） |
| 扫描结果提示“文件名不是 UTF-8 编码，已跳过” | 同上，这些文件不会被索引；在服务器上重命名后点“重新扫描”。直接在页面上传压缩包则不会有这个问题（GBK 文件名会自动转换） |
| 数据集显示“文件夹缺失” | 数据集文件夹在服务器上被移动或删除了；放回原位置后点“重新扫描”，或删除该数据集记录 |
| 训练节点（GPU、驱动、CUDA 环境）相关的问题 | 见第 10.10 节 |
| 日志提示 `migration versions must never be reused or renamed` | 两个不同的迁移文件用了同一个编号（如不同分支各自新增了 `0002_*.sql`），或改了已执行文件的名字，见[架构说明 · 数据库迁移约定](architecture.md#7-数据库迁移约定) |

容器日志按 10MB × 5 个文件轮转（`docker-compose.yml` 中的 `x-logging`），`docker compose logs` 只能看到最近的日志，需要长期保存时请接入日志系统。

健康检查接口：`/api/healthz`（存活，Docker HEALTHCHECK 使用）、`/api/readyz`（就绪，含数据库与存储检查，可接入监控）。训练节点的 Docker HEALTHCHECK 检查最近一次心跳是否及时（`python -m trainmaster_trainer healthcheck`），与平台连接中断时显示为 unhealthy。
