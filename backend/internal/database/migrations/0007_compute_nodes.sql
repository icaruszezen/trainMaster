-- 0007_compute_nodes: training nodes that registered with the platform.
--
-- A training node is a trainer service (usually one container per GPU
-- machine) that registers itself and then sends heartbeats. It identifies
-- itself by node_key, a random ID it generates once and keeps in its state
-- directory, so a restarted node updates its row instead of adding one.
-- hardware, environment and metrics hold what the node last reported, in
-- the normalized form the API returns (see internal/compute). Whether a
-- node is online is not stored: the server derives it from last_seen_at
-- and the heartbeat interval. The recent metrics history is kept in memory
-- only.

CREATE TABLE compute_nodes (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    node_key           text NOT NULL UNIQUE,
    name               text NOT NULL,
    hostname           text NOT NULL DEFAULT '',
    address            text NOT NULL DEFAULT '',   -- client IP of the last registration or heartbeat
    agent_version      text NOT NULL DEFAULT '',
    hardware           jsonb NOT NULL,             -- static hardware, replaced on every registration
    environment        jsonb,                      -- CUDA / PyTorch / YOLO check; NULL until the first one finished
    metrics            jsonb,                      -- latest live metrics
    -- Set when a user asks the node to probe again; cleared by the next
    -- heartbeat that carries an environment.
    probe_requested_at timestamptz,
    registered_at      timestamptz NOT NULL DEFAULT now(), -- last (re)start of the node
    last_seen_at       timestamptz NOT NULL DEFAULT now(), -- last registration or heartbeat
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
