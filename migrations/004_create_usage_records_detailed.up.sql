CREATE TABLE usage_records (
    id                  BIGSERIAL PRIMARY KEY,
    request_id          VARCHAR(100) NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Identity
    organization_id     VARCHAR(100) NOT NULL,
    team_id             VARCHAR(100) NOT NULL,
    user_id             VARCHAR(100),
    api_key_id          VARCHAR(100) NOT NULL,
    
    -- Request details
    model_requested     VARCHAR(100) NOT NULL,
    model_served        VARCHAR(100) NOT NULL,
    provider            VARCHAR(50) NOT NULL,
    classification      VARCHAR(20) NOT NULL,
    
    -- Usage
    prompt_tokens       INT NOT NULL DEFAULT 0,
    completion_tokens   INT NOT NULL DEFAULT 0,
    total_tokens        INT NOT NULL DEFAULT 0,
    
    -- Cost
    estimated_cost_usd  DECIMAL(12, 6) NOT NULL DEFAULT 0,
    
    -- Performance
    duration_ms         INT NOT NULL,
    status_code         INT NOT NULL,
    
    -- Metadata
    project             VARCHAR(100),
    stream              BOOLEAN NOT NULL DEFAULT FALSE,
    
    CONSTRAINT fk_api_key FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE
);

-- Indexes for common query patterns
CREATE INDEX idx_usage_records_org_created ON usage_records(organization_id, created_at DESC);
CREATE INDEX idx_usage_records_team_created ON usage_records(team_id, created_at DESC);
CREATE INDEX idx_usage_records_api_key ON usage_records(api_key_id, created_at DESC);
CREATE INDEX idx_usage_records_model ON usage_records(model_requested, created_at DESC);
CREATE INDEX idx_usage_records_created ON usage_records(created_at DESC);
