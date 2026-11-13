package training

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

// Initialization modes of a training job.
const (
	InitScratch  = "scratch"
	InitFinetune = "finetune"
	InitResume   = "resume"
)

// Parameter group keys.
const (
	GroupTrain           = "train"
	GroupHyperparameters = "hyperparameters"
	GroupAugmentation    = "augmentation"
	GroupVal             = "val"
)

// ModelScale describes an architecture scale of a YOLO version (e.g. n, s, c).
type ModelScale struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var (
	standardScales = []ModelScale{
		{Value: "n", Label: "Nano (n)", Description: "最轻量，推理最快，适合边缘设备"},
		{Value: "s", Label: "Small (s)", Description: "轻量级，兼顾速度与精度"},
		{Value: "m", Label: "Medium (m)", Description: "中等规模，通用工业检测推荐"},
		{Value: "l", Label: "Large (l)", Description: "大规模，适合高复杂度场景"},
		{Value: "x", Label: "XLarge (x)", Description: "最大规模，追求最高检测精度"},
	}
	yolov9Scales = []ModelScale{
		{Value: "t", Label: "Tiny (t)", Description: "最轻量 GELAN 结构，适合低算力环境"},
		{Value: "s", Label: "Small (s)", Description: "轻量级 GELAN 结构，兼顾速度与精度"},
		{Value: "m", Label: "Medium (m)", Description: "中等规模 GELAN 结构"},
		{Value: "c", Label: "Compact (c)", Description: "YOLOv9 标准主力结构，精度与效率均衡"},
		{Value: "e", Label: "Extended (e)", Description: "最大规模 GELAN 结构，追求最高精度"},
	}
)

// AllowedScales returns the official Ultralytics model scales of yoloVersion.
func AllowedScales(yoloVersion string) []ModelScale {
	if yoloVersion == "yolov9" {
		return slices.Clone(yolov9Scales)
	}
	return slices.Clone(standardScales)
}

// DefaultScale returns the recommended default scale of yoloVersion.
func DefaultScale(yoloVersion string) string {
	if yoloVersion == "yolov9" {
		return "t"
	}
	return "n"
}

// ArchYAML returns the Ultralytics model architecture YAML name for building
// a model from scratch, e.g. "yolo11n.yaml" or "yolov9c.yaml".
func ArchYAML(yoloVersion, scale string) (string, error) {
	if _, ok := model.LookupYOLOVersion(yoloVersion); !ok {
		return "", newError(ErrInvalid, "不支持的 YOLO 版本：%s", yoloVersion)
	}
	scales := AllowedScales(yoloVersion)
	ok := false
	for _, sc := range scales {
		if sc.Value == scale {
			ok = true
			break
		}
	}
	if !ok {
		vals := make([]string, len(scales))
		for i, sc := range scales {
			vals[i] = sc.Value
		}
		return "", newError(ErrInvalid, "%s 的模型规模必须是 %s 之一", yoloVersion, strings.Join(vals, "、"))
	}
	return yoloVersion + scale + ".yaml", nil
}

// ParamOption is a selectable value of an enum or hybrid parameter.
type ParamOption struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

// ParamSpec describes one editable Ultralytics YOLO configuration parameter
// and its official default value from ultralytics/cfg/default.yaml (8.4.198).
type ParamSpec struct {
	Key         string        `json:"key"`
	Label       string        `json:"label"`
	Group       string        `json:"group"`
	Type        string        `json:"type"` // int, float, nullable_float, bool, nullable_bool, string, enum, batch, cache, compile, freeze, int_list, quantize
	Default     any           `json:"default"`
	Min         *float64      `json:"min,omitempty"`
	Max         *float64      `json:"max,omitempty"`
	Step        *float64      `json:"step,omitempty"`
	Options     []ParamOption `json:"options,omitempty"`
	Description string        `json:"description"`
	TaskScope   string        `json:"task_scope,omitempty"` // "", "detect", "segment", "classify", "pose", "obb", "depth"
}

// ParamGroupMeta describes a UI section of YOLO parameters.
type ParamGroupMeta struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ParamsSchemaResponse is returned by GET /api/v1/training/params-schema.
type ParamsSchemaResponse struct {
	Groups   []ParamGroupMeta        `json:"groups"`
	Specs    []ParamSpec             `json:"specs"`
	Defaults map[string]any          `json:"defaults"`
	Scales   map[string][]ModelScale `json:"scales"`
}

func fptr(v float64) *float64 { return &v }

var paramGroups = []ParamGroupMeta{
	{
		Key:         GroupTrain,
		Label:       "核心训练设置",
		Description: "训练轮数、批次大小、图像尺寸、设备、优化器及检查点策略。",
	},
	{
		Key:         GroupHyperparameters,
		Label:       "优化器与损失超参数",
		Description: "初始/最终学习率、动量、权重衰减、预热策略及各项损失函数权重。",
	},
	{
		Key:         GroupAugmentation,
		Label:       "在线数据增强",
		Description: "训练过程中的颜色空间扰动、几何变换、Mosaic、MixUp 与 Copy-Paste 等实时增强参数。",
	},
	{
		Key:         GroupVal,
		Label:       "验证与可视化设置",
		Description: "训练期间的验证集评估阈值、NMS 策略及曲线图输出选项。",
	},
}

