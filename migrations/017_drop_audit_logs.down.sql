-- Recreates audit_logs as schema 16 left it: migration 002's definition without
-- filter_results, which migration 012 dropped.
--
-- Restored in full rather than as a stub because migration 012's own down adds
-- filter_results back to this table, and a rollback past 017 would otherwise
-- fail on a table that does not exist. Nothing writes it in either direction, so
-- the table comes back empty, which is the state it was in.
CREATE TABLE IF NOT EXISTS audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    request_id      VARCHAR(50) NOT NULL UNIQUE,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms     INT NOT NULL,
    gateway_overhead_ms INT NOT NULL,
    status_code     INT NOT NULL,

    organization_id VARCHAR(100) NOT NULL,
    team_id         VARCHAR(100) NOT NULL,
    user_id         VARCHAR(100),
    api_key_id      UUID NOT NULL REFERENCES api_keys(id),

    model_requested VARCHAR(100) NOT NULL,
    model_served    VARCHAR(100) NOT NULL,
    provider        VARCHAR(50) NOT NULL,
    endpoint        VARCHAR(100) NOT NULL,
    stream          BOOLEAN NOT NULL DEFAULT FALSE,
    classification  classification_tier NOT NULL,

    prompt_tokens       INT NOT NULL DEFAULT 0,
    completion_tokens   INT NOT NULL DEFAULT 0,
    total_tokens        INT NOT NULL DEFAULT 0,
    estimated_cost_cents INT NOT NULL DEFAULT 0,

    routing_attempts    INT NOT NULL DEFAULT 1,
    failovers           INT NOT NULL DEFAULT 0,

    project         VARCHAR(100),
    trace_id        VARCHAR(100)
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_org_ts ON audit_logs(organization_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_team_ts ON audit_logs(team_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_key_ts ON audit_logs(api_key_id, timestamp DESC);
