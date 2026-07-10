ALTER TABLE domain_event_outbox
    ADD COLUMN dead_lettered_at timestamptz,
    ADD COLUMN dead_letter_reason text,
    ADD CONSTRAINT domain_event_outbox_single_terminal_state
        CHECK (processed_at IS NULL OR dead_lettered_at IS NULL);

DROP INDEX idx_domain_event_outbox_pending;
CREATE INDEX idx_domain_event_outbox_pending
    ON domain_event_outbox (available_at, sequence_no)
    WHERE processed_at IS NULL AND dead_lettered_at IS NULL;

DROP INDEX idx_domain_event_outbox_pending_stream;
CREATE INDEX idx_domain_event_outbox_pending_stream
    ON domain_event_outbox (stream_key, sequence_no)
    WHERE processed_at IS NULL AND dead_lettered_at IS NULL AND stream_key IS NOT NULL;

CREATE INDEX idx_domain_event_outbox_dead_lettered
    ON domain_event_outbox (dead_lettered_at, sequence_no)
    WHERE dead_lettered_at IS NOT NULL;
