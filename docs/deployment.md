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
- 模型仓库下载 ONNX 时，转换由训练节点执行：需要至少一个在线的训练节点（0.2.0 或更新；CPU 训练节点即可，不需要 GPU），见第 10.10 节。没有训练节点时其它功能（包括上传模型、下载 PT）不受影响
- 模型审图的识别同样由训练节点执行（CPU 训练节点即可，有 GPU 时更快），本地审图程序在用户的 64 位 Windows 10 / 11 电脑上运行，见第 10.11 节
- `make setup` / `make up` 需要 bash 与 make（Linux、macOS、WSL 中都有）；没有时可以手动配置（见第 10.4 节）
- 训练节点也可以不用 Docker，直接安装在 Linux（Python 3.10 或更新）或 64 位 Windows 10 / 11、Windows Server 2019 及更新（x64）的电脑上，见第 10.7 节

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
- 不需要训练节点时：`make up SETUP_ARGS="--mode off"`，模式写入 `.env`，之后的 `make up` 只启动 postgres 与 app（恢复：`make up SETUP_ARGS="--mode auto"`，见第 10.5 节）。
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
| `TM_HOST_STORAGE_DIR` | `./data/storage` | 文件存储目录（图片、数据集、模型、审图标注、运行产物），挂载为容器内 `/data` |
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
| `TM_TRAINER_MODE` | `auto` | 训练节点模式，`make setup` 读取：`auto`（显卡、驱动与 Docker 的 GPU 支持都正常时用 GPU，否则用 CPU）、`gpu` / `cpu`（强制）、`off`（不运行），见第 10 节。也传给训练节点容器（为 `cpu` 时节点知道是有意选择了 CPU）与 app（页面据此知道本机的训练节点被关闭或有意使用 CPU，见 10.5） |
| `TM_TRAINER_VARIANT` | `auto` | 固定训练节点的 PyTorch 变体（`cu130` / `cu126` / `cu128` / `cu118`）；`auto` 时 `make setup` 按驱动与显卡选择。用 `make setup SETUP_ARGS="--variant cu126"` 写入。也传给训练节点容器：固定了变体时，页面上的修复建议用 `--variant auto` 恢复自动选择，而不是再固定另一个变体 |
| `COMPOSE_PROFILES` | 空 | docker compose 启用的 profile：`gpu` → `trainer` 服务，`cpu` → `trainer-cpu` 服务，都没有时不运行训练节点。由 `make setup` 写入，其中的其它 profile 会保留 |
| `TM_TORCH_VARIANT` | `cu130`（`trainer-cpu` 为 `cpu`） | 训练节点镜像中的 PyTorch 变体（`cu130` / `cu126` / `cu128` / `cu118` / `cpu`，见第 10.3 节）。**`make setup` 每次运行（包括 `make up`）都按检测结果重新写入**，手动修改会被覆盖：要换变体请用 `--variant`（写入 `TM_TRAINER_VARIANT`），如 `make up SETUP_ARGS="--variant cu128"`。只有不用 `make setup` 的手动配置（第 10.4 节）才直接修改它，然后 `docker compose up -d --build` |
| `TM_NODE_NAME` | 空 | 训练节点在平台上显示的名称，为空时为主机名。为空时 `make setup` 写入本机的主机名；它仍是 `make setup` 写入的主机名（等于原来的 `TM_HOST_HOSTNAME`）时跟随本机当前的主机名（如把 `.env` 迁移到新服务器后，见第 6 节），自己设置的名称不变 |
| `TM_HOST_HOSTNAME` | 空 | 本机的主机名，`make setup` 每次写入。app 与训练节点容器以它作为主机名（compose 中的 `hostname`），页面上显示的服务器与节点主机名就是真实的机器名，重建容器后也不变；为空时 app 为 `trainmaster`、训练节点为 `trainmaster-trainer`。本机的主机名与 compose 中的服务名 `app`、`postgres` 相同（或第一个点之前的部分相同）时 `make setup` 把它留空：容器中这个名字会指向容器自己 |
| `TM_SERVER_URL` | `http://app:8080` | 训练节点连接的平台地址；只在其它机器上运行训练节点时设置，如 `http://192.168.1.10:8080` |
| `TM_HOST_TRAINER_DIR` | `./data/trainer` | 训练节点的状态目录（节点标识 `node-key`、心跳文件、Ultralytics 配置、模型转换与审图识别的临时工作目录 `work/`），挂载为容器内 `/var/lib/trainmaster`。训练节点还把 `TM_HOST_STORAGE_DIR` 挂载为 `/data`。不要拷贝到其它机器（见第 6 节） |
| `TM_TRAINER_SHM_SIZE` | `8gb` | 训练节点容器的 `/dev/shm` 大小（PyTorch DataLoader 多进程加载数据使用） |
| `TM_EXPORT_TIMEOUT` | 空（15 分钟） | 训练节点上一次模型转换（pt → ONNX，含校验）的时间上限，传给 `trainer` 与 `trainer-cpu`：秒数，或带 `s` / `m` 后缀（如 `900`、`30m`），不超过 24 小时。见第 10.10 节 |
| `TM_INFERENCE_IMAGE_TIMEOUT` | 空（5 分钟） | 训练节点识别审图任务时单张图片的时间上限（启动识别进程与加载模型也使用它，至少 2 分钟），传给 `trainer` 与 `trainer-cpu`：秒数，或带 `s` / `m` 后缀（如 `300`、`10m`），不超过 24 小时。见第 10.11 节 |
| `TRAINER_PYTHON_IMAGE` | `python:3.12-slim-bookworm` | 构建参数：训练节点的基础镜像，可换成内网镜像仓库地址；`make setup` 也用它验证 Docker 能否使用显卡 |
| `DEBIAN_MIRROR` | 空（官方源） | 构建参数：训练节点镜像的 Debian 软件源，如 `https://mirrors.aliyun.com/debian` |
| `PIP_INDEX_URL` | `https://pypi.org/simple` | 构建参数：PyPI 源（Ultralytics 等依赖，以及 `cu130` 版 PyTorch） |
| `TORCH_INDEX_URL` | 空（`https://download.pytorch.org/whl`） | 构建参数：PyTorch 官方索引（`cu126` / `cu128` / `cu118` / `cpu` 变体），自动拼接 `/<变体>` |
| `TORCH_FIND_LINKS` | 空 | 构建参数：扁平目录形式的 PyTorch 镜像（如 `https://mirrors.aliyun.com/pytorch-wheels`），设置后代替 `TORCH_INDEX_URL`，自动拼接 `/<变体>/` |
| `NVIDIA_TOOLKIT_REPO` | `https://nvidia.github.io/libnvidia-container` | `--install-toolkit` 使用的 NVIDIA Container Toolkit 软件源，可换成镜像站（见第 7 节） |
| `TM_VERSION` / `TM_COMMIT` / `TM_BUILD_TIME` | 空 | 可选：写入镜像的版本信息；`make up` / `make docker-build` 自动填入 git 提交号，构建时间为该提交的提交时间（UTC；不是 git 检出时为当前时间）。代码没有变化时构建参数不变，`make up` 复用构建缓存，不会重建 app 镜像、重启 app |

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
| `TM_TRAINER_MODE` | 空 | 来自 `.env`，默认 `auto` | 本机训练节点的模式（`make setup` 的 `--mode`），只用于页面上的提示：`GET /api/v1/compute/server` 的 `node_registration.local_trainer_mode`。`auto` / `gpu` / `cpu` / `off` 之外的值按 `auto` 处理（不报错），未设置或为空时为 null。启动时读取，修改后需要重新创建 app 容器（`make up`） |
| `TM_TRAINER_PACKAGE_DIR` | 空 | `/app/tools/trainer` | Windows 训练节点的安装包：目录结构与仓库的 `trainer/` 相同（`pyproject.toml`、`requirements.txt`、`trainmaster_trainer/`、`scripts/install.ps1`）。设置后平台经 `GET /api/v1/compute/windows/install.ps1` 与 `/trainer.zip` 提供安装脚本与安装包（不需要令牌），“接入节点”中 Windows 电脑的命令直接从平台下载它们；为空或文件不全时不提供（页面改为给出在仓库目录中运行的命令）。每次请求时读取文件（安装包在文件变化后重新打包），启动日志说明是否可用，见第 10.7 节 |
| `TM_REVIEWER_TOOL` | 空 | `/app/tools/trainmaster-reviewer.exe` | 附带的 Windows 本地审图程序，审图任务页面经 `GET /api/v1/reviews/tool/download` 提供下载；为空或文件不存在时不提供（页面提示联系管理员）。每次请求时查找文件，启动日志说明是否找到，见第 10.11 节 |

