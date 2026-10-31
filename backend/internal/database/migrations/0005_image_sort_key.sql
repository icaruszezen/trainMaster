-- 0005_image_sort_key: the fixed order of a dataset's images.
--
-- A dataset's images are listed, browsed and downloaded in one order, the
-- default "Name" order of Windows Explorer: folders before files, names
-- compared case-insensitively with digit runs compared as numbers (img2
-- before img10). sort_key encodes that order for the image's path, so a
-- byte-wise comparison of two keys gives the order of the two images. The
-- server computes it whenever it indexes an image (dataset.ImageSortKey);
-- rows indexed before this migration, or by another version of the rule,
-- get their key when the server starts.

ALTER TABLE dataset_images ADD COLUMN sort_key bytea;
CREATE INDEX dataset_images_order_idx ON dataset_images (dataset_id, sort_key, id);
-- Rows still waiting for a key; normally empty.
CREATE INDEX dataset_images_sort_key_missing_idx ON dataset_images (id) WHERE sort_key IS NULL;