// paramSpecs lists all 86 tunable Ultralytics 8.4.198 configuration parameters
// with their exact official default values from ultralytics/cfg/default.yaml.
var paramSpecs = []ParamSpec{
	// ---- 1. Core Training Settings (31) ----
	{Key: "epochs", Label: "训练轮数 (epochs)", Group: GroupTrain, Type: "int", Default: 100, Min: fptr(1), Max: fptr(10000), Step: fptr(1), Description: "总训练轮数。若同时设置了最大训练时长 time，将以先到达者为准。"},
	{Key: "time", Label: "最大训练时长 (time, 小时)", Group: GroupTrain, Type: "nullable_float", Default: nil, Min: fptr(0.01), Max: fptr(8760), Step: fptr(0.5), Description: "最大训练小时数；留空（null）表示不按时间限制，仅受 epochs 控制。"},
	{Key: "patience", Label: "早停耐心轮数 (patience)", Group: GroupTrain, Type: "int", Default: 100, Min: fptr(0), Max: fptr(10000), Step: fptr(1), Description: "验证集指标连续多少轮无提升时提前停止训练；设为 0 表示禁用早停。"},
	{Key: "batch", Label: "批次大小 (batch)", Group: GroupTrain, Type: "batch", Default: 16, Min: fptr(-1), Max: fptr(4096), Step: fptr(1), Description: "正整数表示固定批大小（默认 16）；-1 表示按 60% 显存自动计算；(0, 1) 小数表示按指定显存占比自动计算。"},
	{Key: "imgsz", Label: "输入图像尺寸 (imgsz)", Group: GroupTrain, Type: "imgsz", Default: 640, Min: fptr(32), Max: fptr(4096), Step: fptr(32), Description: "训练与验证输入图像的边长（像素，如 640，或 [高, 宽]），需为 32 的倍数，官方默认 640。"},
	{Key: "optimizer", Label: "优化器 (optimizer)", Group: GroupTrain, Type: "enum", Default: "auto", Options: []ParamOption{
		{Value: "auto", Label: "auto（按模型与迭代数自动选择）"},
		{Value: "SGD", Label: "SGD"},
		{Value: "MuSGD", Label: "MuSGD"},
		{Value: "Adam", Label: "Adam"},
		{Value: "AdamW", Label: "AdamW"},
		{Value: "Adamax", Label: "Adamax"},
		{Value: "NAdam", Label: "NAdam"},
		{Value: "RAdam", Label: "RAdam"},
		{Value: "RMSProp", Label: "RMSProp"},
	}, Description: "设为 auto 时 Ultralytics 会自动选择优化器并计算 lr0 与 momentum；手动指定时使用下方设置的 lr0 与 momentum。"},
	{Key: "cos_lr", Label: "余弦退火学习率 (cos_lr)", Group: GroupTrain, Type: "bool", Default: false, Description: "使用余弦学习率调度替代默认的线性衰减调度。"},
	{Key: "amp", Label: "自动混合精度 (amp)", Group: GroupTrain, Type: "bool", Default: true, Description: "启用混合精度训练（AMP），可显著节省显存并提升 GPU 训练速度。"},
	{Key: "device", Label: "训练设备 (device)", Group: GroupTrain, Type: "string", Default: nil, Description: "留空自动选择可用 GPU（无 GPU 时回退 CPU）；也可填 0、0,1、cpu、mps 等。"},
	{Key: "workers", Label: "数据加载线程数 (workers)", Group: GroupTrain, Type: "int", Default: 8, Min: fptr(0), Max: fptr(64), Step: fptr(1), Description: "DataLoader 工作进程数（Windows 或共享内存较小时可调低为 0–4）。"},
	{Key: "cache", Label: "图像缓存策略 (cache)", Group: GroupTrain, Type: "cache", Default: false, Options: []ParamOption{
		{Value: false, Label: "false（不缓存）"},
		{Value: "ram", Label: "ram（缓存在内存，最快）"},
		{Value: "disk", Label: "disk（缓存在磁盘 .npy）"},
		{Value: true, Label: "true（同 ram）"},
	}, Description: "是否在训练前将数据集图像缓存至内存或磁盘以加速读取。"},
	{Key: "pretrained", Label: "启用预训练权重迁移 (pretrained)", Group: GroupTrain, Type: "bool", Default: true, Description: "选择基础模型微调时是否迁移预训练权重；从头构建 YAML 网络结构时始终随机初始化。"},
	{Key: "cls_remap", Label: "自动类别重映射 (cls_remap)", Group: GroupTrain, Type: "bool", Default: true, Description: "按数据集类别自动重映射预训练分类头权重。"},
	{Key: "resume", Label: "检查点断点续训 (resume)", Group: GroupTrain, Type: "bool", Default: false, Description: "是否恢复上一个检查点的优化器与轮数状态（由任务初始化模式自动联动）。"},
	{Key: "save", Label: "保存训练检查点 (save)", Group: GroupTrain, Type: "bool", Default: true, Description: "保存训练检查点与最终模型权重（best.pt / last.pt）。"},
	{Key: "save_period", Label: "周期保存间隔 (save_period)", Group: GroupTrain, Type: "int", Default: -1, Min: fptr(-1), Max: fptr(10000), Step: fptr(1), Description: "每隔多少轮额外保存一次 epochN.pt 检查点；小于 1（默认 -1）表示禁用。"},
	{Key: "close_mosaic", Label: "末尾关闭 Mosaic 轮数 (close_mosaic)", Group: GroupTrain, Type: "int", Default: 10, Min: fptr(0), Max: fptr(10000), Step: fptr(1), Description: "在最后 N 轮关闭 Mosaic 拼接增强以提升收敛精度；0 表示全程开启。"},
	{Key: "seed", Label: "随机种子 (seed)", Group: GroupTrain, Type: "int", Default: 0, Min: fptr(0), Max: fptr(2147483647), Step: fptr(1), Description: "训练随机种子，配合 deterministic 可保证结果复现。"},
	{Key: "deterministic", Label: "确定性训练 (deterministic)", Group: GroupTrain, Type: "bool", Default: true, Description: "强制使用确定性算法以保证相同种子下训练结果可复现。"},
	{Key: "single_cls", Label: "单类别模式 (single_cls)", Group: GroupTrain, Type: "bool", Default: false, Description: "将多类别数据集中的所有目标合并为单一类别进行训练。"},
	{Key: "rect", Label: "矩形训练 (rect)", Group: GroupTrain, Type: "bool", Default: false, Description: "按图像长宽比对批次排序并最小化填充区域。"},
	{Key: "multi_scale", Label: "多尺度训练幅度 (multi_scale)", Group: GroupTrain, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(0.9), Step: fptr(0.05), Description: "每批次随机缩放图像尺寸的比例幅度（0.0 禁用，例如 0.5 表示在 ±50% 范围内变化）。"},
	{Key: "fraction", Label: "训练集采样比例 (fraction)", Group: GroupTrain, Type: "float", Default: 1.0, Min: fptr(0.01), Max: fptr(1.0), Step: fptr(0.05), Description: "使用的训练集样本比例（1.0 使用全量训练数据，调小可用于快速调试）。"},
	{Key: "freeze", Label: "冻结网络层 (freeze)", Group: GroupTrain, Type: "freeze", Default: nil, Description: "留空不冻结；填正整数 N 冻结前 N 层，或填逗号分隔的层索引列表（如 0,1,2）。"},
	{Key: "compile", Label: "图编译加速 (compile)", Group: GroupTrain, Type: "compile", Default: false, Options: []ParamOption{
		{Value: false, Label: "false（禁用）"},
		{Value: true, Label: "true（默认编译模式）"},
		{Value: "default", Label: "default"},
		{Value: "reduce-overhead", Label: "reduce-overhead"},
		{Value: "max-autotune-no-cudagraphs", Label: "max-autotune-no-cudagraphs"},
	}, Description: "启用 PyTorch 2 torch.compile 图编译加速。"},
	{Key: "channels_last", Label: "NHWC 内存格式 (channels_last)", Group: GroupTrain, Type: "nullable_bool", Default: nil, Options: []ParamOption{
		{Value: nil, Label: "null（自动）"},
		{Value: true, Label: "true（启用）"},
		{Value: false, Label: "false（禁用）"},
	}, Description: "使用 channels_last (NHWC) 张量内存布局加速 Tensor Core 卷积。"},
	{Key: "profile", Label: "性能剖析 (profile)", Group: GroupTrain, Type: "bool", Default: false, Description: "训练期间记录 ONNX / TensorRT 算子耗时分析。"},
	{Key: "verbose", Label: "详细日志 (verbose)", Group: GroupTrain, Type: "bool", Default: true, Description: "输出详细的训练进度与验证指标日志。"},
	{Key: "overlap_mask", Label: "合并重叠掩码 (overlap_mask)", Group: GroupTrain, Type: "bool", Default: true, TaskScope: "segment", Description: "分割任务专用：将实例掩码合并至单张重叠图像训练。"},
	{Key: "mask_ratio", Label: "掩码下采样倍率 (mask_ratio)", Group: GroupTrain, Type: "int", Default: 4, Min: fptr(1), Max: fptr(16), Step: fptr(1), TaskScope: "segment", Description: "分割任务专用：训练掩码相对于输入图像的降采样比例。"},
	{Key: "dropout", Label: "分类 Dropout 概率 (dropout)", Group: GroupTrain, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(0.99), Step: fptr(0.05), TaskScope: "classify", Description: "分类任务专用：分类头的 Dropout 正则化概率。"},

	// ---- 2. Optimizer & Loss Hyperparameters (20) ----
	{Key: "lr0", Label: "初始学习率 (lr0)", Group: GroupHyperparameters, Type: "float", Default: 0.01, Min: fptr(1e-7), Max: fptr(10.0), Step: fptr(0.001), Description: "初始学习率（SGD 常用 0.01，Adam/AdamW 常用 0.001；optimizer=auto 时由框架自动覆盖）。"},
	{Key: "lrf", Label: "最终学习率比例 (lrf)", Group: GroupHyperparameters, Type: "float", Default: 0.01, Min: fptr(1e-5), Max: fptr(1.0), Step: fptr(0.01), Description: "最终学习率相对于初始学习率的比例（最终学习率 = lr0 × lrf）。"},
	{Key: "momentum", Label: "动量 / Beta1 (momentum)", Group: GroupHyperparameters, Type: "float", Default: 0.937, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.001), Description: "SGD 动量因子或 Adam 系列优化器的 beta1（optimizer=auto 时自动覆盖）。"},
	{Key: "weight_decay", Label: "权重衰减 (weight_decay)", Group: GroupHyperparameters, Type: "float", Default: 0.0005, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.0001), Description: "优化器 L2 正则化权重衰减系数。"},
	{Key: "warmup_epochs", Label: "预热轮数 (warmup_epochs)", Group: GroupHyperparameters, Type: "float", Default: 3.0, Min: fptr(0.0), Max: fptr(1000.0), Step: fptr(0.5), Description: "学习率线性预热的轮数（支持小数，0 表示不预热）。"},
	{Key: "warmup_momentum", Label: "预热初始动量 (warmup_momentum)", Group: GroupHyperparameters, Type: "float", Default: 0.8, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "学习率预热阶段的起始动量值。"},
	{Key: "warmup_bias_lr", Label: "预热偏置学习率 (warmup_bias_lr)", Group: GroupHyperparameters, Type: "float", Default: 0.1, Min: fptr(0.0), Max: fptr(10.0), Step: fptr(0.01), Description: "预热阶段偏置参数（bias）的起始学习率。"},
	{Key: "nbs", Label: "名义批次大小 (nbs)", Group: GroupHyperparameters, Type: "int", Default: 64, Min: fptr(1), Max: fptr(4096), Step: fptr(1), Description: "名义批大小（默认 64），用于根据实际 batch 自动换算梯度累积步数与权重衰减。"},
	{Key: "box", Label: "框回归损失权重 (box)", Group: GroupHyperparameters, Type: "float", Default: 7.5, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.5), Description: "边界框回归损失（IoU / CIoU Loss）的增益权重。"},
	{Key: "cls", Label: "分类损失权重 (cls)", Group: GroupHyperparameters, Type: "float", Default: 0.5, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), Description: "目标类别分类损失（BCE Loss）的增益权重。"},
	{Key: "cls_pw", Label: "分类正样本权重 (cls_pw)", Group: GroupHyperparameters, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), Description: "分类损失的 BCE 正样本权重（0.0 表示标准权重）。"},
	{Key: "dfl", Label: "分布焦点损失权重 (dfl)", Group: GroupHyperparameters, Type: "float", Default: 1.5, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), Description: "Distribution Focal Loss 边界框精细回归损失增益权重。"},
	{Key: "pose", Label: "姿态关键点损失权重 (pose)", Group: GroupHyperparameters, Type: "float", Default: 12.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.5), TaskScope: "pose", Description: "姿态任务专用：关键点位置回归损失增益权重。"},
	{Key: "kobj", Label: "关键点置信度损失权重 (kobj)", Group: GroupHyperparameters, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), TaskScope: "pose", Description: "姿态任务专用：关键点可见性 BCE 损失增益权重。"},
	{Key: "rle", Label: "残差对数似然损失权重 (rle)", Group: GroupHyperparameters, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), TaskScope: "pose", Description: "姿态任务专用：RLE 损失增益权重。"},
	{Key: "angle", Label: "旋转角损失权重 (angle)", Group: GroupHyperparameters, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), TaskScope: "obb", Description: "旋转框（OBB）任务专用：有向边界框角度损失增益权重。"},
	{Key: "dlog", Label: "深度对数损失权重 (dlog)", Group: GroupHyperparameters, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), TaskScope: "depth", Description: "深度估计任务专用：尺度不变对数损失权重。"},
	{Key: "dgrad", Label: "深度梯度损失权重 (dgrad)", Group: GroupHyperparameters, Type: "float", Default: 0.5, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.1), TaskScope: "depth", Description: "深度估计任务专用：空间梯度匹配损失权重。"},
	{Key: "dlam", Label: "深度方差因子 (dlam)", Group: GroupHyperparameters, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), TaskScope: "depth", Description: "深度估计任务专用：尺度不变方差项系数。"},
	{Key: "dis", Label: "知识蒸馏损失权重 (dis)", Group: GroupHyperparameters, Type: "float", Default: 6.0, Min: fptr(0.0), Max: fptr(100.0), Step: fptr(0.5), Description: "知识蒸馏损失增益权重。"},

	// ---- 3. Online Data Augmentation (18) ----
	{Key: "hsv_h", Label: "HSV 色相增强 (hsv_h)", Group: GroupAugmentation, Type: "float", Default: 0.015, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.005), Description: "图像色相（Hue）随机偏移比例（0.0–1.0）。"},
	{Key: "hsv_s", Label: "HSV 饱和度增强 (hsv_s)", Group: GroupAugmentation, Type: "float", Default: 0.7, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "图像饱和度（Saturation）随机调整比例（0.0–1.0）。"},
	{Key: "hsv_v", Label: "HSV 明度增强 (hsv_v)", Group: GroupAugmentation, Type: "float", Default: 0.4, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "图像明度（Value）随机调整比例（0.0–1.0）。"},
	{Key: "degrees", Label: "随机旋转角度 (degrees)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(180.0), Step: fptr(1.0), Description: "在 [-degrees, +degrees] 角度范围内随机旋转图像。"},
	{Key: "translate", Label: "随机平移比例 (translate)", Group: GroupAugmentation, Type: "float", Default: 0.1, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "水平与垂直方向随机平移图像尺寸的最大比例。"},
	{Key: "scale", Label: "随机缩放增益 (scale)", Group: GroupAugmentation, Type: "float", Default: 0.5, Min: fptr(0.0), Max: fptr(2.0), Step: fptr(0.05), Description: "图像随机缩放比例增益（例如 0.5 表示缩放范围为 0.5x–1.5x）。"},
	{Key: "shear", Label: "随机错切角度 (shear)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(180.0), Step: fptr(1.0), Description: "在 [-shear, +shear] 度范围内对图像做随机错切几何变换。"},
	{Key: "perspective", Label: "随机透视变换 (perspective)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(0.01), Step: fptr(0.0001), Description: "随机透视变换系数（建议范围 0.0–0.001）。"},
	{Key: "flipud", Label: "上下翻转概率 (flipud)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "以指定概率将图像上下垂直翻转。"},
	{Key: "fliplr", Label: "左右翻转概率 (fliplr)", Group: GroupAugmentation, Type: "float", Default: 0.5, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "以指定概率将图像左右水平翻转（官方默认 0.5）。"},
	{Key: "bgr", Label: "BGR 通道翻转概率 (bgr)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "以指定概率交换 RGB 与 BGR 颜色通道。"},
	{Key: "mosaic", Label: "Mosaic 拼接概率 (mosaic)", Group: GroupAugmentation, Type: "float", Default: 1.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "四图拼接增强概率，显著提升小目标与复杂背景泛化能力（默认 1.0）。"},
	{Key: "mixup", Label: "MixUp 混合概率 (mixup)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "按透明度混合两张图像及其标注框的概率。"},
	{Key: "cutmix", Label: "CutMix 裁剪混合概率 (cutmix)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), Description: "将一张图像的矩形区域裁剪粘贴到另一张图像的概率。"},
	{Key: "copy_paste", Label: "Copy-Paste 增强概率 (copy_paste)", Group: GroupAugmentation, Type: "float", Default: 0.0, Min: fptr(0.0), Max: fptr(1.0), Step: fptr(0.05), TaskScope: "segment", Description: "分割任务专用：跨图像复制粘贴目标实例的概率。"},
	{Key: "copy_paste_mode", Label: "Copy-Paste 模式 (copy_paste_mode)", Group: GroupAugmentation, Type: "enum", Default: "flip", TaskScope: "segment", Options: []ParamOption{
		{Value: "flip", Label: "flip（同图翻转粘贴）"},
		{Value: "mixup", Label: "mixup（跨图混合粘贴）"},
	}, Description: "分割任务专用：Copy-Paste 增强的实现策略。"},
	{Key: "auto_augment", Label: "分类自动增强策略 (auto_augment)", Group: GroupAugmentation, Type: "enum", Default: "randaugment", TaskScope: "classify", Options: []ParamOption{
		{Value: "randaugment", Label: "randaugment"},
		{Value: "autoaugment", Label: "autoaugment"},
		{Value: "augmix", Label: "augmix"},
		{Value: "", Label: "禁用"},
	}, Description: "分类任务专用：自动增强策略名称。"},
	{Key: "erasing", Label: "随机擦除概率 (erasing)", Group: GroupAugmentation, Type: "float", Default: 0.4, Min: fptr(0.0), Max: fptr(0.9), Step: fptr(0.05), TaskScope: "classify", Description: "分类任务专用：随机擦除图像矩形区域的概率（0.0–0.9）。"},

	// ---- 4. Validation & Plotting Settings (17) ----
	{Key: "val", Label: "开启训练期验证 (val)", Group: GroupVal, Type: "bool", Default: true, Description: "每轮训练结束后在验证集上评估指标并选择最佳权重 best.pt。"},
	{Key: "split", Label: "验证集划分 (split)", Group: GroupVal, Type: "enum", Default: "val", Options: []ParamOption{
		{Value: "val", Label: "val（验证集）"},
		{Value: "test", Label: "test（测试集）"},
		{Value: "train", Label: "train（训练集）"},
	}, Description: "训练期评估使用的划分（默认 val；选 test 时数据版本需包含测试集）。"},
	{Key: "iou", Label: "验证 NMS IoU 阈值 (iou)", Group: GroupVal, Type: "float", Default: 0.7, Min: fptr(0.01), Max: fptr(1.0), Step: fptr(0.05), Description: "验证阶段非极大值抑制（NMS）的 IoU 阈值，官方默认 0.7。"},
	{Key: "conf", Label: "验证置信度阈值 (conf)", Group: GroupVal, Type: "nullable_float", Default: nil, Min: fptr(0.0001), Max: fptr(1.0), Step: fptr(0.01), Description: "验证阶段的最低检测置信度；留空（null）使用 Ultralytics 验证默认值 0.001。"},
	{Key: "max_det", Label: "单图最大检测数 (max_det)", Group: GroupVal, Type: "int", Default: 300, Min: fptr(1), Max: fptr(10000), Step: fptr(10), Description: "验证时每张图像最多保留的检测框数量。"},
	{Key: "plots", Label: "生成评估图表 (plots)", Group: GroupVal, Type: "bool", Default: true, Description: "生成训练损失曲线、PR/F1 曲线、混淆矩阵及验证批次检测对比图。"},
	{Key: "save_json", Label: "输出 COCO JSON 结果 (save_json)", Group: GroupVal, Type: "bool", Default: false, Description: "将最终验证预测结果保存为 COCO 格式的 predictions.json。"},
	{Key: "nms", Label: "验证启用 NMS (nms)", Group: GroupVal, Type: "nullable_bool", Default: nil, Options: []ParamOption{
		{Value: nil, Label: "null（按模型架构自动决定）"},
		{Value: true, Label: "true（强制启用）"},
		{Value: false, Label: "false（禁用）"},
	}, Description: "端到端模型（YOLOv10/YOLO26）默认无需 NMS，设为 null 自动适配。"},
	{Key: "agnostic_nms", Label: "类别无关 NMS (agnostic_nms)", Group: GroupVal, Type: "bool", Default: false, Description: "跨不同类别执行非极大值抑制，抑制不同类别间的高度重叠框。"},
	{Key: "augment", Label: "测试时增强验证 (augment)", Group: GroupVal, Type: "bool", Default: false, Description: "验证评估时启用测试时数据增强（TTA），会增加验证耗时。"},
	{Key: "retina_masks", Label: "高分辨率掩码 (retina_masks)", Group: GroupVal, Type: "bool", Default: false, TaskScope: "segment", Description: "分割任务专用：在验证阶段输出原图分辨率的高精度分割掩码。"},
	{Key: "dnn", Label: "OpenCV DNN 后端 (dnn)", Group: GroupVal, Type: "bool", Default: false, Description: "ONNX 模型验证时是否使用 OpenCV DNN 模块推理。"},
	{Key: "quantize", Label: "验证量化精度 (quantize)", Group: GroupVal, Type: "quantize", Default: nil, Options: []ParamOption{
		{Value: nil, Label: "null（不量化）"},
		{Value: "fp16", Label: "fp16（半精度）"},
		{Value: "int8", Label: "int8（8 位整数量化）"},
	}, Description: "验证评估时的量化模式（留空为默认精度）。"},
	{Key: "classes", Label: "指定评估类别 ID (classes)", Group: GroupVal, Type: "int_list", Default: nil, Description: "仅评估指定的类别编号列表（如 0,2）；留空表示评估全部类别。"},
	{Key: "save_txt", Label: "保存验证预测文本 (save_txt)", Group: GroupVal, Type: "bool", Default: false, Description: "将验证集预测结果另存为 YOLO txt 标注文件。"},
	{Key: "save_conf", Label: "预测文本包含置信度 (save_conf)", Group: GroupVal, Type: "bool", Default: false, Description: "配合 save_txt 在输出的 txt 中附带置信度分数。"},
	{Key: "line_width", Label: "可视化框线宽度 (line_width)", Group: GroupVal, Type: "nullable_int", Default: nil, Min: fptr(1), Max: fptr(20), Step: fptr(1), Description: "验证预览图中边界框线条像素宽度；留空按图像尺寸自动计算。"},
}

var (
	paramSpecByKey = func() map[string]ParamSpec {
		m := make(map[string]ParamSpec, len(paramSpecs))
		for _, sp := range paramSpecs {
			m[sp.Key] = sp
		}
		return m
	}()

	reservedKeys = map[string]string{
		"mode":          "mode 由平台固定为 train",
		"task":          "task 由数据版本与基础模型决定",
		"model":         "模型架构或基础权重请在表单顶部的“模型配置”中选择",
		"data":          "训练数据集请在表单顶部的“数据版本”中选择",
		"project":       "输出目录由训练节点自动托管",
		"name":          "任务名称请在基本信息中填写",
		"exist_ok":      "输出目录由训练节点自动托管",
		"cfg":           "请直接编辑训练参数，不支持外部宿主机 cfg 路径",
		"source":        "source 仅用于推理模式",
		"distill_model": "不支持直接填写宿主机模型路径",
	}

	devicePattern = regexp.MustCompile(`^[A-Za-z0-9_,:\-\s]{1,64}$`)
)

// DefaultParams returns a fresh map of all 86 YOLO parameters initialized to
// their official Ultralytics default values.
func DefaultParams() map[string]any {
	out := make(map[string]any, len(paramSpecs))
	for _, sp := range paramSpecs {
		out[sp.Key] = sp.Default
	}
	return out
}

// ParamsSchema returns the full parameter catalog, groups, defaults, and model
// scales for the frontend.
func ParamsSchema() ParamsSchemaResponse {
	scales := make(map[string][]ModelScale, len(model.YOLOVersions))
	for _, v := range model.YOLOVersions {
		scales[v.Value] = AllowedScales(v.Value)
	}
	return ParamsSchemaResponse{
		Groups:   slices.Clone(paramGroups),
		Specs:    slices.Clone(paramSpecs),
		Defaults: DefaultParams(),
		Scales:   scales,
	}
}

// NormalizeParams overlays raw onto DefaultParams(), validates every parameter
// key, type, and range, and aligns resume with initMode.
func NormalizeParams(raw map[string]any, initMode string) (map[string]any, error) {
	out := DefaultParams()
	for k, v := range raw {
		if reason, reserved := reservedKeys[k]; reserved {
			return nil, newError(ErrInvalid, "参数 %s 不可直接指定：%s", k, reason)
		}
		sp, ok := paramSpecByKey[k]
		if !ok {
			return nil, newError(ErrInvalid, "不支持的 YOLO 训练参数：%s", k)
		}
		norm, err := normalizeOneParam(sp, v)
		if err != nil {
			return nil, err
		}
		out[k] = norm
	}

	// Align imgsz stride (must be multiple of 32).
	if imgsz, ok := out["imgsz"].(int); ok && imgsz%32 != 0 {
		return nil, newError(ErrInvalid, "参数 imgsz（%d）必须是 32 的倍数（如 320、640、1280）", imgsz)
	}

	// Enforce resume consistency with initMode.
	if initMode == InitResume {
		out["resume"] = true
	} else {
		out["resume"] = false
	}

	return out, nil
}

func normalizeOneParam(sp ParamSpec, raw any) (any, error) {
	switch sp.Type {
	case "bool":
		b, ok := raw.(bool)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是布尔值（true 或 false）", sp.Key)
		}
		return b, nil

	case "nullable_bool":
		if raw == nil {
			return nil, nil
		}
		if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
			return nil, nil
		}
		b, ok := raw.(bool)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是 true、false 或 null", sp.Key)
		}
		return b, nil

	case "int":
		n, ok := toInt(raw)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是整数", sp.Key)
		}
		if err := checkRange(sp, float64(n)); err != nil {
			return nil, err
		}
		return n, nil

	case "nullable_int":
		if raw == nil {
			return nil, nil
		}
		if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
			return nil, nil
		}
		n, ok := toInt(raw)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是整数或 null", sp.Key)
		}
		if err := checkRange(sp, float64(n)); err != nil {
			return nil, err
		}
		return n, nil

	case "float":
		f, ok := toFloat(raw)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是数值", sp.Key)
		}
		if err := checkRange(sp, f); err != nil {
			return nil, err
		}
		return f, nil

	case "nullable_float":
		if raw == nil {
			return nil, nil
		}
		if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
			return nil, nil
		}
		f, ok := toFloat(raw)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是数值或 null", sp.Key)
		}
		if err := checkRange(sp, f); err != nil {
			return nil, err
		}
		return f, nil

	case "string":
		if raw == nil {
			return nil, nil
		}
		if sp.Key == "device" {
			if n, ok := toInt(raw); ok && n >= 0 && n <= 64 {
				return fmt.Sprintf("%d", n), nil
			}
		}
		s, ok := raw.(string)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是字符串或 null", sp.Key)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		if sp.Key == "device" && !devicePattern.MatchString(s) {
			return nil, newError(ErrInvalid, "参数 device 格式无效（示例：0、0,1、cpu、mps）")
		}
		return s, nil

	case "imgsz":
		if n, ok := toInt(raw); ok {
			if n < 32 || n > 4096 || n%32 != 0 {
				return nil, newError(ErrInvalid, "参数 imgsz（%d）必须是 32–4096 之间且为 32 的倍数（如 320、640、1280）", n)
			}
			return n, nil
		}
		if arr, ok := raw.([]any); ok && len(arr) == 2 {
			h, okH := toInt(arr[0])
			w, okW := toInt(arr[1])
			if !okH || !okW || h < 32 || h > 4096 || w < 32 || w > 4096 || h%32 != 0 || w%32 != 0 {
				return nil, newError(ErrInvalid, "参数 imgsz 数组元素必须是 32–4096 之间且为 32 的倍数")
			}
			return []int{h, w}, nil
		}
		if arr, ok := raw.([]int); ok && len(arr) == 2 {
			if arr[0] < 32 || arr[0] > 4096 || arr[1] < 32 || arr[1] > 4096 || arr[0]%32 != 0 || arr[1]%32 != 0 {
				return nil, newError(ErrInvalid, "参数 imgsz 数组元素必须是 32–4096 之间且为 32 的倍数")
			}
			return []int{arr[0], arr[1]}, nil
		}
		return nil, newError(ErrInvalid, "参数 imgsz 必须是 32 的倍数整数或 [高, 宽] 二元整数数组")

	case "enum":
		if raw == nil {
			for _, opt := range sp.Options {
				if opt.Value == nil || opt.Value == "" {
					return opt.Value, nil
				}
			}
			return nil, newError(ErrInvalid, "参数 %s 不能为空", sp.Key)
		}
		s, ok := raw.(string)
		if !ok {
			return nil, newError(ErrInvalid, "参数 %s 必须是字符串选项", sp.Key)
		}
		for _, opt := range sp.Options {
			if optStr, isStr := opt.Value.(string); isStr && optStr == s {
				return s, nil
			}
		}
		allowed := make([]string, 0, len(sp.Options))
		for _, opt := range sp.Options {
			allowed = append(allowed, fmt.Sprintf("%v", opt.Value))
		}
		return nil, newError(ErrInvalid, "参数 %s 的值无效，允许值为：%s", sp.Key, strings.Join(allowed, "、"))

	case "batch":
		if n, ok := toInt(raw); ok {
			if n == -1 || (n >= 1 && n <= 4096) {
				return n, nil
			}
			return nil, newError(ErrInvalid, "参数 batch 为整数时必须是 -1（自动）或 1–4096 之间的正整数")
		}
		if f, ok := toFloat(raw); ok {
			if f > 0.0 && f < 1.0 {
				return f, nil
			}
			return nil, newError(ErrInvalid, "参数 batch 为小数时必须在 (0, 1) 之间（表示显存占比，如 0.7）")
		}
		return nil, newError(ErrInvalid, "参数 batch 必须是正整数、-1 或 (0, 1) 之间的小数")

	case "cache":
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "false", "":
				return false, nil
			case "true":
				return true, nil
			case "ram":
				return "ram", nil
			case "disk":
				return "disk", nil
			}
		}
		return nil, newError(ErrInvalid, "参数 cache 必须是 false、true、ram 或 disk")

	case "compile":
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			s := strings.TrimSpace(v)
			switch s {
			case "false", "":
				return false, nil
			case "true":
				return true, nil
			case "default", "reduce-overhead", "max-autotune-no-cudagraphs":
				return s, nil
			}
		}
		return nil, newError(ErrInvalid, "参数 compile 必须是 false、true、default、reduce-overhead 或 max-autotune-no-cudagraphs")

	case "quantize":
		if raw == nil {
			return nil, nil
		}
		switch v := raw.(type) {
		case bool:
			if !v {
				return nil, nil
			}
			return "fp16", nil
		case string:
			s := strings.ToLower(strings.TrimSpace(v))
			switch s {
			case "", "null", "none", "false":
				return nil, nil
			case "int8", "fp16", "fp32":
				return s, nil
			}
		}
		if n, ok := toInt(raw); ok && (n == 8 || n == 16 || n == 32) {
			return n, nil
		}
		return nil, newError(ErrInvalid, "参数 quantize 必须是 null、fp16 或 int8")

	case "freeze":
		if raw == nil {
			return nil, nil
		}
		if s, ok := raw.(string); ok {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil, nil
			}
			if !strings.Contains(s, ",") {
				var single int
				if _, err := fmt.Sscanf(s, "%d", &single); err == nil && fmt.Sprintf("%d", single) == s {
					if single < 0 || single > 256 {
						return nil, newError(ErrInvalid, "参数 freeze 冻结层数必须在 0–256 之间")
					}
					if single == 0 {
						return nil, nil
					}
					return single, nil
				}
			}
			list, err := parseIntCSV(s, 0, 256, "freeze")
			if err != nil {
				return nil, err
			}
			return list, nil
		}
		if n, ok := toInt(raw); ok {
			if n < 0 || n > 256 {
				return nil, newError(ErrInvalid, "参数 freeze 冻结层数必须在 0–256 之间")
			}
			if n == 0 {
				return nil, nil
			}
			return n, nil
		}
		if arr, ok := raw.([]any); ok {
			if len(arr) == 0 {
				return nil, nil
			}
			if len(arr) > 256 {
				return nil, newError(ErrInvalid, "参数 freeze 列表过长")
			}
			out := make([]int, len(arr))
			for i, item := range arr {
				n, ok := toInt(item)
				if !ok || n < 0 || n > 256 {
					return nil, newError(ErrInvalid, "参数 freeze 列表元素必须是 0–256 的整数")
				}
				out[i] = n
			}
			return out, nil
		}
		return nil, newError(ErrInvalid, "参数 freeze 必须是正整数、整数数组或 null")

	case "int_list":
		if raw == nil {
			return nil, nil
		}
		if s, ok := raw.(string); ok {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil, nil
			}
			return parseIntCSV(s, 0, 10000, sp.Key)
		}
		if arr, ok := raw.([]any); ok {
			if len(arr) == 0 {
				return nil, nil
			}
			if len(arr) > 1000 {
				return nil, newError(ErrInvalid, "参数 %s 列表过长", sp.Key)
			}
			out := make([]int, len(arr))
			for i, item := range arr {
				n, ok := toInt(item)
				if !ok || n < 0 || n > 10000 {
					return nil, newError(ErrInvalid, "参数 %s 列表元素必须是非负整数", sp.Key)
				}
				out[i] = n
			}
			return out, nil
		}
		return nil, newError(ErrInvalid, "参数 %s 必须是整数数组或 null", sp.Key)
	}

	return nil, newError(ErrInvalid, "参数 %s 类型不受支持", sp.Key)
}

func checkRange(sp ParamSpec, v float64) error {
	if sp.Min != nil && v < *sp.Min {
		return newError(ErrInvalid, "参数 %s 不能小于 %v", sp.Key, *sp.Min)
	}
	if sp.Max != nil && v > *sp.Max {
		return newError(ErrInvalid, "参数 %s 不能大于 %v", sp.Key, *sp.Max)
	}
	return nil
}

func parseIntCSV(s string, lo, hi int, key string) ([]int, error) {
	parts := strings.Split(s, ",")
	if len(parts) > 1000 {
		return nil, newError(ErrInvalid, "参数 %s 列表过长", key)
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || fmt.Sprintf("%d", n) != p || n < lo || n > hi {
			return nil, newError(ErrInvalid, "参数 %s 包含无效整数：%s", key, p)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x < -2147483648 || x > 2147483647 {
			return 0, false
		}
		return int(x), true
	case json.Number:
		if i, err := x.Int64(); err == nil && i >= -2147483648 && i <= 2147483647 {
			return int(i), true
		}
		if f, err := x.Float64(); err == nil && math.Trunc(f) == f && f >= -2147483648 && f <= 2147483647 {
			return int(f), true
		}
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}
