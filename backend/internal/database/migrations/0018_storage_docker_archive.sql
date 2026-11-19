-- 0018_storage_docker_archive: Docker storage container node kind and bulk archive migration telemetry.

ALTER TABLE storage_nodes
    DROP CONSTRAINT IF EXISTS storage_nodes_kind_check,
    ADD CONSTRAINT storage_nodes_kind_check CHECK (kind IN ('local', 'docker', 's3', 'smb'));

ALTER TABLE storage_migrations
    ADD COLUMN transfer_mode text NOT NULL DEFAULT 'direct'
        CHECK (transfer_mode IN ('direct', 'archive')),
    ADD COLUMN stage text NOT NULL DEFAULT '',
    ADD COLUMN archive_bytes bigint NOT NULL DEFAULT 0;