非法取值（如端口不是数字、未知的日志级别、过短的令牌）会在启动时报错退出，并列出所有有问题的变量。

### 3.3 训练节点配置（容器内由 compose 和镜像设置）

训练节点（`python -m trainmaster_trainer`）读取下列变量；裸机安装时写在配置文件 `<安装目录>/trainer.env` 中（见第 10.7 节）。空值视为未设置。`--env-file <文件>`（放在子命令之前或之后都可以）先读取这个配置文件、写入环境变量（覆盖同名的已有值）：Windows 的计划任务这样运行训练节点（Linux 由 systemd 的 `EnvironmentFile` 读取）。配置文件每行一个 `KEY=VALUE`，值可以用单引号（原样）或双引号（`\`、`"`、`$`、`` ` `` 前加 `\`）括起来，Windows 路径中的反斜杠不需要转义。

| 变量 | 默认值 | 容器内取值 | 说明 |
|---|---|---|---|
| `TM_SERVER_URL` | — | 来自 `.env`，默认 `http://app:8080` | 平台地址（运行时必填） |
| `TM_NODE_TOKEN` | 空 | 来自 `.env` | 与平台的 `TM_NODE_TOKEN` 相同 |
| `TM_NODE_NAME` | 主机名 | 来自 `.env` | 显示名称 |
| `TM_TRAINER_SERVICE` | 空（按 `trainer` 处理） | `trainer` / `trainer-cpu` | 节点所在的 compose 服务，修复建议中的命令用它（如 `docker compose up -d --build trainer-cpu`）；裸机安装不设置 |
| `TM_TRAINER_MODE` | `auto` | 来自 `.env` | `make setup` 的模式。为 `cpu` 时节点看到没有传入容器的显卡，给出“已选择 CPU 模式”的提示，而不是“显卡没有传入容器”的警告 |
| `TM_TRAINER_VARIANT` | 空（不固定） | 来自 `.env`，默认 `auto` | `make setup --variant` 固定的 PyTorch 变体，`auto` 为不固定。固定时修复建议用 `--variant auto` 恢复自动选择，而不是再固定另一个变体 |
| `TM_STATE_DIR` | `/var/lib/trainmaster`（Windows 为 `C:\ProgramData\trainMaster\state`） | `/var/lib/trainmaster` | 状态目录：节点标识 `node-key`、心跳文件、Ultralytics 配置、模型转换与审图识别的工作目录 `work/model-export-<ID>/`、`work/review-inference-<ID>/`（结束后删除）；不可写时退回 `~/.local/state/trainmaster`（节点标识不能持久保存，日志中有提示） |
| `TM_DATA_DIR` | `/data`（Windows 为空） | `/data` | 数据目录，上报其磁盘空间；为空或不存在时使用状态目录 |
| `TM_TORCH_VARIANT` | 空 | 镜像构建时的变体 | 已安装的 PyTorch 变体，用于诊断 |
| `TM_SELF_TEST` | `true` | `true` | 检测 CUDA 环境时是否执行自检（矩阵乘法与 YOLO 前向推理） |
| `TM_SELF_TEST_TIMEOUT` | `300` | `300` | 环境检测子进程的超时秒数 |
| `TM_EXPORT_TIMEOUT` | `900` | 来自 `.env`，留空为 `900` | 一次模型转换（pt → ONNX 子进程）的超时：秒数，可带 `s` / `m` 后缀（如 `900`、`30m`），大于 0 且不超过 24 小时（规则同 `TM_SELF_TEST_TIMEOUT`），见第 10.10 节 |
| `TM_INFERENCE_IMAGE_TIMEOUT` | `300` | 来自 `.env`，留空为 `300` | 审图识别时单张图片的超时（从识别子进程开始处理这张图片算起）；启动识别子进程与加载模型也使用它，但至少 120 秒。规则同 `TM_EXPORT_TIMEOUT`，见第 10.11 节 |
| `TM_LOG_LEVEL` | `info` | 来自 `.env` | debug / info / warn / error |
| `TM_LOG_FILE` | 空 | 不设置 | 日志文件（同时写到标准错误），按大小轮转：每个 10 MiB，保留 5 个旧文件；文件被其它程序占用、无法轮转时继续写入，1 分钟后再试。Windows 的 `install.ps1` 设置为 `<状态目录>\logs\trainer.log` |

## 4. 目录权限（PUID / PGID）

- app 容器以 root 启动，入口脚本 `deploy/docker/entrypoint.sh` 会创建 `/data` 及 7 个子目录，并在属主不一致时把**这几个目录本身**（非递归）的属主改为 `PUID:PGID`，然后用 `su-exec` 降权，以 `PUID:PGID` 运行服务。
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
- **从没有训练节点的版本升级**：`make up` 现在会运行 `make setup`，在 `.env` 中启用训练节点（有可用的显卡时为 GPU 版 `trainer`，否则为 `trainer-cpu`）并构建它的镜像，第一次需要下载 PyTorch（GPU 版数 GB）。国内网络先按第 7 节配置镜像源；不需要训练节点时把第 3 步的 `make up` 换成 `make up SETUP_ARGS="--mode off"`。
- 新旧版本的平台与训练节点可以混用一段时间（上报的数据允许未知字段），其它机器上的训练节点按第 10.6 节分别升级。
- **升级到带模型仓库的版本**：迁移 `0008_create_models` 自动执行。ONNX 转换需要 0.2.0 或更新的训练节点（新增依赖 onnx、onnxslim、onnxruntime）：本机的训练节点由 `make up` 重新构建（PyTorch 变体不变时 PyTorch 层使用构建缓存）；其它机器上的训练节点、裸机节点按第 10.10 节升级。旧版训练节点照常注册与上报心跳，只是不领取转换任务；新版训练节点连接旧版平台时同样只是不领取任务（每 5 分钟检查一次平台是否已支持）。
- **升级到带模型审图的版本**：迁移 `0010_create_reviews` 自动执行，存储目录中的 `reviews/` 在启动时自动创建。审图的识别由训练节点执行（0.3.0 或更新）：本机的训练节点由 `make up` 重新构建；其它机器上的训练节点、裸机节点按 10.10 的方法升级。旧版训练节点照常工作，只是不领取审图任务（任务一直排队，页面提示没有可以识别的训练节点）；新版训练节点连接旧版平台时不受影响。本地审图程序随平台镜像一起构建，不需要另外部署（见 10.11）。
- **升级到支持 Windows 训练节点的版本**：平台镜像附带 Windows 的安装脚本与安装包（`TM_TRAINER_PACKAGE_DIR`），“接入节点”中多了 Windows 电脑的命令（见 10.7）。训练节点 0.4.0 上报所在的系统，页面据此显示 Windows 节点的系统版本与操作说明；旧版节点不上报，按 Linux 显示。已有的 Linux 节点不需要为此升级。
- 迁移只向前，不提供自动回滚。需要回退版本时，先恢复升级前的数据库备份（第 6 节），再用保留的旧镜像启动 postgres 与 app（`--no-build`：不构建任何镜像）：

  ```bash
  TM_IMAGE_TAG=rollback docker compose up -d --no-build postgres app
  ```

  这只回退平台：训练节点继续使用当前的镜像（新旧版本的平台与训练节点可以混用，见上文）。不要省略服务名：启用了训练节点时 `TM_IMAGE_TAG=rollback` 也用于训练节点的镜像名，compose 会从当前代码构建并不存在的 `trainmaster-trainer:rollback-<变体>`（数 GB），构建失败时 app 也不会回退。
- 升级 PostgreSQL 主版本（如 17 → 18）时数据目录格式不兼容，需要用 `pg_dump` 导出后在新版本中导入。

## 6. 备份与恢复

需要备份两部分：**数据库**（元数据）和**存储目录**（图片、模型等文件）。两者应在同一时间点附近备份。训练节点的状态目录（`TM_HOST_TRAINER_DIR`）只保存节点标识，不需要备份：丢失后训练节点会作为新节点注册，旧的节点记录显示为离线，可以在页面上删除。

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

