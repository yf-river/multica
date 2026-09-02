CREATE TABLE domain_event_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type text NOT NULL,
    stream_key text,
    workspace_id uuid REFERENCES workspace(id) ON DELETE CASCADE,
    actor_type text,
    actor_id text,
    task_id text,
    chat_session_id text,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    last_error text,
    processed_at timestamptz,
    dead_lettered_at timestamptz,
    dead_letter_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    sequence_no bigint GENERATED ALWAYS AS IDENTITY,
    CHECK (processed_at IS NULL OR dead_lettered_at IS NULL)
);
CREATE INDEX domain_event_outbox_pending_idx ON domain_event_outbox (available_at, sequence_no)
  WHERE processed_at IS NULL AND dead_lettered_at IS NULL;
CREATE INDEX domain_event_outbox_stream_idx ON domain_event_outbox (stream_key, sequence_no)
  WHERE processed_at IS NULL AND dead_lettered_at IS NULL AND stream_key IS NOT NULL;
CREATE TABLE domain_event_delivery (
    event_id uuid NOT NULL REFERENCES domain_event_outbox(id) ON DELETE CASCADE,
    consumer text NOT NULL,
    PRIMARY KEY (event_id, consumer)
);
