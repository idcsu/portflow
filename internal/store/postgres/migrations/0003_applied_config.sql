ALTER TABLE nodes
    ADD COLUMN IF NOT EXISTS applied_config_version BIGINT NOT NULL DEFAULT 1 CHECK (applied_config_version > 0);

