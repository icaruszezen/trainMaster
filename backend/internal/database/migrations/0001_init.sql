-- 0001_init: infrastructure only.
-- Business tables (datasets, annotations, training jobs, ...) are added by
-- later migrations together with the features that use them.

-- app_settings stores platform-wide settings as key/value pairs.
CREATE TABLE app_settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
