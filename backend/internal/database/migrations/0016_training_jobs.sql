-- 0016_training_jobs: YOLO model training jobs (训练任务).
--
-- A training job trains a YOLO detection model on a ready data version of a
-- project using a compute node (task kind training_job, claimed via
-- POST /api/v1/compute/agent/tasks/claim). All Ultralytics YOLO configuration
-- parameters are stored normalized in params (jsonb) with official defaults.
--
-- Run files (train.log, weights/best.pt, weights/last.pt, results.csv,
-- args.yaml and validation plots) live under runs/job-<id>/. When
-- auto_register is true, completing the job registers weights/best.pt into
-- the project's model repository via model.Service.RegisterTrainedModel;
-- users can also register best.pt or last.pt afterwards.
--
-- Both project_id and data_version_id use ON DELETE RESTRICT so a trained
-- model's lineage back to its data version and project stays intact while the
-- training job exists.

CREATE TABLE training_jobs (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id        bigint NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    data_version_id   bigint NOT NULL REFERENCES data_versions (id) ON DELETE RESTRICT,
    -- Snapshot of the data version when the job was created:
    -- {id, version, dir_name, class_names, image_count, train_count, val_count, test_count, augmented_count}.
    data_version_ref  jsonb NOT NULL,
    name              text NOT NULL,
    description       text NOT NULL DEFAULT '',
    -- Initialization mode:
    --   scratch:  build network from <yolo_version><model_scale>.yaml
    --   finetune: transfer pretrained weights from base_model_id
    --   resume:   resume an interrupted checkpoint (last.pt) from base_model_id
    init_mode         text NOT NULL CHECK (init_mode IN ('scratch', 'finetune', 'resume')),
    yolo_version      text NOT NULL,
    model_scale       text NOT NULL DEFAULT '',
    task              text NOT NULL DEFAULT 'detect',
    base_model_id     bigint REFERENCES models (id) ON DELETE SET NULL,
    base_model_ref    jsonb,
    params            jsonb NOT NULL,
    auto_register     boolean NOT NULL DEFAULT true,
    status            text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    stage             text NOT NULL DEFAULT '' CHECK (stage IN ('', 'preparing', 'downloading', 'training', 'uploading')),
    node_id           bigint REFERENCES compute_nodes (id) ON DELETE SET NULL,
    attempts          integer NOT NULL DEFAULT 0,
    lease_expires_at  timestamptz,
    current_epoch     integer NOT NULL DEFAULT 0,
    total_epochs      integer NOT NULL DEFAULT 0,
    batch_done        integer NOT NULL DEFAULT 0,
    batch_total       integer NOT NULL DEFAULT 0,
    eta_seconds       integer,
    metrics           jsonb NOT NULL DEFAULT '{}',
    artifacts         jsonb NOT NULL DEFAULT '[]',
    best_model_id     bigint REFERENCES models (id) ON DELETE SET NULL,
    last_model_id     bigint REFERENCES models (id) ON DELETE SET NULL,
    error             text NOT NULL DEFAULT '',
    error_detail      text NOT NULL DEFAULT '',
    result            jsonb,
    queued_at         timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX training_jobs_status_idx ON training_jobs (status, queued_at, id);
CREATE INDEX training_jobs_project_idx ON training_jobs (project_id, created_at DESC, id DESC);
CREATE INDEX training_jobs_data_version_idx ON training_jobs (data_version_id);
CREATE INDEX training_jobs_base_model_idx ON training_jobs (base_model_id) WHERE base_model_id IS NOT NULL;
CREATE INDEX training_jobs_lease_idx ON training_jobs (lease_expires_at) WHERE status = 'running';

-- Per-epoch training and validation metrics reported during the run.
CREATE TABLE training_job_epochs (
    job_id      bigint NOT NULL REFERENCES training_jobs (id) ON DELETE CASCADE,
    epoch       integer NOT NULL CHECK (epoch >= 1),
    metrics     jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, epoch)
);
