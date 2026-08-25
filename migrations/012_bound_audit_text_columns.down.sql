-- Reverses 012, with one deliberate exception.
--
-- filter_results comes back as an empty JSONB column. The original data is not
-- restored because there was none: no code path ever wrote it.
--
-- ip_address is NOT narrowed back to VARCHAR(45). That width is the defect 012
-- repairs, not a decision it changes: at 45 characters an IPv6 client's
-- RemoteAddr overflows, PostgreSQL rejects the insert, and the audit row is lost.
-- Reintroducing that is not a rollback, it is a regression, and it is one a
-- rollback cannot even perform safely: any row already stored with a longer
-- address makes the ALTER fail partway through the migration and leaves
-- schema_migrations dirty. Observed on PostgreSQL 16 while testing this pair:
--
--     pq: value too long for type character varying(45)   (schema_migrations: 11, dirty)
--
-- Widening is forward-compatible in both directions. Version 11 code writes
-- addresses that fit in 45 characters and is unaffected by a column that allows
-- 64, so leaving it wide costs a rollback nothing.

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_metadata_bounded;

ALTER TABLE audit_events ALTER COLUMN user_agent TYPE TEXT;
ALTER TABLE audit_events ALTER COLUMN error_message TYPE TEXT;

ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS filter_results JSONB NOT NULL DEFAULT '{}';
