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
  Gauge,
  GitBranch,
  HardDrive,
  LayoutDashboard,
  ListChecks,
  PenTool,
  Rocket,
  ScanSearch,
  Server,
  Settings,
  Sparkles,
  Tags,
  Users,
  type LucideIcon,
} from "lucide-react"

export type ModuleId =
  | "dashboard"
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
  | "inference"
  | "members"
  | "storage"
  | "settings"

export type NavItem = {
  id: ModuleId
  title: string
  path: string
  icon: LucideIcon
  description: string
  /** ready：已有真实页面；planned：功能占位页。 */
  status: "ready" | "planned"
  /** 规划中的功能，展示在占位页上。 */
  features: string[]
  /** 后端为该模块预留的 API 路径（当前返回 501）。 */
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
    id: "data",
    title: "数据管理",
    items: [
      {
        id: "datasets",
        title: "数据集",
        path: "/datasets",
        icon: Database,
        description: "集中管理产线采集的工业图片，按项目组织数据集。",
        status: "planned",
        apiPath: "/api/v1/datasets",
        features: [
          "创建数据集与项目分组",
          "从本地目录批量导入或上传图片",
          "缩略图浏览、筛选与检索",
          "图片元数据：产线、工位、相机、批次",
          "数据统计与质量检查：重复、损坏、分辨率",
        ],
      },
      {
        id: "dataset-versions",
        title: "数据版本",
        path: "/datasets/versions",
        icon: GitBranch,
        description: "冻结数据集快照，划分训练集并导出为训练格式。",
        status: "planned",
        features: [
          "训练 / 验证 / 测试集划分",
          "版本快照与可追溯",
          "导出 YOLO / VOC / COCO 格式",
          "数据增强配置",
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
        description: "面向缺陷检测场景的高效在线标注工具。",
        status: "planned",
        features: [
          "矩形框、多边形、关键点标注",
          "快捷键与批量操作",
          "缺陷类别快速切换",
          "标注历史与撤销",
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
        description: "基于数据版本发起训练，实时查看训练过程。",
        status: "planned",
        apiPath: "/api/v1/training/jobs",
        features: [
          "选择数据版本、算法与超参数创建任务",
          "排队与调度",
          "实时日志与损失曲线",
          "中断、恢复与重跑",
        ],
      },
      {
        id: "compute",
        title: "计算资源",
        path: "/training/compute",
        icon: Server,
        description: "管理团队的 GPU 训练节点与任务队列。",
        status: "planned",
        features: ["GPU 训练节点注册", "显存 / 利用率监控", "任务队列管理"],
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
        description: "统一存放训练产出的模型，记录来源并管理发布。",
        status: "planned",
        apiPath: "/api/v1/models",
        features: [
          "模型版本管理",
          "数据版本与训练任务溯源",
          "导出 ONNX / TensorRT",
          "发布状态管理",
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
