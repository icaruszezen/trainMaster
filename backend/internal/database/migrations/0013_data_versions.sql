-- 0013_data_versions: data versions (数据版本) of projects.
--
-- A data version is a frozen training set of a project: the images of
-- some of the project's datasets (each with the versions of its
-- annotations a download would combine: the base and review versions),
-- the project's class list as it was, a split into train / val / test and
-- optionally augmented copies of the train images. It is built in the
-- background (queued -> building -> ready, or failed) into a folder of its
-- own under exports/ (data-versions/project-<project>/v<version>): the
-- original images are hard links to the dataset files (copies when that is
-- not possible) laid out for Ultralytics (images/ and labels/, data.yaml,
-- train.txt / val.txt / test.txt), with classes.txt and version.json, so
-- the folder alone is a complete training set. Once ready it never
-- changes, except for its note: later changes to the datasets do not
-- reach it.
--
-- Versions are numbered per project, v1, v2, ... when they are created
-- (projects.data_version_seq, like model versions); a number is never
-- given again. A project with data versions (also in the recycle bin)
-- cannot be removed (ON DELETE RESTRICT, like models): deleting it moves
-- them to the bin and soft deletes the project until the last of them and
-- of its models is purged.
--
-- The images record where they came from (dataset, path, annotation
-- version) without foreign keys: a version outlives its datasets and their
-- review versions.

ALTER TABLE projects ADD COLUMN data_version_seq integer NOT NULL DEFAULT 0;

CREATE TABLE data_versions (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id      bigint NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    version         integer NOT NULL CHECK (version > 0),
    dir_name        text NOT NULL,                   -- data-versions/project-<project>/v<version>, under exports/
    note            text NOT NULL,
    status          text NOT NULL CHECK (status IN ('queued', 'building', 'ready', 'failed')),
    error           text NOT NULL DEFAULT '',        -- failed: shown to users
    stage           text NOT NULL DEFAULT '',        -- building: preparing, linking, augmenting, finishing
    progress_done   integer NOT NULL DEFAULT 0,
    progress_total  integer NOT NULL DEFAULT 0,
    classes         jsonb NOT NULL,                  -- the project's class list when created: [{name, color}]
    config          jsonb NOT NULL,                  -- split, filters, augmentation
    sources         jsonb NOT NULL,                  -- [{dataset_id, dataset_name, dir_name, include_base, versions: [{id, name}], image_count}]
    -- Counts, set when it is ready.
    image_count     integer NOT NULL DEFAULT 0,      -- all images, augmented copies included
    object_count    integer NOT NULL DEFAULT 0,
    train_count     integer NOT NULL DEFAULT 0,
    val_count       integer NOT NULL DEFAULT 0,
    test_count      integer NOT NULL DEFAULT 0,
    augmented_count integer NOT NULL DEFAULT 0,
    total_bytes     bigint NOT NULL DEFAULT 0,       -- the image files (linked, copied and generated)
    stats           jsonb NOT NULL DEFAULT '{}',     -- per class and split, skipped images, storage, comparison
    created_at      timestamptz NOT NULL DEFAULT now(),
    queued_at       timestamptz NOT NULL DEFAULT now(), -- the build queue's order (a retry goes to its end)
    updated_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,                     -- ready or failed
    deleted_at      timestamptz,                     -- in the recycle bin (ready versions only)
    UNIQUE (project_id, version),
    CHECK (deleted_at IS NULL OR status = 'ready')
);
CREATE INDEX data_versions_project_idx ON data_versions (project_id, version DESC);
CREATE INDEX data_versions_active_idx ON data_versions (queued_at, id) WHERE status IN ('queued', 'building');
CREATE INDEX data_versions_deleted_idx ON data_versions (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE data_version_images (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    version_id        bigint NOT NULL REFERENCES data_versions (id) ON DELETE CASCADE,
    -- The order of the images in the version: the sources in order, each in
    -- the dataset's image order; augmented copies right after their original.
    position          integer NOT NULL,
    split             text NOT NULL CHECK (split IN ('train', 'val', 'test')),
    split_by          text NOT NULL CHECK (split_by IN ('ratio', 'folder')),
    path              text NOT NULL,                 -- in the version folder: images/<dataset dir>/...
    -- The stored file (a hard link shares the dataset file's inode: a
    -- program rewriting that file in place would change it; downloads
    -- compare and log).
    size_bytes        bigint NOT NULL,
    mod_time          timestamptz NOT NULL,
    width             integer NOT NULL CHECK (width > 0),
    height            integer NOT NULL CHECK (height > 0),
    objects           jsonb NOT NULL,                -- [{name, xmin, ymin, xmax, ymax, difficult, truncated}]; [] = negative sample
    object_count      integer NOT NULL,
    labels            jsonb NOT NULL,                -- {name: count}
    -- Where it came from (augmented copies: their original's).
    dataset_id        bigint NOT NULL,
    source_path       text NOT NULL,                 -- the image's path in the dataset folder
    source_size       bigint NOT NULL,               -- the dataset file when it was taken
    source_mod_time   timestamptz NOT NULL,
    annotation_source bigint,                        -- the review version (dataset_versions.id) whose annotation it has; NULL: the base
    augmented_from    bigint REFERENCES data_version_images (id) ON DELETE CASCADE,
    augment           jsonb NOT NULL DEFAULT '[]',   -- the methods applied to an augmented copy, in order
    stored            text NOT NULL CHECK (stored IN ('link', 'copy', 'generated')),
    UNIQUE (version_id, path),
    CHECK ((augmented_from IS NULL) = (stored <> 'generated'))
);
CREATE INDEX data_version_images_order_idx ON data_version_images (version_id, position);
CREATE INDEX data_version_images_split_idx ON data_version_images (version_id, split, position);
CREATE INDEX data_version_images_from_idx ON data_version_images (augmented_from) WHERE augmented_from IS NOT NULL;
CREATE INDEX data_version_images_labels_idx ON data_version_images USING gin (labels);
