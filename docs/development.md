# 开发指南

## 1. 环境准备

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.24+ | 后端 |
| Node.js / npm | 22 LTS | 前端（包管理统一使用 npm，提交 `package-lock.json`） |
| Docker + Compose v2 | 24+ | 开发数据库、镜像构建 |
| Python（含 venv 模块） | 3.10+ | 训练节点（`trainer/`）的开发与测试；Debian / Ubuntu 需要 `python3-venv` |
| bash、shellcheck | bash 3.2+、shellcheck 可选 | 部署与安装脚本及其测试；`make lint` 需要 shellcheck（也可 `pip install shellcheck-py`） |
| make | 可选 | 常用命令封装；Windows 下可直接执行下文列出的命令 |

国内网络可设置：`go env -w GOPROXY=https://goproxy.cn,direct`，`npm config set registry https://registry.npmmirror.com`，`pip config set global.index-url https://mirrors.aliyun.com/pypi/simple`（`make test-trainer` 创建虚拟环境时也使用 `.env` 中的 `PIP_INDEX_URL`）。

训练节点的开发与测试不需要 GPU、CUDA 或 PyTorch（测试用假的 NVML / torch 模块），任何开发机上都可以运行。

## 2. 本地运行

开发时由 Vite 托管前端（热更新），Go 服务只提供 API：

```text
浏览器 → http://localhost:5173（Vite） ──/api──→ http://localhost:8080（go run） → PostgreSQL（Docker，127.0.0.1:5432）
```

```bash
make dev-db        # 1. 启动 PostgreSQL（只暴露到 127.0.0.1:5432）
make dev-backend   # 2. 新终端：启动后端
make dev-frontend  # 3. 新终端：启动前端，访问 http://localhost:5173
```

不使用 make 时的等价命令：

```bash
# 1. 数据库（先以当前用户创建数据目录，否则 Docker 会以 root 身份创建 ./data，后端无法写入）
mkdir -p data/postgres data/storage
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d postgres

# 2. 后端（在 backend/ 目录下）
cd backend
TM_DATA_DIR=../data/storage go run ./cmd/trainmaster
# Windows PowerShell：
#   $env:TM_DATA_DIR="../data/storage"; go run ./cmd/trainmaster

# 3. 前端（在 frontend/ 目录下）
cd frontend
npm ci
npm run dev
```

说明：

- 后端默认连接 `localhost:5432`，账号 `trainmaster/trainmaster`，与 compose 中 postgres 的默认值一致。`make dev-backend` 会从 `.env` 中读取 `TM_DB_USER` / `TM_DB_PASSWORD` / `TM_DB_NAME` / `TM_LOG_LEVEL`（已在环境变量中设置的优先），并使用 `TM_DEV_DB_PORT` 作为端口。Makefile 只按 `KEY=值` 原样读取这几项（去掉一层引号），不会把 `.env` 的其它内容（如 `GOPROXY`）带进命令；docker compose 则自己解析 `.env`。
- 开发时的文件存储目录为仓库下的 `data/storage`（与 Docker 部署的默认目录相同）。直接 `go run` 而不设置 `TM_DATA_DIR` 时，默认是当前目录下的 `./data`（即 `backend/data`，已被 git 忽略）。
- 5432 端口被占用时，在 `.env` 中设置 `TM_DEV_DB_PORT=55432`，后端相应设置 `TM_DB_PORT=55432`（make 会自动处理）。
- 前端开发服务器把 `/api` 代理到 `http://localhost:8080`，后端在其他地址时设置 `VITE_API_PROXY_TARGET`（可写在 `frontend/.env.local`）。
- 想在本地验证“Go 托管前端”的生产形态：`make build` 后执行 `TM_WEB_DIR=frontend/dist TM_DATA_DIR=data/storage backend/bin/trainmaster`，访问 <http://localhost:8080>。

