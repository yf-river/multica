CREATE TABLE domain_event_outbox (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    event_type text NOT NULL,
    workspace_id uuid REFERENCES workspace(id) ON DELETE CASCADE,
    actor_type text,
    actor_id text,
    task_id text,
    chat_session_id text,
    payload jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    last_error text,
    processed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX idx_domain_event_outbox_pending
    ON domain_event_outbox (available_at, created_at, id)
    WHERE processed_at IS NULL;

CREATE TABLE domain_event_delivery (
    event_id uuid NOT NULL REFERENCES domain_event_outbox(id) ON DELETE CASCADE,
    consumer text NOT NULL,
    delivered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, consumer)
);
