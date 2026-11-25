import dashboardOverviewImg from "@/features/guide/assets/dashboard-overview.webp"
import datasetDetailImg from "@/features/guide/assets/dataset-detail.webp"
import datasetImportDialogImg from "@/features/guide/assets/dataset-import-dialog.webp"
import datasetsListImg from "@/features/guide/assets/datasets-list.webp"
import projectDetailImg from "@/features/guide/assets/project-detail.webp"
import projectsListImg from "@/features/guide/assets/projects-list.webp"
import type { GuideChapter } from "@/features/guide/types"

export const quickstartAndDataChapters: GuideChapter[] = [
  {
    id: "quickstart",
    groupId: "quickstart",
    title: "快速上手与工作台",
    shortTitle: "快速上手与工作台",
    summary:
      "了解 trainMaster 工业视觉平台的整体界面布局、从数据导入到模型发布的 6 步闭环，以及随时呼出本操作指南的快捷入口。",
    moduleId: "dashboard",
    targetPath: "/",
    targetLabel: "工作台",
    keywords: [
      "首页",
      "工作台",
      "快速入门",
      "流程",
      "侧边栏",
      "主题",
      "浅色",
      "深色",
      "帮助按钮",
    ],
    sections: [
      {
        id: "dashboard-layout",
        title: "工作台界面导览",
        summary:
          "打开平台默认进入「工作台」，一屏汇总当前团队的项目、数据集、图片标注进度、训练与评估状态，以及服务器与 GPU 节点在线概况。",
        figures: [
          {
            id: "fig-dashboard-overview",
            src: dashboardOverviewImg,
            alt: "trainMaster 工作台主界面截图",
            caption: "工作台总览：侧边导航、资产统计、系统状态与平台流程入口",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "左侧功能导航栏",
                description:
                  "按「概览 / 项目管理 / 数据管理 / 标注 / 模型训练 / 模型评估 / 模型管理 / 系统管理」分组切换页面；点击顶栏左上角图标可折叠为窄图标栏。",
                x: 17.2,
                y: 22.0,
                box: { x: 0.4, y: 7.5, width: 17.0, height: 84.0 },
              },
              {
                index: 2,
                label: "核心资产统计卡片",
                description:
                  "实时展示项目、数据集、图片总数与已标注占比、模型、训练任务及模型评估数量，点击卡片底部链接可直达对应列表或新建页。",
                x: 20.0,
                y: 18.2,
                box: { x: 19.3, y: 17.5, width: 79.1, height: 33.2 },
              },
              {
                index: 3,
                label: "系统状态与在线算力摘要",
                description:
                  "查看平台版本、运行时长、PostgreSQL 连接状态、存储磁盘余量以及当前在线训练节点与 GPU 卡数，点击计算资源卡片可查看显卡负载。",
                x: 20.2,
                y: 53.5,
                box: { x: 19.4, y: 52.8, width: 25.6, height: 43.2 },
              },
              {
                index: 4,
                label: "平台流程 6 步入口",
                description:
                  "按照「数据导入 → 标注 → 数据版本 → 模型训练 → 模型评估 → 模型发布」排布，新手可直接按顺序点击卡片进入对应页面。",
                x: 47.0,
                y: 53.5,
                box: { x: 46.1, y: 52.8, width: 52.2, height: 43.0 },
              },
              {
                index: 5,
                label: "快捷帮助（?）与明暗主题切换",
                description:
                  "在任意业务页面点击顶栏右上角「?」按钮，都会直接跳转到该模块对应的操作指南章节；右侧图标可切换浅色 / 深色 / 跟随系统主题。",
                x: 93.4,
                y: 3.1,
                box: { x: 93.8, y: 0.9, width: 5.2, height: 4.4 },
              },
            ],
          },
        ],
      },
      {
        id: "first-time-steps",
        title: "新手完成首轮训练的最短操作路径",
        steps: [
          {
            title: "1. 创建项目并配置 YOLO 类别列表",
            description:
              "进入「项目管理 → 项目」新建检测项目，在项目详情的「类别」标签中按顺序添加需要检测的缺陷类别（如 solder_bridge、missing_solder）。该顺序会作为训练和导出 YOLO 时的类别编号。",
          },
          {
            title: "2. 导入图片与已有标注，并纳入项目",
            description:
              "进入「数据管理 → 数据集」新建数据集，拖拽上传图片压缩包或文件夹（支持 VOC / X-AnyLabeling / YOLO 自动转换），勾选纳入刚才创建的项目。",
          },
          {
            title: "3. 在线画框标注或用旧模型审图加样",
            description:
              "在数据集详情页点击「标注」进入标注工作台逐张画框（自动保存为 VOC）；若已有上一代模型，也可通过「模型审图」先批量推理再人工复核并合并为审图版本。",
          },
          {
            title: "4. 冻结数据版本",
            description:
              "在「数据版本」页面选择项目与数据集来源，按比例划分训练 / 验证 / 测试集并生成冻结版本（如数据版本 v1）。生成后不再随日常标注重改，保证实验可复现。",
          },
          {
            title: "5. 发起训练并实时评估、导出部署",
            description:
              "在「训练任务」中选择冻结的数据版本与参数模板启动 YOLO 训练；训练产出入库后，前往「模型评估」调节 Conf / IoU 阈值检查过杀率与漏杀率，确认达标后在「模型仓库」下载 PT 或一键转换 ONNX。",
          },
        ],
        tips: [
          {
            variant: "tip",
            title: "上传任务后台不中断",
            content:
              "上传大体积数据集压缩包或 .pt 模型权重时，进度会自动停靠在页面右下角的上传面板中。上传期间可以自由切换其他菜单浏览页面，只要不关闭或刷新整个浏览器标签页，上传就会持续进行。",
          },
        ],
      },
    ],
  },
  {
    id: "projects",
    groupId: "data",
    title: "项目管理与类别体系",
    shortTitle: "项目管理",
    summary:
      "按检测任务（如「PCB 焊点缺陷检测」）组织数据集、数据版本、训练任务、模型仓库与评估记录，并统一维护训练时的 YOLO 类别顺序。",
    moduleId: "projects",
    targetPath: "/projects",
    targetLabel: "项目",
    keywords: [
      "项目",
      "新建项目",
      "类别列表",
      "classes.txt",
      "YOLO 编号",
      "标签分布",
      "添加数据集",
    ],
    sections: [
      {
        id: "projects-overview",
        title: "浏览与创建项目",
        summary:
          "一个数据集可以同时纳入多个项目；项目作为业务容器，汇总旗下全部数据集的图片量、标注率与迭代模型。",
        figures: [
          {
            id: "fig-projects-list",
            src: projectsListImg,
            alt: "项目列表页面截图",
            caption: "项目列表：统计概览、检索筛选与新建项目入口",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "新建项目按钮",
                description:
                  "点击打开新建对话框，填写项目名称与描述，并可直接勾选已存在的数据集纳入该项目。",
                x: 90.6,
                y: 10.9,
                box: { x: 91.0, y: 8.8, width: 7.4, height: 4.2 },
              },
              {
                index: 2,
                label: "项目与图片统计栏",
                description:
                  "快速核对已纳入项目与未纳入项目的数据集数量，防止遗漏新采集的产线数据。",
                x: 20.2,
                y: 18.4,
                box: { x: 19.3, y: 17.6, width: 79.1, height: 17.2 },
              },
              {
                index: 3,
                label: "搜索、排序与视图切换",
                description:
                  "按项目名称或描述检索，支持切换卡片网格视图与紧凑列表视图。",
                x: 20.2,
                y: 39.2,
                box: { x: 19.3, y: 36.8, width: 79.1, height: 5.0 },
              },
              {
                index: 4,
                label: "项目卡片与菜单",
                description:
                  "卡片展示 4 图拼贴封面、数据集数、模型数及总体标注进度；点击卡片进入项目详情，右上角「…」可编辑或删除项目。",
                x: 44.5,
                y: 45.2,
                box: { x: 19.4, y: 44.0, width: 25.6, height: 50.0 },
              },
            ],
          },
        ],
      },
      {
        id: "project-classes",
        title: "项目详情与 YOLO 类别列表管理",
        summary:
          "类别列表归属于项目而非单个数据集。标注工作台和生成数据版本都会自动读取项目的类别顺序与配色。",
        figures: [
          {
            id: "fig-project-detail",
            src: projectDetailImg,
            alt: "项目详情页面（类别标签页）截图",
            caption: "项目详情：6 个聚合标签页与 YOLO 类别编号管理",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "发起训练 / 添加数据集",
                description:
                  "在项目上下文中一键跳转到「新建训练任务」（自动预选本项目）或将更多数据集关联到本项目。",
                x: 78.2,
                y: 10.9,
                box: { x: 78.8, y: 8.8, width: 19.6, height: 4.2 },
              },
              {
                index: 2,
                label: "6 大业务聚合标签页",
                description:
                  "在同一项目下集中切换查看「数据集 / 类别 / 数据版本 / 训练任务 / 模型 / 模型评估」，全链路可追溯。",
                x: 55.8,
                y: 43.1,
                box: { x: 19.4, y: 41.0, width: 35.6, height: 4.2 },
              },
              {
                index: 3,
                label: "编辑类别按钮",
                description:
                  "打开类别编辑器：支持新增、重命名、更改显示颜色、上下拖拽调整顺序，或直接读取现有 classes.txt 对齐已有模型的类别编号。",
                x: 89.0,
                y: 51.4,
                box: { x: 89.5, y: 49.5, width: 7.2, height: 3.8 },
              },
              {
                index: 4,
                label: "YOLO 类别编号与标签统计",
                description:
                  "左侧 0、1、2、3 即训练与导出 YOLO 时的 class_id；右侧显示每个类别在项目所有数据集中的目标数、图片数与占比。",
                x: 21.4,
                y: 58.2,
                box: { x: 20.6, y: 57.2, width: 76.4, height: 31.5 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "对齐或维护类别顺序",
            description:
              "点击「编辑类别」，如需延续既有产线模型的类别编号，可直接点击读取本地 classes.txt，系统会按文本行顺序排列类别。",
          },
          {
            title: "处理数据集里「未在列表中的标签」",
            description:
              "若数据集中已标注了新标签但尚未加入项目类别列表，页面下方会单独列出这些标签，点击「加入类别列表」即可一键纳入，避免导出训练数据时被忽略。",
          },
        ],
        tips: [
          {
            variant: "note",
            title: "修改类别列表不会改写标注文件",
            content:
              "底层 VOC (.xml) 文件保存的是类别名称而非数字编号。因此调整项目类别顺序、改色或增删类别仅更新项目元数据，不会改动图片目录中的标注文件；删除项目本身也始终保留其关联的数据集。",
          },
        ],
      },
    ],
  },
  {
    id: "datasets",
    groupId: "data",
    title: "数据集与导入导出",
    shortTitle: "数据集管理",
    summary:
      "支持拖拽上传压缩包 / 文件夹或直接索引服务器已有目录；自动将 VOC / X-AnyLabeling / YOLO 标注统一保存为 VOC，支持按状态流转、浏览框与多格式叠加导出。",
    moduleId: "datasets",
    targetPath: "/datasets",
    targetLabel: "数据集",
    keywords: [
      "数据集",
      "导入",
      "压缩包",
      "文件夹",
      "VOC",
      "YOLO",
      "X-AnyLabeling",
      "下载",
      "状态",
      "重新扫描",
      "审图版本",
    ],
    sections: [
      {
        id: "datasets-list-view",
        title: "数据集列表与工作流状态管理",
        summary:
          "每个数据集对应存储目录 datasets/<数据集名称>/ 下的真实文件夹。可通过工作流状态清晰标记每批产线数据的当前阶段。",
        figures: [
          {
            id: "fig-datasets-list",
            src: datasetsListImg,
            alt: "数据集列表页面截图",
            caption:
              "数据集列表：新建/添加目录、按项目与状态筛选、卡片状态快捷切换",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "添加已有目录 / 新建数据集",
                description:
                  "「新建数据集」会创建空目录供网页上传；如果图片已经通过拷贝或 rsync 放到服务器的 datasets/ 目录下，直接点「添加已有目录」即可零拷贝建立索引。",
                x: 79.5,
                y: 10.9,
                box: { x: 80.1, y: 8.8, width: 18.3, height: 4.2 },
              },
              {
                index: 2,
                label: "搜索与多维筛选栏",
                description:
                  "支持按名称/描述搜索、按所属项目筛选（含「未纳入项目」）、按工作流状态筛选，右侧可切换卡片或表格视图。",
                x: 40.6,
                y: 41.5,
                box: { x: 19.4, y: 39.2, width: 52.0, height: 4.6 },
              },
              {
                index: 3,
                label: "工作流状态快捷切换",
                description:
                  "直接点击徽章即可在「待标注 / 标注中 / 待审图 / 审图中 / 可训练」间切换，修改后支持一键撤销且不影响排序。",
                x: 44.6,
                y: 72.1,
                box: { x: 38.0, y: 70.6, width: 6.2, height: 3.2 },
              },
              {
                index: 4,
                label: "数据集卡片与更多操作",
                description:
                  "展示 4 格封面图、标注进度、所属项目与所在存储节点；右上角「…」菜单支持纳入项目、导出下载或删除（移入回收站）。",
                x: 20.4,
                y: 47.4,
                box: { x: 19.4, y: 46.2, width: 25.6, height: 52.8 },
              },
            ],
          },
        ],
      },
      {
        id: "dataset-detail-browse",
        title: "数据集详情：图片浏览、审核记录与导出",
        summary:
          "点入任意数据集，可以浏览所有图片缩略图与标注框预览，按标注状态或标签过滤，查看导入记录与审图版本，或一键下载。",
        figures: [
          {
            id: "fig-dataset-detail",
            src: datasetDetailImg,
            alt: "数据集详情页面截图",
            caption: "数据集详情：顶部操作栏、5 个详情标签页与缩略图网格",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "高频操作按钮组",
                description:
                  "依次为「重新扫描」（同步服务器上手动增删的文件）、「下载」（按 VOC/X-AnyLabeling/YOLO 格式导出）、「导入数据」（继续追加图片）与「标注」（直达标注工作台）。",
                x: 67.4,
                y: 10.9,
                box: { x: 68.0, y: 8.8, width: 29.8, height: 4.2 },
              },
              {
                index: 2,
                label: "状态、存储路径与所属项目",
                description:
                  "查看当前存储节点与物理路径（可一键复制），点击「管理项目」随时调整该数据集归属的一个或多个项目。",
                x: 20.2,
                y: 17.7,
                box: { x: 19.3, y: 16.0, width: 66.8, height: 3.4 },
              },
              {
                index: 3,
                label: "详情子标签页",
                description:
                  "切换查看「图片」缩略图、「标签分布」柱状图、「导入记录」（含同名改名明细）、「审核记录」及合并生成的「审图版本（审图1.0、1.1…）」。",
                x: 49.6,
                y: 43.3,
                box: { x: 19.4, y: 41.2, width: 29.5, height: 4.2 },
              },
              {
                index: 4,
                label: "图片状态与标签筛选栏",
                description:
                  "支持按文件名/路径搜索，按「全部 / 已标注 / 空标注 / 未标注」状态过滤，以及按具体缺陷类别过滤。",
                x: 71.2,
                y: 49.0,
                box: { x: 19.4, y: 46.8, width: 51.0, height: 4.6 },
              },
              {
                index: 5,
                label: "缩略图网格",
                description:
                  "每张缩略图右上角标注框数量或「空标注」，右下角以彩色圆点提示所含类别；点击任意缩略图可打开高清大图并叠加查看所有检测框。",
                x: 20.4,
                y: 54.0,
                box: { x: 19.4, y: 52.8, width: 79.0, height: 42.0 },
              },
            ],
          },
        ],
      },
      {
        id: "dataset-import-guide",
        title: "导入图片与多格式标注自动转换",
        summary:
          "点击「导入数据」弹出上传对话框，支持超大文件 8 MiB 分块断点续传，并自动识别 Windows 打包的 GBK 中文文件名。",
        figures: [
          {
            id: "fig-dataset-import",
            src: datasetImportDialogImg,
            alt: "导入数据弹窗截图",
            caption: "导入数据弹窗：上传方式、源标注格式与冲突策略设置",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "选择上传模式",
                description:
                  "支持「压缩包」（zip / tar / tar.gz / 7z / rar 等）与「文件夹」（直接拖入整个本地目录并保留原有子文件夹结构）。",
                x: 27.8,
                y: 20.1,
                box: { x: 28.3, y: 18.0, width: 43.4, height: 4.2 },
              },
              {
                index: 2,
                label: "文件拖放与选择区",
                description:
                  "直接从桌面拖拽文件到此虚线框内，或点击按钮选择压缩包 / 文件夹。",
                x: 72.2,
                y: 34.5,
                box: { x: 28.3, y: 23.8, width: 43.4, height: 23.7 },
              },
              {
                index: 3,
                label: "源标注格式选择",
                description:
                  "可选 Pascal VOC (.xml)、X-AnyLabeling (.json，兼容 LabelMe) 或 YOLO (.txt)。后两者在导入时自动转换为 VOC 保存；选 YOLO 时会自动读取文件夹中的 classes.txt 或允许手动填写类别顺序。",
                x: 27.8,
                y: 59.5,
                box: { x: 28.2, y: 52.6, width: 43.6, height: 14.0 },
              },
              {
                index: 4,
                label: "同路径冲突处理策略",
                description:
                  "向已有数据集追加导入时，可指定「跳过（默认，保留已有图片与标注）」或「覆盖（替换为新文件与标注）」。",
                x: 27.8,
                y: 76.0,
                box: { x: 28.2, y: 71.0, width: 43.6, height: 10.0 },
              },
            ],
          },
        ],
        table: {
          caption: "下载数据集时的审图版本叠加规则",
          headers: ["勾选组合", "导出范围与标注优先级"],
          rows: [
            [
              "仅基础版本（默认）",
              "导出数据集全部图片以及文件夹中原本的 VOC 标注。",
            ],
            [
              "仅选审图版本",
              "仅导出所选审图版本中标为「需加样」的图片及其审图标注，适合提取难例做增量训练。",
            ],
            [
              "基础版本 + 一个或多个审图版本",
              "导出数据集全部图片：审图版本中存在的图片按「版本号越大越优先」覆盖基础标注，其余图片沿用基础标注。",
            ],
          ],
        },
        tips: [
          {
            variant: "warning",
            title: "不同子文件夹同名图片自动防撞",
            content:
              "若不同子目录下存在同名但内容不同的图片（如 cam1/0001.jpg 与 cam2/0001.jpg），导入时系统会自动把后者连同标注改名为 cam2/0001_2.jpg，防止训练与导出冲突；具体改名记录可在「导入记录」中查阅。",
          },
        ],
      },
    ],
  },
]
