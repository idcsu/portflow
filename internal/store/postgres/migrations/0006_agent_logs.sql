CREATE TABLE agent_logs (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    event_id TEXT NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('info', 'warning', 'error')),
    component TEXT NOT NULL,
    message TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (node_id, event_id)
);

CREATE INDEX agent_logs_occurred_at_idx ON agent_logs(occurred_at DESC);
CREATE INDEX agent_logs_node_occurred_at_idx ON agent_logs(node_id, occurred_at DESC);
CREATE INDEX agent_logs_received_at_idx ON agent_logs(received_at);
