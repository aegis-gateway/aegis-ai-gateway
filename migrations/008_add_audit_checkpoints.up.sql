-- audit_checkpoints stores Merkle checkpoint records for tamper-evident audit.
-- See docs/AUDIT-INTEGRITY.md for the full design and hash construction spec.
CREATE TABLE IF NOT EXISTS audit_checkpoints (
    id                    BIGSERIAL PRIMARY KEY,
    range_start           BIGINT NOT NULL,            -- first event_id covered (inclusive)
    range_end             BIGINT NOT NULL,            -- last event_id covered (inclusive)
    event_count           INTEGER NOT NULL,
    merkle_root           BYTEA NOT NULL,             -- 32-byte SHA-256 root (RFC 6962)
    prev_checkpoint_id    BIGINT REFERENCES audit_checkpoints(id),
    prev_checkpoint_hash  BYTEA,                      -- NULL for genesis; otherwise previous checkpoint_hash
    checkpoint_hash       BYTEA NOT NULL,             -- see docs/AUDIT-INTEGRITY.md §3 for hash input
    hash_schema_version   INTEGER NOT NULL DEFAULT 1,
    sealed_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sealer_version        TEXT NOT NULL,              -- unauthenticated debug metadata; not in hash
    canonicalization_spec TEXT NOT NULL DEFAULT 'rfc8785-v1'
);

CREATE UNIQUE INDEX ON audit_checkpoints (range_start, range_end);
CREATE INDEX ON audit_checkpoints (prev_checkpoint_id);
