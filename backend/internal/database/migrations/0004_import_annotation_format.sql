-- 0004_import_annotation_format: the annotation format of an upload.
--
-- Datasets always keep Pascal VOC files next to their images. An archive or
-- folder upload may carry X-AnyLabeling JSON or YOLO txt annotations
-- instead; the import converts them to VOC before moving the files into the
-- dataset. YOLO files number their classes, so such an import also records
-- the class names in order (index = class id). Scan jobs are always 'voc'.

ALTER TABLE dataset_imports
    ADD COLUMN annotation_format text NOT NULL DEFAULT 'voc'
        CHECK (annotation_format IN ('voc', 'xanylabeling', 'yolo')),
    ADD COLUMN classes jsonb NOT NULL DEFAULT '[]'; -- ["scratch", "dent"]
