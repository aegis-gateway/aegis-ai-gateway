-- usage_records stored prompt_tokens as one opaque number, so a row could not
-- say how much of the prompt was served from cache or written to it. Those
-- subsets are priced differently — a cache read at 0.1x base input, a 5-minute
-- write at 1.25x, a one-hour write at 2x — which means estimated_cost_usd could
-- not be recomputed from the row that recorded it.
--
-- That is what made the cost defects fixed in #59 and #60 unreconcilable after
-- the fact: the mispricing is known, the affected rows are known, and the
-- inputs needed to recompute them were never written down. Rows created from
-- this migration onwards carry their own breakdown.
--
-- DEFAULT 0 backfills existing rows with a value that is not a measurement.
-- Zero here means "not recorded", not "no caching happened"; created_at against
-- this migration's deploy is what separates the two, and cmd/reconcile-usage
-- refuses to recompute rows that predate it.
ALTER TABLE usage_records
    ADD COLUMN cached_tokens          INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN cache_write_5m_tokens  INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN cache_write_1h_tokens  INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN usage_records.cached_tokens IS
    'Subset of prompt_tokens served from the provider cache. 0 on rows created before migration 014, where it means "not recorded".';
COMMENT ON COLUMN usage_records.cache_write_5m_tokens IS
    'Subset of prompt_tokens written to a 5-minute cache entry, disjoint from cached_tokens.';
COMMENT ON COLUMN usage_records.cache_write_1h_tokens IS
    'Subset of prompt_tokens written to a 1-hour cache entry, disjoint from cached_tokens.';
