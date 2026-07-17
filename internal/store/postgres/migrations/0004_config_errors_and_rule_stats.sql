ALTER TABLE nodes
    ADD COLUMN IF NOT EXISTS attempted_config_version BIGINT NOT NULL DEFAULT 1 CHECK (attempted_config_version > 0),
    ADD COLUMN IF NOT EXISTS last_config_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_config_attempt TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS rule_runtime_stats (
    rule_id             TEXT PRIMARY KEY REFERENCES forward_rules(id) ON DELETE CASCADE,
    node_id             TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    active_connections  BIGINT NOT NULL DEFAULT 0 CHECK (active_connections >= 0),
    bytes_in            BIGINT NOT NULL DEFAULT 0 CHECK (bytes_in >= 0),
    bytes_out           BIGINT NOT NULL DEFAULT 0 CHECK (bytes_out >= 0),
    updated_at          TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS rule_runtime_stats_node_id_idx ON rule_runtime_stats(node_id);

