-- 0014_dataset_status: the status of a dataset (数据集状态).
--
-- A status says where a dataset is in the team's workflow: waiting to be
-- annotated (pending_annotation, 待标注), being annotated (annotating,
-- 标注中), waiting for a model review (pending_review, 待审图), being
-- reviewed (reviewing, 审图中) or ready to train (trainable, 可训练). It is
-- set by hand on the dataset pages; '' is a dataset nobody has marked yet
-- (every existing dataset). The platform does not change it by itself.
--
-- status_updated_at is when the status last changed (NULL: never set).
-- Changing the status does not touch updated_at, so marking datasets one
-- after the other does not reorder the dataset list.

ALTER TABLE datasets ADD COLUMN status text NOT NULL DEFAULT ''
    CHECK (status IN ('', 'pending_annotation', 'annotating', 'pending_review', 'reviewing', 'trainable'));
ALTER TABLE datasets ADD COLUMN status_updated_at timestamptz;
