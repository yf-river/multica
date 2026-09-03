CREATE TABLE domain_event_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key text NOT NULL DEFAULT gen_random_uuid()::text,
    event_type text NOT NULL,
    stream_key text,
    workspace_id uuid,
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
    CHECK (processed_at IS NULL OR dead_lettered_at IS NULL),
    CHECK (length(btrim(idempotency_key)) > 0)
);

CREATE TABLE domain_event_delivery (
    event_id uuid NOT NULL,
    consumer text NOT NULL,
    delivered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, consumer)
);
