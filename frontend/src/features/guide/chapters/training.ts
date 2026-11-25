import computeResourcesImg from "@/features/guide/assets/compute-resources.webp"
import trainingJobDetailImg from "@/features/guide/assets/training-job-detail.webp"
import trainingJobNewImg from "@/features/guide/assets/training-job-new.webp"
import trainingJobsListImg from "@/features/guide/assets/training-jobs-list.webp"
import trainingTemplatesImg from "@/features/guide/assets/training-templates.webp"
import type { GuideChapter } from "@/features/guide/types"

export const trainingChapters: GuideChapter[] = [
  {
    id: "training-jobs",
    groupId: "training",
    title: "训练任务与实时监控",
    shortTitle: "训练任务",
    summary:
      "基于冻结数据版本发起 Ultralytics YOLO 目标检测训练（支持 YOLOv8 / v9 / v10 / 11 / 12 / 26），开放全部 86 项官方参数编辑，实时追踪 Loss / mAP 曲线与增量日志并一键入库模型。",
    moduleId: "training-jobs",
    targetPath: "/training/jobs",
    targetLabel: "训练任务",
    keywords: [
      "训练任务",
      "YOLO",
      "YOLO26",
      "从头训练",
      "预训练微调",
      "断点续训",
      "Loss",
      "mAP",
      "曲线",
      "日志",
      "args.yaml",
      "模型入库",
    ],
    sections: [
      {
        id: "training-list",
        title: "训练任务列表与筛选",
        figures: [
          {
            id: "fig-training-jobs-list",
            src: trainingJobsListImg,
            alt: "训练任务列表页面截图",
            caption:
              "训练任务列表：多维筛选、新建训练入口与最佳指标/产出模型一览",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "新建训练任务按钮",
                description:
                  "打开训练配置页，选择项目数据版本、初始化模式与参数模板发起新任务。",
                x: 88.6,
                y: 10.9,
                box: { x: 89.1, y: 8.8, width: 9.3, height: 4.2 },
              },
              {
                index: 2,
                label: "按项目、状态、YOLO 版本与关键词筛选",
                description:
                  "快速定位排队中、运行中、已完成或失败的训练实验，支持按模板名和备注搜索。",
                x: 20.2,
                y: 21.8,
                box: { x: 19.4, y: 19.8, width: 62.5, height: 4.4 },
              },
              {
                index: 3,
                label: "任务进度、最佳 mAP 与产出模型关联",
                description:
                  "直接查看所用数据版本、参数模板、最佳 mAP50 / mAP50-95，点击「模型 v2」胶囊直达模型仓库详情，点行进入实时监控页。",
                x: 20.2,
                y: 34.4,
                box: { x: 19.5, y: 31.2, width: 78.8, height: 6.4 },
              },
            ],
          },
        ],
      },
      {
        id: "training-create",
        title: "新建训练任务与全量 86 项参数编辑",
        summary:
          "打开新建页时若设置了默认参数模板会自动预填；所有参数分为 12 项高频常用参数与可折叠的 74 项高级选项，并支持搜索与实时预览 args.yaml。",
        figures: [
          {
            id: "fig-training-job-new",
            src: trainingJobNewImg,
            alt: "新建训练任务页面截图",
            caption:
              "新建训练任务：选择数据版本、3 种权重初始化模式与底部吸顶提交栏",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "选择所属项目与冻结数据版本",
                description:
                  "展示该数据版本的划分张数、所在存储节点、训练机拷贝策略（本机零拷贝 / 远程流式拉取）及完整 YOLO 类别编号。",
                x: 20.2,
                y: 24.5,
                box: { x: 19.4, y: 22.2, width: 78.9, height: 44.3 },
              },
              {
                index: 2,
                label: "三种模型初始化方式",
                description:
                  "「从头训练（scratch）」离线按内置 YAML 随机初始化；「预训练微调（finetune）」从模型仓库选用 .pt 迁移微调（支持跨项目）；「断点续训（resume）」从含优化器状态的 last.pt 检查点续跑。",
                x: 20.2,
                y: 71.2,
                box: { x: 19.4, y: 69.2, width: 78.9, height: 22.4 },
              },
              {
                index: 3,
                label: "底部吸顶配置摘要与「开始训练」按钮",
                description:
                  "无论滚动到多深的高级参数区，底部始终摘要展示当前架构、已加载的参数模板、epochs/batch/imgsz 及已自定义项数，随时可一键提交排队。",
                x: 89.2,
                y: 95.2,
                box: { x: 19.4, y: 92.2, width: 78.9, height: 6.2 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "一键加载或微调参数模板",
            description:
              "在页面下半部分的「训练参数」区选择任意内置或自定义模板后，可按需微调单一项；支持点击「只看已修改」、搜索参数键名/中文名、分组恢复官方默认，或将微调结果直接「另存为新模板」。",
          },
          {
            title: "选择自动入库策略",
            description:
              "默认开启「训练完成后自动将 best.pt 注册到模型仓库」，训练结束即自动生成项目的新模型版本；关闭后也可在任务详情页手动注册 best.pt 或 last.pt。",
          },
        ],
      },
      {
        id: "training-monitor",
        title: "实时曲线监控、增量日志与一键评估",
        figures: [
          {
            id: "fig-training-job-detail",
            src: trainingJobDetailImg,
            alt: "训练任务详情页面截图，显示 mAP 与 Loss 曲线及快捷操作按钮",
            caption:
              "训练任务详情：入库模型/评估快捷入口、4 大详情标签与交互训练曲线",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "产出模型、评估与参数复用操作栏",
                description:
                  "训练完成后可直接「查看入库模型」「评估产出模型」或「复用参数新建」（完整克隆全部 86 项参数做消融实验）；运行中此处显示「停止训练」。",
                x: 65.8,
                y: 15.0,
                box: { x: 66.2, y: 13.0, width: 32.0, height: 4.0 },
              },
              {
                index: 2,
                label: "监控与曲线 / 训练日志 / 评估图表与产物 / 训练参数",
                description:
                  "「训练日志」按字节增量自动滚动追踪 train.log；「评估图表与产物」提供 PR/混淆矩阵预览图与 best.pt / results.csv / args.yaml 下载；「训练参数」展示全部参数并与官方默认对照。",
                x: 53.0,
                y: 34.2,
                box: { x: 19.4, y: 32.2, width: 32.9, height: 4.1 },
              },
              {
                index: 3,
                label: "核心精度与损失指标卡",
                description:
                  "实时展示最佳 mAP50、最佳 mAP50-95、精确率与召回率、最新 Box/Cls/L1（或 DFL）Loss 及已完成轮次。",
                x: 20.2,
                y: 40.0,
                box: { x: 19.4, y: 38.6, width: 78.9, height: 14.8 },
              },
              {
                index: 4,
                label: "交互式 mAP 与 Loss 曲线图",
                description:
                  "鼠标悬停曲线可查看每一轮的精确数值；点击顶部彩色图例胶囊可单独显示或隐藏某条曲线（支持继续向下滚动查看学习率曲线与逐轮明细表）。",
                x: 20.2,
                y: 57.5,
                box: { x: 19.4, y: 55.4, width: 78.9, height: 40.2 },
              },
            ],
          },
        ],
        tips: [
          {
            variant: "tip",
            title: "从历史优秀任务沉淀为团队模板",
            content:
              "如果某次训练收敛效果特别理想，可以在任务详情右上角「…」菜单中直接点击「保存为参数模板」，下次团队成员新建训练时即可一键复用。",
          },
        ],
      },
    ],
  },
  {
    id: "training-templates",
    groupId: "training",
    title: "参数模板管理",
    shortTitle: "参数模板",
    summary:
      "集中管理内置工业视觉 YOLO 参数预设与团队自定义模板，支持指定全局「默认模板」在新任务中自动预填，减少重复调参失误。",
    moduleId: "training-templates",
    targetPath: "/training/templates",
    targetLabel: "参数模板",
    keywords: [
      "参数模板",
      "预设",
      "默认模板",
      "高分辨率微小缺陷",
      "通用工业检测基准",
      "快速验证",
      "复制模板",
    ],
    sections: [
      {
        id: "templates-manage",
        title: "浏览内置模板、自定义模板与设为默认",
        figures: [
          {
            id: "fig-training-templates",
            src: trainingTemplatesImg,
            alt: "训练参数模板页面截图",
            caption: "参数模板管理：系统内置预设、自定义默认模板与一键发起训练",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "新建参数模板按钮",
                description:
                  "打开独立模板表单页，配置 12 项常用参数与 74 项高级参数并保存为团队共享模板。",
                x: 88.6,
                y: 10.9,
                box: { x: 89.1, y: 8.8, width: 9.3, height: 4.2 },
              },
              {
                index: 2,
                label: "搜索与来源筛选（全部 / 自定义 / 系统内置）",
                description:
                  "快速区分平台内置的 6 套工业视觉最佳实践模板（如「快速验证 / 试跑」「高分辨率微小缺陷」「方向与色差敏感检测」等）与团队自定义模板。",
                x: 65.2,
                y: 22.0,
                box: { x: 46.9, y: 19.9, width: 17.6, height: 4.2 },
              },
              {
                index: 3,
                label: "模板摘要卡片与默认模板标识",
                description:
                  "直观列出核心 6 项锚点参数（epochs / batch / imgsz / optimizer / lr0 / mosaic）及偏离官方默认的高级项；带星标「默认模板」的预设会在新建训练任务时自动预填。",
                x: 20.2,
                y: 28.5,
                box: { x: 19.4, y: 26.6, width: 38.9, height: 33.1 },
              },
              {
                index: 4,
                label: "用此模板训练 / 复制 / 查看参数",
                description:
                  "系统内置模板为只读预设，可点击「复制」生成可编辑的团队副本；点击「用此模板训练」直接带入该配置进入新建训练页，右上角「…」可设为或取消默认模板。",
                x: 52.5,
                y: 55.6,
                box: { x: 42.2, y: 53.8, width: 14.8, height: 4.4 },
              },
            ],
          },
        ],
        tips: [
          {
            variant: "tip",
            title: "首次跑通新数据推荐先用「快速验证 / 试跑」",
            content:
              "新建数据版本后建议先用内置「快速验证 / 试跑（30 轮）」模板快速验证标注质量、类别顺序与显存占用，确认无异常后再切换为「通用工业检测基准」或「高分辨率微小缺陷」进行完整训练。",
          },
        ],
      },
    ],
  },
  {
    id: "compute",
    groupId: "training",
    title: "计算资源与节点接入",
    shortTitle: "计算资源",
    summary:
      "集中监控所有已接入训练节点的 GPU 型号、显存、利用率、温度、功率、CUDA/PyTorch/Ultralytics 环境健康度以及平台服务器状态，并提供一键复制的节点接入命令。",
    moduleId: "compute",
    targetPath: "/training/compute",
    targetLabel: "计算资源",
    keywords: [
      "计算资源",
      "训练节点",
      "GPU",
      "显卡",
      "CUDA",
      "PyTorch",
      "接入节点",
      "Windows",
      "Docker",
      "自检",
      "重新检测",
    ],
    sections: [
      {
        id: "compute-monitor",
        title: "实时算力监控与环境自检",
        summary:
          "训练节点每 10 秒上报一次心跳，页面每 5 秒自动刷新。训练任务、模型审图识别、模型评估推理与 ONNX 转换都由这里的在线节点自动领取执行。",
        figures: [
          {
            id: "fig-compute-resources",
            src: computeResourcesImg,
            alt: "计算资源监控页面截图，显示在线双卡 RTX 4090 训练节点与平台服务器状态",
            caption:
              "计算资源页：全局算力统计、平台服务器信息、在线 GPU 节点卡片与接入按钮",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "接入节点按钮",
                description:
                  "弹出节点接入指引对话框，自动填充当前平台地址，一键复制 4 种部署方式命令（本机 Docker / 远程 Linux GPU 机 Docker / Linux systemd / Windows 管理员 PowerShell）。",
                x: 90.5,
                y: 10.9,
                box: { x: 91.1, y: 8.8, width: 7.3, height: 4.2 },
              },
              {
                index: 2,
                label: "在线算力汇总统计卡",
                description:
                  "一眼掌握当前在线节点数、可用 GPU 总卡数、总显存与已用显存占比，以及 CUDA 环境就绪节点数。",
                x: 20.2,
                y: 19.6,
                box: { x: 19.4, y: 17.8, width: 78.9, height: 16.0 },
              },
              {
                index: 3,
                label: "平台服务器状态",
                description:
                  "显示运行 Web 平台后端所在机器的 CPU、内存、负载、系统内核及本机显卡检测概况。",
                x: 20.2,
                y: 37.5,
                box: { x: 19.4, y: 35.8, width: 78.9, height: 21.8 },
              },
              {
                index: 4,
                label: "训练节点详情卡片",
                description:
                  "显示节点在线状态、CUDA 就绪徽章、PyTorch/CUDA/Ultralytics/驱动版本、CPU/内存/数据盘与 /dev/shm，下方列出每块 GPU 的实时显存、利用率、温度与功率；右上角「…」支持查看完整详情、触发「重新检测」或移除退役节点。",
                x: 20.2,
                y: 68.5,
                box: { x: 19.4, y: 66.8, width: 78.9, height: 32.2 },
              },
            ],
          },
        ],
        table: {
          caption:
            "接入新训练节点的 4 种常见方式（点击右上角「接入节点」即可复制对应命令）",
          headers: ["节点类型", "适用场景与操作方式"],
          rows: [
            [
              "平台本机（Docker）",
              "平台服务器自带 NVIDIA 显卡时，运行 make up（或加 --install-toolkit 自动配置容器显卡）即可随平台拉起 GPU 节点。",
            ],
            [
              "其它 Linux GPU 机器（Docker）",
              "克隆仓库后运行 make setup 指定平台地址与 TM_NODE_TOKEN，再单独启动 trainer 容器。",
            ],
            [
              "Linux 宿主机直装（无 Docker）",
              "运行 sudo trainer/scripts/install.sh --server-url ... --token ... --systemd，自动配置独立虚拟环境与开机自启服务。",
            ],
            [
              "Windows 10/11 / Server 电脑",
              "复制「接入节点」中的 PowerShell 命令在管理员终端运行：自动从平台下载脚本、匹配安装对应 CUDA 版本的 PyTorch 并注册计划任务。",
            ],
          ],
        },
        tips: [
          {
            variant: "warning",
            title: "CPU 节点与 GPU 节点的分工",
            content:
              "无 NVIDIA 显卡的机器同样可以接入为 CPU 节点，用来执行 ONNX 模型转换或小批量模型审图；大规模模型训练与评估推荐接入带 NVIDIA GPU 且「CUDA 就绪」的节点。",
          },
        ],
      },
    ],
  },
]
