CREATE TABLE usage_daily (
    id              BIGSERIAL PRIMARY KEY,
    date            DATE NOT NULL,
    organization_id VARCHAR(100) NOT NULL,
    team_id         VARCHAR(100) NOT NULL,
    model           VARCHAR(100) NOT NULL,
    provider        VARCHAR(50) NOT NULL,

    request_count       INT NOT NULL DEFAULT 0,
    prompt_tokens       BIGINT NOT NULL DEFAULT 0,
    completion_tokens   BIGINT NOT NULL DEFAULT 0,
    total_cost_cents    BIGINT NOT NULL DEFAULT 0,

    -- Filter stats
    pii_redactions      INT NOT NULL DEFAULT 0,
    pii_blocks          INT NOT NULL DEFAULT 0,
    secret_blocks       INT NOT NULL DEFAULT 0,
    injection_blocks    INT NOT NULL DEFAULT 0,
    policy_blocks       INT NOT NULL DEFAULT 0,

    UNIQUE(date, organization_id, team_id, model, provider)
);

CREATE INDEX idx_usage_daily_org ON usage_daily(organization_id, date DESC);
