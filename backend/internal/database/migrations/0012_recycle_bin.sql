-- 0012_recycle_bin: the recycle bin (回收站).
--
-- Deleting a dataset, a review version of a dataset or a model no longer
-- removes it: it goes to the recycle bin for 7 days (internal/recycle),
-- where it can be restored, and is purged for good when they are over.
-- Items in the bin are soft deleted: deleted_at says since when, and every
-- other query treats such a row as gone.
--
-- A dataset in the bin keeps its images, imports and review versions. A
-- dataset deleted with its files (delete_files) has its folder renamed to
-- datasets/.recycle-<id> meanwhile, so its name and folder name can be used
-- again; without, the folder stays where it is and still belongs to the
-- dataset. The links of a deleted dataset that other modules read (its
-- projects, the review tasks it is part of) move to the recycled_* tables
-- below and come back when it is restored, as far as the other side still
-- exists.
--
-- A model in the bin keeps its folder (versions are never reused). Its
-- project cannot be removed while it has a model, even one in the bin
-- (models.project_id is ON DELETE RESTRICT): a project deleted while some
-- of its models are in the bin is soft deleted as well (hidden, its name
-- free, its dataset links in recycled_project_datasets) until the last of
-- them is purged, and comes back when one of them is restored.
ALTER TABLE datasets ADD COLUMN deleted_at timestamptz;
-- The folder is removed when the dataset is purged.
ALTER TABLE datasets ADD COLUMN delete_files boolean NOT NULL DEFAULT false;
ALTER TABLE models ADD COLUMN deleted_at timestamptz;
ALTER TABLE projects ADD COLUMN deleted_at timestamptz;
ALTER TABLE dataset_versions ADD COLUMN deleted_at timestamptz;

-- Names (and folders) only have to be unique among the items in use. A
-- dataset in the bin that kept its folder in place still owns the folder.
DROP INDEX datasets_name_key;
CREATE UNIQUE INDEX datasets_name_key ON datasets (lower(name)) WHERE deleted_at IS NULL;
DROP INDEX datasets_dir_name_key;
CREATE UNIQUE INDEX datasets_dir_name_key ON datasets (lower(dir_name)) WHERE deleted_at IS NULL OR NOT delete_files;
DROP INDEX projects_name_key;
CREATE UNIQUE INDEX projects_name_key ON projects (lower(name)) WHERE deleted_at IS NULL;

CREATE INDEX datasets_deleted_idx ON datasets (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX models_deleted_idx ON models (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX projects_deleted_idx ON projects (id) WHERE deleted_at IS NOT NULL;
CREATE INDEX dataset_versions_deleted_idx ON dataset_versions (deleted_at) WHERE deleted_at IS NOT NULL;

-- The links set aside while one side is in the recycle bin, with the same
-- columns as the tables they come from (LIKE: same names, types and order,
-- so rows move with SELECT *). A migration that changes the columns of
-- project_datasets, review_datasets or review_images must change these
-- tables the same way (TestIntegrationRecycledTablesMatch checks it).
-- Removing the other side for good removes the link here too.
CREATE TABLE recycled_project_datasets (LIKE project_datasets);
ALTER TABLE recycled_project_datasets
    ADD PRIMARY KEY (project_id, dataset_id),
    ADD FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE,
    ADD FOREIGN KEY (dataset_id) REFERENCES datasets (id) ON DELETE CASCADE;
CREATE INDEX recycled_project_datasets_dataset_idx ON recycled_project_datasets (dataset_id);

-- The review tasks a dataset in the bin is part of, with the review's
-- images of it (their review annotations and status stay in the review
-- folder and here). Restoring the dataset puts them back.
CREATE TABLE recycled_review_datasets (LIKE review_datasets);
ALTER TABLE recycled_review_datasets
    ADD PRIMARY KEY (review_id, dataset_id),
    ADD FOREIGN KEY (review_id) REFERENCES reviews (id) ON DELETE CASCADE,
    ADD FOREIGN KEY (dataset_id) REFERENCES datasets (id) ON DELETE CASCADE;
CREATE INDEX recycled_review_datasets_dataset_idx ON recycled_review_datasets (dataset_id);

CREATE TABLE recycled_review_images (LIKE review_images);
ALTER TABLE recycled_review_images
    ADD PRIMARY KEY (id),
    ADD FOREIGN KEY (review_id, dataset_id) REFERENCES recycled_review_datasets (review_id, dataset_id) ON DELETE CASCADE;
CREATE INDEX recycled_review_images_dataset_idx ON recycled_review_images (review_id, dataset_id);
