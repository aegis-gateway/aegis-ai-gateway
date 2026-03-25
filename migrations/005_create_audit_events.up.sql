-- Audit events table for security and compliance logging
CREATE TABLE IF NOT EXISTS audit_events (
    id                  BIGSERIAL PRIMARY KEY,
    request_id          VARCHAR(50) NOT NULL,
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type          VARCHAR(50) NOT NULL,
    
    -- Identity (nullable for unauthenticated failures)
    organization_id     VARCHAR(100),
    team_id             VARCHAR(100),
    user_id             VARCHAR(100),
    api_key_id          VARCHAR(100),
    
    -- Request context
    ip_address          VARCHAR(45),  -- IPv6 max length
    user_agent          TEXT,
    endpoint            VARCHAR(200),
    method              VARCHAR(10),
    status_code         INT,
    
    -- Event details
    error_message       TEXT,
    metadata            JSONB NOT NULL DEFAULT '{}'
);

-- Indexes for common queries
CREATE INDEX idx_audit_events_timestamp ON audit_events(timestamp DESC);
CREATE INDEX idx_audit_events_org_time ON audit_events(organization_id, timestamp DESC);
CREATE INDEX idx_audit_events_team_time ON audit_events(team_id, timestamp DESC);
CREATE INDEX idx_audit_events_event_type ON audit_events(event_type, timestamp DESC);
CREATE INDEX idx_audit_events_request_id ON audit_events(request_id);
CREATE INDEX idx_audit_events_api_key ON audit_events(api_key_id, timestamp DESC);

-- GIN index for JSONB metadata queries
CREATE INDEX idx_audit_events_metadata ON audit_events USING GIN(metadata);
