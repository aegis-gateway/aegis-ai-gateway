-- hash_version=1: legacy SHA-256 (existing rows)
-- hash_version=2: HMAC-SHA256 with server-side pepper (new rows)
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS hash_version INTEGER NOT NULL DEFAULT 1;

COMMENT ON COLUMN api_keys.hash_version IS '1=SHA-256 (legacy), 2=HMAC-SHA256 with server pepper';
