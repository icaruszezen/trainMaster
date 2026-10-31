-- 0003_create_projects: projects and the datasets they include.
--
-- A project groups the datasets (and, later, the training jobs and models)
-- of one inspection task. Projects are metadata only: they have no folder
-- on disk. A dataset can belong to any number of projects; deleting either
-- side removes only the links.

CREATE TABLE projects (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Also touched when datasets are added or removed.
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX projects_name_key ON projects (lower(name));

CREATE TABLE project_datasets (
    project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    dataset_id bigint NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, dataset_id)
);
CREATE INDEX project_datasets_dataset_idx ON project_datasets (dataset_id);
