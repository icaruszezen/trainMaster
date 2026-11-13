/**
 * 平台导航与功能模块的唯一配置源。
 *
 * 侧边栏、路由表、面包屑和功能占位页的文案都从这里读取。
 * 新增模块：在 ModuleId 中加入 id → 在下方分组中加一项 → 在
 * src/features/ 下新建页面文件 → 在 src/router.tsx 的 pages 中登记。
 */
import {
  Boxes,
  Database,
  FolderKanban,
  Gauge,
  HardDrive,
  Layers,
  LayoutDashboard,
  ListChecks,
  PenTool,
  Rocket,
  ScanEye,
  ScanSearch,
  Server,
  Settings,
  Sparkles,
  Tags,
  Trash2,
  Users,
  type LucideIcon,
} from "lucide-react"

export type ModuleId =
  | "dashboard"
  | "projects"
  | "datasets"
  | "dataset-versions"
  | "annotation-tasks"
  | "annotation-workspace"
  | "labels"
  | "prelabel"
  | "training-jobs"
  | "compute"
  | "evaluations"
  | "models"
  | "reviews"
  | "inference"
  | "members"
  | "storage"
  | "recycle-bin"
  | "settings"

export type NavItem = {
  id: ModuleId
  title: string
  path: string
  icon: LucideIcon
  description: string
  /** ready：已有真实页面；planned：功能占位页。 */
  status: "ready" | "planned"
  /** 规划中的模块：展示在占位页上的规划功能；已上线的模块：已有的主要能力。 */
  features: string[]
  /** 该模块的后端 API 路径前缀（规划中的模块返回 501）。 */
  apiPath?: string
}

export type NavGroup = {
  id: string
  title: string
  items: NavItem[]
}

