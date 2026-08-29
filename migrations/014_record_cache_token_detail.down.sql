ALTER TABLE usage_records
    DROP COLUMN cached_tokens,
    DROP COLUMN cache_write_5m_tokens,
    DROP COLUMN cache_write_1h_tokens;
