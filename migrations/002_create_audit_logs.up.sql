-- DEPRECATED, AND NEVER WRITTEN.
--
-- No component inserts into audit_logs. A repository-wide search for the table
-- name finds this migration, internal/purge (which deletes from it),
-- internal/purge/schema_guard_test.go, and the audit canary's sweep. Nothing
-- writes it, and nothing ever has. The decision record lives in audit_events.
--
-- GET /aegis/v1/audit/logs served this table and now returns 410 Gone; see
-- internal/gateway/audit_handler.go. The table is left in place deliberately:
-- purge targets it by name, the schema guard asserts a time column exists on
-- it, and the no-payload canary sweeps it. Dropping it is a separate change
-- with its own risk, tracked in docs/evidence/known-limitations.md.
--
-- This header is a comment only. golang-migrate selects migrations by version
-- and does not checksum their contents, and migration 002 has already applied
-- everywhere it is going to, so adding prose here changes nothing executable.
-- Do not add DDL to this file: a schema change belongs in a new migration.

CREATE TABLE audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    request_id      VARCHAR(50) NOT NULL UNIQUE,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms     INT NOT NULL,
    gateway_overhead_ms INT NOT NULL,
    status_code     INT NOT NULL,

    -- Identity
    organization_id VARCHAR(100) NOT NULL,
    team_id         VARCHAR(100) NOT NULL,
    user_id         VARCHAR(100),
    api_key_id      UUID NOT NULL REFERENCES api_keys(id),

    -- Request details
    model_requested VARCHAR(100) NOT NULL,
    model_served    VARCHAR(100) NOT NULL,
    provider        VARCHAR(50) NOT NULL,
    endpoint        VARCHAR(100) NOT NULL,
    stream          BOOLEAN NOT NULL DEFAULT FALSE,
    classification  classification_tier NOT NULL,

    -- Tokens and cost
    prompt_tokens       INT NOT NULL DEFAULT 0,
    completion_tokens   INT NOT NULL DEFAULT 0,
    total_tokens        INT NOT NULL DEFAULT 0,
    estimated_cost_cents INT NOT NULL DEFAULT 0,

    -- Filter results (JSONB for flexibility)
    filter_results  JSONB NOT NULL DEFAULT '{}',

    -- Routing
    routing_attempts    INT NOT NULL DEFAULT 1,
    failovers           INT NOT NULL DEFAULT 0,

    -- Metadata
    project         VARCHAR(100),
    trace_id        VARCHAR(100)
);

CREATE INDEX idx_audit_logs_org_ts ON audit_logs(organization_id, timestamp DESC);
CREATE INDEX idx_audit_logs_team_ts ON audit_logs(team_id, timestamp DESC);
CREATE INDEX idx_audit_logs_key_ts ON audit_logs(api_key_id, timestamp DESC);
