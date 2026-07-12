CREATE TABLE autopilot_trigger_rotation_request (
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    trigger_id uuid NOT NULL REFERENCES autopilot_trigger(id) ON DELETE CASCADE,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (workspace_id, actor_id, idempotency_key),
    CONSTRAINT autopilot_trigger_rotation_request_completion_check CHECK (
        (response_status IS NULL AND response_body IS NULL AND completed_at IS NULL)
        OR
        (response_status BETWEEN 200 AND 599 AND response_body IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX idx_autopilot_trigger_rotation_request_completed_at
    ON autopilot_trigger_rotation_request (completed_at)
    WHERE completed_at IS NOT NULL;

CREATE INDEX idx_autopilot_trigger_rotation_request_incomplete_created_at
    ON autopilot_trigger_rotation_request (created_at)
    WHERE completed_at IS NULL;