export const navGroups: NavGroup[] = [
  {
    id: "overview",
    title: "概览",
    items: [
      {
        id: "dashboard",
        title: "工作台",
        path: "/",
        icon: LayoutDashboard,
        description: "平台概况、系统状态与常用流程入口。",
        status: "ready",
        features: [],
      },
    ],
  },
  {
    id: "project",
    title: "项目管理",
    items: [
      {
        id: "projects",
        title: "项目",
        path: "/projects",
        icon: FolderKanban,
        description:
          "按检测任务把数据集组织成项目，集中查看数据、标注与模型的进展。",
        status: "ready",
        apiPath: "/api/v1/projects",
        features: [
          "新建、修改、删除项目",
          "数据集可以纳入一个或多个项目，数据集页面可按项目筛选",
          "项目详情：所含数据集、图片与标注进度汇总",
          "项目的类别列表（训练时的 YOLO 类别顺序）与合计的标签分布",
          "项目的数据版本（冻结的训练数据）、训练任务与模型的版本迭代，可直接生成新数据版本、发起训练或上传新模型",
        ],
      },
    ],
  },
  {
    id: "data",
    title: "数据管理",
    items: [
      {
        id: "datasets",
        title: "数据集",
        path: "/datasets",
        icon: Database,
        description:
          "上传图片压缩包或文件夹、添加服务器上已有的目录，浏览图片与 VOC 标注。",
        status: "ready",
        apiPath: "/api/v1/datasets",
        features: [
          "上传压缩包（zip / tar / 7z / rar 等）或整个文件夹导入图片",
          "自动识别与图片同目录、同名的 VOC 标注",
          "把服务器数据目录下已有的文件夹添加为数据集",
          "缩略图浏览，按标注状态、标签筛选，查看标注框",
          "标签分布统计与导入记录",
          "审图版本（审图1.0、1.1…）：查看、修改备注、删除；下载时选择基础版本与审图版本叠加导出",
        ],
      },
      {
        id: "reviews",
        title: "模型审图",
        path: "/datasets/reviews",
        icon: ScanEye,
        description:
          "用模型识别一批数据集，逐张确认识别结果：标为已审核（识别正确）或需加样（参与后续训练），审图标注可以合并为数据集的审图版本。",
        status: "ready",
        apiPath: "/api/v1/reviews",
        features: [
          "选择模型与一个或多个数据集创建审图任务，由训练节点自动识别并生成只属于这次审图的 VOC 标注",
          "网页审图：S 已审核、X 需加样、U 待审核，标记后自动跳到下一张；框可以像标注工作台一样修改",
          "按审核状态、数据集、类别、已修改、无框、识别失败与文件名筛选图片",
          "下载审图包，用 Windows 本地审图程序离线审图后上传结果",
          "合并到数据集时生成审图版本（审图1.0、1.1…）并填写备注，不修改数据集的标注；下载数据集时可选版本叠加导出",
        ],
      },
      {
        id: "dataset-versions",
        title: "数据版本",
        path: "/datasets/versions",
        icon: Layers,
        description:
          "从项目的数据集生成冻结的训练数据：划分训练 / 验证 / 测试集，导出 YOLO / VOC / COCO。",
        status: "ready",
        apiPath: "/api/v1/data-versions",
        features: [
          "从项目的数据集生成数据版本（v1、v2…）：每个数据集选择基础标注与审图版本作为标注来源，冻结项目的类别列表",
          "按比例与固定随机种子划分训练 / 验证 / 测试集，可沿用 train / val 文件夹，同名图片总在同一划分中",
          "生成前预览各划分与各类别的数量、跳过的图片与原因，并与上一个数据版本对比",
          "可选的离线数据增强：翻转、旋转、亮度对比度、模糊与噪声",
          "生成后不再变化，每张图片记录来源；下载 YOLO / VOC / COCO 压缩包，删除后可在回收站中恢复",
        ],
      },
    ],
  },
  {
    id: "annotation",
    title: "标注",
    items: [
      {
        id: "annotation-tasks",
        title: "标注任务",
        path: "/annotation/tasks",
        icon: ListChecks,
        description: "按批次分发标注工作，跟踪进度并把控标注质量。",
        status: "planned",
        apiPath: "/api/v1/annotation/tasks",
        features: [
          "按批次创建标注任务并分配成员",
          "标注进度跟踪",
          "审核与返修流程",
          "标注质量抽检",
        ],
      },
      {
        id: "annotation-workspace",
        title: "标注工作台",
        path: "/annotation/workspace",
        icon: PenTool,
        description:
          "面向缺陷检测的在线矩形框标注：按固定顺序逐张标注，直接保存为数据集文件夹中的 VOC 文件。",
        status: "ready",
        apiPath: "/api/v1/datasets",
        features: [
          "矩形框：拖动或两次单击画框，拖动手柄调整，方向键逐像素微调",
          "画框后按项目的类别列表与数据集已有的标签提示类别，可搜索、可输入新类别",
          "LabelImg 风格快捷键，撤销 / 重做，复制粘贴框、沿用上一张",
          "滚轮 / 双指缩放到 32 倍，亮度、对比度调节",
          "自动保存为 VOC（.xml），多人同时修改时提示冲突",
        ],
      },
      {
        id: "labels",
        title: "标签体系",
        path: "/annotation/labels",
        icon: Tags,
        description: "统一维护缺陷与目标类别，保证跨项目标注口径一致。",
        status: "planned",
        apiPath: "/api/v1/annotation/labels",
        features: [
          "缺陷 / 目标类别定义",
          "类别颜色与快捷键",
          "类别层级与别名",
          "跨数据集复用",
        ],
      },
      {
        id: "prelabel",
        title: "智能预标注",
        path: "/annotation/prelabel",
        icon: Sparkles,
        description:
          "借助已有模型与视觉大模型自动生成候选标注，减少人工工作量。",
        status: "planned",
        features: [
          "使用已有模型批量预标注",
          "接入视觉大模型 API",
          "模板匹配",
          "预标注结果人工确认",
        ],
      },
    ],
  },
  {
    id: "training",
    title: "模型训练",
    items: [
      {
        id: "training-jobs",
        title: "训练任务",
        path: "/training/jobs",
        icon: Rocket,
        description:
          "基于数据版本发起 Ultralytics YOLO 训练：支持从头训练、预训练微调与断点续训，开放全部官方参数可编辑，实时查看曲线与日志并一键入库模型。",
        status: "ready",
        apiPath: "/api/v1/training/jobs",
        features: [
          "选择项目冻结的数据版本，支持从头训练（YOLOv8 / v9 / v10 / 11 / 12 / 26 各规模）、预训练权重微调与检查点断点续训",
          "开放 Ultralytics YOLO 全量训练参数可编辑，默认按官方基准参数（cfg/default.yaml）预填，支持搜索、分组重置与导出 args.yaml",
          "任务排队与在线训练节点自动调度，支持同机共享目录零拷贝训练与远程自动拉取数据",
          "实时轮次进度、损失与 mAP 曲线、增量训练日志流与评估图表（混淆矩阵、PR 曲线、批次检测预览）",
          "随时取消停止、失败或取消后重跑、复用参数克隆任务，训练最佳权重自动或手动注册到项目模型仓库",
        ],
      },
      {
        id: "compute",
        title: "计算资源",
        path: "/training/compute",
        icon: Server,
        description:
          "训练节点自动上报显卡、CPU 与 CUDA 环境，集中查看各节点的资源与实时负载。",
        status: "ready",
        apiPath: "/api/v1/compute",
        features: [
          "训练节点自动注册与心跳，显示在线 / 离线状态",
          "自动识别 GPU、CPU、内存与磁盘，实时显示利用率、显存、温度与功率",
          "检测 PyTorch / CUDA / Ultralytics 环境并运行 YOLO 自检，给出修复建议",
          "平台服务器自身的 CPU、内存与显卡信息",
          "一键重新检测节点环境、删除不再使用的节点",
        ],
      },
    ],
  },
  {
    id: "evaluation",
    title: "模型评估",
    items: [
      {
        id: "evaluations",
        title: "模型评估",
        path: "/evaluation",
        icon: Gauge,
        description: "在验证集上评估模型效果，定位漏检与误检。",
        status: "planned",
        apiPath: "/api/v1/evaluations",
        features: [
          "mAP / Precision / Recall / F1 指标",
          "混淆矩阵与 PR 曲线",
          "漏检、误检样本浏览",
          "多模型对比与报告导出",
        ],
      },
    ],
  },
  {
    id: "models",
    title: "模型管理",
    items: [
      {
        id: "models",
        title: "模型仓库",
        path: "/models",
        icon: Boxes,
        description:
          "按项目保存 YOLO 模型并迭代版本：上传训练好的 .pt 文件，下载时可选 PT 或自动转换的 ONNX。",
        status: "ready",
        apiPath: "/api/v1/models",
        features: [
          "上传 .pt 模型（YOLOv8 / v9 / v10 / 11 / 12 / 26），选择所属项目与 YOLO 版本",
          "每个项目内按 v1、v2… 递增版本号，断点续传，校验文件并识别任务、类别与训练指标",
          "下载 PT，或由训练节点自动转换为 ONNX（输入尺寸、动态尺寸、NMS / 端到端、opset），结果缓存复用",
          "模型详情：训练信息、mAP 等指标、类别列表、SHA256 与转换记录",
          "为平台训练预留：训练任务产出的模型也保存在这里",
        ],
      },
      {
        id: "inference",
        title: "推理测试",
        path: "/models/inference",
        icon: ScanSearch,
        description: "上传图片快速验证模型的实际检测效果。",
        status: "planned",
        features: ["上传图片在线推理", "检测结果可视化", "置信度阈值调节"],
      },
    ],
  },
  {
    id: "system",
    title: "系统管理",
    items: [
      {
        id: "members",
        title: "成员与权限",
        path: "/system/members",
        icon: Users,
        description: "管理团队成员账号与角色权限。",
        status: "planned",
        features: [
          "成员账号管理",
          "角色与权限：管理员、标注员、算法工程师",
          "操作审计",
        ],
      },
      {
        id: "storage",
        title: "存储管理",
        path: "/system/storage",
        icon: HardDrive,
        description: "查看平台数据目录与磁盘容量，规划备份与清理。",
        status: "planned",
        features: [
          "存储目录与容量监控",
          "数据备份与清理",
          "按数据集统计空间占用",
        ],
      },
      {
        id: "recycle-bin",
        title: "回收站",
        path: "/system/recycle-bin",
        icon: Trash2,
        description:
          "删除的数据集、审图版本、数据版本与模型保留 7 天，期间可以恢复，到期后自动永久删除。",
        status: "ready",
        apiPath: "/api/v1/recycle-bin",
        features: [
          "删除的数据集、审图版本、数据版本与模型（含随项目删除的数据版本与模型）强制保留 7 天",
          "7 天内一键恢复：数据集回到原来的项目与审图任务，数据版本与模型连同项目一起恢复",
          "名称已被占用时换名恢复",
          "到期自动永久删除，不能提前删除",
        ],
      },
      {
        id: "settings",
        title: "系统设置",
        path: "/system/settings",
        icon: Settings,
        description: "平台级的基础配置与默认参数。",
        status: "planned",
        features: ["平台基础配置", "默认训练参数", "视觉大模型 API 配置"],
      },
    ],
  },
]

