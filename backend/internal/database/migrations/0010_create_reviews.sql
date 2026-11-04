-- 0010_create_reviews: model reviews (模型审图).
--
-- A review runs a model of the model repository over the images of one or
-- more datasets and lets people check the result image by image: they mark
-- an image as approved (the model got it right, no need to learn it) or as
-- needs_sample (it should join later training), editing the boxes where
-- needed. The images are a snapshot of the datasets' images when the
-- review was created; a training node claims the review (task
-- review_inference, the claim protocol of model_exports) and uploads the
-- boxes it predicts.
--
-- The review's annotations are VOC files of its own under the storage
-- directory reviews/review-<id>/ (review_images.annotation_path, relative
-- to that folder); they never touch the datasets' files unless a merge
-- (review_merges) copies them into the datasets. People can also download
-- a package (images, VOC files and review.json) to review offline with the
-- local reviewer and upload the results (review_uploads).
--
-- Counts (how many images are approved, ...) are always computed with
-- GROUP BY, never stored: deleting a dataset or rescanning images away
-- removes review rows by cascade without leaving counters stale.

CREATE TABLE reviews (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name             text NOT NULL,
    description      text NOT NULL DEFAULT '',
    -- The model and its project; the model can be deleted later (model_ref
    -- keeps what the review needs to describe it).
    model_id         bigint REFERENCES models (id) ON DELETE SET NULL,
    project_id       bigint REFERENCES projects (id) ON DELETE SET NULL,
    -- Snapshot of the model at creation:
    -- {id, project_id, project_name, version, yolo_version, task, file_name, class_names}.
    model_ref        jsonb NOT NULL,
    options          jsonb NOT NULL,               -- inference options {conf, iou, imgsz: [h, w], max_det}
    -- queued -> running -> ready; failed (a retry queues it again and
    -- continues with the images not inferred yet).
    status           text NOT NULL CHECK (status IN ('queued', 'running', 'ready', 'failed')),
    stage            text NOT NULL DEFAULT '' CHECK (stage IN ('', 'downloading', 'inferring')), -- running: as reported
    node_id          bigint REFERENCES compute_nodes (id) ON DELETE SET NULL, -- the node that claimed (or finished) it
    attempts         integer NOT NULL DEFAULT 0,   -- times it was claimed since it was (re)queued by a user
    lease_expires_at timestamptz,                  -- running: the node must report before this, or it is queued again
    error            text NOT NULL DEFAULT '',     -- failed: shown to users (Chinese)
    error_detail     text NOT NULL DEFAULT '',     -- failed: the node's error output (truncated)
    result           jsonb,                        -- ready: what the node reported about the run (sanitized)
    started_at       timestamptz,                  -- first claim of the current run
    finished_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_status_idx ON reviews (status, id);
CREATE INDEX reviews_model_idx ON reviews (model_id);
CREATE INDEX reviews_project_idx ON reviews (project_id);

-- The datasets of a review, in the order they were selected.
CREATE TABLE review_datasets (
    review_id    bigint NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    dataset_id   bigint NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    position     integer NOT NULL CHECK (position >= 0), -- selection order, 0-based
    -- Folder of the dataset inside the review folder and the package
    -- (unique within the review ignoring case, valid on Windows).
    folder       text NOT NULL,
    dataset_name text NOT NULL,                  -- name at creation
    PRIMARY KEY (review_id, dataset_id),
    UNIQUE (review_id, position)
);
CREATE INDEX review_datasets_dataset_idx ON review_datasets (dataset_id);

-- The images of a review: a snapshot of the datasets' images at creation.
-- Removing a dataset image (rescan, re-import) or the dataset removes the
-- review image too.
CREATE TABLE review_images (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id          bigint NOT NULL,
    dataset_id         bigint NOT NULL,
    image_id           bigint NOT NULL REFERENCES dataset_images (id) ON DELETE CASCADE,
    -- 0-based order in the review: the datasets in selection order, within
    -- each the dataset's image order. Cascades leave gaps.
    position           integer NOT NULL CHECK (position >= 0),
    rel_path           text NOT NULL,            -- image path in the dataset folder at creation
    file_name          text NOT NULL,
    package_path       text NOT NULL,            -- the image inside the review folder / package
    annotation_path    text NOT NULL,            -- its VOC file inside the review folder / package
    image_version      text NOT NULL,            -- dataset image version at creation (cache busting)
    width              integer,
    height             integer,
    inferred           boolean NOT NULL DEFAULT false,
    inference_error    text NOT NULL DEFAULT '', -- the node could not infer it ('' = fine)
    -- The model's boxes [{name, confidence, xmin, ymin, xmax, ymax}], as
    -- inferred (clamped to the image, whole pixels); never edited.
    predictions        jsonb NOT NULL DEFAULT '[]',
    object_count       integer NOT NULL DEFAULT 0,  -- of the current review annotation
    labels             jsonb NOT NULL DEFAULT '{}', -- {name: count} of the current review annotation
    edited             boolean NOT NULL DEFAULT false, -- the annotation differs from the predictions
    annotation_version text NOT NULL DEFAULT '',     -- version of the VOC file ('' = no file)
    status             text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'needs_sample')),
    revision           integer NOT NULL DEFAULT 0,   -- +1 on every annotation or status change
    reviewed_at        timestamptz,                  -- last change to approved / needs_sample; NULL when pending
    merged_at          timestamptz,                  -- last merge into the dataset
    merged_revision    integer,                      -- the revision merged then
    -- The package (review.json package_id) whose uploaded results last
    -- changed the image, and the revision they made: while the revision is
    -- still that one, a later upload from the same package continues from
    -- it (no conflict; the local state counts, reverts included).
    upload_package_id  text,
    upload_revision    integer,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (review_id, dataset_id) REFERENCES review_datasets (review_id, dataset_id) ON DELETE CASCADE,
    UNIQUE (review_id, image_id),
    UNIQUE (review_id, position)
);
CREATE INDEX review_images_status_idx ON review_images (review_id, status, position);
CREATE INDEX review_images_dataset_idx ON review_images (review_id, dataset_id, position);
CREATE INDEX review_images_uninferred_idx ON review_images (review_id, id) WHERE NOT inferred;
CREATE INDEX review_images_image_idx ON review_images (image_id);

