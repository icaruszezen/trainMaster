-- 0019_training_param_templates: reusable global YOLO training parameter
-- templates (参数模板) and optional lineage snapshot on training_jobs.
--
-- A template stores a normalized map of Ultralytics YOLO training parameters
-- (with resume kept false since resume is tied to a job's init_mode). At most
-- one template may be marked is_default = true so new training jobs load it
-- automatically when neither ?from= nor ?template= is specified.
--
-- Deleting a template removes it directly and sets training_jobs.template_id
-- to NULL while preserving training_jobs.template_name as a historical label;
-- existing jobs' locked params snapshot is never affected.

CREATE TABLE training_param_templates (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    params      jsonb NOT NULL,
    is_default  boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX training_param_templates_name_key
    ON training_param_templates (lower(name));

CREATE UNIQUE INDEX training_param_templates_default_key
    ON training_param_templates (is_default)
    WHERE is_default;

CREATE INDEX training_param_templates_order_idx
    ON training_param_templates (is_default DESC, updated_at DESC, id DESC);

ALTER TABLE training_jobs
    ADD COLUMN template_id   bigint REFERENCES training_param_templates (id) ON DELETE SET NULL,
    ADD COLUMN template_name text NOT NULL DEFAULT '';

CREATE INDEX training_jobs_template_idx
    ON training_jobs (template_id)
    WHERE template_id IS NOT NULL;