所有可用的环境变量见 [部署指南 · 环境变量](deployment.md#3-环境变量)。

### 训练节点

训练节点（`trainer/`，Python 包 `trainmaster_trainer`）可以直接连接本地运行的后端，在 <http://localhost:5173> 的“计算资源”页面中显示：

```bash
cd trainer
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt     # psutil、nvidia-ml-py、pytest、ruff；不含 torch
.venv/bin/python -m trainmaster_trainer probe      # 打印检测报告（JSON），不连接平台
TM_SERVER_URL=http://localhost:8080 TM_STATE_DIR=../data/trainer-dev .venv/bin/python -m trainmaster_trainer run
```

- 在 `trainer/` 目录下运行 `python -m trainmaster_trainer` 即可导入本包，不需要安装；也可以 `pip install -e .` 后在任意目录运行 `trainmaster-trainer`。
- 没有安装 PyTorch 时，硬件与实时指标照常上报，CUDA 环境显示为异常（`torch_missing`）。需要完整的环境时安装 CPU 版或与本机驱动匹配的 PyTorch 与 Ultralytics：`bash scripts/install-torch.sh cpu --python .venv/bin/python && .venv/bin/pip install -r requirements.txt`（变体见 [部署指南第 10.3 节](deployment.md#103-驱动与-pytorch-变体)）。
- 本地后端默认不要求令牌；设置了 `TM_NODE_TOKEN` 时训练节点也要设置同一个值。`TM_STATE_DIR` 中保存节点标识，换一个目录就会作为新节点注册。
- 其它子命令：`selftest`（中文的 CUDA 环境检测结果，`--json` 输出 JSON）、`export`（在本机把 pt 转换为 ONNX，与平台的转换任务使用同一个子进程）、`infer`（在本机用 pt 识别图片并打印检测框，与平台的审图识别任务使用同一个子进程）、`healthcheck`；配置变量见 [部署指南第 3.3 节](deployment.md#33-训练节点配置容器内由-compose-和镜像设置)。
- 模型仓库的 ONNX 转换：节点注册后自动领取本地后端的转换任务。`requirements-dev.txt` 不含 torch 与 onnx，此时节点不领取转换任务（日志中有说明）；要在本机跑真实的转换，按上一条安装 CPU 版 PyTorch 与 `requirements.txt`（含 onnx、onnxslim、onnxruntime），可以先用 `.venv/bin/python -m trainmaster_trainer export --weights best.pt --imgsz 320` 单独验证。
- 模型审图的识别：同样只在能找到 torch 与 ultralytics 时领取（不需要 onnx）。按上一条安装 CPU 版 PyTorch 与 `requirements.txt` 后，节点领取本地后端的审图任务并在 CPU 上识别（下载模型与图片都走 HTTP，不需要访问 `data/storage`）；可以先用 `.venv/bin/python -m trainmaster_trainer infer --weights best.pt a.jpg b.jpg` 单独验证模型与图片。单张图片的超时为 `TM_INFERENCE_IMAGE_TIMEOUT`（默认 300 秒）。
- 构建并运行镜像：`make docker-build-trainer`（使用 `.env` 中的 `TM_TORCH_VARIANT`），或不需要 GPU 的 CPU 版：`docker build --build-arg TORCH_VARIANT=cpu -t trainmaster-trainer:dev-cpu trainer`，再 `docker run --rm trainmaster-trainer:dev-cpu python -m trainmaster_trainer probe`。

### 本地审图程序

本地审图程序（`backend/cmd/trainmaster-reviewer`，见[架构说明 · 本地审图程序](architecture.md#本地审图程序)）是一个 Go 程序，界面是 `frontend/src/reviewer/`（与平台的审图编辑器共用 `src/features/reviews/` 与 `src/features/annotation/` 中的代码）。界面单独构建：入口 `frontend/reviewer.html`，配置 `frontend/vite.reviewer.config.ts`，输出到 `backend/cmd/trainmaster-reviewer/web/dist`（已被 git 忽略），编译程序时用 `go:embed` 嵌入；没有构建界面时嵌入的是仓库中的占位页，程序照样能编译（`go build ./...`、`go vet ./...` 不需要先构建前端）。

```bash
make reviewer      # npm run build:reviewer，再编译 Windows 版 backend/bin/trainmaster-reviewer.exe（amd64，-H windowsgui，带版本信息）
```

不使用 make 时的等价命令：

```bash
cd frontend && npm run build:reviewer     # tsc -b && vite build -c vite.reviewer.config.ts
cd backend && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o bin/trainmaster-reviewer.exe ./cmd/trainmaster-reviewer
```

- `TM_REVIEWER_OUT_DIR` 改变界面的输出目录（Docker 构建用它输出到 `/out/reviewer-web`，再复制到程序的 `web/dist`）。
- 在开发机上试用：Linux / macOS 上构建的程序用于开发，不打开窗口，在浏览器中打开同样的页面（`-no-open` 时只打印地址），Ctrl+C 或在页面上“退出”结束。先在本地平台的审图任务页面“下载审图包”，然后：

  ```bash
  cd frontend && npm run build:reviewer
  cd ../backend && go run ./cmd/trainmaster-reviewer -browser ~/Downloads/审图-1-测试.zip
  ```

  审图包解压到 zip 旁边的同名文件夹；导出的审图结果 `<文件夹名>-审图结果.zip` 在它旁边，再到本地平台的审图任务页面“上传审图结果”。不带审图包启动时在开始页中打开。在 Windows 上去掉 `-browser` 即为程序窗口（需要 WebView2 运行时），设置 `TM_REVIEWER_DEBUG=1` 时窗口中可以打开开发者工具。
- 修改界面时设置 `TM_REVIEWER_WEB_DIR`（一个包含 `dist/` 的文件夹）让程序从磁盘读取页面：`TM_REVIEWER_WEB_DIR=cmd/trainmaster-reviewer/web go run ./cmd/trainmaster-reviewer -browser`，之后每次修改只需重新 `npm run build:reviewer` 并刷新页面，不需要重新编译程序。页面的接口（`/api/session`、`/api/review/*` 等）由程序提供，平台的 Vite 开发服务器（`npm run dev`）不提供它们。
- 其它参数：`-addr 127.0.0.1:<端口>`（只能是本机地址；默认先试 47821，被占用时随机选择）、`-version`。日志写在用户缓存目录的 `trainmaster-reviewer/reviewer.log`（Go 的 `os.UserCacheDir()`，Linux 上为 `~/.cache`），最近打开的列表在用户配置目录的 `trainmaster-reviewer/recent.json`（`os.UserConfigDir()`）。
- 让本地平台提供程序的下载：`make reviewer` 后带上 `TM_REVIEWER_TOOL="$PWD/backend/bin/trainmaster-reviewer.exe" make dev-backend`（在仓库根目录执行；没有设置时页面提示“平台没有附带本地审图程序”）。

## 3. 测试与检查

```bash
make test     # go test ./... + npm test + 训练节点（pytest 与 bash 测试）
make lint     # gofmt、go vet、ESLint、TypeScript 类型检查、ruff、shellcheck
make build    # frontend/dist + backend/bin/trainmaster
make reviewer # 本地审图程序：界面 + backend/bin/trainmaster-reviewer.exe（见上文）
```

只运行训练节点相关的部分：`make test-trainer`、`make lint-trainer`（第一次运行时创建虚拟环境 `trainer/.venv`，`requirements-dev.txt` 变化时重新安装；Python 版本过低、缺少 venv 模块或 shellcheck 时会提示安装方法，可用 `PYTHON=python3.12` 指定解释器）。

对应的原始命令：

```bash
cd backend && gofmt -l . && go vet ./... && go test ./... && go build ./...
cd frontend && npm run lint && npm run typecheck && npm test && npm run build
cd trainer && .venv/bin/python -m pytest && .venv/bin/ruff check . && .venv/bin/ruff format --check .
for t in trainer/scripts/tests/test-*.sh deploy/scripts/tests/test-*.sh; do bash "$t" || break; done   # 在仓库根目录
shellcheck -x $(git ls-files '*.sh')
```

后端单元测试不依赖数据库（数据集模块的压缩包、VOC、缩略图等测试使用 `backend/internal/dataset/testdata/` 中的小样本，来源见其中的 README）。数据库集成测试（迁移、并发加锁、`app_settings` 读写、数据集导入 / 扫描 / 接口、审图版本与按版本组合的下载，模型仓库的上传、版本分配、转换队列与租约，模型审图的各个流程，数据版本的创建、生成、下载与回收站）通过 `TM_TEST_DATABASE_URL` 启用，未设置时自动跳过。数据集相关的测试会为每个测试新建并删除一个独立的数据库，所以该账号需要 `CREATEDB` 权限（postgres 超级用户即可）。请使用一次性的数据库：

```bash
docker run -d --rm --name tm-test-pg -e POSTGRES_PASSWORD=x -p 55432:5432 postgres:17-alpine
cd backend
TM_TEST_DATABASE_URL='postgres://postgres:x@localhost:55432/postgres?sslmode=disable' go test ./...
docker rm -f tm-test-pg
```

### 模型文件解析的测试

`backend/internal/model/checkpoint`（读取 `.pt` 的 pickle 解析器）的测试不需要 Python 与 PyTorch：

- **夹具**：`testdata/*.pt` 是真实的 Ultralytics 检查点（YOLOv8 / v9 / v10 / 11 / 12 / 26 的各种任务、训练产出的 `best.pt` 与 `last.pt`、自定义配置名、不同的 pickle 协议，以及平台拒绝的 RT-DETR、原版 YOLOv5、纯 `state_dict`、TorchScript、旧格式），只保留 zip 中的 `data.pkl` 与 `version`（不含张量数据）。生成脚本与版本、需要同步更新的期望值见 `testdata/README.md`；`internal/model` 的集成测试（`checkpoint_e2e_test.go`）也用这些文件走完上传与转换请求的流程。
- **安全加载的允许清单**：`safe.go` 照抄 Ultralytics 安全加载（`_SafeLoad`）在 torch 2.14.1、Ultralytics 8.4.198 下允许的对象，`safeGlobalCases`（`safe_test.go`）与 `TestFixtureGlobals` 记录了各名称与夹具的结论。升级训练节点的 torch 或 Ultralytics 后，用 `testdata/README.md`“Safe globals”一节中的 `safe_globals.py` 在装有它们的 Python 中对照一遍，不一致时同步修改 `safe.go`。
- **模糊测试**：`FuzzInspect` 用夹具与构造的恶意 pickle 作为种子，检查解析器不 panic、不耗尽内存；`FuzzCleanText` 检查先截断再清洗的字符串与整串清洗的结果一致。平时的 `go test` 只运行种子与 `testdata/fuzz/FuzzInspect/` 中保存的输入；需要时运行一段时间的模糊测试，发现的输入会自动保存到该目录，修复后随代码提交作为回归用例：

  ```bash
  cd backend
  go test ./internal/model/checkpoint -run '^$' -fuzz '^FuzzInspect$' -fuzztime 5m
  ```

### 模型审图的测试

- **后端**：`internal/reviewfile`（清单的读写与校验、Windows 安全的路径、VOC）是纯函数的单元测试；`internal/review`（创建、识别任务的领取与租约、结果写入、审核与并发保存、审图包、上传结果的冲突规则、合并生成审图版本、合并结束时的重试）与 `internal/httpapi` 的 `reviews_test.go`、`review_exchange_test.go`（接口的完整流程、失败与重试）是数据库集成测试，按上面的方法用 `TM_TEST_DATABASE_URL` 启用。`internal/reviewer` 覆盖打开与解压（不安全的条目、剩余空间、zip 与文件夹的各种打开方式）、导出、本机接口与访问控制；`contract_test.go` 检查本机接口的 JSON 字段与平台的类型一致（编辑器两边共用，改了平台的类型这里会失败）；`platform_test.go` 的 `TestIntegrationPlatformRoundTrip` 在测试数据库上运行平台的审图服务，走完下载审图包 → 在本地审图程序中打开、审核、导出 → 上传到平台（同样需要 `TM_TEST_DATABASE_URL`）。
- **训练节点**：`tests/test_review_inference.py` 覆盖识别子进程（图片读取与方向，包括 TIFF 的 Orientation 标签、框的提取、错误翻译、JSON lines 协议）、父进程对子进程的处理（超时、取消、崩溃）、结果的整理与攒批，以及 `infer` 子命令；`tests/test_review_tasks.py` 让任务线程对假平台（`tests/fake_platform.py`，含审图识别的接口）领取并执行审图任务（分页与攒批、从还没有识别的图片继续、续租、单张图片的错误、超时与显存不足、可重试的错误、停止与放弃、模型的校验与磁盘检查）。假的 `ultralytics`、`torch`、`numpy`、`cv2`、`PIL` 在 `tests/inference_fakes.py` 中：识别子进程只继承允许清单中的环境变量，所以它们以同名模块文件放在 `PYTHONPATH` 上，配置从同目录的 `fake_inference.json` 读取。`tests/task_harness.py` 是模型转换与审图识别共用的任务线程替身。只运行这部分：

  ```bash
  cd trainer && .venv/bin/python -m pytest tests/test_review_inference.py tests/test_review_tasks.py
  ```

  真实的识别（torch 与 Ultralytics）没有自动化测试，需要时在装好 CPU 版 PyTorch 的环境中用 `infer` 子命令或本地后端验证（见第 2 节“训练节点”）。
- **前端**：`npm test` 包括 `src/features/reviews/` 中的纯函数（地址栏参数、审核状态与计数的更新、格式化、编辑器的缓存更新）、`src/reviewer/format.test.ts`，以及 `features/annotation/shortcuts.test.ts` 中审图模式的快捷键。

### 数据版本的测试

- **后端**：`internal/augment`（增强方法的规范化与 `MaxCopies`、增强计划的确定性与各份互不相同、框的变换、输出的像素格式与编码、EXIF / TIFF 方向标记的识别与重置）与 `internal/dataversion` 的划分（`split.go`：沿用文件夹、分组与文件名的规范化、稳定的哈希、小数据集的修正）是不需要数据库的单元测试；`internal/dataversion` 的其余部分（校验与预览、编号、后台生成与重启后的恢复、删除与停止、重新生成、清理、图片、YOLO / VOC / COCO 下载、回收站）与 `internal/httpapi` 中数据版本的接口是数据库集成测试，按上面的方法用 `TM_TEST_DATABASE_URL` 启用。硬链接失败与磁盘空间不足不需要特殊的文件系统：服务中的 `link`（模拟 `EXDEV`、`EPERM` 或链接到别的文件）、`removeAll` 与 `Options.DiskUsage` 可以在测试中替换。只运行这部分：

  ```bash
  cd backend
  TM_TEST_DATABASE_URL='postgres://postgres:x@localhost:55432/postgres?sslmode=disable' go test ./internal/augment ./internal/dataversion
  ```

- **前端**：`npm test` 包括 `src/features/data-versions/format.test.ts`（标题“数据版本 v4”、状态、划分摘要、比例预设与校验、跳过原因、新增占用）。

### 训练节点与脚本的测试

- **pytest**（`trainer/tests/`）：不依赖 GPU、torch 与网络——NVML、torch、ultralytics 用假模块代替，平台用本地 `http.server` 模拟（`tests/fake_platform.py`，含任务的领取、下载、进度、上传、完成与失败接口）。模型转换的测试（`test_model_export.py`、`test_tasks.py`）用 `tests/export_fakes.py` 中假的 `ultralytics`（含 `ultralytics.nn.tasks._SafeLoad`）、`onnx`、`onnxruntime` 覆盖各 YOLO 版本与后处理方式的参数、结果核对、错误翻译、安全加载、子进程的环境变量（没有 `TM_NODE_TOKEN`）、超时、SHA256 校验、上传前的预检、停止与放弃任务。转换子进程只继承允许清单中的环境变量，所以假模块从同目录的 `fake_export.json` 读取配置，而不是从环境变量；真实的转换由 CPU 版训练节点镜像构建时的冒烟检查覆盖（也检查安全加载可用）。CI 在 Python 3.10（`requires-python` 的下限）与 3.12（镜像中的版本）上运行。
- **`trainer/scripts/tests/test-torch-variant.sh`**：变体选择规则（`lib/torch-variant.sh`）；Python 侧的 `tests/test_variants.py` 覆盖同样的用例，两边必须一致。修改 `torch_variants.txt` 时运行这两组测试，并同步 README 与部署指南第 10.3 节中抄录的表格。
- **`trainer/scripts/tests/test-install.sh`**：裸机安装脚本 `install.sh` 与 `install-torch.sh`。
- **Windows**：`tests/` 中标了 `posix_only` 的测试（信号、进程组、shell 脚本）只在 Linux / macOS 上运行，`windows_only` 的（Job 对象、注册表、真实的 PowerShell 查询）只在 Windows 上运行，其余的在两边都运行；假的 `nvidia-smi` 用 `fakes.write_fake_smi`（Windows 上写出 `.cmd`）。`tests/test_install_ps1.py` 检查 `install.ps1`：脚本文本（BOM、没有使用调用方相关的 `$PSScriptRoot` / `$PSCommandPath`）在任何系统上检查；有 Windows PowerShell 时还检查语法（包括用中文引号括起来的字符串，PowerShell 也把“”‘’当作引号）、配置文件的读写与 `envfile.py` 一致、命令行参数的拼接、`-DryRun` 的检测结果与步骤（假的 `nvidia-smi`），以及从假平台下载后以脚本块运行（“接入节点”中的命令，结束后调用方继续运行）。只加载脚本中的函数时点运行 `# ==== 主流程 ====` 之前的部分。`trainer/scripts/tests/install-windows-e2e.py` 做一次真实的安装（CPU 版 PyTorch、以 SYSTEM 运行的计划任务连接假平台、目录权限、重新运行与卸载），需要管理员与网络，并会改动本机，只在 CI 的 Windows 虚拟机上运行。
- **`deploy/scripts/tests/test-setup-compute.sh`**：`make setup` 的脚本。用假的 `nvidia-smi`、`docker`、sysfs、`sudo`、apt / dnf / yum / zypper 等覆盖各种机器（有 / 没有显卡与驱动、旧驱动、RTX 50 系列、有 / 没有 NVIDIA Container Toolkit、新旧 Docker 的报错（包括 Docker 29.5 的 `AMD CDI spec not found`）、Docker Desktop、snap 与 rootless Docker、有 / 没有 systemd、WSL、无法连接 Docker、离线、超时等）、`.env` 的合并与安装流程，不需要 Docker 与 GPU，也不会改动本机。`VERBOSE=1` 打印每次运行的输出；脚本兼容 macOS 自带的 bash 3.2。
- **`deploy/scripts/tests/test-compose.sh`**：用真实的 `docker compose config` 检查各 profile 的服务、GPU 预留与变量插值（包括传给训练节点与 app 的 `TM_TRAINER_MODE`、`TM_TRAINER_VARIANT`），不启动容器（没有 docker compose 时跳过）。
- **`deploy/scripts/tests/test-makefile.sh`**：用假的 `docker compose`（`COMPOSE=<替身>`）运行 `make up` / `make down`，检查先启动平台、训练节点构建失败只警告、`make down` 包括全部 profile，以及代码没有变化时两次 `make up` 的构建时间（`TM_BUILD_TIME`，取当前提交的提交时间）相同（app 镜像的构建参数因此不变，不会重建 app 镜像）、`.env` 与环境变量中的值优先。

在真实机器上调试 `make setup` 时，加 `SETUP_ARGS=--dry-run` 只检测、不修改任何文件，或用 `--env-file` 写到别的文件。

CI（`.github/workflows/ci.yml`）在每次 push 到 main 和每个 Pull Request 上运行：后端（gofmt、vet、带 PostgreSQL 服务的测试、Windows/macOS 交叉编译）、前端（lint、typecheck、test、build，以及本地审图程序的界面与 Windows 版程序，产物上传为 `trainmaster-reviewer`）、训练节点（ruff、Python 3.10 与 3.12 上的 pytest、shellcheck、bash 测试）、Windows 上的训练节点（windows-latest 上的 pytest，包括 Windows PowerShell 5.1 与 PowerShell 7 运行的 `install.ps1` 检查，最后是 `install-windows-e2e.py` 的真实安装），以及 Docker 镜像构建：应用镜像、CPU 版训练节点镜像（构建时执行一次 CPU 上的 YOLO 自检与一次离线的 ONNX 转换，再检查 `probe` 的输出），并分别在不启用训练节点、`COMPOSE_PROFILES=gpu`、`COMPOSE_PROFILES=cpu` 下检查 `docker compose config`。

## 4. 添加新的后端 API

以实现“标签体系”模块为例（数据集模块 `internal/dataset` + `internal/httpapi/datasets.go` 是一个完整的参考实现）：

1. **建表**：新增迁移文件 `backend/internal/database/migrations/0004_create_labels.sql`（编号连续，不要修改已有文件，不要写 `BEGIN/COMMIT`），约定见 [架构说明 · 数据库迁移约定](architecture.md#7-数据库迁移约定)。
2. **写 handler**：在 `backend/internal/httpapi/` 下新建 `labels.go`。成功时用 `writeJSON` 返回资源对象，列表用 `NewList(items, total)`，失败时用 `writeError(w, status, code, message)`（`message` 用中文，面向用户）。
3. **注入依赖**：handler 需要数据库或存储时，在 `Options` / `server` 中增加所需依赖（例如 `*database.DB`，或按功能定义的小接口，便于测试时替换）。文件路径一律通过 `storage.Resolve(key, rel)` 得到，数据库中只存相对路径。
4. **注册路由**：在 `router.go` 的 `/v1` 路由组中挂载新路由，并从 `plannedModules` 中删除对应的占位项：

   ```go
   r.Route("/annotation/labels", func(r chi.Router) {
       r.Get("/", s.listLabels)
       r.Post("/", s.createLabel)
       r.Get("/{id}", s.getLabel)
   })
   ```

5. **测试**：参考 `httpapi_test.go`，用 `httptest` 覆盖成功与错误路径；需要数据库的测试用 `TM_TEST_DATABASE_URL` 门控。
6. **文档**：在 [架构说明](architecture.md) 的接口表中补充新接口；如果新增了错误码，一并登记。

## 5. 添加新页面 / 功能模块

前端的导航、路由、面包屑和占位页文案都来自 `frontend/src/config/navigation.ts`。

**实现一个已有的占位模块**（如“标签体系”）：直接替换对应页面文件的内容，例如 `src/features/annotation/labels-page.tsx`；完成后把 `navigation.ts` 中该模块的 `status` 改为 `"ready"`。可参考已实现的数据集模块 `src/features/datasets/`。

**新增一个模块**：

1. 在 `navigation.ts` 的 `ModuleId` 中加入新 id，并在对应分组的 `items` 中添加一项（标题、路径、lucide 图标、描述、`status`、规划功能、预留 API 路径）。
2. 在 `src/features/<领域>/` 下新建页面文件，初期可渲染 `<ModulePlaceholder moduleId="新id" />`。
3. 在 `src/router.tsx` 的 `pages` 中登记（类型检查会提示遗漏）。
4. 需要后端接口时，按第 4 节添加，或先在后端 `plannedModules` 中预留占位路径。

调用后端使用 `src/lib/api.ts` 的 `apiGet<T>()`，配合 TanStack Query（参考 `src/features/system/api.ts`）；错误统一是 `ApiError`，可读取 `status`、`code`、`message`。

## 6. 添加 shadcn/ui 组件

项目使用 shadcn/ui 的 new-york 风格、neutral 配色，配置在 `frontend/components.json`。组件源码在 `frontend/src/components/ui/`，可以直接修改。与上游不同的本地修改（用 CLI 覆盖这些组件后请重新改回）：

- `sidebar.tsx`：移动端抽屉的读屏文案（`SheetTitle` / `SheetDescription`）改成了中文；`SidebarProvider` 新增 `persist` 属性（默认 `true`，与上游相同），为 `false` 时展开 / 收起不写入 `sidebar_state` cookie，布局在全屏页面（标注编辑器）中传 `false`，那里的临时状态不会覆盖普通页面保存的状态；
- `dialog.tsx`：关闭按钮的读屏文案改为“关闭”；
- `spinner.tsx`：`aria-label` 改为“加载中”；
- `slider.tsx`：`aria-label` 加在滑块手柄（`role="slider"`）上，而不是外层容器，读屏软件才能读出名称；
- `sonner.tsx`：使用项目自带的 `useTheme`（`@/components/theme-provider`）而不是 `next-themes`，并依赖 npm 包 `sonner`。

**方式一：CLI（需要能访问 ui.shadcn.com）**

```bash
cd frontend
npx shadcn@latest add dialog
```

**方式二：手动拷贝（内网或无法访问注册表时）**

1. 从 shadcn 仓库 `apps/v4/registry/new-york-v4/ui/<组件>.tsx` 获取源码，放到 `frontend/src/components/ui/`。
2. 改写 import：
   - `@/lib/utils` 保持不变（`cn` 工具函数）；
   - `@/registry/new-york-v4/ui/<x>` → `@/components/ui/<x>`；
   - `@/registry/new-york-v4/hooks/use-mobile` → `@/hooks/use-mobile`。
3. 安装组件依赖的 npm 包（多数组件只依赖已安装的 `radix-ui`、`class-variance-authority`、`lucide-react`）。
4. 不要引入 `next-themes`：主题切换使用项目自带的 `src/components/theme-provider.tsx`。

只添加实际用到的组件。

## 7. 代码规范

**通用**

- 面向用户的文案（界面、API 错误 `message`、文档）使用简体中文；代码标识符、日志字段名、提交信息使用英文。
- 不引入外部 CDN、在线字体或第三方统计脚本，平台需要在内网离线运行。
- 不在页面或接口中放假数据；未实现的功能使用占位页 / 501。
- 只为当前功能建表，不提前设计未来功能的表结构。

**Go**

- 提交前 `gofmt` 与 `go vet ./...` 必须通过（CI 会检查）。
- 依赖保持最少，优先使用标准库（`net/http`、`log/slog` 等）。新增依赖需确认支持 `go.mod` 中的 Go 版本（当前 `go 1.24`），不要让 `go mod tidy` 抬高 `go` 指令。
- 错误向上返回并用 `fmt.Errorf("...: %w", err)` 补充上下文；只在入口处决定退出或返回 HTTP 错误。
- 日志使用 `slog`，键名用 snake_case；不要打印密码、令牌等敏感信息。
- 与平台相关的代码（如磁盘用量）用构建标签分文件，保证 `GOOS=windows` / `GOOS=darwin` 能编译。

**Python（训练节点）**

- 提交前 `ruff check` 与 `ruff format --check` 必须通过（配置在 `trainer/pyproject.toml`，CI 会检查）；代码需兼容 Python 3.10。
- 常驻进程只依赖标准库、`psutil` 与 `nvidia-ml-py`（HTTP 客户端用 `urllib`）；`torch`、`ultralytics`、`onnx`（以及审图识别用的 OpenCV、Pillow）只在环境检测、模型转换与审图识别的子进程中导入，常驻进程不占用显存。
- 运行依赖在 `requirements.txt` 中固定版本；`psutil`、`nvidia-ml-py` 的版本同时写在 `requirements-dev.txt` 中（有测试检查两者一致）。

**Shell 脚本**

- 使用 bash 与 `set -euo pipefail`，`shellcheck -x` 无警告（CI 会检查所有 `.sh`）；输出给用户的提示用中文。
- 外部命令（`nvidia-smi`、`docker` 等）不存在时给出说明而不是报错退出；可通过环境变量替换（如 `TM_NVIDIA_SMI`、`TM_DOCKER`、`TM_SYSFS_ROOT`），测试用假命令覆盖，不能改动运行测试的机器。

**前端**

- TypeScript 严格模式；提交前 `npm run lint`、`npm run typecheck`、`npm test` 通过，`npm run format` 统一格式（Prettier，含 Tailwind 类名排序）。
- 组件文件使用 kebab-case 命名；按功能放在 `src/features/<领域>/`，通用组件放在 `src/components/`。
- 样式使用 Tailwind 工具类和 shadcn 主题变量（`bg-background`、`text-muted-foreground` 等），保证浅色 / 深色主题都正常，并兼容 375px 宽的移动端。
