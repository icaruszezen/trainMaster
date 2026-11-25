import annotationEditorImg from "@/features/guide/assets/annotation-editor.webp"
import annotationPickerImg from "@/features/guide/assets/annotation-picker.webp"
import dataVersionDetailImg from "@/features/guide/assets/data-version-detail.webp"
import dataVersionNewImg from "@/features/guide/assets/data-version-new.webp"
import dataVersionsListImg from "@/features/guide/assets/data-versions-list.webp"
import reviewDetailImg from "@/features/guide/assets/review-detail.webp"
import reviewWorkspaceImg from "@/features/guide/assets/review-workspace.webp"
import reviewsListImg from "@/features/guide/assets/reviews-list.webp"
import type { GuideChapter } from "@/features/guide/types"

export const annotationAndReviewChapters: GuideChapter[] = [
  {
    id: "annotation",
    groupId: "annotation",
    title: "标注工作台与快捷键",
    shortTitle: "标注工作台",
    summary:
      "面向工业缺陷检测的在线矩形框标注器：沿用 LabelImg 快捷键习惯，按项目类别顺序提示候选标签，自动保存为数据集文件夹中的标准 VOC (.xml) 文件。",
    moduleId: "annotation-workspace",
    targetPath: "/annotation/workspace",
    targetLabel: "标注工作台",
    keywords: [
      "标注",
      "画框",
      "矩形框",
      "快捷键",
      "LabelImg",
      "Shift+D",
      "空标注",
      "负样本",
      "自动保存",
      "VOC",
      "难例",
      "difficult",
    ],
    sections: [
      {
        id: "annotation-entry",
        title: "选择数据集进入工作台",
        summary:
          "除从侧边栏「标注 → 标注工作台」选择数据集外，也可以在数据集详情页或查看大图弹窗中直接点击「标注」进入。",
        figures: [
          {
            id: "fig-annotation-picker",
            src: annotationPickerImg,
            alt: "标注工作台数据集选择页面截图",
            caption: "标注工作台入口：常用快捷键提示、数据集搜索与标注进度卡片",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "高频快捷键提示条",
                description:
                  "快速回忆画框（W）、确认类别（Enter）、数字切类（1–9）及切图自动保存（A / D）。",
                x: 20.2,
                y: 19.6,
                box: { x: 19.3, y: 18.6, width: 65.5, height: 3.4 },
              },
              {
                index: 2,
                label: "搜索数据集",
                description: "按名称快速定位待标注的数据集。",
                x: 40.0,
                y: 26.8,
                box: { x: 19.4, y: 24.8, width: 20.1, height: 4.2 },
              },
              {
                index: 3,
                label: "数据集进度卡片",
                description:
                  "显示当前工作流状态、已标注 / 未标注张数与进度条；点击任意卡片即全屏进入该数据集的在线标注编辑器。",
                x: 44.5,
                y: 35.0,
                box: { x: 19.4, y: 31.6, width: 25.8, height: 10.0 },
              },
            ],
          },
        ],
      },
      {
        id: "annotation-canvas-layout",
        title: "在线标注编辑器界面分区",
        summary:
          "进入编辑器后左侧主导航会自动收起为图标栏以留出最大画布宽度；编辑画框后停止操作约 1 秒即自动保存为 VOC (.xml)。",
        figures: [
          {
            id: "fig-annotation-editor",
            src: annotationEditorImg,
            alt: "在线标注编辑器界面截图，显示 PCB 缺陷框选中手柄与右侧属性面板",
            caption:
              "在线标注编辑器：顶栏工具、左侧筛选列表、中心画布与右侧对象属性",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "顶部工具栏（切图 / 工具 / 缩放 / 亮度对比度）",
                description:
                  "左侧显示当前图片序号（支持输入页码直跳），中间为上一张 / 下一张 / 下一张未标注、选择（V）/ 画框（W）/ 平移工具、撤销重做、缩放比例及图像亮度对比度调节。",
                x: 63.6,
                y: 9.0,
                box: { x: 3.8, y: 6.4, width: 60.0, height: 5.2 },
              },
              {
                index: 2,
                label: "左侧图片列表与状态筛选",
                description:
                  "按文件名、标注状态（已标注 / 空标注 / 未标注 / 标注无效）与标签筛选，点击任意条目切换图片（切换前自动保存）。",
                x: 19.5,
                y: 24.0,
                box: { x: 3.8, y: 12.6, width: 16.9, height: 86.2 },
              },
              {
                index: 3,
                label: "中心画布与 8 手柄精确调框",
                description:
                  "滚轮最高可放大至 32 倍逐像素对齐缺陷边缘；选中标注框后可拖曳 8 个白色控制点缩放，或用方向键按 1px / 10px 微调位置。",
                x: 23.6,
                y: 27.6,
                box: { x: 22.6, y: 26.2, width: 55.8, height: 59.5 },
              },
              {
                index: 4,
                label: "右侧框列表、属性编辑与图片信息",
                description:
                  "列出本图全部标注框及宽高像素；选中框后可更换类别、输入精确坐标、勾选难例（difficult，显示虚线框）或截断（truncated）标记。",
                x: 80.5,
                y: 28.0,
                box: { x: 80.3, y: 12.6, width: 19.3, height: 61.0 },
              },
              {
                index: 5,
                label: "保存状态、更多操作与快捷键面板",
                description:
                  "右侧实时提示「已保存 / 未保存」，点击「…」可沿用上一张框或标为空标注，点击键盘图标（或按 ? 键）随时呼出完整快捷键速查。",
                x: 96.2,
                y: 9.2,
                box: { x: 89.8, y: 6.4, width: 9.8, height: 5.2 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "按 W 画矩形框并回车确认类别",
            description:
              "按 W 进入画框模式，在缺陷区域拖动（或先后单击两个对角）完成画框；按住 Shift 可画正方形。松手后立即弹出类别选择器，默认高亮上一次使用的类别，直接按 Enter 确定，或按 1–9 / 0 快速指定前 10 个候选类别。",
          },
          {
            title: "标为空标注（负样本）与快速跳图",
            description:
              "遇到无任何缺陷的良品图时，直接按 N 键即可将其保存为空标注（负样本，写入空 <annotation> 文件），再按 Shift+D 跳到下一张未标注图片。",
          },
          {
            title: "多人协作防覆盖保护",
            description:
              "如果同一张图片在打开期间被其他同事保存过或服务器文件发生了变化，保存时会弹出冲突提示，可选择载入最新版本或用自己的修改覆盖，不会静默丢失标注。",
          },
        ],
      },
      {
        id: "annotation-shortcuts",
        title: "标注工作台快捷键速查表",
        summary:
          "字母与数字按物理键位匹配，不受大小写锁定影响；光标位于搜索或输入框中时自动让位给文字输入。",
        shortcuts: [
          {
            title: "画框与选择",
            items: [
              {
                keys: ["W", "R"],
                description: "画矩形框（拖动或两次单击对角）",
              },
              { keys: ["Shift"], description: "画框时按住，约束为正方形" },
              {
                keys: ["Enter"],
                description: "画框后确认类别（默认沿用上次）",
              },
              { keys: ["V"], description: "切换选择 / 编辑模式" },
              {
                keys: ["Esc"],
                description: "放弃未定类的框 / 取消选中 / 回到选择",
              },
              { keys: ["Tab"], description: "画布聚焦时依次选中下一个框" },
            ],
          },
          {
            title: "类别、属性与微调",
            items: [
              {
                keys: ["1–9", "0"],
                description: "快捷切换或修改为前 10 个候选类别",
              },
              { keys: ["T"], description: "打开可搜索的类别选择框" },
              {
                keys: ["Q", "E"],
                description: "切换「难例 difficult」/「截断 truncated」",
              },
              {
                keys: ["方向键"],
                description: "移动选中框 1px（加 Shift 为 10px）",
              },
              {
                keys: ["Ctrl+方向键"],
                description: "微调选中框右边 / 下边尺寸",
              },
              { keys: ["Delete"], description: "删除当前选中的标注框" },
            ],
          },
          {
            title: "切图、复制与保存",
            items: [
              { keys: ["A", "D"], description: "上一张 / 下一张（自动保存）" },
              {
                keys: ["Shift+A", "Shift+D"],
                description: "上一张 / 下一张未标注图片",
              },
              { keys: ["G"], description: "聚焦页码框，跳转到第 N 张" },
              { keys: ["N"], description: "标为空标注（负样本）" },
              {
                keys: ["Ctrl+C", "Ctrl+V"],
                description: "跨图复制 / 粘贴标注框",
              },
              {
                keys: ["Ctrl+Z", "Ctrl+S"],
                description: "撤销 / 立即手动保存",
              },
            ],
          },
          {
            title: "画布视图控制",
            items: [
              { keys: ["滚轮"], description: "以光标为中心缩放（最高 32×）" },
              { keys: ["空格+拖动"], description: "平移画布（或中键拖动）" },
              { keys: ["F"], description: "缩放至适应窗口" },
              { keys: ["Ctrl+0"], description: "按原图 100% 像素显示" },
              { keys: ["Z"], description: "缩放聚焦到当前选中的框" },
              {
                keys: ["H", "L", "?"],
                description: "隐显所有框 / 隐显类别名 / 快捷键帮助",
              },
            ],
          },
        ],
      },
    ],
  },
  {
    id: "reviews",
    groupId: "annotation",
    title: "模型审图与离线审图",
    shortTitle: "模型审图",
    summary:
      "用模型仓库中的已有模型批量推理数据集，逐张复核识别框并标记为「已审核」或「需加样」，再将结果合并为数据集的审图版本（审图1.0、1.1…），不破坏原始标注。",
    moduleId: "reviews",
    targetPath: "/datasets/reviews",
    targetLabel: "模型审图",
    keywords: [
      "模型审图",
      "审图",
      "已审核",
      "需加样",
      "待审核",
      "S",
      "X",
      "U",
      "合并到数据集",
      "审图版本",
      "本地审图",
      "trainmaster-reviewer.exe",
      "离线审图",
    ],
    sections: [
      {
        id: "reviews-create-list",
        title: "创建与管理审图任务",
        summary:
          "审图任务独立存放模型的推理标注（reviews/review-<ID>/），绝不直接改写数据集的原始标注文件。",
        figures: [
          {
            id: "fig-reviews-list",
            src: reviewsListImg,
            alt: "模型审图任务列表页截图",
            caption: "模型审图列表：审核状态汇总、任务识别与审核进度总览",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "新建审图任务按钮",
                description:
                  "点击选择模型仓库中的检测/分割/姿态/旋转框模型、勾选一个或多个数据集（最多 50 个），配置置信度、IoU、输入尺寸后交由在线训练节点自动识别。",
                x: 88.6,
                y: 10.9,
                box: { x: 89.1, y: 8.8, width: 9.3, height: 4.2 },
              },
              {
                index: 2,
                label: "审核状态统计卡片",
                description:
                  "全局汇总全部审图任务中的「待审核」「已审核（模型识别正确）」与「需加样（需进入下一轮训练）」图片数量。",
                x: 20.2,
                y: 20.6,
                box: { x: 19.3, y: 19.8, width: 79.1, height: 15.6 },
              },
              {
                index: 3,
                label: "审图任务行与进度条",
                description:
                  "展示所用模型、推理状态与彩色三段审核进度条（绿=已审核、橙=需加样、灰=待审核）；点击任务进入详情页。",
                x: 20.2,
                y: 51.0,
                box: { x: 19.4, y: 45.2, width: 79.0, height: 10.6 },
              },
            ],
          },
        ],
      },
      {
        id: "review-detail-offline",
        title: "审图详情：网页审图、Windows 本地离线审图与合并版本",
        summary:
          "训练节点推理出第一批图片后即可立即开始审核，无需等整批识别完毕；大批量或弱网环境下可使用 Windows 本地审图程序离线处理。",
        figures: [
          {
            id: "fig-review-detail",
            src: reviewDetailImg,
            alt: "模型审图任务详情页截图",
            caption:
              "审图详情：网页审图入口、合并为审图版本及 Windows 本地审图指引",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "网页审图入口",
                description:
                  "打开网页审图工作台，自动定位到第一张「待审核」图片开始快速逐张过图。",
                x: 91.6,
                y: 13.8,
                box: { x: 88.0, y: 8.8, width: 7.4, height: 4.2 },
              },
              {
                index: 2,
                label: "合并到数据集",
                description:
                  "把已审出的标注（默认仅「需加样」，也可选含「已审核」）保存为各数据集的新审图版本（审图1.0、审图1.1…）并填写版本备注，可边审边分批合并。",
                x: 82.8,
                y: 13.8,
                box: { x: 78.1, y: 8.8, width: 9.4, height: 4.2 },
              },
              {
                index: 3,
                label: "下载审图包 / 上传审图结果",
                description:
                  "与下方「本地审图」卡片配套使用，实现离线导出、本机极速过图与结果增量回传。",
                x: 69.0,
                y: 13.8,
                box: { x: 59.3, y: 8.8, width: 18.2, height: 4.2 },
              },
              {
                index: 4,
                label: "本地审图（Windows 离线）步骤卡片",
                description:
                  "依次提供 ① 下载单文件免安装程序 trainmaster-reviewer.exe → ② 下载包含图片与清单的审图包 zip → ③ 本地双击审图并导出轻量结果 zip → ④ 上传审图结果回平台。",
                x: 20.4,
                y: 68.5,
                box: { x: 19.4, y: 67.6, width: 38.6, height: 31.8 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "Windows 本地离线审图 4 步流",
            description:
              "① 点击「下载程序」获取免安装单文件 trainmaster-reviewer.exe；② 点击「下载审图包」按需筛选待审核状态下载 zip；③ 运行程序打开（或直接把 zip 拖到程序图标上），快捷键与网页审图完全一致，进度随时落地本地；④ 完成后点击「导出审图结果」生成不含图片的小体积结果 zip，回到详情页点「上传审图结果」自动合入。",
            tip: "重复上传同一份结果不会重复覆盖；若下载审图包后平台上有人改过同一张图，系统会明确列出冲突清单供确认。",
          },
        ],
      },
      {
        id: "review-workspace-ops",
        title: "网页审图工作台操作",
        summary:
          "网页审图复用标注工作台的画布引擎，在右侧新增「审核状态」与「模型识别结果」对照面板，支持一键打标并自动跳下一张。",
        figures: [
          {
            id: "fig-review-workspace",
            src: reviewWorkspaceImg,
            alt: "网页审图工作台界面截图，展示模型预测框置信度与右侧 S/X/U 审核面板",
            caption:
              "网页审图工作台：三态筛选、带置信度预测框、S/X/U 快捷判别与一键恢复",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "多维筛选面板（状态 / 类别 / 已修改 / 无框）",
                description:
                  "快速筛选「待审核 / 已审核 / 需加样」，或一键过滤「已修改」「无框」「识别失败」图片，聚焦高风险漏检样本。",
                x: 22.8,
                y: 22.0,
                box: { x: 3.8, y: 12.6, width: 18.9, height: 22.8 },
              },
              {
                index: 2,
                label: "画布预测框与人工修正",
                description:
                  "每个模型检测框顶部显示类别与置信度（如 solder_bridge 0.93、误检 scratch 0.34）；可直接删除误检框、补画漏检框或拖拽调整边框，操作与标注工作台完全一致。",
                x: 52.0,
                y: 44.0,
                box: { x: 24.8, y: 28.4, width: 51.6, height: 54.8 },
              },
              {
                index: 3,
                label: "S / X / U 快捷审核按钮与自动跳下一张",
                description:
                  "按 S 标为「已审核」（识别无误，不需加样）、按 X 标为「需加样」（漏检/误检/已修正，进入下轮训练）、按 U 退回「待审核」；下方开关控制打标后是否自动跳至下一张。",
                x: 78.6,
                y: 22.5,
                box: { x: 78.4, y: 12.8, width: 21.0, height: 26.2 },
              },
              {
                index: 4,
                label: "模型原始识别结果与一键「恢复」",
                description:
                  "始终保留模型推理原始框与置信度供对比；若手动改乱了框，点击「恢复」可立即还原为模型原始识别结果（同样支持 Ctrl+Z 撤销）。",
                x: 78.6,
                y: 60.0,
                box: { x: 78.4, y: 58.2, width: 21.0, height: 15.0 },
              },
            ],
          },
        ],
        shortcuts: [
          {
            title: "审图专属快捷键",
            items: [
              { keys: ["S"], description: "标为「已审核」并自动跳下一张" },
              { keys: ["X"], description: "标为「需加样」并自动跳下一张" },
              { keys: ["U"], description: "改回「待审核」（不自动跳图）" },
              {
                keys: ["Shift+D", "Shift+A"],
                description: "跳到下一张 / 上一张待审核图片",
              },
            ],
          },
          {
            title: "改框与视图快捷键",
            items: [
              {
                keys: ["W", "Delete"],
                description: "补画新框 / 删除选中误检框",
              },
              { keys: ["1–9", "T"], description: "修改选中框类别 / 搜索类别" },
              { keys: ["Ctrl+Z"], description: "撤销画框、删框或恢复操作" },
              { keys: ["?", "F1"], description: "查看全部审图快捷键说明" },
            ],
          },
        ],
      },
    ],
  },
  {
    id: "data-versions",
    groupId: "annotation",
    title: "数据版本与冻结划分",
    shortTitle: "数据版本",
    summary:
      "从项目数据集生成不可变的冻结训练集（v1、v2…）：固定类别顺序、稳定划分 train / val / test 防数据泄漏、支持离线增强，并可直接供 YOLO 训练或导出 YOLO / VOC / COCO。",
    moduleId: "dataset-versions",
    targetPath: "/datasets/versions",
    targetLabel: "数据版本",
    keywords: [
      "数据版本",
      "冻结",
      "训练集",
      "验证集",
      "测试集",
      "划分",
      "数据增强",
      "离线增强",
      "防泄漏",
      "YOLO",
      "COCO",
      "硬链接",
    ],
    sections: [
      {
        id: "data-versions-overview",
        title: "数据版本列表与概念区分",
        paragraphs: [
          "「审图版本」属于单个数据集，仅保存合并审图产生的一层 VOC 标注快照；而「数据版本」属于整个项目，可以组合项目下多个数据集的基础标注与审图版本，连同类别编号、训练/验证/测试划分和图片文件一起冻结下来。",
          "数据版本生成后除备注外永不改变——即使后续继续修改数据集标注、更改项目类别顺序或删除来源数据集，已冻结的数据版本依然可以完整复现当时训练与评估结果。",
        ],
        figures: [
          {
            id: "fig-data-versions-list",
            src: dataVersionsListImg,
            alt: "数据版本列表页面截图",
            caption: "数据版本列表：按项目筛选、版本状态与生成新数据版本入口",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "生成数据版本按钮",
                description:
                  "进入数据版本生成向导，默认沿用项目最新版本的来源、划分比例与随机种子设置。",
                x: 88.6,
                y: 10.9,
                box: { x: 89.1, y: 8.8, width: 9.3, height: 4.2 },
              },
              {
                index: 2,
                label: "按项目筛选",
                description:
                  "快速只看某个检测项目下依次递增的 v1、v2… 数据版本。",
                x: 35.6,
                y: 22.1,
                box: { x: 19.4, y: 19.8, width: 15.5, height: 4.6 },
              },
              {
                index: 3,
                label: "数据版本列表行",
                description:
                  "显示版本号、备注、生成状态、所在存储节点及 train / val / test 划分张数；点击行进入详情页。",
                x: 20.2,
                y: 34.4,
                box: { x: 19.5, y: 31.2, width: 78.8, height: 6.4 },
              },
            ],
          },
        ],
      },
      {
        id: "data-version-wizard",
        title: "生成数据版本向导与实时预览",
        summary:
          "右侧实时预览面板会在勾选数据集或调整比例时自动计算各划分与各类别数量，并与上一个版本比对差异，提前发现验证集或测试集缺类风险。",
        figures: [
          {
            id: "fig-data-version-new",
            src: dataVersionNewImg,
            alt: "生成数据版本向导页面截图，右侧展示实时分布预览与缺类提示",
            caption:
              "生成数据版本向导：沿用模板、类别冻结、样本范围与右侧实时预览",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "选择所属项目与沿用上版模板",
                description:
                  "选定项目后自动加载其最新数据版本配置（保证相同比例与随机种子下同一张图仍落在同一划分中），也可点击「改用默认设置」从头配。",
                x: 20.2,
                y: 22.5,
                box: { x: 19.4, y: 20.0, width: 78.9, height: 15.0 },
              },
              {
                index: 2,
                label: "冻结类别预览",
                description:
                  "展示将被永久冻结到该版本的 YOLO 类别编号（0, 1, 2…）；如需调整顺序可点右侧「在项目中修改」。",
                x: 20.2,
                y: 59.8,
                box: { x: 19.4, y: 58.6, width: 53.3, height: 15.0 },
              },
              {
                index: 3,
                label: "数据集与「参与训练的图片」范围",
                description:
                  "支持「仅有标注的图片（默认）」「仅无标注的背景图（适合良品负样本数据集）」「整个数据集」，且可为每个数据集单独叠加勾选基础标注与审图版本。",
                x: 20.2,
                y: 77.6,
                box: { x: 19.4, y: 76.0, width: 53.3, height: 23.2 },
              },
              {
                index: 4,
                label: "右侧实时分布预览与差异预警",
                description:
                  "实时显示训练 / 验证 / 测试集图片数、负样本数、各类别在各划分的目标分布，并自动提醒某类别在测试/验证集缺失，以及相对上一版的新增/移除/换划分变化。",
                x: 74.6,
                y: 39.2,
                box: { x: 73.9, y: 37.6, width: 24.4, height: 61.5 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "选择划分比例与防泄漏分组",
            description:
              "向下滚动表单可选择 80/20/0、70/20/10、90/10/0 或自定义比例；若源数据集自带 train / val / test 文件夹可直接沿用。需要避免同一工位/零件多张连拍跨集合泄漏时，可在高级设置中把分组方式改为「文件夹」。",
          },
          {
            title: "按需开启离线数据增强（仅作用于训练集）",
            description:
              "面对极小样本缺陷，可勾选水平/垂直翻转、90° 旋转、亮度对比度、轻微模糊或高斯噪声并设置每图生成 1–5 份（验证集与测试集始终保持原始真实图片）。",
          },
          {
            title: "填写必填备注并提交排队生成",
            description:
              "写明本版本的数据变更点（如「合入 B 线审图1.0 难例」）后点击「生成数据版本」，后台采用硬链接零额外空间拷贝原图并生成标准 Ultralytics data.yaml 布局。",
          },
        ],
      },
      {
        id: "data-version-detail-export",
        title: "查看详情、追溯来源与多格式导出",
        figures: [
          {
            id: "fig-data-version-detail",
            src: dataVersionDetailImg,
            alt: "数据版本详情页面截图",
            caption:
              "数据版本详情：一键发起训练、以本版为模板新建、多格式下载与类别分布",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "发起训练 / 以此为模板新建 / 下载",
                description:
                  "直接基于该冻结版本跳转「新建训练任务」、复制本版设置快速迭代下一版，或打包下载 YOLO (Ultralytics) / Pascal VOC / COCO 压缩包（支持选择是否包含增强图）。",
                x: 71.0,
                y: 10.9,
                box: { x: 71.6, y: 8.8, width: 26.8, height: 4.2 },
              },
              {
                index: 2,
                label: "新增占用空间说明",
                description:
                  "原图默认使用文件系统硬链接与源数据集共享存储，因此未开启离线增强时「新增占用」为 0 B，几乎不额外消耗硬盘。",
                x: 80.2,
                y: 23.0,
                box: { x: 79.4, y: 21.5, width: 18.9, height: 15.4 },
              },
              {
                index: 3,
                label: "概览 / 图片明细标签",
                description:
                  "切换到「图片」标签可按 train / val / test 划分、类别、来源数据集和是否增强图过滤，点开大图叠加核查最终冻结的标注框。",
                x: 29.8,
                y: 41.5,
                box: { x: 19.4, y: 39.5, width: 9.6, height: 4.2 },
              },
              {
                index: 4,
                label: "各划分各类别分布表",
                description:
                  "逐类核查训练、验证、测试集的目标数与图片数，以及底部负样本张数。",
                x: 20.4,
                y: 47.0,
                box: { x: 19.4, y: 45.8, width: 78.9, height: 42.6 },
              },
            ],
          },
        ],
        tips: [
          {
            variant: "note",
            title: "被训练任务引用的数据版本受保护",
            content:
              "已关联训练任务的数据版本不能直接删除（需先删除对应训练任务），以确保每个模型都能完整回溯其训练集；普通已生成版本删除后会进入回收站保留 7 天。",
          },
        ],
      },
    ],
  },
]
