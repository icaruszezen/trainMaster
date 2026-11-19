-- 0020_evaluations: model evaluation tasks (模型评估).
--
-- An evaluation runs a bounding-box YOLO model (.pt) against either a ready
-- data version (selected splits: val / test / train) or one or more datasets
-- (composed base + review versions, with annotated / background / all sample
-- selection). Ground-truth boxes and model metadata are snapshotted when the
-- evaluation is created so later edits or deletions of the model, data version
-- or datasets never break historical reports.
--
-- Compute nodes claim queued evaluations via TaskKind "model_evaluation"
-- (POST /api/v1/compute/agent/tasks/claim), run low-confidence (0.001) batched
-- inference to compute exact Ultralytics/COCO-compatible mAP & PR/F1/P/R
-- curves, and upload compact curves + per-image predictions (conf >=
-- store_min_conf) to runs/evaluations/eval-<id>/. The platform backend then
-- supports real-time confidence / IoU threshold sliding, interactive confusion
-- matrix drilldown, image-level OK/NG overkill/escape analysis, multi-model
-- comparison and offline HTML/CSV/JSON report export.

CREATE TABLE evaluations (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id         bigint NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    model_id           bigint REFERENCES models (id) ON DELETE SET NULL,
    model_ref          jsonb NOT NULL,
    name               text NOT NULL,
    description        text NOT NULL DEFAULT '',
    source_type        text NOT NULL CHECK (source_type IN ('data_version', 'datasets')),
    source_fingerprint text NOT NULL,
    data_version_id    bigint REFERENCES data_versions (id) ON DELETE SET NULL,
    source_ref         jsonb NOT NULL,
    options            jsonb NOT NULL,
    classes            jsonb NOT NULL DEFAULT '[]',
    status             text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    stage              text NOT NULL DEFAULT '' CHECK (stage IN ('', 'preparing', 'downloading', 'inferring', 'computing', 'uploading')),
    node_id            bigint REFERENCES compute_nodes (id) ON DELETE SET NULL,
    attempts           integer NOT NULL DEFAULT 0,
    lease_expires_at   timestamptz,
    progress_done      integer NOT NULL DEFAULT 0,
    progress_total     integer NOT NULL DEFAULT 0,
    eta_seconds        integer,
    summary            jsonb NOT NULL DEFAULT '{}',
    error              text NOT NULL DEFAULT '',
    error_detail       text NOT NULL DEFAULT '',
    result             jsonb,
    queued_at          timestamptz NOT NULL DEFAULT now(),
    started_at         timestamptz,
    finished_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX evaluations_status_idx ON evaluations (status, queued_at, id);
CREATE INDEX evaluations_project_idx ON evaluations (project_id, created_at DESC, id DESC);
CREATE INDEX evaluations_model_idx ON evaluations (model_id) WHERE model_id IS NOT NULL;
CREATE INDEX evaluations_data_version_idx ON evaluations (data_version_id) WHERE data_version_id IS NOT NULL;
CREATE INDEX evaluations_fingerprint_idx ON evaluations (source_fingerprint);
CREATE INDEX evaluations_lease_idx ON evaluations (lease_expires_at) WHERE status = 'running';

-- Snapshotted evaluation samples with ground truth boxes and cached default-
-- threshold counts (updated whenever the default working conf/iou is saved).
CREATE TABLE evaluation_images (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    evaluation_id         bigint NOT NULL REFERENCES evaluations (id) ON DELETE CASCADE,
    position              integer NOT NULL,
    sample_key            text NOT NULL,
    split                 text NOT NULL DEFAULT '',
    dataset_id            bigint,
    dataset_name          text NOT NULL DEFAULT '',
    dataset_image_id      bigint,
    data_version_image_id bigint,
    path                  text NOT NULL,
    width                 integer NOT NULL DEFAULT 0,
    height                integer NOT NULL DEFAULT 0,
    gt_objects            jsonb NOT NULL DEFAULT '[]',
    gt_count              integer NOT NULL DEFAULT 0,
    inference_error       text NOT NULL DEFAULT '',
    tp_count              integer NOT NULL DEFAULT 0,
    fp_count              integer NOT NULL DEFAULT 0,
    fn_count              integer NOT NULL DEFAULT 0,
    misclassified_count   integer NOT NULL DEFAULT 0,
    image_outcome         text NOT NULL DEFAULT '',
    max_fp_conf           double precision NOT NULL DEFAULT 0,
    UNIQUE (evaluation_id, position)
);

CREATE INDEX evaluation_images_eval_pos_idx ON evaluation_images (evaluation_id, position);
CREATE INDEX evaluation_images_eval_sample_idx ON evaluation_images (evaluation_id, sample_key);
