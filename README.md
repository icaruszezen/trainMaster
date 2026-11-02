# trainMaster

面向团队内部的**工业视觉平台**：把图片数据集管理、标注、数据版本、模型训练、模型评估和模型管理放进一个 Web 平台，供团队在内网中协作使用。

它是桌面工具 [ez_training](https://github.com/icaruszezen/ez_training)（PyQt 版，覆盖数据集、预标注、标注、数据准备、训练、验证等）的团队 Web 版。

> **当前进度：** 平台框架（Go + React 工程骨架、shadcn/ui 界面框架、Docker 部署与数据持久化）已完成；**数据集管理**已上线：压缩包 / 文件夹导入（VOC、X-AnyLabeling、YOLO 标注，统一转换为 VOC 保存）、服务器上已有文件夹直接添加为数据集、缩略图浏览与标注预览、按 VOC / X-AnyLabeling / YOLO 格式下载；**项目管理**已上线：按检测任务把数据集组织成项目（一个数据集可以属于多个项目），项目页汇总其中的数据、标注与模型情况；**标注工作台**已上线：在浏览器中逐张画矩形框，标注直接保存为数据集文件夹中的 VOC 文件；**计算资源**已上线：训练节点自动识别显卡、驱动、CPU 与内存并实时上报，按驱动与显卡自动准备好 YOLO 训练所需的 CUDA 版 PyTorch 环境并自检，`make setup` 自动检测并配置 Docker 的显卡支持。其余模块已有占位页，将在后续迭代中逐个实现。

## 功能模块

| 分组 | 模块 | 页面路径 | 预留 API | 状态 |
|---|---|---|---|---|
| 概览 | 工作台（系统状态、平台流程入口） | `/` | `GET /api/v1/system/info` | 已完成 |
| 项目管理 | 项目：新建 / 编辑 / 删除、纳入数据集、项目详情（数据集、标签分布、模型占位） | `/projects` | `/api/v1/projects` | 已完成 |
| 数据管理 | 数据集：压缩包 / 文件夹导入（VOC / X-AnyLabeling / YOLO 标注）、添加已有目录、缩略图浏览与标注预览、按格式下载 | `/datasets` | `/api/v1/datasets` | 已完成 |
| 数据管理 | 数据版本：划分、快照、导出 YOLO/VOC/COCO、增强 | `/datasets/versions` | `/api/v1/datasets` | 规划中 |
| 标注 | 标注任务：分配、进度、审核返修、抽检 | `/annotation/tasks` | `/api/v1/annotation/tasks` | 规划中 |
| 标注 | 标注工作台：矩形框画框与编辑、类别列表、快捷键、撤销重做、自动保存为 VOC | `/annotation/workspace` | `/api/v1/datasets` | 已完成 |
| 标注 | 标签体系：缺陷类别、颜色与快捷键、层级与别名 | `/annotation/labels` | `/api/v1/annotation/labels` | 规划中 |
| 标注 | 智能预标注：模型预标注、视觉大模型、模板匹配 | `/annotation/prelabel` | — | 规划中 |
| 模型训练 | 训练任务：创建、排队调度、实时日志与曲线 | `/training/jobs` | `/api/v1/training/jobs` | 规划中 |
| 模型训练 | 计算资源：训练节点自动接入、显卡 / CPU / 内存与 CUDA 环境识别、实时监控、重新检测 | `/training/compute` | `/api/v1/compute` | 已完成 |
| 模型评估 | 模型评估：mAP/P/R/F1、混淆矩阵、PR 曲线、对比报告 | `/evaluation` | `/api/v1/evaluations` | 规划中 |
| 模型管理 | 模型仓库：版本、溯源、ONNX/TensorRT 导出、发布 | `/models` | `/api/v1/models` | 规划中 |
| 模型管理 | 推理测试：在线推理、结果可视化、阈值调节 | `/models/inference` | `/api/v1/models` | 规划中 |
| 系统管理 | 成员与权限：账号、角色、操作审计 | `/system/members` | — | 规划中 |
| 系统管理 | 存储管理：存储目录与容量（已接入真实数据）、备份清理 | `/system/storage` | `GET /api/v1/system/info` | 部分完成 |
| 系统管理 | 系统设置：平台配置、默认训练参数、视觉 API | `/system/settings` | — | 规划中 |

“规划中”的模块已有独立的页面文件和路由，预留的 API 路径目前统一返回 `501 not_implemented`。

## 数据集管理

每个数据集就是存储目录下 `datasets/` 里的一个普通文件夹（宿主机默认 `./data/storage/datasets/<数据集名称>/`），图片和 VOC 标注文件直接以文件形式保存，可以用文件管理器、`rsync` 直接查看和备份。添加数据有三种方式：

| 方式 | 说明 |
|---|---|
| 上传压缩包 | 支持 zip、tar、tar.gz / tgz、tar.bz2、tar.xz、tar.zst、7z、rar（含 RAR5）。分块上传、断点续传；Windows 下打包的中文文件名（GBK 编码）自动识别 |
| 上传文件夹 | 在页面上选择或直接拖入整个文件夹，保留子目录结构 |
| 添加已有目录 | 先把文件夹拷贝到服务器的 `datasets/` 目录下，再在页面上选择它作为新数据集；只建立索引，不移动文件 |

- 新建数据集时自动在 `datasets/` 下创建与数据集**同名的文件夹**。
- **VOC 标注自动识别**：与图片同目录、同名的 `.xml`（如 `NG/0001.jpg` ↔ `NG/0001.xml`），以及标准 VOC 结构 `JPEGImages/` ↔ `Annotations/`。页面上可以按已标注 / 空标注 / 标注无效 / 未标注、按标签筛选，查看图片时叠加显示标注框。
- **上传时选择标注格式**：Pascal VOC（`.xml`）、[X-AnyLabeling](https://github.com/CVHub520/X-AnyLabeling)（`.json`，兼容 LabelMe）或 YOLO（`.txt`，同目录同名或 Ultralytics 的 `images/` ↔ `labels/` 结构）。X-AnyLabeling 与 YOLO 标注在导入时**自动转换为 VOC** 保存（多边形、旋转框等取外接矩形）；选择 YOLO 时需同时提供类别顺序（每行一个类别，可直接输入或读取 `classes.txt`，上传文件夹时自动读取其中的 `classes.txt`）。
- **下载数据集**：在数据集详情页或卡片菜单中选择 VOC、X-AnyLabeling 或 YOLO 格式（YOLO 同样需要类别顺序，默认先沿用最近一次 YOLO 导入时的顺序（类别编号不变），再接上标注工作台类别列表中的其它类别；没有 YOLO 导入时按类别列表；其余标签按名称排列），服务器边打包边下载 zip，YOLO 包含 `images/`、`labels/`、`classes.txt` 与 `data.yaml`。
- **图片顺序固定**：图片网格、查看大图时的上一张 / 下一张、封面和下载的压缩包都按同一个顺序排列，与 Windows 资源管理器默认的“按名称”排序一致：文件夹在前，名称不区分大小写，数字按数值比较（`img2` 在 `img10` 前），中文按拼音。详见 [架构说明 · 图片顺序](docs/architecture.md#图片顺序)。
- 导入到已有数据集时，同名文件可选择跳过或覆盖；在服务器上直接增删文件后，点“重新扫描”即可同步。

识别规则、导入流程和接口详见 [架构说明 · 数据集与导入](docs/architecture.md#6-数据集与导入)。

## 项目管理

项目用于按检测任务（如“A 线外观检测”）组织数据集，以后训练任务和模型也会归到项目下。

- 在项目页新建、编辑、删除项目；删除项目只删除项目本身，其中的数据集保留。
- **一个数据集可以纳入多个项目**：新建项目时直接勾选数据集，或在项目详情页“添加数据集”，也可以在数据集卡片 / 详情页的“纳入项目”中勾选它所属的项目。
- 数据集页可以按项目筛选，也可以筛选“未纳入项目”的数据集；卡片和列表上会显示数据集所属的项目。
- 项目详情页汇总其中所有数据集的图片数、标注进度、占用空间，按数据集列出明细，并合计标签分布；“模型”一栏为占位，模型仓库上线后显示本项目的模型。

接口详见 [架构说明 · 项目接口](docs/architecture.md#项目接口)。

## 标注工作台

在“标注工作台”中选择数据集（或在数据集详情页、查看大图时点“标注”）进入编辑器，按与数据集页相同的固定顺序逐张画矩形框。

- **画框与编辑**：按 <kbd>W</kbd> 后拖动（或先后点击两个对角）画框，按住 <kbd>Shift</kbd> 画正方形；拖动框或八个手柄移动、缩放，方向键逐像素微调；重叠的框再次单击可以依次选中下面的框。支持难例（difficult）、截断（truncated）标记，撤销 / 重做，复制框并粘贴到别的图片，或沿用上一张的框。
- **类别**：每个数据集有自己的类别列表（名称、颜色、顺序），数字键 <kbd>1</kbd>–<kbd>9</kbd>、<kbd>0</kbd> 切换前 10 个类别，<kbd>T</kbd> 搜索类别；已有标注中的类别可以一键加入列表。类别列表也是下载 YOLO 时的默认类别顺序（由 YOLO 导入的数据集先沿用导入时的顺序）。
- **浏览**：滚轮 / 双指缩放、空格或中键拖动平移，可放大到逐像素对齐；<kbd>A</kbd> / <kbd>D</kbd> 上一张 / 下一张，<kbd>Shift</kbd>+<kbd>D</kbd> 跳到下一张未标注的图片，左侧图片列表可按状态、标签和文件名筛选。按 <kbd>?</kbd> 查看全部快捷键。
- **保存**：停止编辑后自动保存，切换图片（包括浏览器后退 / 前进）或离开工作台前也会先保存，保存失败时不会离开，可以选择留下处理或放弃修改。标注写回数据集文件夹中与图片同名的 `.xml`（或 `Annotations/` 中），保存时保留文件中平台不编辑的内容（如 LabelImg 的 `verified`、`<path>`、多边形等），文件可以继续用 LabelImg 等工具打开。没有目标的图片可以按 <kbd>N</kbd> 标为空标注（负样本）。
- **多人同时标注**：别人保存过同一张图片、或文件在服务器上被改动时，不会悄悄覆盖，页面会提示载入最新版本或用自己的版本覆盖；放弃修改或之后再打开这张图片时显示文件的当前内容。
- 手机和平板上也可以使用（触屏画框、双指缩放），工具栏与侧边面板自动收起。

保存规则与接口详见 [架构说明 · 标注](docs/architecture.md#标注)。

交互设计参考了 [LabelImg](https://github.com/HumanSignal/labelImg)（快捷键习惯与 VOC 格式）、[CVAT](https://github.com/cvat-ai/cvat)、[Label Studio](https://github.com/HumanSignal/label-studio) 与 [X-AnyLabeling](https://github.com/CVHub520/X-AnyLabeling)，代码为独立实现。

## 计算资源 / 训练节点

训练节点（`trainer/`，Python）运行在有显卡的机器上：启动后识别本机硬件、检测 YOLO 训练环境，向平台注册并每 10 秒上报一次心跳。“计算资源”页面（`/training/compute`）每 5 秒刷新，显示所有训练节点以及平台服务器自身的资源。

- **自动识别硬件**：显卡型号、显存、计算能力、驱动与 CUDA 版本，CPU 型号与核数、内存、数据盘与 `/dev/shm`；实时显示每块显卡的利用率、显存、温度、功率，以及 CPU 与内存占用和最近的变化曲线（曲线只保存在平台服务的内存中，重启后清空）。
- **自动准备 YOLO 的 CUDA 环境**：按驱动与显卡自动选择 PyTorch 的 CUDA 版本（见下表），训练节点镜像中预装对应的 PyTorch、torchvision 与 Ultralytics；CUDA 运行库随 PyTorch 安装，宿主机只需要 NVIDIA 驱动。节点启动后检测 CUDA 是否可用，在每块显卡上做一次计算并用 YOLO11n 做一次前向推理（不下载权重、不访问外网）；驱动过旧、显卡架构不受支持、显卡没有传入容器等问题会在页面上给出原因与修复命令，修复后可以在页面上“重新检测”。
- **自动配置 Docker 的显卡**：`make setup`（`make up` 会先自动执行它）检测显卡、驱动，并实际运行一次 `docker run --gpus all … nvidia-smi -L` 验证 Docker 能否使用显卡，据此在 `.env` 中启用 GPU 或 CPU 训练节点、选好 PyTorch 版本、生成节点令牌。Docker 还不能使用显卡时，`make setup SETUP_ARGS=--install-toolkit` 按发行版（apt / dnf / yum / zypper）安装并配置 NVIDIA Container Toolkit——每条需要 root 的命令都先列出并确认。
- **平台服务器**：页面同时显示平台所在机器的 CPU、内存、负载与检测到的 NVIDIA 显卡；机器上有显卡但还没有 GPU 训练节点在线时会给出提示。

PyTorch 版本的选择规则（变体表 [`trainer/trainmaster_trainer/torch_variants.txt`](trainer/trainmaster_trainer/torch_variants.txt)）：按下表顺序，取第一个**驱动版本满足、且支持全部显卡**的变体；一个都不满足时使用 CPU 版。

| 变体 | PyTorch / torchvision | CUDA | 最低驱动 | 支持的显卡（计算能力） |
|---|---|---|---|---|
| `cu130` | 2.14.1 / 0.29.1 | 13.0 | 580 | 7.5、8.x、9.0、10.x、12.x：RTX 20 系列到 RTX 50 系列、T4、A100、H100、B200 等 |
| `cu126` | 2.14.1 / 0.29.1 | 12.6 | 525.60.13 | 5.x–9.0：GTX 900 / 10 系列、V100 到 H100（不含 RTX 50 系列） |
| `cu128` | 2.11.0 / 0.26.0 | 12.8 | 525.60.13 | 7.5、8.x、9.0、10.x、12.x：驱动低于 580 时的 RTX 50 系列等 |
| `cu118` | 2.7.1 / 0.22.1 | 11.8 | 450.80.02 | 3.7、5.x–9.0：驱动低于 525.60.13 的机器 |
| `cpu` | 2.14.1 / 0.29.1 | — | — | 没有 NVIDIA 显卡、没有驱动或驱动过旧时 |

例如驱动 550 + RTX 4090 → `cu126`；驱动 570 + RTX 5090 → `cu128`；驱动 580 及以上、RTX 20 系列或更新的显卡 → `cu130`。多块显卡中有不受支持的型号时，选支持显卡最多的变体并提示哪些显卡无法使用；ARM 服务器（如 Grace Hopper）只在驱动 ≥ 580 且显卡为 Ampere 或更新时使用 `cu130`。升级驱动后再次 `make up` 会自动换用更合适的版本；也可以用 `make setup SETUP_ARGS="--variant cu126"` 固定版本。

接入训练节点的三种方式：

| 方式 | 做法 |
|---|---|
| 平台所在机器（Docker） | `make setup && make up`：有可用的显卡时启动 GPU 训练节点（`trainer` 服务），否则启动 CPU 训练节点（`trainer-cpu`） |
| 其它 GPU 机器（Docker） | 克隆本仓库，`make setup SETUP_ARGS="--server-url http://<平台IP>:8080 --token <令牌>"`（令牌即平台 `.env` 中的 `TM_NODE_TOKEN`），再按输出的提示只启动训练节点：`docker compose up -d --build trainer`（CPU 时为 `trainer-cpu`） |
| 不用 Docker | `sudo trainer/scripts/install.sh --server-url http://<平台IP>:8080 --token <令牌> --systemd`：安装到虚拟环境并注册为 systemd 服务 |

完整说明（手动配置、只使用部分显卡、国内镜像、常见问题）见 [部署文档 · GPU 训练节点](docs/deployment.md#10-gpu-训练节点)。训练任务的排队与调度将在后续迭代中实现。

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.24、[chi](https://github.com/go-chi/chi) 路由、[pgx](https://github.com/jackc/pgx) 连接池、标准库 `log/slog`；内置 SQL 迁移 |
| 数据库 | PostgreSQL 17（只存元数据） |
| 文件存储 | 宿主机本地目录（图片等文件直接以文件形式保存） |
| 前端 | React 19、TypeScript、Vite、Tailwind CSS v4、[shadcn/ui](https://ui.shadcn.com)（new-york 风格，neutral 配色）、React Router、TanStack Query |
| 训练节点 | Python 3.12、[PyTorch](https://pytorch.org)（按驱动自动选择 CUDA 版本）、[Ultralytics](https://github.com/ultralytics/ultralytics)（YOLO）、NVML（nvidia-ml-py）、psutil |
| 部署 | Docker 多阶段构建（单镜像：Go 服务同时托管前端）、Docker Compose（训练节点为单独的镜像，按 profile 启用）、NVIDIA Container Toolkit |

前端不引用任何外部 CDN 或在线字体，所有资源都打包进镜像，可在无外网的内网环境运行。

## 快速开始（Docker 部署）

需要 Docker Engine 24+ 与 Docker Compose v2。使用 GPU 训练还需要 NVIDIA 驱动与 NVIDIA Container Toolkit（`make setup` 会检查，加 `SETUP_ARGS=--install-toolkit` 可自动安装）。

```bash
git clone https://github.com/icaruszezen/trainMaster.git && cd trainMaster
cp .env.example .env          # 按需修改端口、数据目录、数据库密码、PUID/PGID
make setup                    # 检测显卡、驱动与 Docker 的 GPU 支持，把训练节点配置写入 .env
make up                       # 构建并启动（会先再执行一次 make setup），等同 docker compose up -d --build
```

`make setup` 的输出会说明检测到的显卡、选择的 GPU / CPU 模式与 PyTorch 版本，以及不能使用 GPU 的原因与修复方法。没有 `make`（如 Windows）时直接 `docker compose up -d --build`：此时不启动训练节点，需要时按 [部署文档第 10 节](docs/deployment.md#10-gpu-训练节点) 在 `.env` 中手动配置。训练节点镜像包含 PyTorch 与 CUDA 运行库，第一次构建需要下载数 GB。

启动完成后打开 <http://localhost:8080>（或 `http://<服务器IP>:<TM_HTTP_PORT>`）。

```bash
docker compose ps                         # 所有服务都应为 healthy（训练节点首次检测 CUDA 环境需要一两分钟）
curl http://localhost:8080/api/healthz    # {"status":"ok"}
curl http://localhost:8080/api/readyz     # 数据库与存储检查
docker compose logs -f app                # 查看应用日志
```

国内网络构建较慢时，在 `.env` 中设置 `NPM_REGISTRY`、`GOPROXY`、`ALPINE_MIRROR`（平台镜像）与 `PIP_INDEX_URL`、`TORCH_FIND_LINKS`、`DEBIAN_MIRROR`（训练节点镜像）使用国内镜像。完整说明见 [部署文档](docs/deployment.md)。

**Windows（Docker Desktop）**：PostgreSQL 数据目录不能放在 NTFS 盘符路径上（如 `C:\...` 下克隆的仓库里的 `./data/postgres`），否则数据库无法初始化。请在 WSL 2 的文件系统中克隆并执行上述命令，或按 [部署文档第 8 节](docs/deployment.md#8-windows--docker-desktop-注意事项) 改用命名卷。

## 数据持久化

所有数据都**绑定挂载到宿主机目录**（默认在仓库下的 `./data/`），容器删除、重建、升级都不会丢失数据：

```text
data/
├── postgres/          # PostgreSQL 数据目录（TM_HOST_POSTGRES_DIR），属主为容器内 postgres 用户（uid 70）
├── storage/           # 文件存储根目录（TM_HOST_STORAGE_DIR），挂载为容器内 /data
│   ├── datasets/      # 数据集：每个数据集一个文件夹，图片与 VOC 标注直接以文件保存
│   ├── models/        # 模型权重文件
│   ├── runs/          # 训练/评估运行产物（日志、曲线、中间权重）
│   ├── exports/       # 导出的训练集（YOLO / VOC / COCO）、打包导出与报告
│   ├── cache/         # 缩略图等可再生缓存（可随时删除，无需备份）
│   └── tmp/           # 上传、解压等临时文件
└── trainer/           # 训练节点的状态目录（TM_HOST_TRAINER_DIR）：节点标识 node-key、心跳文件
```

- **数据库只存元数据**（数据集、图片索引、导入记录等），图片和模型等大文件都在 `storage/` 下，可以直接用文件管理器、`rsync` 等工具查看和备份。
- 应用启动时会自动创建上述子目录并检查是否可写；应用进程以 `.env` 中的 `PUID:PGID` 运行，建议设为宿主机上存储目录属主的 UID/GID（`id` 命令查看）。
- 存储目录可以放在任意磁盘，例如在 `.env` 中设置 `TM_HOST_STORAGE_DIR=/mnt/bigdisk/trainmaster`。
- 默认的 `./data` 位于 git 检出目录内，适合试用。**正式部署建议把 `TM_HOST_STORAGE_DIR`、`TM_HOST_POSTGRES_DIR` 设为仓库之外的绝对路径**（如 `/srv/trainmaster/storage`、`/srv/trainmaster/postgres`）：在仓库里执行 `git clean -x` 之类的清理命令会把 `./data` 一并删除。

### 备份、恢复与迁移

下面的命令在仓库目录（`docker-compose.yml` 所在目录）中执行。`STORAGE_DIR` 是 `.env` 中 `TM_HOST_STORAGE_DIR` 的值（默认 `./data/storage`），`BACKUP` 是仓库之外的备份目录——不要把备份放进 git 检出目录。

```bash
STORAGE_DIR=./data/storage        # 改过 TM_HOST_STORAGE_DIR 时换成实际目录
BACKUP=/backup/trainmaster
mkdir -p "$BACKUP"

# 备份数据库（在线备份，PostgreSQL 自定义格式）
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$BACKUP/db-$(date +%F).dump"
# 备份文件存储（增量同步；缓存和临时文件不需要备份）
rsync -a --delete --exclude /cache/ --exclude /tmp/ "$STORAGE_DIR/" "$BACKUP/storage/"

# 恢复
docker compose stop app
docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner' < "$BACKUP/db-2026-10-30.dump"
rsync -a --delete --exclude /cache/ --exclude /tmp/ "$BACKUP/storage/" "$STORAGE_DIR/"
docker compose start app
```

迁移到新服务器时先 `docker compose down`，再把 `.env` 与**两个数据目录**（`TM_HOST_STORAGE_DIR` 和 `TM_HOST_POSTGRES_DIR`，默认都在 `./data` 下）用 `sudo tar --numeric-owner` 打包拷贝过去，详见 [部署文档第 6 节](docs/deployment.md#6-备份与恢复)。直接拷贝 PostgreSQL 数据目录要求新旧环境的 PostgreSQL 主版本一致（当前为 17）；跨版本请使用 `pg_dump` / `pg_restore`。

## 本地开发

需要 Go 1.24+、Node.js 22、Docker；训练节点的测试需要 Python 3.10+（不需要 GPU）。

```bash
make dev-db        # 启动开发用 PostgreSQL（端口只暴露到 127.0.0.1:5432）
make dev-backend   # 后端 http://localhost:8080（go run）
make dev-frontend  # 前端 http://localhost:5173（Vite 热更新，/api 代理到后端）
```

开发时访问 <http://localhost:5173>。常用命令：`make test`、`make lint`、`make build`，`make help` 查看全部。没有 `make` 的环境（如 Windows）可直接执行对应命令，见 [开发文档](docs/development.md)。

## 目录结构

```text
trainMaster/
├── backend/                     # Go 服务（module github.com/icaruszezen/trainmaster/backend）
│   ├── cmd/trainmaster/         # 程序入口：配置 → 存储 → 数据库 → 迁移 → HTTP
│   └── internal/
│       ├── buildinfo/           # 版本信息（构建时注入）
│       ├── config/              # TM_* 环境变量加载与校验
│       ├── database/            # 连接池、启动重试、迁移执行器
│       │   └── migrations/      # NNNN_name.sql（嵌入二进制）
│       ├── storage/             # 数据目录布局、可写检查、磁盘用量、安全路径解析
│       ├── dataset/             # 数据集：文件识别、VOC 解析、解压、扫描、缩略图、导入任务
│       ├── project/             # 项目：项目与数据集的多对多关系、合计统计
│       ├── compute/             # 计算资源：训练节点注册、心跳、在线状态、指标历史
│       ├── hostinfo/            # 平台服务器自身的 CPU、内存、负载与 NVIDIA 显卡（/proc、/sys）
│       ├── httpapi/             # 路由、中间件、统一响应与错误、handler
│       └── web/                 # 前端静态文件服务（SPA 回退、缓存策略）
├── frontend/                    # React + Vite + shadcn/ui
│   └── src/
│       ├── config/navigation.ts # 导航与模块的唯一配置源
│       ├── features/            # 各功能模块页面（每个模块一个页面文件）
│       ├── components/ui/       # shadcn/ui 组件
│       └── ...
├── trainer/                     # 训练节点（Python）：硬件与 CUDA 环境检测、自检、注册与心跳
│   ├── trainmaster_trainer/     # 节点程序；torch_variants.txt 为 PyTorch 变体表
│   ├── scripts/                 # install.sh（裸机安装）、install-torch.sh、lib/torch-variant.sh（选择变体）
│   ├── tests/                   # pytest（不需要 GPU 与 torch）
│   └── Dockerfile               # 训练节点镜像（构建上下文为 trainer/）
├── deploy/
│   ├── docker/entrypoint.sh     # 容器入口：修正数据目录属主后降权运行
│   └── scripts/setup-compute.sh # make setup：检测显卡与 Docker 的 GPU 支持，写入 .env
├── docs/                        # 架构、部署、开发文档
├── Dockerfile                   # 多阶段构建：前端 → 后端 → Alpine 运行镜像
├── docker-compose.yml           # 默认部署：postgres + app；gpu / cpu profile 加入训练节点
├── docker-compose.dev.yml       # 开发叠加：暴露 postgres 端口
├── .env.example                 # 部署配置模板
└── Makefile                     # 常用命令
```

## 路线图

1. **平台框架**（已完成）：工程骨架、界面框架、模块占位、Docker 部署与持久化、CI。
2. **数据集**（进行中）：已完成压缩包 / 文件夹导入、VOC 标注识别、已有目录添加、缩略图浏览与标注预览、项目分组（项目管理）；后续：图片元数据（产线、工位、相机、批次）、质量检查（重复、损坏、分辨率）。
3. **标注**（进行中）：已完成标注工作台（矩形框、数据集类别列表、快捷键、自动保存为 VOC）；后续：标签体系（跨数据集的缺陷类别、层级与别名）、标注任务分配与审核、智能预标注。
4. **数据版本**：训练/验证/测试划分、版本快照、导出 YOLO / VOC / COCO。
5. **训练**（进行中）：已完成训练节点（自动识别显卡与 CUDA 环境、自动选择 PyTorch 版本、注册与监控、Docker 显卡配置）；后续：训练任务队列与调度、实时日志与曲线。
6. **评估与模型**：评估指标与报告、模型仓库与溯源、推理测试、预标注。
7. **成员与权限**：账号登录、角色权限、操作审计。

## 文档

- [架构说明](docs/architecture.md)：组件、模块划分、API 约定、存储布局、迁移约定、训练节点
- [部署指南](docs/deployment.md)：环境变量、权限、升级、备份恢复、镜像加速、Windows、GPU 训练节点、反向代理
- [开发指南](docs/development.md)：本地开发、训练节点的开发与测试、添加 API 与页面、添加 shadcn 组件、代码规范
