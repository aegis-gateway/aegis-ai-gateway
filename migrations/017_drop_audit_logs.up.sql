-- Drops audit_logs, which nothing has ever written.
--
-- The table was created by migration 002 to hold the per-request decision
-- record. No component in this repository ever inserted into it: a
-- repository-wide search finds it in that migration, in internal/audit/reader.go
-- and in the purge command, and nowhere else. The decision record lives in
-- audit_events, which is the table the sealer covers and the only one that is
-- sealed.
--
-- Why remove it rather than leave it. An empty table is not inert when it is
-- named as though it holds the evidence. `GET /aegis/v1/audit/logs` returned an
-- empty list for its whole life and, in CSV, a full 21-column header before the
-- empty body, so an operator exporting the decision record received a
-- well-formed file with no rows. That reads as "no activity in this window",
-- which is a worse failure than an error: it answers the question incorrectly
-- instead of declining to answer. The endpoint was retired to 410 on 2026-08-29;
-- the table is the other half of the same trap, still reachable by anyone with
-- psql and a plausible-looking table name.
--
-- This is safe in a way most table drops are not: the table is provably empty in
-- every deployment, because no code path can have filled it. The migration still
-- checks rather than asserting it, and refuses if a row exists, since "provably
-- empty" is a claim about this repository and not about whatever else may have
-- been pointed at the database.
-- The check is the first statement and runs before the DROP, so a refusal
-- leaves the schema as it found it. golang-migrate runs the migration in a
-- transaction, which is what makes that true; running this file through psql
-- directly does NOT, because psql commits each statement separately and the
-- DROP would still execute after the exception. Verified through cmd/migrate.
--
-- golang-migrate marks version 17 dirty on a refusal, as it did for 13: the
-- flag records a migration that did not run, not one that ran halfway. Clear it
-- with UPDATE schema_migrations SET version=16, dirty=false.
DO $$
DECLARE
  n bigint;
BEGIN
  IF to_regclass('public.audit_logs') IS NULL THEN
    RAISE NOTICE 'audit_logs does not exist; nothing to drop';
    RETURN;
  END IF;
  EXECUTE 'SELECT count(*) FROM audit_logs' INTO n;
  IF n > 0 THEN
    RAISE EXCEPTION
      'refusing to drop audit_logs: it holds % row(s). Nothing in this repository writes that table, so something else did. Export or verify those rows before dropping.',
      n;
  END IF;
END $$;

DROP TABLE IF EXISTS audit_logs;
