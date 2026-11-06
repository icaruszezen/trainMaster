# trainMaster

面向团队内部的**工业视觉平台**：把图片数据集管理、标注、数据版本、模型训练、模型评估和模型管理放进一个 Web 平台，供团队在内网中协作使用。

它是桌面工具 [ez_training](https://github.com/icaruszezen/ez_training)（PyQt 版，覆盖数据集、预标注、标注、数据准备、训练、验证等）的团队 Web 版。

> **当前进度：** 平台框架（Go + React 工程骨架、shadcn/ui 界面框架、Docker 部署与数据持久化）已完成；**数据集管理**已上线：压缩包 / 文件夹导入（VOC、X-AnyLabeling、YOLO 标注，统一转换为 VOC 保存）、服务器上已有文件夹直接添加为数据集、缩略图浏览与标注预览、按 VOC / X-AnyLabeling / YOLO 格式下载；**项目管理**已上线：按检测任务把数据集组织成项目（一个数据集可以属于多个项目），项目页汇总其中的数据、标注与模型情况，并维护训练用的类别列表（顺序即 YOLO 的类别编号）；**标注工作台**已上线：在浏览器中逐张画矩形框，画完框按项目的类别列表和数据集中已有的标签提示类别，标注直接保存为数据集文件夹中的 VOC 文件；**计算资源**已上线：训练节点自动识别显卡、驱动、CPU 与内存并实时上报，按驱动与显卡自动准备好 YOLO 训练所需的 CUDA 版 PyTorch 环境并自检，`make setup` 自动检测并配置 Docker 的显卡支持；**模型仓库**已上线：按项目上传 Ultralytics YOLO 的 `.pt` 模型（YOLOv8 / v9 / v10 / 11 / 12 / 26）并按 v1、v2… 迭代版本，平台直接解析文件、校验所选的 YOLO 版本，下载时可选 PT 或由训练节点自动转换的 ONNX；**模型审图**已上线：用模型仓库中的模型由训练节点识别所选数据集的图片，在网页上或用 Windows 本地审图程序（离线）逐张检查、修正识别框并标为已审核 / 需加样，再把审图标注合并为数据集的审图版本（审图1.0、审图1.1…），下载数据集时可以选择版本。其余模块已有占位页，将在后续迭代中逐个实现。

## 功能模块

| 分组 | 模块 | 页面路径 | 预留 API | 状态 |
|---|---|---|---|---|
| 概览 | 工作台（系统状态、平台流程入口） | `/` | `GET /api/v1/system/info` | 已完成 |
| 项目管理 | 项目：新建 / 编辑 / 删除、纳入数据集、类别列表（训练时的 YOLO 类别顺序）、项目详情（数据集、类别与标签分布、模型版本） | `/projects` | `/api/v1/projects` | 已完成 |
| 数据管理 | 数据集：压缩包 / 文件夹导入（VOC / X-AnyLabeling / YOLO 标注）、添加已有目录、缩略图浏览与标注预览、按格式下载 | `/datasets` | `/api/v1/datasets` | 已完成 |
| 数据管理 | 模型审图：用模型识别数据集（训练节点执行）、网页审图与 Windows 本地审图程序（标为已审核 / 需加样、修正识别框）、审图包下载与结果上传、合并为数据集的审图版本 | `/datasets/reviews` | `/api/v1/reviews` | 已完成 |
| 数据管理 | 数据版本：划分、快照、导出 YOLO/VOC/COCO、增强 | `/datasets/versions` | `/api/v1/datasets` | 规划中 |
| 标注 | 标注任务：分配、进度、审核返修、抽检 | `/annotation/tasks` | `/api/v1/annotation/tasks` | 规划中 |
| 标注 | 标注工作台：矩形框画框与编辑、按项目类别列表与已有标签提示类别、快捷键、撤销重做、自动保存为 VOC | `/annotation/workspace` | `/api/v1/datasets` | 已完成 |
| 标注 | 标签体系：缺陷类别、颜色与快捷键、层级与别名 | `/annotation/labels` | `/api/v1/annotation/labels` | 规划中 |
| 标注 | 智能预标注：模型预标注、视觉大模型、模板匹配 | `/annotation/prelabel` | — | 规划中 |
| 模型训练 | 训练任务：创建、排队调度、实时日志与曲线 | `/training/jobs` | `/api/v1/training/jobs` | 规划中 |
| 模型训练 | 计算资源：训练节点自动接入、显卡 / CPU / 内存与 CUDA 环境识别、实时监控、重新检测 | `/training/compute` | `/api/v1/compute` | 已完成 |
| 模型评估 | 模型评估：mAP/P/R/F1、混淆矩阵、PR 曲线、对比报告 | `/evaluation` | `/api/v1/evaluations` | 规划中 |
| 模型管理 | 模型仓库：上传 .pt、按项目迭代版本、PT / ONNX 下载（训练节点自动转换） | `/models` | `/api/v1/models` | 已完成 |
| 模型管理 | 推理测试：在线推理、结果可视化、阈值调节 | `/models/inference` | — | 规划中 |
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
- **下载数据集**：在数据集详情页或卡片菜单中选择 VOC、X-AnyLabeling 或 YOLO 格式，服务器边打包边下载 zip，YOLO 包含 `images/`、`labels/`、`classes.txt` 与 `data.yaml`。数据集有审图版本时还可以选择下载哪些版本（见“模型审图 · 审图版本”），默认只下载基础版本（数据集自己的标注文件）。YOLO 同样需要类别顺序：数据集纳入了项目时使用项目的类别列表的顺序（与用这个项目训练时一致），属于多个项目时选择用哪个项目的顺序；没有纳入项目时手动指定（每行一个类别，可直接输入或读取 `classes.txt`），默认先沿用最近一次 YOLO 导入时的顺序（类别编号不变），其余标签按名称排列。
- **图片顺序固定**：图片网格、查看大图时的上一张 / 下一张、封面和下载的压缩包都按同一个顺序排列，与 Windows 资源管理器默认的“按名称”排序一致：文件夹在前，名称不区分大小写，数字按数值比较（`img2` 在 `img10` 前），中文按拼音。详见 [架构说明 · 图片顺序](docs/architecture.md#图片顺序)。
- 导入到已有数据集时，同名文件可选择跳过或覆盖；在服务器上直接增删文件后，点“重新扫描”即可同步。

识别规则、导入流程和接口详见 [架构说明 · 数据集与导入](docs/architecture.md#6-数据集与导入)。

## 项目管理

项目用于按检测任务（如“A 线外观检测”）组织数据集和模型，以后训练任务也会归到项目下。

- 在项目页新建、编辑、删除项目；删除项目只删除项目本身，其中的数据集保留。项目中有模型时，需要在删除对话框中勾选“同时删除 N 个模型”（模型文件与 ONNX 缓存一并删除）。
- **一个数据集可以纳入多个项目**：新建项目时直接勾选数据集，或在项目详情页“添加数据集”，也可以在数据集卡片 / 详情页的“纳入项目”中勾选它所属的项目。
- 数据集页可以按项目筛选，也可以筛选“未纳入项目”的数据集；卡片和列表上会显示数据集所属的项目。
- 项目详情页汇总其中所有数据集的图片数、标注进度、占用空间，按数据集列出明细；“类别”一栏是项目的类别列表与合计的标签分布（见下）；“模型”一栏按版本倒序列出本项目的模型，可以直接上传新版本（见下方“模型仓库”）。
- **类别列表属于项目**：每个项目有一份类别列表（名称、颜色、顺序），顺序就是训练时 YOLO 的类别编号（第一个为 0），标注时画完框也优先提示这些类别。数据集和标注本身不需要类别顺序（VOC 文件直接记录类别名称），同一个数据集在不同项目中可以使用不同的类别顺序。项目详情页的“类别”中列出每个类别在项目数据集中的目标数、图片数与所在的数据集数；数据集中已有、但不在列表中的标签列在后面（训练导出时忽略），可以逐个加入或全部加入。“编辑类别”中可以添加、改名、改颜色、调整顺序、删除类别，或读取已有的 `classes.txt` 按其中的顺序排列（沿用已有模型或其它工具的类别编号）。类别列表只是元数据，修改它不会改写任何标注文件；多人同时修改时不会悄悄覆盖别人的修改。详见 [架构说明 · 类别列表](docs/architecture.md#类别列表)。

接口详见 [架构说明 · 项目接口](docs/architecture.md#项目接口)。

## 标注工作台

在“标注工作台”中选择数据集（或在数据集详情页、查看大图时点“标注”）进入编辑器，按与数据集页相同的固定顺序逐张画矩形框。

- **画框与编辑**：按 <kbd>W</kbd> 后拖动（或先后点击两个对角）画框，按住 <kbd>Shift</kbd> 画正方形；拖动框或八个手柄移动、缩放，方向键逐像素微调；重叠的框再次单击可以依次选中下面的框。支持难例（difficult）、截断（truncated）标记，撤销 / 重做，复制框并粘贴到别的图片，或沿用上一张的框。
- **类别**：画完框后弹出类别选择框，候选依次是数据集所属项目的类别列表（属于多个项目时按项目名称依次合并，同名的类别只列一次）和数据集中已有的标签（按目标数量从多到少）；默认选中上一次使用的类别，直接按 <kbd>Enter</kbd> 确定（<kbd>Esc</kbd> 放弃这个框），输入文字可以筛选，也可以直接输入新的类别名称。数据集所属的项目有类别列表时，不在类别列表中的标签标为“不在类别列表”（训练导出 YOLO 时不包含它们的框）。数字键 <kbd>1</kbd>–<kbd>9</kbd>、<kbd>0</kbd> 对应前 10 个候选类别（选中框时修改它的类别），<kbd>T</kbd> 搜索类别。标注时不需要关心类别顺序：类别列表在项目页面中管理，“更多操作”中可以直接前往；右侧面板只显示本图的框与图片信息。
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
- **自动配置 Docker 的显卡**：`make setup`（`make up` 会先自动执行它）检测显卡、驱动，并实际运行一次 `docker run --gpus all … nvidia-smi -L` 验证 Docker 能否使用显卡，据此在 `.env` 中启用 GPU 或 CPU 训练节点、选好 PyTorch 版本、生成节点令牌。Docker 还不能使用显卡时，`make up SETUP_ARGS="--install-toolkit"` 按发行版（apt / dnf / yum / zypper）安装并配置 NVIDIA Container Toolkit（已安装时只配置并重启 Docker，Docker 已能使用显卡时什么也不做），再启动 GPU 训练节点——每条需要 root 的命令都先列出并确认；snap 版、rootless 模式的 Docker 与没有 systemd 的 WSL 给出手动步骤。
- **平台服务器**：页面同时显示平台所在机器的 CPU、内存、负载与检测到的 NVIDIA 显卡；机器上有显卡但还没有 GPU 训练节点在线时会给出提示（本机的训练节点用 `--mode off` 关闭或选择了 CPU 模式时不提示；驱动过旧时提示先升级驱动）。

PyTorch 版本的选择规则（变体表 [`trainer/trainmaster_trainer/torch_variants.txt`](trainer/trainmaster_trainer/torch_variants.txt)）：按下表顺序，取第一个**驱动版本满足、且支持全部显卡**的变体；一个都不满足时使用 CPU 版。

| 变体 | PyTorch / torchvision | CUDA | 最低驱动 | 支持的显卡（计算能力） |
|---|---|---|---|---|
| `cu130` | 2.14.1 / 0.29.1 | 13.0 | 580 | 7.5、8.x、9.0、10.x、12.x：RTX 20 系列到 RTX 50 系列、T4、A100、H100、B200 等 |
| `cu126` | 2.14.1 / 0.29.1 | 12.6 | 525.60.13 | 5.x–9.0：GTX 900 / 10 系列、V100 到 H100（不含 RTX 50 系列） |
| `cu128` | 2.11.0 / 0.26.0 | 12.8 | 525.60.13 | 7.5、8.x、9.0、10.x、12.x：驱动低于 580 时的 RTX 50 系列等 |
| `cu118` | 2.7.1 / 0.22.1 | 11.8 | 450.80.02 | 3.7、5.x–9.0：驱动低于 525.60.13 的机器 |
| `cpu` | 2.14.1 / 0.29.1 | — | — | 没有 NVIDIA 显卡、没有驱动或驱动过旧时 |

例如驱动 550 + RTX 4090 → `cu126`；驱动 570 + RTX 5090 → `cu128`；驱动 580 及以上、RTX 20 系列或更新的显卡 → `cu130`。多块显卡中有不受支持的型号时，选支持显卡最多的变体并提示哪些显卡无法使用；ARM 服务器（如 Grace Hopper）只在驱动 ≥ 580 且显卡为 Ampere 或更新时使用 `cu130`。Windows 电脑的最低驱动略高：CUDA 12.x 为 528.33，11.8 为 452.39（13.0 同为 580）。升级驱动后再次 `make up` 会自动换用更合适的版本；也可以用 `make setup SETUP_ARGS="--variant cu126"` 固定版本。

接入训练节点的四种方式：

| 方式 | 做法 |
|---|---|
| 平台所在机器（Docker） | `make up`（先自动执行 `make setup`）：有可用的显卡时启动 GPU 训练节点（`trainer` 服务），否则启动 CPU 训练节点（`trainer-cpu`）；Docker 还不能使用显卡时用 `make up SETUP_ARGS="--install-toolkit"` |
| 其它 GPU 机器（Docker） | 克隆本仓库，`make setup SETUP_ARGS="--server-url http://<平台IP>:8080 --token <令牌>"`（令牌即平台 `.env` 中的 `TM_NODE_TOKEN`），再只启动训练节点：`docker compose up -d --build trainer`（按 `make setup` 最后输出的服务名，选择 CPU 时为 `trainer-cpu`；需要时加 `--install-toolkit` 让脚本安装 NVIDIA Container Toolkit）。不要在这台机器上运行 `make up`，它会再启动一套平台 |
| 不用 Docker（Linux） | `sudo trainer/scripts/install.sh --server-url http://<平台IP>:8080 --token <令牌> --systemd`：安装到虚拟环境并注册为 systemd 服务 |
| Windows 电脑 | 在“计算资源 → 接入节点”中复制 Windows 的命令，在这台电脑的管理员 PowerShell 中运行：从平台下载安装脚本，检测显卡与驱动后安装匹配的 PyTorch（没有 Python 时自动安装），并注册为开机以 SYSTEM 运行的计划任务 `trainmaster-trainer`。支持 64 位 Windows 10 / 11、Windows Server 2019 及更新 |

完整说明（手动配置、只使用部分显卡、国内镜像、常见问题）见 [部署文档 · GPU 训练节点](docs/deployment.md#10-gpu-训练节点)。训练节点目前还负责模型仓库的 ONNX 转换（见下一节）与模型审图的识别（见“模型审图”）；训练任务的排队与调度将在后续迭代中实现。

## 模型仓库

模型仓库（`/models`）按项目保存用 Ultralytics 训练的 YOLO 模型：**一个模型属于一个项目，一个项目可以有多个迭代版本**，同一项目中的模型依次编号为 v1、v2…，版本号只增不减，删除后也不会被复用。模型以普通文件保存在存储目录的 `models/` 下（宿主机默认 `./data/storage/models/project-<项目ID>/v<版本号>/`）：`model.pt` 是上传的原始文件（字节不变），`model.json` 记录项目、版本、YOLO 版本、SHA256 与从文件中读出的信息，不看数据库也能知道它是什么。

- **上传**：在模型仓库，或项目详情页的“模型”中上传 `.pt` 文件（最大 8 GiB），选择所属项目与 YOLO 版本（YOLOv8、YOLOv9、YOLOv10、YOLO11、YOLO12、YOLO26），可以填写说明。按 8 MiB 分块上传、断点续传，进度显示在右下角的上传面板中，切换页面不会中断。
- **检查文件**：平台不安装 PyTorch，而是直接读取 `.pt` 中保存的模型信息（只读的解析器，不执行文件中的任何代码），得到任务（检测 / 分割 / 分类 / 姿态 / 旋转框，以及 YOLO26 的语义分割 / 深度估计）、类别名称、训练输入尺寸、模型规模与参数量、训练轮数与 mAP 等指标。不是 Ultralytics 模型的文件会被拒绝并说明原因：纯 `state_dict`、TorchScript、PyTorch 1.6 之前的旧格式、原版 YOLOv5 仓库（`ultralytics/yolov5`）训练的模型、RT-DETR、YOLO-NAS 等。文件中引用了 Ultralytics 安全加载不允许的对象（如 `os.system`、自定义模块中的类）时也会被拒绝（“文件中引用了平台不允许加载的对象…”）：训练节点不会加载这样的文件，它也可能是构造的恶意文件。
- **YOLO 版本**：平台从模型配置名（如 `yolo11n.yaml`）或网络结构识别文件中的版本，与所选的不同时不保存，提示改选识别出的版本；已上传的文件留在服务器上，改选后直接保存，不需要重新上传。识别不出版本的文件按所选的版本保存（如配置名是自定义的分类模型：YOLO11 与 YOLO26 的分类网络相同）。
- **模型详情**（`/models/<模型ID>`）：YOLO 版本与规模、任务、输入尺寸、参数量、训练信息与指标、类别列表、SHA256、ONNX 转换记录；说明可以修改。删除模型同时删除它的文件夹（包括 ONNX 缓存）。

**下载**：PT 直接下载原始文件（文件名为 `<项目名>_v<版本>.pt`）。选择 ONNX 时，平台把转换放入队列，由在线的训练节点用 Ultralytics 在 CPU 上转换并用 ONNX Runtime 验证一次，再传回平台；页面显示排队位置与进度，完成后自动开始下载。可以选择输入尺寸（高、宽：32–4096 且为 32 的倍数，默认为训练时的尺寸）、动态尺寸（batch、高、宽；不能与嵌入 NMS 同时使用）、opset（自动或 11–21）与后处理方式：

| 后处理方式 | 输出（检测模型，640×640 输入） | 说明 |
|---|---|---|
| `raw` 原始输出 | `1 × (4 + 类别数) × 8400` | 推理后需要自行 NMS |
| `nms` 嵌入 NMS | `1 × 300 × 6`（x1, y1, x2, y2, 置信度, 类别） | NMS 已嵌入模型；用于检测、分割、姿态、旋转框模型。只能固定 batch 为 1：Ultralytics 按导出时的 batch 展开嵌入的 NMS，动态 batch 时第一张之后的图片没有结果，所以不能与动态尺寸同时使用 |
| `end2end` 端到端 | `1 × 300 × 6` | 使用端到端（NMS-free）检测头，不需要 NMS；只用于带这种检测头的检测、分割、姿态、旋转框模型 |

| YOLO 版本 | 检测 / 分割 / 姿态 / 旋转框 | 分类、语义分割、深度估计（没有检测框） |
|---|---|---|
| YOLOv8、YOLOv9、YOLO11、YOLO12 | `raw`（默认）、`nms` | `raw` |
| YOLOv10、YOLO26 | `end2end`（默认）、`raw`、`nms` | `raw` |

- **缓存**：同一模型、同样的选项只转换一次，之后再下载直接返回保存在模型文件夹 `onnx/` 中的结果；结果文件被删除或上次转换失败时，再次下载会重新转换。关闭下载对话框不会取消转换，之后可以在模型详情页的转换记录中下载。
- **需要在线的训练节点**：转换需要至少一个在线、版本不低于 0.2.0 的训练节点，CPU 训练节点即可，不需要 GPU，也不需要访问外网。没有这样的节点时转换一直排队，页面提示“没有可以执行转换的训练节点”，节点上线后自动开始；PT 下载不受影响。执行转换的节点中途掉线时，任务在租约（60 秒）过期后重新排队，最多尝试 3 次。见 [部署文档 · 模型转换](docs/deployment.md#1010-模型转换onnx)。
- **安全**：`.pt` 是 pickle，加载时可以执行文件中的代码。平台只解析、从不加载它；训练节点转换时用 Ultralytics 的安全加载（`ULTRALYTICS_SAFE_LOAD=1`，只还原其允许清单中的类），转换子进程也拿不到节点令牌 `TM_NODE_TOKEN` 等配置。下载 PT 后自己用 Ultralytics（默认不启用安全加载）或 `torch.load(weights_only=False)` 加载时，文件仍会被完整反序列化：上传时的检查不能保证这样加载是安全的，请只加载来源可信的模型文件。
- **为平台训练预留**：模型记录了来源（用户上传 / 平台训练）；训练任务上线后，训练产出的模型也会作为所属项目的新版本存入模型仓库，使用同样的检查与存储方式。

存储方式、文件解析与接口见 [架构说明 · 模型仓库](docs/architecture.md#10-模型仓库)，转换任务的协议见 [架构说明 · 任务协议](docs/architecture.md#任务协议)。

## 模型审图

模型审图（侧边栏“数据管理 → 模型审图”，`/datasets/reviews`）用模型仓库中的一个模型识别一个或多个数据集的图片，再由人逐张检查识别结果：识别正确的标为**已审核**，识别不对、需要参与后续训练的修正识别框后标为**需加样**，最后把审图标注合并为数据集的一个审图版本，下载时作为下一轮训练的样本。审图不改动数据集：识别结果与修改后的框保存在审图任务自己的文件夹中（存储目录的 `reviews/review-<任务ID>/` 下，每张图片一个 VOC `.xml`，目录结构与数据集相同），合并时保存为数据集的审图版本，数据集的标注文件也不会被修改。

- **新建**：在模型审图页点“新建审图任务”，或在模型详情页点“用这个模型审图”。选择模型（需要模型文件还在，且是检测、分割、姿态或旋转框模型：分类等没有检测框的模型不能用于审图）与数据集（默认选中模型所属项目的数据集；最多 50 个，合计不超过 20 万张图片），可以调整识别参数：置信度阈值（默认 0.25）、IoU 阈值（默认 0.7）、输入尺寸（默认为训练时的尺寸）、最多检测框数（默认 300）。图片按所选数据集的先后、数据集内按与数据集页相同的固定顺序排列。
- **识别**：任务排队后由在线的训练节点识别（节点需要装有 PyTorch 与 Ultralytics，CPU 训练节点即可，有 GPU 时更快），页面显示排队位置与进度。已识别的图片可以马上开始审核，不必等全部识别完。没有这样的节点时任务一直排队，页面提示“没有可以识别的训练节点”，节点上线后自动开始。识别中的节点掉线时，任务在租约（60 秒）过期后重新排队，最多尝试 3 次；“识别失败”的任务可以“重新识别”，已经识别过的图片不会再识别一遍。个别图片无法识别（如文件损坏）时只记录这张图片的原因，审核时可以手动画框。
- **网页审图**：在审图任务详情页点“网页审图”，进入与标注工作台相同的编辑器：左侧列表显示全部图片，从第一张待审核的图片开始（识别完成的都审完时从第一张开始）。<kbd>S</kbd> 标为已审核，<kbd>X</kbd> 标为需加样，<kbd>U</kbd> 改回待审核；默认标记后自动跳到下一张（<kbd>U</kbd> 不跳，工具栏中可以关闭），<kbd>Shift</kbd>+<kbd>D</kbd> / <kbd>Shift</kbd>+<kbd>A</kbd> 跳到下一张 / 上一张待审核的图片。画框、改类别、撤销重做等操作与快捷键和标注工作台相同（<kbd>N</kbd> 空标注不可用），按 <kbd>?</kbd> 查看全部快捷键。右侧面板列出模型识别出的框与置信度，“恢复”把框换回模型的识别结果（可以撤销）；左侧列表可以按审核状态、数据集、类别、文件名筛选，或只看改过框的、没有框的、识别失败的图片。修改停止编辑后自动保存；多人同时审核同一张图片时不会悄悄覆盖，与标注工作台相同。
- **本地审图（Windows，离线）**：图片很多或网络不便时，可以在 Windows 电脑上离线审图，步骤列在详情页的“本地审图”中：① 下载本地审图程序 `trainmaster-reviewer.exe`（单个文件，不需要安装）；② 下载审图包：一个 zip，包含图片、当前的审图标注与清单 `review.json`，可以只包含某些审核状态或某个数据集的图片（下载链接 1 小时内有效）；③ 运行程序打开审图包（也可以把 zip 拖到程序上），程序把它解压到 zip 旁边的同名文件夹，界面与快捷键和网页审图相同，审核进度随时保存在这个文件夹中，关闭后再打开会继续；审完后点“导出审图结果”，在旁边生成 `<文件夹名>-审图结果.zip`（审核状态与改过的标注，不含图片）；④ 回到平台“上传审图结果”。上传时只应用本地改过的内容，重复上传同一份结果不会重复应用；下载审图包之后平台上又被修改过的图片作为冲突跳过并列出，可以选择用本地结果覆盖。程序用 WebView2 显示窗口（Windows 11 与更新过的 Windows 10 已自带），没有时改用默认浏览器打开。
- **合并到数据集**：把审图标注保存为数据集的一个新的**审图版本**。默认只合并需加样的图片，也可以加上已审核的图片、只合并某个数据集；合并时必须填写**版本备注**（说明这个版本的内容，最多 500 个字符），对话框中列出将生成的版本号。每个有要合并的图片的数据集各生成一个版本，按数据集依次编号为审图1.0、审图1.1、审图1.2…，编号不重复使用（删除的版本号不会再分配；几个审图任务同时合并时按完成的先后编号）。版本是合并时审图标注的快照：包含所选审核状态的全部图片（不只是上次合并之后改过的），之后再修改审图任务不影响已生成的版本。没有框的图片作为空标注（负样本）保存；识别失败、审核时也没有画框的图片跳过；合并过程中审核状态被改为不合并的状态的图片也跳过。合并在后台进行，页面显示进度，完成后显示生成的版本与合并、跳过、失败的图片数；合并中断（如服务重启）时不生成版本，可以重新合并。可以审完一批合并一批，每次合并生成下一个版本。
- **审核记录**：数据集详情页的“审核记录”列出包含这个数据集的审图任务，以及这个数据集中各审核状态的图片数与合并记录（生成的版本），可以直接审核或合并这个数据集的图片。
- **审图版本**：数据集详情页的“审图版本”列出基础版本（数据集文件夹中的标注文件，标注工作台与标签分布显示的就是它）与各审图版本（新的在前），每个版本显示备注、图片数（需加样 / 已审核）、目标数、来自的审图任务与模型；可以修改备注、删除版本。下载数据集时可以多选版本，组合方式如下：
  - **只选基础版本**：与没有审图版本时一样，导出数据集的全部图片与它们自己的标注。
  - **只选审图版本**：只导出所选版本中标为**需加样**的图片与它们的审图标注，不含基础版本的标注；版本中已审核的图片不导出。版本卡片上的“只导出需加样”就是这种方式。
  - **基础版本 + 审图版本**：导出数据集的全部图片，所选审图版本中有的图片用版本中的标注（包括已审核的图片），其他图片用基础版本的标注。版本卡片上的“覆盖基础版本导出”就是这种方式。
  - **多个审图版本**：同一张图片在几个版本中都有时，以新版本的标注为准。例如选择审图1.3、1.2、1.1 与基础版本：1.3 中的图片用 1.3 的标注，1.2 中有、1.3 中没有的用 1.2 的，1.1 中有、1.2 与 1.3 中都没有的用 1.1 的，其余图片用基础版本的标注，全部导出。
  - 版本按图片在数据集文件夹中的路径对应。合并后被删除的图片不再导出；合并后文件被替换（大小或修改时间变了）的图片，版本中的标注不再适用：有基础版本时用基础版本的标注，没有时不导出。下载对话框中会显示将导出的图片数、其中用审图版本标注的图片数，以及这些不再适用的图片数。压缩包与其中的文件夹以所选版本命名，如 `line_a-基础+审图1.1+审图1.3-yolo.zip`；只导出部分图片时 YOLO 的 `data.yaml` 中会注明。
- **删除**：删除审图任务同时删除它的文件夹（审图标注），数据集中的图片与已经生成的审图版本不受影响（版本中显示审图任务已删除）；正在合并时不能删除。删除审图版本不影响数据集的标注文件与审图任务，需要时可以再次合并。
- **以前的合并**：审图版本之前的合并直接写进了数据集的 VOC 标注文件，它们的记录照常显示（“直接写入数据集”），那些标注现在就是基础版本的一部分。

数据模型、识别任务的协议、审图包格式与接口见 [架构说明 · 模型审图](docs/architecture.md#11-模型审图)，训练节点的配置与本地审图程序的运行要求见 [部署文档 · 模型审图](docs/deployment.md#1011-模型审图)。

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.24、[chi](https://github.com/go-chi/chi) 路由、[pgx](https://github.com/jackc/pgx) 连接池、标准库 `log/slog`；内置 SQL 迁移 |
| 数据库 | PostgreSQL 17（只存元数据） |
| 文件存储 | 宿主机本地目录（图片等文件直接以文件形式保存） |
| 前端 | React 19、TypeScript、Vite、Tailwind CSS v4、[shadcn/ui](https://ui.shadcn.com)（new-york 风格，neutral 配色）、React Router、TanStack Query |
| 本地审图程序 | Go（Windows 单个可执行文件，[WebView2](https://github.com/jchv/go-webview2) 窗口，没有时用默认浏览器），界面与平台的审图编辑器共用 React 代码 |
| 训练节点 | Python 3.12、[PyTorch](https://pytorch.org)（按驱动自动选择 CUDA 版本）、[Ultralytics](https://github.com/ultralytics/ultralytics)（YOLO）、ONNX 与 ONNX Runtime（模型转换）、NVML（nvidia-ml-py）、psutil |
| 部署 | Docker 多阶段构建（单镜像：Go 服务同时托管前端）、Docker Compose（训练节点为单独的镜像，按 profile 启用）、NVIDIA Container Toolkit |

前端不引用任何外部 CDN 或在线字体，所有资源都打包进镜像，可在无外网的内网环境运行。

## 快速开始（Docker 部署）

需要 Docker Engine 24+ 与 Docker Compose v2。使用 GPU 训练还需要 NVIDIA 驱动与 NVIDIA Container Toolkit（`make setup` 会检查，`make up SETUP_ARGS="--install-toolkit"` 可自动安装）。

```bash
git clone https://github.com/icaruszezen/trainMaster.git && cd trainMaster
cp .env.example .env          # 按需修改端口、数据目录、数据库密码、PUID/PGID
make setup                    # 检测显卡、驱动与 Docker 的 GPU 支持，把训练节点配置写入 .env
make up                       # 再执行一次 make setup，然后先构建并启动 postgres 与 app，再构建并启动训练节点
```

`make setup` 的输出会说明检测到的显卡、选择的 GPU / CPU 模式与 PyTorch 版本，以及不能使用 GPU 的原因与修复方法。训练节点镜像构建失败（如下载 PyTorch 超时）时 `make up` 只警告，平台照常启动。`make down` 停止并删除全部容器（包括各个 profile 中的训练节点）。没有 `make`（如 Windows）时直接 `docker compose up -d --build`：此时不启动训练节点，需要时按 [部署文档第 10 节](docs/deployment.md#10-gpu-训练节点) 在 `.env` 中手动配置。训练节点镜像包含 PyTorch 与 CUDA 运行库，第一次构建需要下载数 GB。

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
│   ├── models/        # 模型仓库：每个模型一个文件夹 project-<项目ID>/v<版本号>/（model.pt、model.json，onnx/ 为转换缓存）
│   ├── reviews/       # 模型审图：每个审图任务一个文件夹 review-<任务ID>/（review.json 与审图标注 .xml）
│   ├── runs/          # 训练/评估运行产物（日志、曲线、中间权重）
│   ├── exports/       # 导出的训练集（YOLO / VOC / COCO）、打包导出与报告
│   ├── cache/         # 缩略图等可再生缓存（可随时删除，无需备份）
│   └── tmp/           # 上传、解压等临时文件
└── trainer/           # 训练节点的状态目录（TM_HOST_TRAINER_DIR）：节点标识 node-key、心跳文件。不要拷贝到其它机器
```

- **数据库只存元数据**（数据集、图片索引、导入记录、模型版本、审图任务与审核状态等），图片和模型等大文件都在 `storage/` 下，可以直接用文件管理器、`rsync` 等工具查看和备份。
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

模型仓库的文件（`models/`）包含在存储目录的备份中；其中每个模型的 `onnx/` 文件夹只是 ONNX 转换的缓存，可以不备份（rsync 加 `--exclude '/models/*/*/onnx/'`），恢复后再次下载时会重新转换。模型审图的审图标注在 `reviews/` 中、审核状态与数据集的审图版本在数据库中，同样包含在上面的备份中；审图包不在服务器上保存（每次下载时现场打包）。

迁移到新服务器时先 `make down`，再把 `.env` 与**两个数据目录**（`TM_HOST_STORAGE_DIR` 和 `TM_HOST_POSTGRES_DIR`，默认都在 `./data` 下）用 `sudo tar --numeric-owner` 打包拷贝过去，详见 [部署文档第 6 节](docs/deployment.md#6-备份与恢复)。直接拷贝 PostgreSQL 数据目录要求新旧环境的 PostgreSQL 主版本一致（当前为 17）；跨版本请使用 `pg_dump` / `pg_restore`。

## 本地开发

需要 Go 1.24+、Node.js 22、Docker；训练节点的测试需要 Python 3.10+（不需要 GPU）。

```bash
make dev-db        # 启动开发用 PostgreSQL（端口只暴露到 127.0.0.1:5432）
make dev-backend   # 后端 http://localhost:8080（go run）
make dev-frontend  # 前端 http://localhost:5173（Vite 热更新，/api 代理到后端）
```

开发时访问 <http://localhost:5173>。常用命令：`make test`、`make lint`、`make build`，`make reviewer`（构建 Windows 版本地审图程序），`make help` 查看全部。没有 `make` 的环境（如 Windows）可直接执行对应命令，见 [开发文档](docs/development.md)。

## 目录结构

```text
trainMaster/
├── backend/                     # Go 服务（module github.com/icaruszezen/trainmaster/backend）
│   ├── cmd/trainmaster/         # 程序入口：配置 → 存储 → 数据库 → 迁移 → HTTP
│   ├── cmd/trainmaster-reviewer/ # Windows 本地审图程序（WebView2 窗口；web/dist 为嵌入的页面，make reviewer 构建）
│   └── internal/
│       ├── buildinfo/           # 版本信息（构建时注入）
│       ├── config/              # TM_* 环境变量加载与校验
│       ├── database/            # 连接池、启动重试、迁移执行器
│       │   └── migrations/      # NNNN_name.sql（嵌入二进制）
│       ├── storage/             # 数据目录布局、可写检查、磁盘用量、安全路径解析
│       ├── dataset/             # 数据集：文件识别、VOC 解析、解压、扫描、缩略图、导入任务
│       ├── project/             # 项目：项目与数据集的多对多关系、合计统计
│       ├── model/               # 模型仓库：上传、版本分配、下载、ONNX 转换队列与训练节点的任务接口
│       │   └── checkpoint/      # 只读解析 Ultralytics 的 .pt（pickle，不执行代码）：任务、类别、YOLO 版本、引用的对象是否安全
│       ├── review/              # 模型审图：审图任务、识别任务的接口、审核与审图标注、审图包、上传结果、合并为数据集的审图版本
│       ├── reviewfile/          # 审图包的文件格式（平台与本地审图程序共用）：清单 review.json、Windows 安全的路径、审图标注
│       ├── reviewer/            # 本地审图程序的本机服务：打开与解压审图包、审核进度、导出审图结果、访问控制
│       ├── compute/             # 计算资源：训练节点注册、心跳、在线状态、指标历史
│       ├── hostinfo/            # 平台服务器自身的 CPU、内存、负载与 NVIDIA 显卡（/proc、/sys）
│       ├── httpapi/             # 路由、中间件、统一响应与错误、handler
│       └── web/                 # 前端静态文件服务（SPA 回退、缓存策略）
├── frontend/                    # React + Vite + shadcn/ui
│   └── src/
│       ├── config/navigation.ts # 导航与模块的唯一配置源
│       ├── features/            # 各功能模块页面（每个模块一个页面文件）
│       ├── reviewer/            # 本地审图程序的页面（入口 reviewer.html，vite.reviewer.config.ts 单独构建）
│       ├── components/ui/       # shadcn/ui 组件
│       └── ...
├── trainer/                     # 训练节点（Python）：硬件与 CUDA 环境检测、自检、注册与心跳、执行平台的任务
│   ├── trainmaster_trainer/     # 节点程序；tasks.py 领取任务，model_export.py 执行 pt → ONNX 转换，review_inference.py 执行审图识别，torch_variants.txt 为 PyTorch 变体表
│   ├── scripts/                 # install.sh（Linux 裸机安装）、install.ps1（Windows）、install-torch.sh、lib/torch-variant.sh（选择变体）
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
2. **数据集**（进行中）：已完成压缩包 / 文件夹导入、VOC 标注识别、已有目录添加、缩略图浏览与标注预览、项目分组与项目类别列表（项目管理）；后续：图片元数据（产线、工位、相机、批次）、质量检查（重复、损坏、分辨率）。
3. **标注**（进行中）：已完成标注工作台（矩形框、按项目类别列表提示类别、快捷键、自动保存为 VOC）；后续：标签体系（跨项目的缺陷类别、层级与别名）、标注任务分配与审核、智能预标注。
4. **数据版本**：训练/验证/测试划分、版本快照、导出 YOLO / VOC / COCO。
5. **训练**（进行中）：已完成训练节点（自动识别显卡与 CUDA 环境、自动选择 PyTorch 版本、注册与监控、Docker 显卡配置）；后续：训练任务队列与调度、实时日志与曲线。
6. **评估与模型**（进行中）：已完成模型仓库（上传 .pt、按项目迭代版本、下载 PT 或由训练节点自动转换的 ONNX）、模型审图（训练节点识别数据集，网页或 Windows 本地审图，合并为数据集的审图版本）；后续：训练产出的模型自动入库、溯源、TensorRT 导出、发布、推理测试，以及评估指标与报告、预标注。
7. **成员与权限**：账号登录、角色权限、操作审计。

## 文档

- [架构说明](docs/architecture.md)：组件、模块划分、API 约定、存储布局、迁移约定、训练节点与任务协议、模型仓库、模型审图
- [部署指南](docs/deployment.md)：环境变量、权限、升级、备份恢复、镜像加速、Windows、GPU 训练节点、模型转换与审图识别、本地审图程序、反向代理
- [开发指南](docs/development.md)：本地开发、训练节点的开发与测试、本地审图程序、添加 API 与页面、添加 shadcn 组件、代码规范
