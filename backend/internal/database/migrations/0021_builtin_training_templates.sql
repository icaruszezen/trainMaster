-- 0021_builtin_training_templates: add read-only system built-in YOLO training
-- parameter templates (系统内置参数模板) for common industrial visual detection
-- scenarios.
--
-- Built-in templates live in training_param_templates with is_builtin = true
-- and a stable builtin_key so training_jobs.template_id foreign keys and the
-- single-default-template constraint work uniformly across built-in and custom
-- templates. Users can launch jobs from them, toggle is_default, or duplicate
-- them into editable custom templates, while direct edits and deletions are
-- blocked by the service layer.

ALTER TABLE training_param_templates
    ADD COLUMN is_builtin    boolean NOT NULL DEFAULT false,
    ADD COLUMN builtin_key   text,
    ADD COLUMN builtin_order integer NOT NULL DEFAULT 0;

ALTER TABLE training_param_templates
    ADD CONSTRAINT training_param_templates_builtin_key_check
        CHECK (
            (is_builtin AND builtin_key IS NOT NULL AND btrim(builtin_key) <> '')
            OR (NOT is_builtin AND builtin_key IS NULL)
        );

CREATE UNIQUE INDEX training_param_templates_builtin_key_idx
    ON training_param_templates (builtin_key)
    WHERE builtin_key IS NOT NULL;

DROP INDEX IF EXISTS training_param_templates_order_idx;
CREATE INDEX training_param_templates_order_idx
    ON training_param_templates (is_default DESC, is_builtin ASC, builtin_order ASC, updated_at DESC, id DESC);

-- Seed the 6 system built-in templates. All start with is_default = false so
-- existing deployments keep their chosen default (or Ultralytics official
-- defaults). If a user already created a custom template with the exact same
-- name, suffix the built-in template name instead of mutating user data.
WITH seeds(builtin_key, builtin_order, base_name, description, params) AS (
    VALUES
        (
            'fast_debug',
            1,
            '快速验证 / 试跑',
            '30 轮短周期训练（早停 15 轮、末尾 5 轮关闭 Mosaic），用于新数据版本首次跑通训练链路、校验标注质量与类别顺序，或快速获取基线指标。',
            '{"epochs":30,"patience":15,"close_mosaic":5,"warmup_epochs":2.0}'::jsonb
        ),
        (
            'industrial_standard',
            2,
            '通用工业检测基准',
            '常规工业外观缺陷检测主力方案：150 轮配合余弦退火学习率与末尾 15 轮关闭 Mosaic 精修，兼顾收敛精度与训练稳定性，适合绝大多数标准 640px 产线检测任务。',
            '{"epochs":150,"patience":50,"cos_lr":true,"close_mosaic":15}'::jsonb
        ),
        (
            'small_object_1280',
            3,
            '高分辨率微小缺陷',
            '针对 PCB 焊点、细微划痕、针脚异物、微裂纹等像素占比极小的目标：采用 1280px 高分辨输入与 batch=8 防爆显存，250 轮余弦衰减并收紧缩放幅度（scale=0.3）避免小目标缩失。',
            '{"epochs":250,"patience":60,"batch":8,"imgsz":1280,"cos_lr":true,"close_mosaic":20,"scale":0.3}'::jsonb
        ),
        (
            'orientation_color_strict',
            4,
            '方向与色差敏感检测',
            '适合字符/丝印、极性元件、固定工位朝向以及色差/污渍类外观缺陷：关闭左右翻转（fliplr=0），大幅收紧 HSV 颜色扰动与缩放幅度，并降低 Mosaic 比例以保护方向与色泽特征。',
            '{"epochs":150,"patience":50,"cos_lr":true,"close_mosaic":20,"fliplr":0.0,"hsv_h":0.005,"hsv_s":0.2,"hsv_v":0.2,"mosaic":0.5,"scale":0.3}'::jsonb
        ),
        (
            'few_shot_strong_aug',
            5,
            '小样本强增强防过拟合',
            '面向缺陷样本稀缺、易过拟合的场景：250 轮配合 AdamW（lr0=0.001）与更强权重衰减，叠加多尺度训练、随机旋转、MixUp 与宽幅缩放等强在线增强以提升泛化能力。',
            '{"epochs":250,"patience":80,"optimizer":"AdamW","lr0":0.001,"weight_decay":0.001,"cos_lr":true,"close_mosaic":20,"multi_scale":0.25,"degrees":10.0,"mixup":0.15,"scale":0.6}'::jsonb
        ),
        (
            'gentle_finetune',
            6,
            '预训练温和微调',
            '适合基于已有模型权重做审图加样迭代或跨产线迁移微调：采用 AdamW 低学习率（lr0=0.0005、lrf=0.05）、80 轮余弦退火与温和 Mosaic（0.5）防止遗忘已有特征；极小样本时可按需手动调整 freeze。',
            '{"epochs":80,"patience":30,"optimizer":"AdamW","lr0":0.0005,"lrf":0.05,"warmup_epochs":2.0,"cos_lr":true,"mosaic":0.5,"close_mosaic":15}'::jsonb
        )
)
INSERT INTO training_param_templates (
    name, description, params, is_default, is_builtin, builtin_key, builtin_order
)
SELECT
    CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM training_param_templates existing
            WHERE lower(existing.name) = lower(s.base_name)
        ) THEN s.base_name
        WHEN NOT EXISTS (
            SELECT 1 FROM training_param_templates existing
            WHERE lower(existing.name) = lower(s.base_name || '（系统内置）')
        ) THEN s.base_name || '（系统内置）'
        ELSE s.base_name || '（系统内置-' || s.builtin_key || '）'
    END,
    s.description,
    s.params,
    false,
    true,
    s.builtin_key,
    s.builtin_order
FROM seeds s
ORDER BY s.builtin_order ASC
ON CONFLICT (builtin_key) WHERE builtin_key IS NOT NULL DO NOTHING;
