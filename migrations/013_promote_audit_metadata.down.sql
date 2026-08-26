-- Reverses 013: restores audit_events.metadata and folds the twelve columns back
-- into it.
--
-- Like the up migration, this refuses rather than corrupts. A database that has
-- sealed version-2 checkpoints cannot go back: a version-2 leaf hash covers the
-- twelve columns, and once they are gone it cannot be recomputed, so every
-- version-2 checkpoint would become unverifiable. Same failure, opposite
-- direction.
DO $$
DECLARE
    v2_count BIGINT;
BEGIN
    SELECT count(*) INTO v2_count
      FROM audit_checkpoints
     WHERE hash_schema_version = 2;

    IF v2_count > 0 THEN
        RAISE EXCEPTION
            'refusing to restore audit_events.metadata: % checkpoint(s) are sealed at hash_schema_version=2, whose leaf hash covers the columns this migration drops.',
            v2_count
        USING ERRCODE = 'raise_exception',
              HINT = 'The schema is unchanged: this check runs before any DDL. Clear the dirty flag with UPDATE schema_migrations SET version=13, dirty=false, then verify and archive the existing chain before rolling back.';
    END IF;
END
$$;

ALTER TABLE audit_events ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}';

-- Rebuild the JSONB from the columns, dropping nulls so an event that never had
-- a key does not gain one holding null. jsonb_strip_nulls does that in one pass.
UPDATE audit_events SET metadata = jsonb_strip_nulls(jsonb_build_object(
    'api_key_prefix', api_key_prefix,
    'dimension',      limit_dimension,
    'limit',          limit_value,
    'spent_cents',    spent_cents,
    'limit_cents',    limit_cents,
    'filter_type',    filter_type,
    'reason',         reason,
    'provider',       provider,
    'model',          model,
    'mode',           mode,
    'operation',      operation,
    'error',          error_detail
));

CREATE INDEX IF NOT EXISTS idx_audit_events_metadata ON audit_events USING GIN(metadata);

ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_metadata_bounded
    CHECK (pg_column_size(metadata) <= 4096);

ALTER TABLE audit_events
    DROP COLUMN api_key_prefix,
    DROP COLUMN limit_dimension,
    DROP COLUMN limit_value,
    DROP COLUMN spent_cents,
    DROP COLUMN limit_cents,
    DROP COLUMN filter_type,
    DROP COLUMN reason,
    DROP COLUMN provider,
    DROP COLUMN model,
    DROP COLUMN mode,
    DROP COLUMN operation,
    DROP COLUMN error_detail;
