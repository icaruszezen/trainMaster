import evaluationCompareImg from "@/features/guide/assets/evaluation-compare.webp"
import evaluationDetailConfusionImg from "@/features/guide/assets/evaluation-detail-confusion.webp"
import evaluationDetailCurvesImg from "@/features/guide/assets/evaluation-detail-curves.webp"
import evaluationsListImg from "@/features/guide/assets/evaluations-list.webp"
import modelDetailImg from "@/features/guide/assets/model-detail.webp"
import modelsListImg from "@/features/guide/assets/models-list.webp"
import type { GuideChapter } from "@/features/guide/types"

export const evaluationAndModelsChapters: GuideChapter[] = [
  {
    id: "evaluations",
    groupId: "evaluation",
    title: "模型评估与横向对比",
    shortTitle: "模型评估",
    summary:
      "在冻结数据版本或自由组合数据集上批量评估 YOLO 模型：评估完成后毫秒级拖动 Conf / IoU 阈值重算指标与工业过杀/漏杀率，点击混淆矩阵下钻样本框检查，支持 2–6 个模型同源逐图对比及离线 HTML 报告导出。",
    moduleId: "evaluations",
    targetPath: "/evaluation",
    targetLabel: "模型评估",
    keywords: [
      "模型评估",
      "mAP",
      "混淆矩阵",
      "PR 曲线",
      "F1 曲线",
      "过杀率",
      "漏杀率",
      "Conf",
      "IoU",
      "阈值",
      "多模型对比",
      "逐图差异",
      "HTML 报告",
      "漏检",
      "误检",
    ],
    sections: [
      {
        id: "evaluations-list-create",
        title: "发起批量评估与管理评估列表",
        summary:
          "一次最多可勾选 12 个模型在相同数据源上批量发起评估；评估数据源既可以是项目冻结的「数据版本（val/test/train）」，也可以自由组合多个「数据集（含审图版本）」。",
        figures: [
          {
            id: "fig-evaluations-list",
            src: evaluationsListImg,
            alt: "模型评估列表页面截图",
            caption:
              "模型评估列表：新建评估、批量勾选多模型对比与工业核心指标一览",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "多模型对比 / 新建评估入口",
                description:
                  "点击「新建评估」配置数据源与待测模型（发起前右侧实时预览样本总数、OK/NG 构成与类别按名对齐状态）；勾选 2–6 条已完成记录后点击「多模型对比」进入横向对比页。",
                x: 81.8,
                y: 10.9,
                box: { x: 82.4, y: 8.8, width: 16.0, height: 4.2 },
              },
              {
                index: 2,
                label: "评估核心红线指标卡",
                description:
                  "直观聚焦总评估数、最高 mAP50、工业质检最关注的「最低漏杀率（NG→OK）」以及最快端到端推理 FPS。",
                x: 20.2,
                y: 20.6,
                box: { x: 19.3, y: 19.8, width: 79.1, height: 15.6 },
              },
              {
                index: 3,
                label: "多模型对比勾选框",
                description:
                  "勾选 2–6 个已完成评估即可横向对比指标与曲线；若所选评估数据源一致，对比页会自动解锁「同源逐图差异」。",
                x: 19.4,
                y: 55.5,
                box: { x: 20.0, y: 49.0, width: 3.0, height: 13.0 },
              },
              {
                index: 4,
                label: "评估明细指标列",
                description:
                  "同排展示评估数据源、mAP50、mAP50-95、当前默认阈值 F1、过杀/漏杀率及延迟 FPS，点击任意行进入深度诊断详情。",
                x: 79.0,
                y: 50.2,
                box: { x: 23.0, y: 49.0, width: 75.2, height: 13.2 },
              },
            ],
          },
        ],
      },
      {
        id: "evaluation-realtime-threshold",
        title: "实时阈值调优、交互曲线与离线报告",
        summary:
          "训练节点推理时会完整归档低阈值（≥0.05）的原始预测框，因此在详情页拖动置信度或 IoU 滑块时无需重跑 GPU 推理，毫秒级即可重算全部指标、混淆矩阵和样本分类。",
        figures: [
          {
            id: "fig-evaluation-curves",
            src: evaluationDetailCurvesImg,
            alt: "模型评估详情页（指标与曲线标签页）截图",
            caption:
              "评估详情：吸顶 Conf/IoU 实时调节栏、核心 KPI 卡片与交互式 PR/F1 曲线",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "导出离线报告 / 进入多模型对比",
                description:
                  "「导出报告」支持在线预览或下载单文件自包含的离线 HTML 报告（内网无外网依赖，可直接发给质检/研发评审），以及完整指标 JSON、各类别 CSV 和筛选样本 CSV。",
                x: 79.5,
                y: 15.1,
                box: { x: 80.0, y: 13.2, width: 15.6, height: 3.8 },
              },
              {
                index: 2,
                label: "吸顶 Conf / IoU 实时滑块与「跳到最佳 F1」",
                description:
                  "拖动置信度（Conf）与匹配 IoU 滑块实时重算所有指标与过杀/漏杀率；点「跳到最佳 F1」一键定位最优工作点，调优满意后可点击「设为默认阈值」持久化保存。",
                x: 86.0,
                y: 33.9,
                box: { x: 19.4, y: 31.0, width: 78.9, height: 5.8 },
              },
              {
                index: 3,
                label: "5 张核心工业评估 KPI 卡",
                description:
                  "同屏呈现 COCO 口径 mAP50 / mAP50-95、当前工作点 F1（含 P/R）、图片级工业漏杀率（NG→OK）与过杀率（OK→NG）以及端到端延迟 / FPS。",
                x: 20.2,
                y: 39.8,
                box: { x: 19.4, y: 38.8, width: 78.9, height: 15.4 },
              },
              {
                index: 4,
                label: "4 个诊断分析标签页",
                description:
                  "在「指标与曲线」「混淆矩阵」「漏检误检样本」「配置与详情」间切换，并同步响应顶部当前滑块阈值。",
                x: 53.4,
                y: 58.7,
                box: { x: 19.4, y: 56.6, width: 33.2, height: 4.2 },
              },
              {
                index: 5,
                label: "PR / F1 / P / R 交互曲线与类别图例",
                description:
                  "点击上方类别胶囊可单独对比或隐藏某类缺陷曲线，点击曲线工作点也能直接跳转至对应置信度阈值。",
                x: 19.4,
                y: 63.0,
                box: { x: 19.4, y: 63.0, width: 78.9, height: 36.2 },
              },
            ],
          },
        ],
      },
      {
        id: "evaluation-confusion-samples",
        title: "混淆矩阵点击下钻与样本框检视",
        summary:
          "定位模型「哪里漏检、哪里误分类、哪里过杀」的最快方式：直接在混淆矩阵中点击任意非零格即可自动筛选出对应坏例图片。",
        figures: [
          {
            id: "fig-evaluation-confusion",
            src: evaluationDetailConfusionImg,
            alt: "模型评估详情页（混淆矩阵标签页）截图",
            caption:
              "目标检测混淆矩阵：行/列归一化切换与点击非零格一键下钻坏例",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "「混淆矩阵」与「漏检误检样本」联动",
                description:
                  "点击下方矩阵任意单元格会自动带条件切入「漏检误检样本」页；点击任意样本缩略图打开高清检视弹窗，以不同颜色叠画 GT / TP / FP / FN 框（支持分类型开关与直达标注工作台修正标注）。",
                x: 30.5,
                y: 58.7,
                box: { x: 19.4, y: 56.6, width: 33.4, height: 4.2 },
              },
              {
                index: 2,
                label: "显示口径切换（数量 / 召回率 / 精确率）",
                description:
                  "在绝对目标数量、行归一化（每类真实目标的召回率分布）与列归一化（每类预测框的精确率分布）之间一键切换。",
                x: 43.8,
                y: 72.9,
                box: { x: 21.0, y: 71.0, width: 22.0, height: 3.8 },
              },
              {
                index: 3,
                label: "交互式 (N+1)×(N+1) 混淆矩阵",
                description:
                  "绿色对角线为正确检出（TP），橙色非对角格为类间误分类（例如把 2 个 missing_solder 误判为 solder_bridge），最右侧粉红列为漏检（FN），底部行为背景误检（FP）；点击数字即下钻样本。",
                x: 41.0,
                y: 91.5,
                box: { x: 21.2, y: 78.4, width: 75.3, height: 20.8 },
              },
            ],
          },
        ],
      },
      {
        id: "evaluation-multi-compare",
        title: "多模型横向对比与同源逐图差异",
        figures: [
          {
            id: "fig-evaluation-compare",
            src: evaluationCompareImg,
            alt: "多模型评估对比页面截图，显示基线 Δ 指标差异矩阵与同源逐图差异标签",
            caption:
              "多模型对比：基线切换、统一阈值公平重算、Δ 差异矩阵与同源逐图差异",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "导出离线对比报告",
                description:
                  "导出包含全部参与模型 KPI 差异、叠加曲线与类别对比的离线 HTML 报告或 CSV 表格。",
                x: 88.8,
                y: 15.1,
                box: { x: 89.3, y: 13.2, width: 9.0, height: 3.8 },
              },
              {
                index: 2,
                label: "参与对比模型与同源验证集状态",
                description:
                  "点击任意模型胶囊可将其设为基线（Baseline，所有 Δ 均相对它计算）；当全部对比任务数据源指纹一致时，右侧绿色徽章提示已解锁「同源逐图差异」。",
                x: 82.2,
                y: 27.2,
                box: { x: 19.4, y: 24.4, width: 78.9, height: 5.6 },
              },
              {
                index: 3,
                label: "各自默认阈值 / 统一阈值公平对比",
                description:
                  "切换到「统一阈值公平对比」后，可拖动统一的 Conf / IoU 滑块，在同一阈值尺子下公平重算所有模型指标与逐图差异。",
                x: 37.5,
                y: 33.6,
                box: { x: 20.8, y: 31.6, width: 16.0, height: 4.0 },
              },
              {
                index: 4,
                label: "切换「同源逐图差异」",
                description:
                  "在逐图差异页可按「漏检减少」「新增漏检」「误检减少」「新增误检」「OK↔NG 翻转」筛选样本，并弹窗并排对比各模型在同一张图上的检测框。",
                x: 39.0,
                y: 41.5,
                box: { x: 19.4, y: 39.4, width: 18.8, height: 4.2 },
              },
              {
                index: 5,
                label: "核心指标差异矩阵（绿优红劣）",
                description:
                  "每行自动浅绿高亮最优模型，数值下方以 +Δ / -Δ 标明相对基线的提升或回退；继续向下滚动可查看多模型叠加 PR/F1 曲线与推理速度柱状图。",
                x: 95.0,
                y: 68.0,
                box: { x: 19.5, y: 56.8, width: 78.8, height: 42.4 },
              },
            ],
          },
        ],
      },
    ],
  },
  {
    id: "models",
    groupId: "evaluation",
    title: "模型仓库与 ONNX 导出",
    shortTitle: "模型仓库",
    summary:
      "按项目集中归档与迭代 YOLO 模型版本（v1、v2…）：支持直接上传外部训练的 .pt 权重或接收平台训练产出，下载时支持原始 PT 及训练节点自动转换并缓存复用的 ONNX。",
    moduleId: "models",
    targetPath: "/models",
    targetLabel: "模型仓库",
    keywords: [
      "模型仓库",
      "上传模型",
      ".pt",
      "ONNX",
      "下载",
      "双检测头",
      "raw",
      "nms",
      "end2end",
      "opset",
      "SHA256",
    ],
    sections: [
      {
        id: "models-list-upload",
        title: "模型列表与上传外部 .pt 权重",
        summary:
          "同一项目内模型版本号只增不减（v1、v2…，删除后不复用）。上传外部 .pt 时，平台只读解析网络结构、类别与训练指标，不执行 pickle 代码，安全可靠。",
        figures: [
          {
            id: "fig-models-list",
            src: modelsListImg,
            alt: "模型仓库列表页面截图",
            caption:
              "模型仓库列表：上传模型入口、资产统计、筛选栏与项目模型版本表",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "上传模型按钮",
                description:
                  "支持上传最大 8 GiB 的 Ultralytics YOLO .pt 文件（YOLOv8 / v9 / v10 / 11 / 12 / 26），8 MiB 分块断点续传；若所选 YOLO 版本与文件内识别结果不一致，系统会提示改选后直接保存，无需重新上传。",
                x: 90.5,
                y: 10.9,
                box: { x: 91.0, y: 8.8, width: 7.4, height: 4.2 },
              },
              {
                index: 2,
                label: "模型数量与 ONNX 缓存统计",
                description:
                  "汇总平台训练与手动上传的模型总数、已缓存的 ONNX 导出结果数以及总权重占用空间。",
                x: 20.2,
                y: 20.6,
                box: { x: 19.3, y: 19.8, width: 79.1, height: 15.6 },
              },
              {
                index: 3,
                label: "按名称、项目与 YOLO 版本筛选",
                description:
                  "支持按项目、说明文字或原始文件名快速检索模型版本。",
                x: 63.2,
                y: 40.2,
                box: { x: 19.4, y: 38.0, width: 43.0, height: 4.4 },
              },
              {
                index: 4,
                label: "项目模型版本行",
                description:
                  "展示版本号、YOLO 架构规模、任务类型、输入尺寸、大小与来源（平台训练 / 手动上传）；点击进入模型详情或通过右侧「…」快捷下载。",
                x: 20.4,
                y: 50.4,
                box: { x: 19.5, y: 49.2, width: 78.8, height: 13.0 },
              },
            ],
          },
        ],
      },
      {
        id: "model-detail-onnx",
        title: "模型详情、下游闭环与 ONNX 自动转换",
        figures: [
          {
            id: "fig-model-detail",
            src: modelDetailImg,
            alt: "模型详情页面截图，展示操作按钮、训练指标与文件网络结构",
            caption:
              "模型详情：微调/审图/评估联动按钮、PT/ONNX 下载与解析元信息",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "全链路下游快捷操作与下载",
                description:
                  "一键「断点续训 / 微调」（基于此权重发起新训练）、「用这个模型审图」、「评估此模型」、「下载 PT」（直接下载原始权重）或「下载 ONNX」（触发或复用节点转换）。",
                x: 67.0,
                y: 18.4,
                box: { x: 19.3, y: 16.2, width: 47.0, height: 4.4 },
              },
              {
                index: 2,
                label: "核心架构与 ONNX 缓存概览卡",
                description:
                  "清晰展示 YOLO 版本（含双检测头提示）、任务类型、默认训练尺寸、参数量、类别数与已缓存的 ONNX 转换数。",
                x: 20.2,
                y: 28.0,
                box: { x: 19.4, y: 27.0, width: 78.9, height: 19.0 },
              },
              {
                index: 3,
                label: "内置训练指标与来源追溯",
                description:
                  "展示解析自权重文件的 mAP50 / mAP50-95 / P / R、训练轮数与 Ultralytics 版本；平台训练产出的模型附有来源训练任务直达链接。",
                x: 20.4,
                y: 49.2,
                box: { x: 19.4, y: 48.2, width: 38.8, height: 48.0 },
              },
              {
                index: 4,
                label: "文件校验（SHA256）与检测头结构",
                description:
                  "提供下载文件名、字节大小与可复制的 SHA256 校验值；向下滚动还可查看完整类别编号列表及历史 ONNX 转换缓存记录（同参数二次下载秒级返回）。",
                x: 60.4,
                y: 49.2,
                box: { x: 59.4, y: 48.2, width: 38.9, height: 48.0 },
              },
            ],
          },
        ],
        table: {
          caption:
            "下载 ONNX 时的后处理方式选择指南（点击「下载 ONNX」弹出配置）",
          headers: [
            "后处理模式",
            "输出张量（640×640 检测）",
            "适用场景与注意事项",
          ],
          rows: [
            [
              "raw（原始输出）",
              "1 × (4 + 类别数) × 8400",
              "one-to-many 检测头原始输出，优先保证精度；部署端（C++/TensorRT/OpenVINO）自行执行 NMS，支持动态 batch/高宽。",
            ],
            [
              "nms（嵌入 NMS）",
              "1 × 300 × 6 (x1,y1,x2,y2,conf,cls)",
              "将 NMS 算子编入 ONNX 图内，推理端无需手写 NMS；因 Ultralytics 展开机制限制，仅支持固定 batch=1（不可与动态尺寸同选）。",
            ],
            [
              "end2end（端到端）",
              "1 × 300 × 6",
              "仅适用于 YOLOv10 / YOLO26 双检测头或端到端模型，无需 NMS 且延迟更低。",
            ],
          ],
        },
        tips: [
          {
            variant: "tip",
            title: "关闭下载窗口不会取消 ONNX 转换",
            content:
              "ONNX 转换由在线训练节点在后台 CPU 上完成并用 ONNX Runtime 自动验证（无需 GPU）。转换期间可以安全关闭弹窗，稍后在模型详情页底部的「ONNX 转换记录」中直接下载已缓存的文件。",
          },
        ],
      },
    ],
  },
]
