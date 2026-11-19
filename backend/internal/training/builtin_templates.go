package training

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// BuiltinTemplateSpec defines a read-only system built-in YOLO training
// parameter template seeded into training_param_templates.
type BuiltinTemplateSpec struct {
	Key         string
	Order       int
	Name        string
	Description string
	// Overrides holds only the parameters that intentionally differ from
	// Ultralytics official defaults; NormalizeTemplateParams expands them into
	// the full 86-parameter normalized map.
	Overrides map[string]any
}

var builtinTemplateSpecs = []BuiltinTemplateSpec{
	{
		Key:         "fast_debug",
		Order:       1,
		Name:        "快速验证 / 试跑",
		Description: "30 轮短周期训练（早停 15 轮、末尾 5 轮关闭 Mosaic），用于新数据版本首次跑通训练链路、校验标注质量与类别顺序，或快速获取基线指标。",
		Overrides: map[string]any{
			"epochs":        30,
			"patience":      15,
			"close_mosaic":  5,
			"warmup_epochs": 2.0,
		},
	},
	{
		Key:         "industrial_standard",
		Order:       2,
		Name:        "通用工业检测基准",
		Description: "常规工业外观缺陷检测主力方案：150 轮配合余弦退火学习率与末尾 15 轮关闭 Mosaic 精修，兼顾收敛精度与训练稳定性，适合绝大多数标准 640px 产线检测任务。",
		Overrides: map[string]any{
			"epochs":       150,
			"patience":     50,
			"cos_lr":       true,
			"close_mosaic": 15,
		},
	},
	{
		Key:         "small_object_1280",
		Order:       3,
		Name:        "高分辨率微小缺陷",
		Description: "针对 PCB 焊点、细微划痕、针脚异物、微裂纹等像素占比极小的目标：采用 1280px 高分辨输入与 batch=8 防爆显存，250 轮余弦衰减并收紧缩放幅度（scale=0.3）避免小目标缩失。",
		Overrides: map[string]any{
			"epochs":       250,
			"patience":     60,
			"batch":        8,
			"imgsz":        1280,
			"cos_lr":       true,
			"close_mosaic": 20,
			"scale":        0.3,
		},
	},
	{
		Key:         "orientation_color_strict",
		Order:       4,
		Name:        "方向与色差敏感检测",
		Description: "适合字符/丝印、极性元件、固定工位朝向以及色差/污渍类外观缺陷：关闭左右翻转（fliplr=0），大幅收紧 HSV 颜色扰动与缩放幅度，并降低 Mosaic 比例以保护方向与色泽特征。",
		Overrides: map[string]any{
			"epochs":       150,
			"patience":     50,
			"cos_lr":       true,
			"close_mosaic": 20,
			"fliplr":       0.0,
			"hsv_h":        0.005,
			"hsv_s":        0.2,
			"hsv_v":        0.2,
			"mosaic":       0.5,
			"scale":        0.3,
		},
	},
	{
		Key:         "few_shot_strong_aug",
		Order:       5,
		Name:        "小样本强增强防过拟合",
		Description: "面向缺陷样本稀缺、易过拟合的场景：250 轮配合 AdamW（lr0=0.001）与更强权重衰减，叠加多尺度训练、随机旋转、MixUp 与宽幅缩放等强在线增强以提升泛化能力。",
		Overrides: map[string]any{
			"epochs":       250,
			"patience":     80,
			"optimizer":    "AdamW",
			"lr0":          0.001,
			"weight_decay": 0.001,
			"cos_lr":       true,
			"close_mosaic": 20,
			"multi_scale":  0.25,
			"degrees":      10.0,
			"mixup":        0.15,
			"scale":        0.6,
		},
	},
	{
		Key:         "gentle_finetune",
		Order:       6,
		Name:        "预训练温和微调",
		Description: "适合基于已有模型权重做审图加样迭代或跨产线迁移微调：采用 AdamW 低学习率（lr0=0.0005、lrf=0.05）、80 轮余弦退火与温和 Mosaic（0.5）防止遗忘已有特征；极小样本时可按需手动调整 freeze。",
		Overrides: map[string]any{
			"epochs":        80,
			"patience":      30,
			"optimizer":     "AdamW",
			"lr0":           0.0005,
			"lrf":           0.05,
			"warmup_epochs": 2.0,
			"cos_lr":        true,
			"mosaic":        0.5,
			"close_mosaic":  15,
		},
	},
}

// BuiltinTemplateSpecs returns a shallow copy of the authoritative system
// built-in training parameter template definitions.
func BuiltinTemplateSpecs() []BuiltinTemplateSpec {
	return slices.Clone(builtinTemplateSpecs)
}

// EnsureBuiltinTemplates idempotently inserts or updates all system built-in
// training parameter templates in PostgreSQL without altering any template's
// is_default flag or touching user-created templates.
func (s *Service) EnsureBuiltinTemplates(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ensure builtin templates: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, spec := range builtinTemplateSpecs {
		params, err := NormalizeTemplateParams(spec.Overrides)
		if err != nil {
			return fmt.Errorf("normalize builtin template %q: %w", spec.Key, err)
		}
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode builtin template %q params: %w", spec.Key, err)
		}

		var existingID int64
		lookupErr := tx.QueryRow(ctx,
			`SELECT id FROM training_param_templates WHERE builtin_key = $1 FOR UPDATE`,
			spec.Key,
		).Scan(&existingID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return fmt.Errorf("lookup builtin template %q: %w", spec.Key, lookupErr)
		}
		notFound := errors.Is(lookupErr, pgx.ErrNoRows)

		name, err := resolveBuiltinTemplateName(ctx, tx, spec.Name, spec.Key, existingID)
		if err != nil {
			return fmt.Errorf("resolve name for builtin template %q: %w", spec.Key, err)
		}

		if notFound {
			if _, err := tx.Exec(ctx, `INSERT INTO training_param_templates (
					name, description, params, is_default, is_builtin, builtin_key, builtin_order
				) VALUES ($1, $2, $3, false, true, $4, $5)`,
				name, spec.Description, paramsJSON, spec.Key, spec.Order,
			); err != nil {
				return fmt.Errorf("insert builtin template %q: %w", spec.Key, err)
			}
			continue
		}

		if _, err := tx.Exec(ctx, `UPDATE training_param_templates
			SET name = $1, description = $2, params = $3, builtin_order = $4
			WHERE id = $5`,
			name, spec.Description, paramsJSON, spec.Order, existingID,
		); err != nil {
			return fmt.Errorf("sync builtin template %q: %w", spec.Key, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure builtin templates: %w", err)
	}
	return nil
}

func resolveBuiltinTemplateName(ctx context.Context, tx pgx.Tx, baseName, key string, selfID int64) (string, error) {
	candidates := []string{
		baseName,
		baseName + "（系统内置）",
		fmt.Sprintf("%s（系统内置-%s）", baseName, key),
	}
	for _, cand := range candidates {
		var conflictID int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM training_param_templates WHERE lower(name) = lower($1) AND id <> $2`,
			cand, selfID,
		).Scan(&conflictID)
		if errors.Is(err, pgx.ErrNoRows) {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
	}
	return candidates[len(candidates)-1], nil
}
