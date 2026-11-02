-- 0006_dataset_classes: the class list of a dataset.
--
-- The annotation workspace offers these classes when drawing boxes, in this
-- order (number keys 1-9 pick the first nine), each with an optional
-- display color ('' = derived from the name). The VOC files stay the source
-- of truth for the labels actually used: a box may carry a label that is
-- not in the list, and editing the list never rewrites annotation files.

ALTER TABLE datasets
    ADD COLUMN classes jsonb NOT NULL DEFAULT '[]'; -- [{"name": "scratch", "color": "#ef4444"}]
