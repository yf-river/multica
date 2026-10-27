CREATE INDEX CONCURRENTLY IF NOT EXISTS domain_event_outbox_pending_idx
    ON domain_event_outbox (available_at, sequence_no)
    WHERE processed_at IS NULL AND dead_lettered_at IS NULL;
