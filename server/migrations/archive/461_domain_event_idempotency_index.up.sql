CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS domain_event_outbox_idempotency_idx
    ON domain_event_outbox (idempotency_key);