export const navItems: NavItem[] = navGroups.flatMap((group) => group.items)

export function getNavItem(id: ModuleId): NavItem {
  const item = navItems.find((candidate) => candidate.id === id)
  if (!item) {
    throw new Error(`未知的模块：${id}`)
  }
  return item
}

/** 判断 pathname 是否位于 path 之下（按路径段匹配，"/" 只匹配首页）。 */
function isUnderPath(pathname: string, path: string) {
  if (path === "/") {
    return pathname === "/"
  }
  return pathname === path || pathname.startsWith(`${path}/`)
}

export type ActiveNav = {
  group: NavGroup
  item: NavItem
  /** 当前路径恰好是该导航项本身（而不是它的子页面）。 */
  exact: boolean
}

/**
 * 按“最长前缀”找到当前路径对应的导航项：
 * `/datasets/versions` → 数据版本；`/datasets/123` → 数据集。
 */
export function findActiveNavItem(pathname: string): ActiveNav | undefined {
  const normalized = pathname.replace(/\/+$/, "") || "/"

  let best: ActiveNav | undefined
  for (const group of navGroups) {
    for (const item of group.items) {
      if (
        isUnderPath(normalized, item.path) &&
        (!best || item.path.length > best.item.path.length)
      ) {
        best = { group, item, exact: normalized === item.path }
      }
    }
  }
  return best
}
