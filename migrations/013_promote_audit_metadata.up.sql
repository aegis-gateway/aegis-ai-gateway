-- Replaces audit_events.metadata with typed, bounded columns, and cuts
-- hash_schema_version 2.
--
-- Step 2 of the schema work in VERIFICATION.md 4.2. The JSONB column is the last
-- unbounded, untyped surface on the audit table: the one place a future change
-- could put caller text without anyone having to alter a schema. Twelve keys are
-- written across the six Log* methods in internal/audit/logger.go, all of them
-- short and known, so all twelve become columns.
--
-- ---------------------------------------------------------------------------
-- Why this migration can refuse to run
--
-- metadata is one of the fifteen fields the leaf hash covers at
-- hash_schema_version=1. A version-1 leaf cannot be recomputed once the column
-- it hashes is gone, so dropping metadata in a database that holds version-1
-- checkpoints would leave every one of them permanently unverifiable, and it
-- would do so silently: verify-chain would report a chain break long after the
-- cause had scrolled out of anyone's memory.
--
-- Rather than carry metadata forever, or split this across two releases, the
-- migration checks. If any version-1 checkpoint exists it aborts and says so.
-- The operator's choices are then to stay on schema 12, or to verify and archive
-- the existing chain before migrating. Neither is silent.
--
-- The check is the first statement, before any DDL, so a refusal leaves the
-- schema exactly as it found it. golang-migrate still marks version 13 dirty
-- because the migration returned an error, which is worth knowing before it
-- causes alarm: the dirty flag records a migration that did not run, not one that
-- ran halfway. cmd/migrate has no force subcommand, so it is cleared with
-- UPDATE schema_migrations SET version=12, dirty=false. Verified on PostgreSQL 16,
-- where a refused run left audit_events with metadata intact and none of the
-- twelve new columns present.
--
-- The consequence is worth stating plainly, because it is what makes the rest of
-- this change simple: any database that completes this migration provably has no
-- version-1 checkpoints, so the binary that requires it never needs a version-1
-- verifier at all.
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    v1_count BIGINT;
BEGIN
    SELECT count(*) INTO v1_count
      FROM audit_checkpoints
     WHERE hash_schema_version = 1;

    IF v1_count > 0 THEN
        RAISE EXCEPTION
            'refusing to drop audit_events.metadata: % checkpoint(s) are sealed at hash_schema_version=1, and a version-1 leaf hash cannot be recomputed without that column. Verify and archive the existing chain first, or stay on schema 12.',
            v1_count
        USING ERRCODE = 'raise_exception',
              HINT = 'The schema is unchanged: this check runs before any DDL, so nothing is half-applied. The migrator still marks version 13 dirty; cmd/migrate has no force subcommand, so clear it with: UPDATE schema_migrations SET version=12, dirty=false. To proceed instead: verify the chain (cmd/migrate verify-chain --full), archive audit_checkpoints and audit_events, then re-run.';
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- The twelve columns, one per metadata key.
--
-- Widths follow the same rule as migration 012: wide enough that no legitimate
-- value trips them, because a bound a real value trips is not a safety property,
-- it is a way to lose an audit row. internal/audit/limits.go clips every value to
-- these numbers before the insert, and TestSchemaLimitsMatchMigration fails if
-- the two ever disagree.
--
-- reason is the widest at 512. It carries the joined policy deny message, which
-- Rego builds with concat("; ", deny), so it grows with the number of rules that
-- fire: the shipped RESTRICTED rule alone produces 119 characters, and two rules
-- together produce 201.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events
    ADD COLUMN api_key_prefix   VARCHAR(32),
    ADD COLUMN limit_dimension  VARCHAR(32),
    ADD COLUMN limit_value      BIGINT,
    ADD COLUMN spent_cents      BIGINT,
    ADD COLUMN limit_cents      BIGINT,
    ADD COLUMN filter_type      VARCHAR(32),
    ADD COLUMN reason           VARCHAR(512),
    ADD COLUMN provider         VARCHAR(64),
    ADD COLUMN model            VARCHAR(128),
    ADD COLUMN mode             VARCHAR(32),
    ADD COLUMN operation        VARCHAR(64),
    ADD COLUMN error_detail     VARCHAR(512);

-- Two keys are renamed on the way out of the JSONB:
--   "dimension" -> limit_dimension, because a bare "dimension" says nothing
--     about which limit it dimensions.
--   "limit"     -> limit_value, because LIMIT is a reserved word in SQL and a
--     quoted identifier is a trap for whoever writes the next query by hand.
--   "error"     -> error_detail, to keep it distinct from error_message, which
--     is a different string with a different audience.

-- ---------------------------------------------------------------------------
-- Backfill. left(...) rather than a cast so an over-long historical value is
-- carried in truncated rather than failing the migration; the values are short
-- by construction, so this should be a no-op.
--
-- The numeric backfills match on the digit pattern first. A cast on unexpected
-- text would abort the whole migration, and losing a database upgrade to one
-- malformed historical row is a worse outcome than leaving that row's counter
-- null.
-- ---------------------------------------------------------------------------
UPDATE audit_events SET
    api_key_prefix  = left(metadata->>'api_key_prefix', 32),
    limit_dimension = left(metadata->>'dimension',      32),
    filter_type     = left(metadata->>'filter_type',    32),
    reason          = left(metadata->>'reason',        512),
    provider        = left(metadata->>'provider',       64),
    model           = left(metadata->>'model',         128),
    mode            = left(metadata->>'mode',           32),
    operation       = left(metadata->>'operation',      64),
    error_detail    = left(metadata->>'error',         512),
    limit_value     = CASE WHEN metadata->>'limit'       ~ '^-?[0-9]+$' THEN (metadata->>'limit')::BIGINT       END,
    spent_cents     = CASE WHEN metadata->>'spent_cents' ~ '^-?[0-9]+$' THEN (metadata->>'spent_cents')::BIGINT END,
    limit_cents     = CASE WHEN metadata->>'limit_cents' ~ '^-?[0-9]+$' THEN (metadata->>'limit_cents')::BIGINT END
WHERE metadata IS NOT NULL AND metadata <> '{}'::jsonb;

-- ---------------------------------------------------------------------------
-- Drop the column, its GIN index, and the size constraint 012 added as a
-- stopgap. From here the audit table has no untyped column at all.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_metadata_bounded;
DROP INDEX IF EXISTS idx_audit_events_metadata;
ALTER TABLE audit_events DROP COLUMN metadata;

-- Events written from here are sealed at hash_schema_version=2, whose field set
-- is defined in docs/AUDIT-INTEGRITY.md section 5.1.
