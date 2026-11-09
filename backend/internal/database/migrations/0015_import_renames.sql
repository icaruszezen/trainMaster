-- 0015_import_renames: images an import renamed because another image of
-- the dataset has their name (同名图片重命名, see internal/dataset/rename.go).
--
-- Images in different folders of a dataset may share a name (cam1/0001.jpg,
-- cam2/0001.jpg). Uploads, and the first scan of a folder added with
-- "添加已有目录", give a new image whose name (without the extension,
-- ignoring case) an image with different content already has the name
-- <stem>_2 (_3, ...) in its folder, together with its annotation file.
-- rename_images marks the jobs that do this (uploads and that first scan;
-- other scans never rename). dataset_import_renames lists the renamed
-- images for the import records, and the images that kept their name
-- because their content is the same as the image holding it ('identical').
-- Paths are relative to the dataset folder, like dataset_images.rel_path.

ALTER TABLE dataset_imports ADD COLUMN rename_images boolean NOT NULL DEFAULT false;

CREATE TABLE dataset_import_renames (
    import_id       bigint  NOT NULL REFERENCES dataset_imports (id) ON DELETE CASCADE,
    seq             integer NOT NULL,              -- image order of the new images
    kind            text    NOT NULL CHECK (kind IN ('renamed', 'identical')),
    original_path   text    NOT NULL,              -- the path in the upload or folder
    path            text    NOT NULL,              -- the path in the dataset (original_path when identical)
    same_name_as    text    NOT NULL,              -- the image that holds the name
    annotation_path text,                          -- the renamed VOC file, NULL without one
    PRIMARY KEY (import_id, seq)
);
