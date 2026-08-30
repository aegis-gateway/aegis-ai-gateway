-- Repeatable seed for audit_events volume measurements.
--
-- Two ad hoc measurements of the same quantity disagreed by 1.4x because each
-- invented its own corpus, and audit_events row size with indexes depends on
-- the width of the indexed values (request_id above all) rather than on the row
-- count. This file exists so that a later measurement compares against the
-- earlier one instead of against a different corpus.
--
-- The generated row populates exactly the columns audit.Logger.LogRequestComplete
-- sets, leaving user_agent, error_message, reason, error_detail and the limit_*
-- columns NULL as production does. Verified against a gateway-written row:
-- 231 bytes real, 238 bytes generated.
--
--   psql "$DATABASE_URL" -f scripts/measure-audit-volume.sql
--   psql "$DATABASE_URL" -c "select seed_audit_events(540500)"
--
-- Timestamps land two days in the past so the events clear the 300-second lag
-- window; seeding at now() leaves the sealer nothing to do and it exits
-- reporting success.
--
-- They also ASCEND with g, which matters and is not cosmetic. Three indexes are
-- ordered by timestamp DESC, so whether rows arrive oldest-first or newest-first
-- changes how btree pages split, and with it the index size. Seeding in
-- descending order measured 545 bytes per row where ascending measures 610, on
-- byte-identical heap data. Production appends in real time, so ascending is the
-- representative order. This one detail accounts for most of the disagreement
-- between the two earlier ad hoc measurements.
CREATE OR REPLACE FUNCTION seed_audit_events(n bigint) RETURNS void AS $$
INSERT INTO audit_events (
  request_id, timestamp, event_type, organization_id, team_id, user_id,
  api_key_id, api_key_prefix, ip_address, endpoint, method, status_code,
  provider, model, mode
)
SELECT
  'req_' || lpad(g::text, 13, '0') || '_' || md5(g::text)::varchar(16),
  now() - interval '2 days' + (g || ' milliseconds')::interval,
  'request_complete',
  'org-' || (g % 40),
  'team-' || (g % 120),
  CASE WHEN g % 4 = 0 THEN NULL ELSE 'user-' || (g % 900) END,
  gen_random_uuid(),
  'aegis-prod-' || substr(md5(g::text), 1, 8),
  '203.0.113.' || (g % 254),
  '/v1/chat/completions',
  'POST',
  CASE WHEN g % 50 = 0 THEN 503 ELSE 200 END,
  (ARRAY['anthropic','openai','azure_openai'])[1 + g % 3],
  (ARRAY['aegis-fast','aegis-smart','aegis-balanced'])[1 + g % 3],
  CASE WHEN g % 3 = 0 THEN 'stream' ELSE 'buffered' END
FROM generate_series(1, n) g;
$$ LANGUAGE sql;

-- Report the figures the documentation quotes.
CREATE OR REPLACE VIEW audit_volume_report AS
SELECT
  count(*)                                                        AS events,
  pg_size_pretty(pg_relation_size('audit_events'))                AS heap,
  pg_size_pretty(pg_indexes_size('audit_events'))                 AS indexes,
  pg_size_pretty(pg_total_relation_size('audit_events'))          AS total,
  round(pg_relation_size('audit_events')::numeric / count(*), 1)  AS heap_bytes_per_row,
  round(pg_total_relation_size('audit_events')::numeric / count(*), 1) AS total_bytes_per_row
FROM audit_events;
