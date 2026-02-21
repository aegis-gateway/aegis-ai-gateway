CREATE TYPE classification_tier AS ENUM ('PUBLIC', 'INTERNAL', 'CONFIDENTIAL', 'RESTRICTED');
CREATE TYPE key_status AS ENUM ('active', 'revoked', 'expired');

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash        VARCHAR(64) NOT NULL UNIQUE,
    key_prefix      VARCHAR(20) NOT NULL,
    organization_id VARCHAR(100) NOT NULL,
    team_id         VARCHAR(100) NOT NULL,
    user_id         VARCHAR(100),
    name            VARCHAR(255) NOT NULL,
    status          key_status NOT NULL DEFAULT 'active',

    -- Permissions
    max_classification  classification_tier NOT NULL DEFAULT 'INTERNAL',
    allowed_models      JSONB DEFAULT '[]',

    -- Rate limits (NULL = use defaults)
    rpm_limit       INT,
    tpm_limit       INT,
    daily_spend_limit_cents INT,

    -- Lifecycle
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,
    last_used_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    revoked_reason  TEXT,

    CONSTRAINT valid_expiry CHECK (expires_at > created_at)
);

CREATE INDEX idx_api_keys_key_hash ON api_keys(key_hash) WHERE status = 'active';
CREATE INDEX idx_api_keys_org ON api_keys(organization_id);
CREATE INDEX idx_api_keys_expires ON api_keys(expires_at) WHERE status = 'active';
