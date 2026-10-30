-- 0002_create_datasets: datasets, their indexed images and import jobs.
--
-- A dataset is a folder under the storage directory datasets/. The files on
-- disk are the source of truth; these tables are an index that a scan
-- re-synchronizes. Paths are stored relative to that folder (slash
-- separated), never as absolute paths.

CREATE TABLE datasets (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            text NOT NULL,
    dir_name        text NOT NULL,                 -- folder under datasets/
    description     text NOT NULL DEFAULT '',
    origin          text NOT NULL CHECK (origin IN ('upload', 'directory')),
    -- Aggregates over dataset_images, recomputed after every import/scan.
    image_count     integer NOT NULL DEFAULT 0,
    annotated_count integer NOT NULL DEFAULT 0,    -- VOC file with >= 1 object
    empty_count     integer NOT NULL DEFAULT 0,    -- VOC file with 0 objects
    invalid_count   integer NOT NULL DEFAULT 0,    -- VOC file that cannot be parsed
    object_count    integer NOT NULL DEFAULT 0,
    label_count     integer NOT NULL DEFAULT 0,    -- distinct labels
    total_bytes     bigint  NOT NULL DEFAULT 0,    -- sum of image file sizes
    scanned_at      timestamptz,                   -- last finished import/scan
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX datasets_name_key ON datasets (lower(name));
CREATE UNIQUE INDEX datasets_dir_name_key ON datasets (lower(dir_name));

CREATE TABLE dataset_images (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dataset_id          bigint NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    rel_path            text NOT NULL,
    size_bytes          bigint NOT NULL,
    mod_time            timestamptz NOT NULL,
    width               integer,                   -- NULL when unknown
    height              integer,
    -- The paired VOC file, if any. Its size and modification time detect
    -- changes on the next scan without parsing it again.
    annotation_path     text,
    annotation_mod_time timestamptz,
    annotation_size     bigint,
    -- none: no VOC file; invalid: the VOC file cannot be parsed (no
    -- objects); empty: valid with 0 objects; annotated: >= 1.
    annotation_state    text NOT NULL DEFAULT 'none'
                        CHECK (annotation_state IN ('none', 'invalid', 'empty', 'annotated')),
    object_count        integer NOT NULL DEFAULT 0,
    label_counts        jsonb NOT NULL DEFAULT '{}', -- {"scratch": 3, "dent": 1}
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (dataset_id, rel_path)
);
CREATE INDEX dataset_images_label_counts_idx ON dataset_images USING gin (label_counts);

CREATE TABLE dataset_imports (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dataset_id     bigint NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    source         text NOT NULL CHECK (source IN ('archive', 'folder', 'scan')),
    status         text NOT NULL
                   CHECK (status IN ('uploading', 'queued', 'running', 'succeeded', 'failed', 'canceled')),
    on_conflict    text NOT NULL DEFAULT 'skip' CHECK (on_conflict IN ('skip', 'overwrite')),
    file_name      text NOT NULL DEFAULT '',       -- archive file name / uploaded folder name
    file_size      bigint NOT NULL DEFAULT 0,      -- declared total upload bytes
    file_count     integer NOT NULL DEFAULT 0,     -- declared number of files (folder)
    received_bytes bigint NOT NULL DEFAULT 0,
    received_files integer NOT NULL DEFAULT 0,
    stage          text NOT NULL DEFAULT '',
    progress_done  bigint NOT NULL DEFAULT 0,
    progress_total bigint NOT NULL DEFAULT 0,
    progress_unit  text NOT NULL DEFAULT '',
    summary        jsonb NOT NULL DEFAULT '{}',
    error          text NOT NULL DEFAULT '',       -- shown to users (Chinese)
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz
);
CREATE INDEX dataset_imports_dataset_idx ON dataset_imports (dataset_id, id DESC);
CREATE INDEX dataset_imports_active_idx ON dataset_imports (status)
    WHERE status IN ('uploading', 'queued', 'running');