-- Results uploaded from the local reviewer, with what applying them did.
CREATE TABLE review_uploads (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id  bigint NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    file_name  text NOT NULL,
    overwrite  boolean NOT NULL,              -- applied over images changed on the platform since the download
    summary    jsonb NOT NULL,                -- counts of images applied, unchanged, in conflict, ...
    problems   jsonb NOT NULL DEFAULT '[]',   -- up to 100 [{path, kind, message}]
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX review_uploads_review_idx ON review_uploads (review_id, id DESC);

-- Merges of review annotations into the datasets' VOC files (background
-- jobs; at most one runs per review).
CREATE TABLE review_merges (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id   bigint NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    dataset_ids bigint[] NOT NULL,              -- the datasets merged (always explicit)
    statuses    text[] NOT NULL,                -- the image statuses merged, e.g. {needs_sample}
    on_existing text NOT NULL CHECK (on_existing IN ('skip', 'overwrite')), -- images that have a dataset annotation
    status      text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    total       integer NOT NULL DEFAULT 0,
    done        integer NOT NULL DEFAULT 0,
    summary     jsonb NOT NULL DEFAULT '{}',    -- counts of images merged, skipped, in conflict, failed
    problems    jsonb NOT NULL DEFAULT '[]',    -- up to 100 [{path, kind, message}]
    error       text NOT NULL DEFAULT '',       -- failed: shown to users (Chinese)
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX review_merges_review_idx ON review_merges (review_id, id DESC);
CREATE UNIQUE INDEX review_merges_running_key ON review_merges (review_id) WHERE status = 'running';