**模型仓库**（`models/`）在存储目录的备份中：每个模型文件夹中的 `model.pt` 与 `model.json` 需要备份；`onnx/` 中只是 ONNX 转换结果的缓存，可以不备份（rsync 再加 `--exclude '/models/*/*/onnx/'`，tar 再加 `--exclude './models/*/*/onnx'`），恢复后再次下载时由训练节点重新转换。上传中的模型（`tmp/model-uploads/`）不在备份中，恢复后需要重新上传：继续上传或提交时会提示临时文件已丢失，在上传面板中取消即可（24 小时没有进展的上传也会自动取消）。备份时正在入库的模型文件夹带 `.pending` 标记：恢复后没有对应的模型记录时平台自动清理它（这个版本号随之可用），有记录时只删除标记。数据库与 `models/` 不是同一时间点的备份时：数据库中有、磁盘上没有文件的模型在页面上显示文件缺失，不能下载；磁盘上有、数据库中没有的模型文件夹不会显示，之后新上传的模型会跳过这些版本号，不会覆盖它们。

**模型审图**（`reviews/`）同样在存储目录的备份中：审图任务文件夹中的审图标注（`.xml`）只保存在这里，任务、审核状态与模型的识别结果在数据库中，两者需要一起备份、一起恢复。审图包每次下载时现场打包，服务器上不保存；上传中的审图结果暂存在 `tmp/review-uploads/`，不在备份中。数据库中已经没有对应任务的审图文件夹由平台的清理删除（启动时与每小时）。

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
- `make up` 把新服务器的主机名写入 `TM_HOST_HOSTNAME`；`TM_NODE_NAME` 仍是旧服务器的主机名（`make setup` 写入的值）时也改为新服务器的主机名，新节点不会沿用旧服务器的名字，自己设置的名称不变。`.env` 由较早的版本写入、没有 `TM_HOST_HOSTNAME` 时不会自动修改，需要时在 `make up` 之前清空它：`sed -i 's/^TM_NODE_NAME=.*/TM_NODE_NAME=/' .env`。

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

需要通过域名、HTTPS 或统一入口访问时，在 trainMaster 前面加一层 nginx。数据集导入会上传大量数据：压缩包与模型文件按 8 MiB 分块上传（服务端单块上限 64 MiB），文件夹按批上传（服务端单批上限 256 MiB），因此 `client_max_body_size` 不能小于 `300m`。其它机器上的训练节点经这个代理连接平台时（`TM_SERVER_URL` 为代理的地址），ONNX 转换的结果在一个请求中整个上传（平台上限 4 GiB），`client_max_body_size` 还要大于要转换的模型的 ONNX 文件（下例的 2g 足够常见的 YOLO 模型）；上传本地审图程序导出的审图结果也是一个请求（平台上限 4 GiB，只有审核状态与标注文件、不含图片，通常不大）；下载数据集与审图包是边打包边输出的流式响应（下例的 `proxy_buffering off` 让 nginx 不在磁盘上缓存它们）；领取任务的长轮询最长 30 秒，`proxy_read_timeout` 不能比它短。同时注意超时设置：

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

