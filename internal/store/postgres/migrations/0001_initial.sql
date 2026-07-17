CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    username        TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL UNIQUE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS enrollment_tokens (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    token_hash      TEXT NOT NULL UNIQUE,
    created_by      TEXT NOT NULL REFERENCES users(id),
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS enrollment_tokens_expires_at_idx ON enrollment_tokens(expires_at);

CREATE TABLE IF NOT EXISTS nodes (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    region          TEXT NOT NULL DEFAULT '',
    public_ip       INET NOT NULL,
    architecture    TEXT NOT NULL,
    agent_version   TEXT NOT NULL,
    credential_hash TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL CHECK (status IN ('online', 'offline', 'disabled')),
    last_heartbeat  TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS nodes_status_idx ON nodes(status);

CREATE TABLE IF NOT EXISTS forward_rules (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    protocol            TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp', 'tcp_udp')),
    mode                TEXT NOT NULL CHECK (mode IN ('direct', 'relay')),
    ingress_node_id     TEXT NOT NULL REFERENCES nodes(id),
    egress_node_id      TEXT REFERENCES nodes(id),
    listen_host         TEXT NOT NULL,
    listen_port         INTEGER NOT NULL CHECK (listen_port BETWEEN 1 AND 65535),
    target_host         TEXT NOT NULL,
    target_port         INTEGER NOT NULL CHECK (target_port BETWEEN 1 AND 65535),
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    bandwidth_kbps      BIGINT NOT NULL DEFAULT 0 CHECK (bandwidth_kbps >= 0),
    max_connections     INTEGER NOT NULL DEFAULT 0 CHECK (max_connections >= 0),
    allow_cidrs         JSONB NOT NULL DEFAULT '[]'::jsonb,
    deny_cidrs          JSONB NOT NULL DEFAULT '[]'::jsonb,
    config_version      BIGINT NOT NULL DEFAULT 1 CHECK (config_version > 0),
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    CHECK ((mode = 'direct' AND egress_node_id IS NULL) OR (mode = 'relay' AND egress_node_id IS NOT NULL)),
    CHECK (egress_node_id IS NULL OR ingress_node_id <> egress_node_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS forward_rules_listener_idx
    ON forward_rules(ingress_node_id, protocol, listen_host, listen_port);

CREATE TABLE IF NOT EXISTS audit_events (
    id              TEXT PRIMARY KEY,
    actor_type      TEXT NOT NULL,
    actor_id        TEXT,
    action          TEXT NOT NULL,
    target_type     TEXT NOT NULL,
    target_id       TEXT,
    remote_ip       TEXT,
    details         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_created_at_idx ON audit_events(created_at DESC);

