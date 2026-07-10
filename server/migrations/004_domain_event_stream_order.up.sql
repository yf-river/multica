ALTER TABLE domain_event_outbox
    ADD COLUMN stream_key text,
    ADD COLUMN sequence_no bigint GENERATED ALWAYS AS IDENTITY,
    ADD CONSTRAINT domain_event_outbox_stream_key_length
        CHECK (stream_key IS NULL OR char_length(stream_key) BETWEEN 1 AND 512);

CREATE INDEX idx_domain_event_outbox_pending_stream
    ON domain_event_outbox (stream_key, sequence_no)
    WHERE processed_at IS NULL AND stream_key IS NOT NULL;