训练节点（`trainer` / `trainer-cpu` 服务）自动识别本机的显卡、CPU 与 CUDA 环境，向平台注册并定时上报心跳，在“计算资源”页面中显示；镜像中预装与驱动匹配的 PyTorch 与 Ultralytics（YOLO），启动后自检。训练节点还执行模型仓库的 ONNX 转换（见 10.10）与模型审图的识别（见 10.11）。原理见 [架构说明 · 训练节点](architecture.md#9-训练节点)。

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
   - 原来是 GPU、`docker run --gpus all` 验证超时：只说明这次没有得到结果，沿用 GPU 配置。
8. 写入 `.env`：只替换或追加 `COMPOSE_PROFILES`（保留其中的其它 profile，去掉 `gpu` / `cpu` 后按结果加入）、`TM_TORCH_VARIANT`、`TM_NODE_NAME`（为空时设为主机名；仍是上次写入的主机名、即等于原来的 `TM_HOST_HOSTNAME` 时跟随本机当前的主机名）、`TM_HOST_HOSTNAME`（每次写入本机当前的主机名，app 与训练节点容器以它作为主机名；与服务名 `app`、`postgres` 相同时留空）、`TM_NODE_TOKEN`（为空时生成 32 个字符的随机令牌；`TM_SERVER_URL` 指向其它机器时不生成，见 10.6），以及通过参数指定的 `TM_TRAINER_MODE`、`TM_TRAINER_VARIANT`、`TM_SERVER_URL`。其它内容、注释与顺序保持不变，文件权限不变；追加的键放在一个注释标题下。输出中的令牌只显示前 4 个字符。
9. 结果从 GPU 改为 CPU（或反过来，或改为 off）时，删除不再启用的那个训练节点容器，避免同一台机器上有两个节点。
10. 输出中文摘要：显卡（名称、显存、计算能力）、驱动、Docker 的 GPU 支持、模式与变体及原因、下一步命令。

参数通过 `SETUP_ARGS` 传入，如 `make setup SETUP_ARGS="--mode cpu"`（完整说明见 `bash deploy/scripts/setup-compute.sh --help`）：

| 参数 | 说明 |
|---|---|
| `--mode auto\|gpu\|cpu\|off` | 训练节点模式，同时写入 `TM_TRAINER_MODE`，之后的 `make up` 沿用 |
| `--install-toolkit` | Docker 不能使用显卡时安装并配置 NVIDIA Container Toolkit（需要 root 或 sudo）；已安装时只配置并重启 Docker；Docker 已经能使用显卡、没有显卡或不使用 GPU 时什么也不做，照常完成 |
| `--yes` | 安装前不再询问 |
| `--variant cu126` | 固定 PyTorch 变体，写入 `TM_TRAINER_VARIANT`，之后的 `make up` 沿用；`--variant auto` 恢复自动选择 |
| `--server-url URL`、`--token TOKEN` | 在其它机器上运行训练节点时的平台地址与令牌（见 10.6）。地址用平台的 IPv4 地址或域名：训练节点容器的 Docker 网络默认没有 IPv6，`http://[…]` 形式的 IPv6 地址会被拒绝 |
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

| 变体 | PyTorch / torchvision | CUDA | 最低驱动（Linux / Windows） | 编译的显卡架构（计算能力） | 安装来源 |
|---|---|---|---|---|---|
| `cu130` | 2.14.1 / 0.29.1 | 13.0 | 580 / 580 | 7.5、8.0、8.6、9.0、10.0、12.0 | PyPI（`PIP_INDEX_URL`） |
| `cu126` | 2.14.1 / 0.29.1 | 12.6 | 525.60.13 / 528.33 | 5.0、6.0、7.0、7.5、8.0、8.6、9.0 | PyTorch 索引 |
| `cu128` | 2.11.0 / 0.26.0 | 12.8 | 525.60.13 / 528.33 | 7.5、8.0、8.6、9.0、10.0、12.0 | PyTorch 索引 |
| `cu118` | 2.7.1 / 0.22.1 | 11.8 | 450.80.02 / 452.39 | 3.7、5.0、6.0、7.0、7.5、8.0、8.6、9.0 | PyTorch 索引 |
| `cpu` | 2.14.1 / 0.29.1 | — | — | — | PyTorch 索引 |

- 同一主版本中次版本不小于表中架构的显卡都能运行，如 8.6 也覆盖 RTX 40 系列的 8.9、12.0 也覆盖 12.1。常见显卡的计算能力：GTX 10 系列 6.1，V100 7.0，RTX 20 系列与 T4 7.5，A100 8.0，RTX 30 系列 8.6，RTX 40 系列与 L4 8.9，H100 9.0，B200 10.0，RTX 50 系列 12.0。
- 例子：驱动 550 + RTX 4090 → `cu126`；驱动 570 + RTX 5090 → `cu128`；驱动 580 + RTX 3090 → `cu130`；驱动 580 + GTX 1080 → `cu126`（`cu130` 不支持 6.1）；驱动 470 → `cu118`；驱动低于 450.80.02 → `cpu`。
- 多块显卡中有不受支持的型号时，选支持显卡最多的变体，并提示哪些显卡无法用于训练。
- ARM 服务器（aarch64，如 Grace Hopper）只能使用 `cu130`（需要驱动 ≥ 580 且显卡为 Ampere 或更新），否则为 `cpu`。
- Windows（10.7）按 Windows 的最低驱动选择；只有 x64 电脑有 CUDA 版 PyTorch（ARM 版 Windows 为 `cpu`）。Windows 版 PyTorch 一律从 PyTorch 索引安装（PyPI 上的 Windows 版只有 CPU 版），安装来源一列只适用于 Linux。
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

- `make up SETUP_ARGS="--mode cpu"`：始终使用 CPU 训练节点（如显卡留给其它程序使用）；
- `make up SETUP_ARGS="--mode off"`：不运行训练节点（删除本机的训练节点容器），之后的 `make up` 只启动 postgres 与 app；
- `make up SETUP_ARGS="--mode gpu"`：检查没有通过也使用 GPU 训练节点（只警告），用于 `make setup` 误判的环境；
- `make up SETUP_ARGS="--mode auto"`：恢复自动检测（关闭过训练节点时重新启动它）。

模式写入 `.env` 的 `TM_TRAINER_MODE`，之后的 `make up` 都沿用它；只修改 `.env`、暂不启动时用 `make setup SETUP_ARGS="--mode …"`。模式同时传给训练节点与 app（compose 中的 `TM_TRAINER_MODE`）。app 只在启动时读取它，所以修改模式后用 `make up`（app 的环境变量变了，compose 会重新创建 app 容器），页面上的提示才会跟着变化：

- 有显卡的机器选择了 CPU 模式：“计算资源”页面显示“已选择 CPU 模式”的提示，不再警告显卡没有传入容器，服务器卡片也不再提示没有 GPU 训练节点。
- 关闭了训练节点（`off`）：服务器卡片不再提示没有 GPU 训练节点；“接入训练节点”中本机的命令变为 `make up SETUP_ARGS="--mode auto"`，删除节点时的说明也给出这条恢复命令。

页面从 `GET /api/v1/compute/server` 的 `node_registration.local_trainer_mode` 得到模式；不是用 docker-compose.yml 运行 app（没有设置 `TM_TRAINER_MODE`）时它为 null，页面不按模式调整提示。

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
- `TM_SERVER_URL` 填平台的 IPv4 地址或平台机器的主机名（不能用 IPv6 地址：训练节点容器的 Docker 网络默认没有 IPv6，`make setup` 会拒绝；以 IPv6 地址打开页面时，页面上命令中的平台地址是占位符，换成 IPv4 地址或域名），不要填这台机器自己的主机名：训练节点容器的主机名与本机相同（`TM_HOST_HOSTNAME`），在容器中这个名字指向容器自己（`make setup` 发现时会提醒）。
- 平台在 HTTPS 反向代理之后时，填写代理的地址（如 `https://trainmaster.example.internal`）。
- 用 `git clone` 接入新机器，不要从平台或其它节点拷贝整个部署目录：`./data/trainer` 中的节点标识会一起被拷贝，两台机器会成为同一个节点（见第 6 节）。
- 升级：`git pull` 后再次执行 `make setup` 与 `docker compose up -d --build trainer`（或 `make setup` 输出的服务）。修改 `.env` 中的 `TM_TRAINER_SHM_SIZE` 等设置后：`docker compose up -d trainer`。
- 训练节点把这台机器上的 `TM_HOST_STORAGE_DIR` 挂载为 `/data`，目前只用于上报磁盘空间（模型转换与审图识别通过 HTTP 下载模型与图片、上传结果，不需要共享存储）；训练任务上线后需要读取数据集，届时需要通过 NFS 等共享存储挂载与平台相同的目录。

### 10.7 不用 Docker（裸机安装）

Linux 用 `trainer/scripts/install.sh`，Windows 用 `trainer/scripts/install.ps1`（见下方“Windows 电脑”）。

```bash
sudo trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --token <平台的 TM_NODE_TOKEN> --systemd
systemctl status trainmaster-trainer
journalctl -u trainmaster-trainer -f
```

- 按与 `make setup` 相同的规则选择 PyTorch 变体（`--variant` 可手动指定），在 `/opt/trainmaster-trainer`（`--prefix` 可改）中创建虚拟环境，安装 PyTorch、Ultralytics 与训练节点，写入配置文件 `<安装目录>/trainer.env`（权限 600，变量见第 3.3 节）并自检；`--systemd` 生成并启用 systemd 服务 `trainmaster-trainer`。
- 需要 Python 3.10 或更新（`--python` 指定解释器）与 NVIDIA 驱动，不需要 CUDA Toolkit 和 Docker。
- 可以重复运行：复用已有的虚拟环境，变体变化时重新安装 PyTorch。已有 `<安装目录>/trainer.env` 时沿用其中的 `TM_SERVER_URL`、`TM_NODE_TOKEN`、`TM_NODE_NAME` 与状态 / 数据目录（参数可以覆盖），之前安装过 systemd 服务时重新安装并重启它。因此升级驱动后换变体只需 `sudo trainer/scripts/install.sh --variant cu128`（在 trainMaster 仓库目录中执行；安装目录不是默认的 `/opt/trainmaster-trainer` 时加 `--prefix`），“计算资源”页面上给裸机节点的修复建议也是这种形式；不要对裸机节点运行 `make setup` / `make up`。`--dry-run` 只打印检测结果与将要执行的步骤；全部参数见 `trainer/scripts/install.sh --help`。
- 镜像加速同样使用 `PIP_INDEX_URL`、`TORCH_INDEX_URL`、`TORCH_FIND_LINKS`（见第 7 节），sudo 时这样传入：`sudo env PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple trainer/scripts/install.sh …`。

#### Windows 电脑

64 位的 Windows 10 / 11 或 Windows Server 2019 及更新（x64）可以直接作为训练节点，不需要 Docker、WSL 或 CUDA Toolkit，只需要 NVIDIA 驱动（没有显卡时作为 CPU 训练节点）。在“计算资源 → 接入节点”中选择“Windows 电脑（直接安装）”，复制命令，在这台电脑上以管理员身份打开 PowerShell（右键“开始”→“终端（管理员）”或“Windows PowerShell（管理员）”）运行，例如：

```powershell
& ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -ServerUrl 'http://192.168.1.10:8080' -Token '<平台的 TM_NODE_TOKEN>' -InstallPython -Service
```

命令从平台下载安装脚本 `trainer/scripts/install.ps1` 与训练节点的安装包（平台的 Docker 镜像附带它们，见第 3.2 节的 `TM_TRAINER_PACKAGE_DIR`）并运行，PowerShell 窗口在安装结束后保持打开。平台没有附带时，页面给出的是在仓库目录中运行的命令：在这台电脑上克隆 trainMaster 仓库，在仓库目录中运行 `powershell -ExecutionPolicy Bypass -File trainer\scripts\install.ps1 -ServerUrl … -Token … -InstallPython -Service`。

安装脚本做的事：

- 用 NVML（失败时用 `nvidia-smi`）检测显卡与驱动，按 10.3 的规则与 Windows 的最低驱动选择 PyTorch 变体，`-Variant` 可手动指定；有 NVIDIA 显卡但没有可用的驱动时提示先安装驱动（推荐 580 或更新）。
- 需要 64 位的 Python 3.10–3.13（不支持 Microsoft Store 版）与 Microsoft Visual C++ 运行库（PyTorch 需要）。没有时 `-InstallPython` 为所有用户安装 Python 3.12 与运行库：优先用 winget，没有 winget 时下载 python.org 与微软的安装程序；`-Python` 指定已有的解释器。
- 在 `C:\ProgramData\trainMaster\trainer`（`-Prefix`）中创建虚拟环境，从 PyTorch 索引安装带变体后缀的 PyTorch（如 `torch==2.14.1+cu130`；PyPI 上的 Windows 版只有 CPU 版），确认装上的正是这个变体，再安装 Ultralytics 与训练节点。
- 写入配置文件 `C:\ProgramData\trainMaster\trainer\trainer.env`（`-EnvFile`，变量见第 3.3 节），状态目录为 `C:\ProgramData\trainMaster\state`（`-StateDir`），日志写到其中的 `logs\trainer.log`，然后自检。
- `-Service` 注册并启动计划任务 `trainmaster-trainer`：开机时以 SYSTEM 运行，不需要登录；训练节点没有在运行时（崩溃或被结束）每 5 分钟重新启动它。不加 `-Service` 时只安装，最后给出手动运行的命令。
- 计划任务以 SYSTEM 运行安装目录中的程序，所以 `C:\ProgramData\trainMaster` 及其中的安装目录、状态目录只允许 SYSTEM 与管理员修改（普通用户只读），配置文件（含令牌）只有 SYSTEM 与管理员可以读写。其他用户能修改的已有安装目录会被拒绝；使用其它位置的目录时，上级目录允许普通用户改名或删除的（如另一块磁盘上新建的文件夹）会给出提醒。
- 可以重复运行，与 Linux 相同：复用已有的虚拟环境，沿用配置文件中的平台地址、令牌、节点名称、目录与手动添加的其它设置（参数可以覆盖），已注册计划任务时更新并重启它。升级驱动后换变体：在管理员 PowerShell 中重新运行页面上的命令并加 `-Variant cu128`（其它参数可以省略），“计算资源”页面给 Windows 节点的修复建议也是这种形式。`-DryRun` 只打印检测结果与将要执行的步骤（不需要管理员）；全部参数：`Get-Help <install.ps1 的路径> -Detailed`。
- 镜像加速：运行命令之前在同一个 PowerShell 窗口中设置 `$env:PIP_INDEX_URL`、`$env:TORCH_FIND_LINKS` 或 `$env:TORCH_INDEX_URL`（含义见第 7 节），例如 `$env:PIP_INDEX_URL = 'https://mirrors.aliyun.com/pypi/simple'`。

日常管理（在管理员 PowerShell 中）：

| 操作 | 命令 |
|---|---|
| 查看状态 | `Get-ScheduledTask -TaskName trainmaster-trainer`（`State` 为 `Running`） |
| 查看日志 | `Get-Content -Encoding UTF8 -Wait -Tail 50 C:\ProgramData\trainMaster\state\logs\trainer.log`（日志为 UTF-8；Windows PowerShell 5.1 不加 `-Encoding UTF8` 时中文显示为乱码） |
| 修改配置后重启 | 编辑 `trainer.env`，然后 `Stop-ScheduledTask -TaskName trainmaster-trainer; Start-ScheduledTask -TaskName trainmaster-trainer` |
| 检测环境 | `& 'C:\ProgramData\trainMaster\trainer\Scripts\python.exe' -m trainmaster_trainer selftest --env-file 'C:\ProgramData\trainMaster\trainer\trainer.env'` |
| 停止并删除计划任务 | 重新运行安装命令，只加 `-Uninstall`（安装目录、配置文件与状态目录保留，不再需要时手动删除），然后在“计算资源”页面删除这个节点 |

- 重新运行安装命令时，计划任务在安装期间被禁用（以免它用一半新一半旧的文件启动训练节点），安装结束或出错时恢复。安装过程中关闭了 PowerShell 窗口时它可能保持禁用（`Get-ScheduledTask` 的 `State` 为 `Disabled`）：重新运行安装命令，或运行 `Enable-ScheduledTask -TaskName trainmaster-trainer`。
- 电脑睡眠时节点离线：接通电源时把睡眠设为“从不”（设置 → 系统 → 电源，或 `powercfg /change standby-timeout-ac 0`），安装脚本发现会睡眠时会提醒。
- 训练节点只主动连接平台，不需要在 Windows 防火墙中开放端口；平台地址填平台的 IP 地址或域名。
- 卸载或升级了创建虚拟环境时使用的 Python 后，训练节点无法启动：删除安装目录后重新运行安装命令。

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

裸机安装（第 10.7 节）时，在训练节点的配置文件（默认 `<安装目录>/trainer.env`；安装时用 `--env-file` 指定了其它文件时为该文件）中设置 `CUDA_VISIBLE_DEVICES=0,1`（`nvidia-smi -L` 中的序号：`install.sh` 写入了 `CUDA_DEVICE_ORDER=PCI_BUS_ID`，CUDA 的序号与它一致；也可以写 `GPU-…` UUID），然后 `sudo systemctl restart trainmaster-trainer`（Windows 节点的配置文件默认为 `C:\ProgramData\trainMaster\trainer\trainer.env`，修改后在管理员 PowerShell 中 `Stop-ScheduledTask -TaskName trainmaster-trainer; Start-ScheduledTask -TaskName trainmaster-trainer`）。新旧显卡混用、没有同时支持全部显卡的 PyTorch 变体时，“计算资源”页面建议的就是这种做法；用它排除了全部不受支持的显卡后，页面上对这些显卡只给出提示，不再警告。

### 10.9 国内镜像

训练节点镜像的 Debian 软件源、PyPI、PyTorch 与 NVIDIA Container Toolkit 软件源都可以换成国内镜像（`DEBIAN_MIRROR`、`PIP_INDEX_URL`、`TORCH_FIND_LINKS`、`NVIDIA_TOOLKIT_REPO`），基础镜像 `python:3.12-slim-bookworm` 可以换成内网镜像仓库（`TRAINER_PYTHON_IMAGE`），见第 7 节。

### 10.10 模型转换（ONNX）

在模型仓库中下载 ONNX 时，平台把转换放入队列，由训练节点领取后在本机转换并把结果传回平台（原理见 [架构说明 · 模型转换](architecture.md#模型转换)）。

- **需要至少一个在线的训练节点，版本 0.2.0 或更新**（“计算资源”页面上显示为“节点 v0.2.0”；`python -m trainmaster_trainer --version`）。转换在 CPU 上进行（转换子进程中 `CUDA_VISIBLE_DEVICES` 为空），不占用显存，也不影响正在进行的训练，所以 **CPU 训练节点（`trainer-cpu`）就足够，不需要 GPU**。`make up` 在没有可用显卡时也会启动 CPU 训练节点；用 `--mode off` 关闭了训练节点的部署不能转换 ONNX（PT 下载不受影响），需要时改为 `make up SETUP_ARGS="--mode cpu"`。
- **依赖**：转换需要 `onnx`、`onnxslim` 与 `onnxruntime`，版本固定在 `trainer/requirements.txt` 中（1.23.2 / 0.1.98 / 1.23.2），训练节点镜像与 `install.sh` 都会安装。转换用 Ultralytics 的安全加载读取模型文件，需要 PyTorch 2.6 及以上和带安全加载的 Ultralytics：`torch_variants.txt` 中的变体（最旧的 `cu118` 为 2.7.1）与固定的 Ultralytics 版本都满足，不满足时（如自己装了旧版依赖的裸机节点）转换失败并提示升级。镜像构建时会离线做一次 YOLO11n 的 ONNX 转换，依赖不完整或安全加载不可用时构建失败。节点运行时找不到 torch、ultralytics 或 onnx 时不领取转换任务，日志中有 `not claiming model_export tasks: cannot find …`，之后每 5 分钟重新检查一次。
- **升级训练节点**：本机的 Docker 节点 `make up`（重新构建镜像）；其它机器上的 Docker 节点 `git pull` 后 `make setup`，再 `docker compose up -d --build trainer`（CPU 训练节点为 `trainer-cpu`，见 10.6）；裸机节点在新版的仓库目录中重新运行 `sudo trainer/scripts/install.sh`（沿用已有的配置，安装新增的依赖并重启服务，见 10.7）；Windows 节点在平台升级后，在管理员 PowerShell 中重新运行“接入节点”中的命令（从平台下载新版，沿用配置并重启计划任务，见 10.7）。
- **离线 / 内网**：转换不访问网络，也不下载任何权重：子进程设置 `YOLO_OFFLINE=1`，并禁止 Ultralytics 在线安装缺少的依赖（`YOLO_AUTOINSTALL=False`）。依赖只在构建镜像（或运行 `install.sh`）时从 PyPI（`PIP_INDEX_URL`，可用内网镜像，见第 7 节）安装；离线服务器导入在联网机器上构建的训练节点镜像即可（第 7 节）。
- **超时**：一次转换最长 `TM_EXPORT_TIMEOUT`（默认 900 秒），超时后转换失败，可以减小输入尺寸后重试，或延长超时。Docker 部署时在 `.env` 中设置（如 `TM_EXPORT_TIMEOUT=30m`，见 `.env.example`），然后 `docker compose up -d trainer`（CPU 训练节点为 `trainer-cpu`）；裸机节点写在配置文件 `<安装目录>/trainer.env` 中（重新运行 `install.sh` 时会保留），然后 `sudo systemctl restart trainmaster-trainer`。
- **磁盘**：转换在训练节点状态目录的 `work/model-export-<ID>/` 中进行（Docker 为 `TM_HOST_TRAINER_DIR`），需要约 3 倍于模型文件的剩余空间，结束后删除。平台一侧，结果先写入存储目录的 `tmp/model-exports/` 再移入模型文件夹：节点上传前先带上文件大小询问平台，平台空间不足时转换直接失败（“上传转换结果失败：磁盘剩余空间不足…”），不会反复重试。
- **安全**：上传的 pt 是用户提供的 pickle。转换子进程用 Ultralytics 的安全加载（`ULTRALYTICS_SAFE_LOAD=1`，只还原其允许清单中的类），只继承运行 Python 需要的环境变量，拿不到 `TM_NODE_TOKEN`、代理与云服务密钥；平台上传时也已拒绝引用了清单之外对象的文件。安全加载不是沙箱：转换子进程仍能访问训练节点能访问的文件（包括挂载的 `/data`），防护依赖 PyTorch 的安全加载与 Ultralytics 的允许清单本身没有漏洞；训练节点没有关闭它的选项，安全加载不可用时不转换。用户下载 PT 后在自己的机器上用 Ultralytics 或 `torch.load` 普通加载时不受这些保护，只加载来源可信的文件。
- **一个节点同时只做一个转换**，其余的排队（页面上显示排队位置）；有多个在线的训练节点时并行。执行转换的节点中途停止时任务立即重新排队，崩溃或断网时在租约（60 秒）过期后重新排队，一个转换最多尝试 3 次。
- **在节点上手动转换**（排障）：`export` 子命令用与平台的转换任务相同的子进程在本机转换，不连接平台，打印输入输出的形状与 ONNX Runtime 的验证结果。平台所在机器上的训练节点可以直接取模型仓库中的文件（训练节点把存储目录挂载为 `/data`）。先拷贝到容器的 `/tmp` 再转换：Ultralytics 把 ONNX 先写在 pt 文件旁边，不要让它写进模型文件夹：

  ```bash
  docker compose exec trainer sh -c 'cp /data/models/project-1/v3/model.pt /tmp/ &&
    python -m trainmaster_trainer export --weights /tmp/model.pt --postprocess nms --imgsz 640'
  ```

  结果为 `/tmp/model.onnx`（`--output` 可以另指定）。其它选项：`--dynamic`（不能与 `--postprocess nms` 同时使用，规则与平台相同）、`--opset N`（11–21）、`--json`；`python -m trainmaster_trainer export --help` 查看全部。裸机节点用安装目录中的解释器：`/opt/trainmaster-trainer/bin/python -m trainmaster_trainer export --weights best.pt`。

### 10.11 模型审图

模型审图的识别由训练节点执行，审核可以在网页上进行，也可以在用户的 Windows 电脑上用本地审图程序离线进行（原理见 [架构说明 · 模型审图](architecture.md#11-模型审图)）。

- **需要至少一个在线的训练节点**：节点能找到 PyTorch 与 Ultralytics 时才领取审图任务（不需要 onnx），训练节点镜像与 `install.sh` 都已安装。**CPU 训练节点（`trainer-cpu`）就能识别**；节点有可用的 GPU 时在 `cuda:0` 上识别，快得多（与模型转换不同，识别子进程能看到节点的显卡；裸机节点设置了 `CUDA_VISIBLE_DEVICES` 时按它，只用其中的第一块）。一个节点同时只执行一个任务（转换或识别），其余的排队（页面上显示排队位置）；有多个在线的训练节点时并行执行不同的任务。没有这样的节点时任务一直排队，页面提示“没有可以识别的训练节点”，节点上线后自动开始。旧版训练节点不领取审图任务，按 10.10 的方法升级训练节点。
- **不需要共享存储**：节点通过 HTTP 下载模型与图片（工作目录中同时最多预先下载 8 张），识别结果分批上报，审图标注保存在平台存储目录的 `reviews/` 中。识别在训练节点状态目录的 `work/review-inference-<ID>/` 中进行（Docker 为 `TM_HOST_TRAINER_DIR`），需要约“模型文件大小 + 256 MiB”的剩余空间，结束后删除。识别不访问网络（`YOLO_OFFLINE=1`），模型同样用 Ultralytics 的安全加载读取，子进程拿不到节点令牌（见 10.10 的“安全”）。
- **超时**：单张图片最长 `TM_INFERENCE_IMAGE_TIMEOUT`（默认 300 秒）；启动识别进程与加载模型也使用这个时间，但至少 120 秒。超时后任务失败（“识别超时（单张图片 … 秒内没有识别完成）”），可以用较小的输入尺寸新建任务，或延长超时后在页面上“重新识别”。设置方法同 `TM_EXPORT_TIMEOUT`：Docker 部署在 `.env` 中设置（如 `TM_INFERENCE_IMAGE_TIMEOUT=10m`，见 `.env.example`），然后 `docker compose up -d trainer`（CPU 训练节点为 `trainer-cpu`）；裸机节点写在配置文件 `<安装目录>/trainer.env` 中，然后 `sudo systemctl restart trainmaster-trainer`。
- **中断与重试**：节点停止时任务立即重新排队，崩溃或断网时在租约（60 秒）过期后重新排队，一个任务最多尝试 3 次；已经上报的结果保留，重新排队或“重新识别”时从还没有识别的图片继续。个别图片无法识别（文件损坏、超过 1 GiB、已从数据集中删除）时只记录这张图片的原因，任务照常完成。
- **在节点上手动识别**（排障）：`infer` 子命令用与审图任务相同的子进程在本机识别图片，不连接平台，打印设备、每张图片的尺寸、检测框（类别、置信度、坐标）与用时；任何一张失败时退出码为 1。平台所在机器上的训练节点可以直接读取模型仓库与数据集中的文件（存储目录挂载为 `/data`，`infer` 不写文件）：

  ```bash
  docker compose exec trainer python -m trainmaster_trainer infer \
    --weights /data/models/project-1/v3/model.pt --conf 0.25 /data/datasets/产线A-划痕/NG/0001.jpg
  ```

  其它选项：`--iou`（0.1–0.95，默认 0.7）、`--imgsz`（`640` 或 `高x宽`，默认为训练时的尺寸）、`--max-det`（1–1000，默认 300）、`--device`（`cpu`、`cuda:0` …）、`--task`、`--json`；`python -m trainmaster_trainer infer --help` 查看全部。CPU 训练节点把 `trainer` 换成 `trainer-cpu`；裸机节点用安装目录中的解释器：`/opt/trainmaster-trainer/bin/python -m trainmaster_trainer infer --weights best.pt a.jpg`。

**本地审图程序**（`trainmaster-reviewer.exe`）：

- **分发**：平台镜像中附带它（`/app/tools/trainmaster-reviewer.exe`，`TM_REVIEWER_TOOL` 已指向它），审图任务详情页的“本地审图”中“下载本地审图程序（Windows）”直接下载，不需要另外部署。程序与平台一起构建，页面显示的版本就是平台的版本；平台升级后请从页面重新下载程序（新版平台的审图包可能需要新版程序才能打开，旧程序会提示升级）。不用 Docker 镜像部署时用 `make reviewer` 构建（见开发指南），把 `TM_REVIEWER_TOOL` 设为生成的 `backend/bin/trainmaster-reviewer.exe`；没有设置或文件不存在时页面提示“平台没有附带本地审图程序”，其它功能不受影响。平台每次请求时查找这个文件，之后放上的文件不需要重启；启动日志说明是否找到（`local reviewer bundled`；没有设置时为 `no local reviewer bundled`，文件不存在时为 `local reviewer not found`）。
- **运行要求**：64 位 Windows 10 / 11，单个文件，不需要安装，也不需要管理员权限与网络。界面用系统的 Microsoft Edge WebView2 运行时显示（Windows 11 与更新过的 Windows 10 已自带）；没有时程序改用默认浏览器打开同样的页面，并显示“已在浏览器中打开本地审图程序。点“确定”退出。”——需要窗口时安装 Microsoft 提供的 WebView2 运行时即可。程序只监听本机地址 `127.0.0.1`（优先使用端口 47821，被占用时随机选择），只回应自己打开的页面。程序没有数字签名，从浏览器下载后 Windows SmartScreen 可能提示，确认来源后选择仍要运行。
- **磁盘**：打开审图包时解压到 zip 旁边与 zip 同名的文件夹，需要对 zip 所在的文件夹有写权限，剩余空间约为审图包大小 + 64 MiB；审核进度、审图标注与导出的审图结果（`<文件夹名>-审图结果.zip`）都在旁边，删除这些文件夹即可清理。缩略图缓存与日志在 `%LOCALAPPDATA%\trainmaster-reviewer\`（日志为其中的 `reviewer.log`，排障时提供它），最近打开的列表在 `%APPDATA%\trainmaster-reviewer\`。
- **上传结果**：导出的审图结果包含审核状态与各图片的标注文件（不含图片），通常不大；同一个审图包可以边审边多次导出、上传，后一次接着前一次（改回去的也算修改）；经反向代理时它在一个请求中上传（平台上限 4 GiB），见第 9 节。
- **命令行参数**（排障）：`trainmaster-reviewer.exe [-browser] [-no-open] [-addr 127.0.0.1:端口] [审图包.zip | 文件夹]`：`-browser` 在默认浏览器中打开而不用程序窗口，`-no-open` 不自动打开、只显示访问地址，`-addr` 指定监听的本机地址与端口，`-version` 显示版本；设置环境变量 `TM_REVIEWER_DEBUG=1` 后运行，程序窗口中可以打开开发者工具。

### 10.12 常见问题

| 现象 | 原因与解决 |
|---|---|
| 下载 ONNX 时转换一直“排队中”，页面提示“没有可以执行转换的训练节点” | 没有在线的、能执行转换的训练节点（见 10.10）。在“计算资源”页面确认至少有一个在线的节点，且版本（“节点 v…”）不低于 0.2.0：旧版节点照常显示在线，但不领取转换任务，按 10.10 升级。版本已是 0.2.0 时查看节点日志（`docker compose logs trainer`，CPU 训练节点为 `trainer-cpu`；裸机为 `journalctl -u trainmaster-trainer`）：有 `not claiming model_export tasks: cannot find …` → 节点缺少 torch / ultralytics / onnx，重新构建镜像或重新运行 `install.sh`；有 `the platform does not support task claims` → 平台是旧版本，升级平台；有 401 / “节点令牌不正确” → 见下方“训练节点一直显示离线”一行。用 `--mode off` 关闭了本机训练节点时改为 `make up SETUP_ARGS="--mode cpu"`（或 `--mode auto`）。没有这条提示、只是排队位置不变时，节点正在执行排在前面的转换 |
| 转换失败：“训练节点的 Ultralytics … 无法加载该模型” | 模型由比训练节点更新的 Ultralytics 版本训练（如 YOLO26 需要 8.4.0 及以上）：升级训练节点（10.10），再在下载对话框或模型详情页重试 |
| 转换失败：“转换超时（… 秒内没有完成）” | CPU 较慢或模型、输入尺寸较大：减小输入尺寸，或按 10.10 调大训练节点的 `TM_EXPORT_TIMEOUT` |
| 转换失败：“训练节点的 PyTorch … 不支持 Ultralytics 的安全加载（需要 2.6 及以上）”或“训练节点的 Ultralytics … 不支持 / 没有启用安全加载” | 节点上的 torch 或 ultralytics 太旧（多见于自己安装了依赖的裸机节点），节点为了安全不加载上传的文件：按 10.10 升级训练节点 |
| 转换失败：“模型文件中引用了不允许加载的对象（…）” | 文件中有 Ultralytics 安全加载不允许的对象（上传时的检查只按名称判断，个别文件到加载时才被拒绝），可能是构造的恶意文件，或被其它工具改过：上传 Ultralytics 训练直接保存的 `best.pt` / `last.pt` |
| 审图任务一直“排队中”，页面提示“没有可以识别的训练节点” | 没有在线的、能执行审图识别的训练节点（见 10.11）：在“计算资源”页面确认至少有一个在线的节点。旧版训练节点照常显示在线，但不领取审图任务，按 10.10 升级。查看节点日志（同上一行）：有 `not claiming review_inference tasks: cannot find …` → 节点缺少 torch / ultralytics，重新构建镜像或重新运行 `install.sh`；有 `the platform does not support task claims` → 平台是旧版本。没有这条提示、只是排队位置不变时，节点正在执行排在前面的任务（转换或其它审图任务） |
| 审图任务失败：“识别超时（单张图片 … 秒内没有识别完成）”或“加载模型超时…” | CPU 较慢、输入尺寸很大或图片很大：用较小的输入尺寸新建任务，或按 10.11 调大训练节点的 `TM_INFERENCE_IMAGE_TIMEOUT`，再在审图任务页面“重新识别”（超时的那张图片已记为识别失败，重新识别时跳过它） |
| 审图任务失败：“训练节点在识别过程中失去联系（已尝试 3 次）” | 执行识别的节点反复崩溃、断网或被强制停止：查看节点日志（显存或内存不足时日志中有说明），处理后在审图任务页面“重新识别”（已识别的图片保留） |
| 审图任务页面提示“平台没有附带本地审图程序” | `TM_REVIEWER_TOOL` 没有设置或指向的文件不存在（Docker 镜像中已设置，多见于不用镜像的部署）：按 10.11 构建程序并设置 `TM_REVIEWER_TOOL` |
| 本地审图程序打开的是浏览器而不是程序窗口 | 电脑上没有 WebView2 运行时（或窗口创建失败）：可以照常在浏览器中审图，审完在开始页点“退出”或在消息框中点“确定”结束程序（只关闭浏览器标签页时程序仍在运行）；需要程序窗口时安装 Microsoft Edge WebView2 运行时。日志在 `%LOCALAPPDATA%\trainmaster-reviewer\reviewer.log` |
| 本地审图程序提示“这个审图包已在另一个本地审图程序窗口中打开” | 同一个审图包已在另一个程序窗口（或浏览器模式下的另一个程序）中打开：两个程序同时审同一个包会互相覆盖审核状态，所以不允许。切换到那个窗口继续审，或先在那里退出再打开；找不到窗口时在任务管理器中结束多余的 `trainmaster-reviewer.exe` |
| 转换失败：“上传转换结果失败：磁盘剩余空间不足…” | 平台的存储目录（`TM_HOST_STORAGE_DIR`）空间不足，放不下转换结果：清理磁盘后在下载对话框或模型详情页重试 |
| 启动 `trainer` 时报 `could not select device driver "nvidia" with capabilities: [[gpu]]`（较新的 Docker 为 `failed to discover GPU vendor from CDI: no known GPU vendor found`） | Docker 没有安装或没有配置 NVIDIA Container Toolkit：`make up SETUP_ARGS="--install-toolkit"`（其它机器上的训练节点：`make setup SETUP_ARGS="--install-toolkit"`，再 `docker compose up -d --build trainer`）。暂时不用 GPU：`make up SETUP_ARGS="--mode cpu"` |
| `docker run --gpus all …` 或启动 `trainer` 时报 `AMD CDI spec not found` | Docker 29.5 起，dockerd 启动时还没有安装 NVIDIA Container Toolkit（装好 Toolkit 后没有重启 Docker）就会这样报错，与 AMD 显卡无关：`make up SETUP_ARGS="--install-toolkit"`（已安装时只配置并重启 Docker；其它机器上的训练节点：`make setup SETUP_ARGS="--install-toolkit"`，再 `docker compose up -d --build trainer`），或手动 `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker` |
| `--install-toolkit` 只打印了手动步骤，没有安装 | snap 版或 rootless 模式的 Docker、没有运行 systemd 的 WSL：脚本不能可靠地配置它们，按打印的步骤执行后重新运行 `make setup`，见 10.2 |
| `make setup` 报“无法连接 Docker，无法判断训练节点能否使用显卡，没有写入 .env”并退出 | 当前用户不能访问 Docker：`sudo usermod -aG docker $USER` 后重新登录（或用 sudo 运行），确认 `docker info` 正常后重新运行。`.env` 中已有训练节点配置时 `make setup` 不报错，沿用原来的配置 |
| `Failed to initialize NVML: Driver/library version mismatch` | 升级了驱动，但内核中还是旧版的驱动模块：重启机器（或卸载并重新加载 nvidia 内核模块）。`make setup` 与“计算资源”页面会给出同样的提示；重启前 `make setup` 沿用原来的 GPU 配置 |
| 页面提示驱动低于已安装的 PyTorch 变体的要求（`driver_too_old`） | 驱动低于镜像中 PyTorch 变体要求的最低版本（见 10.3）：升级驱动（推荐 580 或更新）后 `make up`（`make setup` 按当前驱动重新选择变体并重新构建）；或按页面建议换用驱动支持的变体：`make up SETUP_ARGS="--variant cu126"`（其它机器上：`make setup SETUP_ARGS="--variant cu126"`，再 `docker compose up -d --build trainer`）。不要直接修改 `.env` 中的 `TM_TORCH_VARIANT`，下次 `make up` 会把它改回去（见 3.1）。固定了变体（`TM_TRAINER_VARIANT`）时用 `--variant auto` 恢复自动选择 |
| 页面提示宿主机 NVIDIA 驱动过旧、将使用 CPU 训练（`driver_too_old`），服务器卡片提示先升级驱动；或 `make setup` 选择了 `cpu` 并说明驱动过旧，加 `--install-toolkit` 时说“驱动不能使用 CUDA 版的 PyTorch”，不安装 | 宿主机的驱动低于所有 CUDA 版 PyTorch 的最低要求（x86_64 为 `cu118` 的 450.80.02，ARM 服务器为 `cu130` 的 580，见 10.3），只能运行 CPU 训练节点，安装 NVIDIA Container Toolkit 解决不了：升级 NVIDIA 驱动（推荐 580 或更新）并重启机器，然后 `make up SETUP_ARGS="--install-toolkit"`（安装 / 配置 Toolkit 并启动 GPU 训练节点；其它机器上：`make setup SETUP_ARGS="--install-toolkit"`，再启动它输出的服务）。裸机节点升级驱动后重新运行 `sudo trainer/scripts/install.sh` |
| RTX 50 系列（Blackwell，计算能力 12.0）提示显卡架构不受支持（`arch_unsupported`），或训练时报 `no kernel image is available for execution on the device` | 只有 `cu128` 与 `cu130` 支持 RTX 50 系列：驱动 ≥ 580 时为 `cu130`，否则为 `cu128`（RTX 50 系列本身需要 570 或更新的驱动）。`make setup` 会自动选择；固定为 `cu126` / `cu118` 时用 `make up SETUP_ARGS="--variant auto"` 恢复 |
| 新旧显卡混用，页面警告部分显卡不受支持、不能用于训练（`arch_unsupported`） | 没有同时支持全部显卡的 PyTorch 变体（如 GTX 10 系列与 RTX 50 系列混用）：`make setup` 选择支持显卡最多的变体（见 10.3），其它显卡照常训练。只把受支持的显卡交给训练节点（见 10.8）：Docker 部署在 `docker-compose.override.yml` 中设置 `device_ids`，容器中不再有不受支持的显卡，警告随之消失；裸机安装在训练节点的配置文件（默认 `<安装目录>/trainer.env`）中设置 `CUDA_VISIBLE_DEVICES` 并重启服务，排除了全部不受支持的显卡后页面上只显示一条提示（没有修复建议） |
| 训练时 DataLoader 报 `Bus error`、`unable to write to file </torch_…>` 或页面提示 `/dev/shm` 过小（`shm_small`） | 共享内存不足：调大 `.env` 中的 `TM_TRAINER_SHM_SIZE`（默认 `8gb`）后 `make up`（其它机器上：`docker compose up -d trainer`，CPU 训练节点为 `trainer-cpu`），或减少 DataLoader 的 workers |
| 宿主机有显卡，页面提示显卡没有传入容器（`gpu_not_passed_through`） | 运行的是 `trainer-cpu`（Docker 不能使用显卡时 `make setup` 选择了 CPU），或 `trainer` 没有拿到显卡：`make up SETUP_ARGS="--install-toolkit"` 安装 / 配置 NVIDIA Container Toolkit 并启动 GPU 训练节点（需要 sudo；其它机器上用 `make setup SETUP_ARGS="--install-toolkit"` 再启动它输出的服务）。有意选择了 CPU 模式（`--mode cpu`）时页面只显示“已选择 CPU 模式”的提示；宿主机驱动过旧、CUDA 版 PyTorch 都不能使用时页面提示的是驱动过旧，先升级驱动（见上面“宿主机 NVIDIA 驱动过旧”一行） |
| `make setup` 提示没能验证 Docker 能否使用显卡（离线或无法访问镜像仓库） | 拉取 `TRAINER_PYTHON_IMAGE` 失败，只能按 Docker 的配置判断：配置镜像加速或内网镜像仓库（第 7 节），或先 `docker load` 导入该镜像，再运行 `make setup` |
| 训练节点一直显示离线或没有出现 | `docker compose logs trainer` 查看：401 / “节点令牌不正确” → 平台与训练节点的 `TM_NODE_TOKEN` 不一致（修改 `.env` 后平台 `make up`，其它机器上的节点 `docker compose up -d trainer`）；连接失败 → 检查 `TM_SERVER_URL` 与防火墙 |
| Windows 节点一直显示离线或没有出现 | 在管理员 PowerShell 中 `Get-ScheduledTask -TaskName trainmaster-trainer` 查看计划任务是否在运行，再看日志 `C:\ProgramData\trainMaster\state\logs\trainer.log`：401 / “节点令牌不正确” → 修改 `trainer.env` 中的 `TM_NODE_TOKEN` 后重启计划任务（见 10.7）；连接失败 → 在这台电脑的浏览器中打开 `TM_SERVER_URL` 确认能访问平台；没有日志 → 安装时没有加 `-Service`（重新运行安装命令并加上）。电脑睡眠期间节点离线 |
| Windows 上 `irm` 报“无法连接到远程服务器”、404 或“平台没有附带 Windows 训练节点的安装脚本” | 连接失败时确认这台电脑能访问平台地址（https 地址的命令开头已启用 TLS 1.2）；404 → 平台不是用 Docker 镜像运行，没有设置 `TM_TRAINER_PACKAGE_DIR`（见第 3.2 节）：设置它，或在这台电脑上克隆仓库，在仓库目录中运行 `powershell -ExecutionPolicy Bypass -File trainer\scripts\install.ps1 …` |
| Windows 上安装或自检时无法导入 PyTorch：`WinError 126`、`DLL load failed`、`Error loading …dll` | 缺少 Microsoft Visual C++ 运行库：安装 https://aka.ms/vs/17/release/vc_redist.x64.exe，或重新运行安装命令并加 `-InstallPython`（会一并安装运行库） |
| Windows 上导入 PyTorch 时报 `WinError 1455`（页面文件太小） | 虚拟内存不足（CUDA 版 PyTorch 的 DLL 很大）：在“系统 → 高级系统设置 → 性能 → 设置 → 高级 → 虚拟内存”中改为“系统管理的大小”或调大页面文件，重启后再试 |
| Windows 上安装脚本提示“没有找到 64 位的 Python 3.10–3.13”或 Python 是 Microsoft Store 版 | 加 `-InstallPython` 重新运行（用 winget 或 python.org 的安装程序为所有用户安装 Python 3.12），或自己从 python.org 安装 64 位 Python（勾选 Install for all users）；以 SYSTEM 运行的计划任务不能使用 Microsoft Store 版的 Python |
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
| 训练节点（GPU、驱动、CUDA 环境）相关的问题 | 见第 10.12 节 |
| 上传模型时提示“磁盘剩余空间不足” | 上传模型需要至少“文件大小 + 512 MiB”的剩余空间：清理磁盘或把 `TM_HOST_STORAGE_DIR` 换到更大的磁盘 |
| 上传模型时提示“文件中引用了平台不允许加载的对象（…）” | 文件引用了 Ultralytics 安全加载不允许的类或函数（如 `os.system`、自定义模块中的类），可能是构造的恶意文件，训练节点也不会加载它：上传由 Ultralytics 训练保存的模型文件。用 pickle 协议 0 / 1 另存过的文件也会这样被拒绝（`torch.save` 默认的协议 2 没有问题） |
| 上传或提交模型时提示“服务器上找不到这次上传已接收的数据（临时文件已丢失）…” | 上传的临时文件（`tmp/model-uploads/<上传ID>/`）不在了，如恢复了数据库而 `tmp/` 不在备份中，或手动清理了 `tmp/`：在上传面板中取消后重新上传 |
| 上传完成后长时间显示“正在校验模型” | 平台同时最多解析 2 个模型文件，其余的提交排队等待；大文件计算 SHA256 也需要一些时间。关闭页面不会中断提交 |
| 存储目录的 `models/` 下有 `.deleting-…` 文件夹 | 删除模型时的临时改名（`.deleting-pending-…` 来自崩溃时还没提交的入库），平台启动时与每小时删除它们；一直留着时多半是权限不足（见第 4 节）。模型文件夹中的 `.pending` 是正在入库的标记，提交后删除，崩溃留下的由同一清理处理，不要手动删除 |
| 下载 ONNX 时一直排队或转换失败 | 见第 10.12 节 |
| 审图任务一直排队或识别失败、本地审图程序打不开窗口 | 见第 10.12 节 |
| 存储目录的 `reviews/` 下有 `.deleting-…` 文件夹 | 删除审图任务时的临时改名，平台启动时与每小时删除它们；一直留着时多半是权限不足（见第 4 节） |
| 上传审图结果时提示“这是审图任务 #N 的审图结果…，不是这个任务的” | 上传到了另一个审图任务：在对应任务（#N）的页面上传 |
| 日志提示 `migration versions must never be reused or renamed` | 两个不同的迁移文件用了同一个编号（如不同分支各自新增了 `0002_*.sql`），或改了已执行文件的名字，见[架构说明 · 数据库迁移约定](architecture.md#7-数据库迁移约定) |

容器日志按 10MB × 5 个文件轮转（`docker-compose.yml` 中的 `x-logging`），`docker compose logs` 只能看到最近的日志，需要长期保存时请接入日志系统。

健康检查接口：`/api/healthz`（存活，Docker HEALTHCHECK 使用）、`/api/readyz`（就绪，含数据库与存储检查，可接入监控）。训练节点的 Docker HEALTHCHECK 检查最近一次心跳是否及时（`python -m trainmaster_trainer healthcheck`），与平台连接中断时显示为 unhealthy。
