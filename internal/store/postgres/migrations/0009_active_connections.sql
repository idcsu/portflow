CREATE TABLE IF NOT EXISTS active_connection_snapshots (
    node_id         TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    connection_id   TEXT NOT NULL,
    rule_id         TEXT NOT NULL,
    protocol        TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp')),
    source_address  TEXT NOT NULL,
    target_address  TEXT NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL,
    last_activity   TIMESTAMPTZ NOT NULL,
    bytes_in        BIGINT NOT NULL CHECK (bytes_in >= 0),
    bytes_out       BIGINT NOT NULL CHECK (bytes_out >= 0),
    observed_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (node_id, connection_id)
);

CREATE INDEX IF NOT EXISTS active_connection_snapshots_activity_idx
    ON active_connection_snapshots(last_activity DESC);
