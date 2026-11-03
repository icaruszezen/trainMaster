-- 0008_create_models: the model repository.
--
-- A model is one weights file (an Ultralytics .pt) bound to exactly one
-- project; a project has any number of models, numbered by version within
-- the project (v1, v2, ...). Versions only grow: projects.model_version_seq
-- remembers the last one handed out, so a deleted model's version (and
-- folder) is never reused. source tells user uploads from models produced
-- by training on the platform (training_job_id names the training job once
-- that table exists; the foreign key is added with it).
--
-- The files live under the storage directory models/, one folder per model
-- (models.dir_name, e.g. project-12/v3, relative to models/): the original
-- model.pt, a model.json sidecar describing it, and the cached conversions
-- in onnx/. The database only stores relative paths.
--
-- model_uploads are chunked uploads in progress (the file is staged in
-- tmp/model-uploads/<id>/ until it is committed). model_exports are the
-- conversions (pt -> onnx) that training nodes claim and execute; a
-- succeeded row doubles as the cache entry for its options.

-- The last model version handed out in the project (never decreases).
ALTER TABLE projects ADD COLUMN model_version_seq integer NOT NULL DEFAULT 0;

CREATE TABLE models (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Deleting a project deletes its models first (with their files, see
    -- internal/model), so the database never drops a model's row while its
    -- folder stays behind.
    project_id      bigint NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    version         integer NOT NULL CHECK (version > 0),
    description     text NOT NULL DEFAULT '',
    source          text NOT NULL CHECK (source IN ('upload', 'training')),
    training_job_id bigint,                  -- source = 'training': the job that produced it
    -- yolov8 / yolov9 / yolov10 / yolo11 / yolo12 / yolo26, validated by
    -- the application so new versions need no migration.
    yolo_version    text NOT NULL,
    task            text NOT NULL,           -- detect / segment / classify / pose / obb ... (from the file; '' unknown)
    file_name       text NOT NULL,           -- original file name, e.g. best.pt
    dir_name        text NOT NULL,           -- folder under models/, e.g. project-12/v3
    size_bytes      bigint NOT NULL,
    sha256          text NOT NULL,
    info            jsonb NOT NULL DEFAULT '{}', -- metadata parsed from the file (internal/model ModelInfo)
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, version),
    CHECK (source <> 'training' OR training_job_id IS NOT NULL)
);
CREATE UNIQUE INDEX models_dir_name_key ON models (dir_name);

CREATE TABLE model_uploads (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Uploads in progress go with their project; the janitor removes the
    -- staged file.
    project_id            bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    yolo_version          text NOT NULL,     -- chosen by the user
    description           text NOT NULL DEFAULT '',
    file_name             text NOT NULL,
    file_size             bigint NOT NULL,   -- declared size
    received_bytes        bigint NOT NULL DEFAULT 0,
    status                text NOT NULL CHECK (status IN ('uploading', 'completed', 'failed', 'canceled')),
    -- The version found in the file by the last commit when it differs
    -- from yolo_version (the upload stays uploading so it can be committed
    -- again with the right one).
    detected_yolo_version text,
    error                 text NOT NULL DEFAULT '', -- failed / canceled: shown to users (Chinese)
    model_id              bigint REFERENCES models (id) ON DELETE SET NULL, -- completed: the model it became
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_uploads_project_idx ON model_uploads (project_id);
CREATE INDEX model_uploads_model_idx ON model_uploads (model_id);
CREATE INDEX model_uploads_active_idx ON model_uploads (status) WHERE status = 'uploading';

CREATE TABLE model_exports (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    model_id         bigint NOT NULL REFERENCES models (id) ON DELETE CASCADE,
    format           text NOT NULL CHECK (format IN ('onnx')), -- later also engine, openvino, ...
    options          jsonb NOT NULL,        -- normalized conversion options
    options_key      text NOT NULL,         -- canonical string of options, unique per model and format
    status           text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    stage            text NOT NULL DEFAULT '', -- running: downloading / converting / uploading
    node_id          bigint REFERENCES compute_nodes (id) ON DELETE SET NULL, -- the node that claimed (or finished) it
    attempts         integer NOT NULL DEFAULT 0, -- times it was claimed
    lease_expires_at timestamptz,           -- running: the node must report before this, or it is queued again
    file_name        text NOT NULL DEFAULT '', -- succeeded: path relative to the model folder, e.g. onnx/17.onnx
    -- The verified size and SHA-256 of the result: set when the node has
    -- uploaded it (still running), final once succeeded.
    size_bytes       bigint,
    sha256           text NOT NULL DEFAULT '',
    result           jsonb,                 -- succeeded: what the node reported about the conversion
    error            text NOT NULL DEFAULT '', -- failed: shown to users (Chinese)
    error_detail     text NOT NULL DEFAULT '', -- failed: the node's error output (truncated)
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    UNIQUE (model_id, format, options_key)
);
CREATE INDEX model_exports_queue_idx ON model_exports (id) WHERE status = 'queued';
CREATE INDEX model_exports_running_idx ON model_exports (lease_expires_at) WHERE status = 'running';
