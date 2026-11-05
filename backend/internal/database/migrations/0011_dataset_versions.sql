-- 0011_dataset_versions: review versions of datasets (审图版本).
--
-- Merging a model review into a dataset no longer writes into the
-- dataset's VOC files. The dataset's own annotations stay its base
-- version (基础版本, still edited in the annotation workspace); every
-- merge adds, to each dataset it covers, a review version: a snapshot of
-- the merged review annotations, named 审图<major>.<minor> (1.0, 1.1, ...
-- per dataset). Downloads choose the versions they combine: newer
-- versions override older ones and the base, image by image (by the
-- image's path in the dataset folder, while the file there is the one that
-- was merged).
--
-- A version is built by its merge (status building, no number yet) and
-- numbered when the merge ends; a merge that fails or is interrupted
-- removes the versions it was building, so a version is always complete.
-- Numbers only grow: datasets.review_version_seq counts the versions ever
-- numbered, which is the minor number of the next one, so a deleted
-- version's number is not given again.

ALTER TABLE datasets ADD COLUMN review_version_seq integer NOT NULL DEFAULT 0;

CREATE TABLE dataset_versions (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dataset_id         bigint NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    kind               text NOT NULL CHECK (kind IN ('review')),
    -- 审图<major>.<minor>; NULL while building.
    major              integer CHECK (major >= 1),
    minor              integer CHECK (minor >= 0),
    note               text NOT NULL,           -- given when merging, editable
    status             text NOT NULL CHECK (status IN ('building', 'ready')),
    -- Where it came from. The review and the merge can be deleted later:
    -- source keeps what describing the version needs,
    -- {review_id, review_name, merge_id, statuses, model: {id, project_id,
    -- project_name, version, yolo_version, task, file_name}}.
    review_id          bigint REFERENCES reviews (id) ON DELETE SET NULL,
    merge_id           bigint REFERENCES review_merges (id) ON DELETE SET NULL,
    source             jsonb NOT NULL,
    -- Counts of its images, set when it is numbered (they never change).
    image_count        integer NOT NULL DEFAULT 0,
    needs_sample_count integer NOT NULL DEFAULT 0,
    approved_count     integer NOT NULL DEFAULT 0,
    object_count       integer NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    finished_at        timestamptz,             -- when it was numbered
    CHECK ((major IS NULL) = (minor IS NULL)),
    CHECK ((status = 'ready') = (major IS NOT NULL AND finished_at IS NOT NULL)),
    UNIQUE (dataset_id, kind, major, minor)
);
CREATE INDEX dataset_versions_dataset_idx ON dataset_versions (dataset_id, id DESC);
CREATE INDEX dataset_versions_merge_idx ON dataset_versions (merge_id);
CREATE INDEX dataset_versions_review_idx ON dataset_versions (review_id);
CREATE INDEX dataset_versions_building_idx ON dataset_versions (id) WHERE status = 'building';

-- The images of a version with their annotation, keyed by the image's path
-- in the dataset folder: a version outlives the image rows (a rescan while
-- the file was moved away) and applies again to an image at the same path,
-- as long as it is the same file: the size and modification time of the
-- dataset image when merged must match. A different picture at the path,
-- or the same one imported again (written with a new modification time),
-- keeps its base annotation.
CREATE TABLE dataset_version_images (
    version_id     bigint NOT NULL REFERENCES dataset_versions (id) ON DELETE CASCADE,
    rel_path       text NOT NULL,
    image_size     bigint NOT NULL,
    image_mod_time timestamptz NOT NULL,
    -- The review status of the image when it was merged: downloads of
    -- versions without the base only take needs_sample images.
    review_status  text NOT NULL CHECK (review_status IN ('pending', 'approved', 'needs_sample')),
    width          integer,                      -- of the image when merged, NULL when unknown
    height         integer,
    objects        jsonb NOT NULL,               -- [{name, xmin, ymin, xmax, ymax, difficult, truncated}]; [] = negative sample
    object_count   integer NOT NULL,
    labels         jsonb NOT NULL,               -- {name: count}
    PRIMARY KEY (version_id, rel_path)
);

-- Merges that create versions have no on_existing (NULL); skip / overwrite
-- remain on the merges made before, which wrote into the VOC files.
ALTER TABLE review_merges ALTER COLUMN on_existing DROP NOT NULL;
ALTER TABLE review_merges ADD COLUMN note text NOT NULL DEFAULT '';
