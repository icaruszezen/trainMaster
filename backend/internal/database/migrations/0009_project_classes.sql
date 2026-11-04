-- 0009_project_classes: the class list belongs to a project, not a dataset.
--
-- A project's class list is the ordered list of the classes it trains:
-- the order is the YOLO class id (the first class is 0) when its datasets
-- are exported for training, and each class has an optional display color
-- ('' = derived from the name). Datasets and annotation do not need an
-- order: VOC files name their labels, and the annotation workspace offers
-- the classes of the dataset's projects followed by the labels already in
-- the dataset. The VOC files stay the source of truth for the labels
-- actually used; editing a class list never rewrites annotation files.
--
-- The class lists that datasets had (0006) are carried over to their
-- projects: a project gets the classes of its datasets in the order the
-- datasets were added to it, each name once (the first color wins), at
-- most 1000. The lists of datasets that belong to no project are dropped;
-- their labels remain in the annotation files.
--
-- The application only ever stored validated arrays of {name, color}; the
-- guards below (a value that is not an array counts as empty, entries
-- without a name are skipped) only keep a hand-edited row from failing the
-- migration. The CASE, not a WHERE condition, protects
-- jsonb_array_elements: SQL does not promise that a WHERE condition on d
-- is checked before the function sees the row.

ALTER TABLE projects
    ADD COLUMN classes jsonb NOT NULL DEFAULT '[]'; -- [{"name": "scratch", "color": "#ef4444"}]

WITH items AS (
    -- pos: the position in the project's carried-over list, datasets in
    -- the order they were added (those added together by ID).
    SELECT pd.project_id, c.value->>'name' AS name, coalesce(c.value->>'color', '') AS color,
           row_number() OVER (PARTITION BY pd.project_id ORDER BY pd.created_at, pd.dataset_id, c.ord) AS pos
    FROM project_datasets pd
    JOIN datasets d ON d.id = pd.dataset_id
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE WHEN jsonb_typeof(d.classes) = 'array' THEN d.classes ELSE '[]' END
    ) WITH ORDINALITY AS c (value, ord)
    WHERE coalesce(c.value->>'name', '') <> ''
), firsts AS (
    -- The first occurrence of each name, with its color.
    SELECT DISTINCT ON (project_id, name) project_id, name, color, pos
    FROM items
    ORDER BY project_id, name, pos
), ranked AS (
    SELECT project_id, name, color, pos,
           row_number() OVER (PARTITION BY project_id ORDER BY pos) AS n
    FROM firsts
)
UPDATE projects p
SET classes = merged.classes
FROM (
    SELECT project_id, jsonb_agg(jsonb_build_object('name', name, 'color', color) ORDER BY pos) AS classes
    FROM ranked
    WHERE n <= 1000
    GROUP BY project_id
) merged
WHERE p.id = merged.project_id;

ALTER TABLE datasets DROP COLUMN classes;
