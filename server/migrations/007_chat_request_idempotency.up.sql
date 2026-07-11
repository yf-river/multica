-- Public Chat creates carry a caller-generated operation UUID. The durable
-- record is written in the same transaction as the session/message/task so a
-- lost HTTP response can be replayed without creating a second business fact.
-- Records intentionally have no TTL: they are tombstones with the same order
-- of growth as chat mutations and must still prevent replay after a session is
-- deleted or a client stays offline for an arbitrary period.
BEGIN;

CREATE TABLE IF NOT EXISTS chat_idempotency_record (
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_idempotency_record_pkey PRIMARY KEY (
        workspace_id, actor_type, actor_id, operation, idempotency_key
    ),
    CONSTRAINT chat_idempotency_record_actor_type_check
        CHECK (actor_type IN ('member', 'agent')),
    CONSTRAINT chat_idempotency_record_operation_check
        CHECK (operation IN ('create_session', 'send_message')),
    CONSTRAINT chat_idempotency_record_request_hash_check
        CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chat_idempotency_record_response_check CHECK (
        (response_status IS NULL AND response_body IS NULL)
        OR
        (response_status = 201 AND response_body IS NOT NULL)
    )
);

COMMIT;
