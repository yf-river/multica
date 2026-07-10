DROP INDEX IF EXISTS idx_domain_event_outbox_pending_stream;

ALTER TABLE domain_event_outbox
    DROP CONSTRAINT IF EXISTS domain_event_outbox_stream_key_length,
    DROP COLUMN IF EXISTS sequence_no,
    DROP COLUMN IF EXISTS stream_key;
