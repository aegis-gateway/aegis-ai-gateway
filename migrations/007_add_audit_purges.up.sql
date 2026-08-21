-- 007_add_audit_purges.up.sql
-- Records every purge run so deletions are part of the audit trail.
-- This table is intentionally never purged.
CREATE TABLE IF NOT EXISTS audit_purges (
    id                       BIGSERIAL PRIMARY KEY,
    purged_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    window_start             TIMESTAMPTZ NOT NULL,
    window_end               TIMESTAMPTZ NOT NULL,
    event_id_min             BIGINT NOT NULL,
    event_id_max             BIGINT NOT NULL,
    rows_deleted             INTEGER NOT NULL,
    affected_checkpoint_ids  BIGINT[] NOT NULL,
    dry_run                  BOOLEAN NOT NULL DEFAULT FALSE
);
