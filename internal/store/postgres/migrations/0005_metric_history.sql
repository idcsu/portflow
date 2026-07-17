CREATE TABLE IF NOT EXISTS node_metric_samples (
    node_id             TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    sampled_at          TIMESTAMPTZ NOT NULL,
    cpu_percent         DOUBLE PRECISION NOT NULL CHECK (cpu_percent BETWEEN 0 AND 100),
    memory_percent      DOUBLE PRECISION NOT NULL CHECK (memory_percent BETWEEN 0 AND 100),
    load_one            DOUBLE PRECISION NOT NULL CHECK (load_one >= 0),
    active_connections  BIGINT NOT NULL CHECK (active_connections >= 0),
    bytes_in            BIGINT NOT NULL CHECK (bytes_in >= 0),
    bytes_out           BIGINT NOT NULL CHECK (bytes_out >= 0),
    PRIMARY KEY (node_id, sampled_at)
);
CREATE INDEX IF NOT EXISTS node_metric_samples_sampled_at_idx ON node_metric_samples(sampled_at);
