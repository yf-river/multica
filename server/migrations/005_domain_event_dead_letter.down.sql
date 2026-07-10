DROP INDEX idx_domain_event_outbox_dead_lettered;

DROP INDEX idx_domain_event_outbox_pending_stream;
CREATE INDEX idx_domain_event_outbox_pending_stream
    ON domain_event_outbox (stream_key, sequence_no)
    WHERE processed_at IS NULL AND stream_key IS NOT NULL;

DROP INDEX idx_domain_event_outbox_pending;
CREATE INDEX idx_domain_event_outbox_pending
    ON domain_event_outbox (available_at, created_at, id)
    WHERE processed_at IS NULL;

ALTER TABLE domain_event_outbox
    DROP CONSTRAINT domain_event_outbox_single_terminal_state,
    DROP COLUMN dead_letter_reason,
    DROP COLUMN dead_lettered_at;
