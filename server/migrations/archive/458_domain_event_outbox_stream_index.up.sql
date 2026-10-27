CREATE INDEX CONCURRENTLY IF NOT EXISTS domain_event_outbox_stream_idx
    ON domain_event_outbox (stream_key, sequence_no)
    WHERE processed_at IS NULL AND dead_lettered_at IS NULL AND stream_key IS NOT NULL;
