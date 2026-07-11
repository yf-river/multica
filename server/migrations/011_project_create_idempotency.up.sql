CREATE TABLE project_create_request (
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    project_id uuid REFERENCES project(id) ON DELETE CASCADE,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (workspace_id, actor_id, idempotency_key),
    CONSTRAINT project_create_request_completion_check CHECK (
        (project_id IS NULL AND response_body IS NULL AND completed_at IS NULL)
        OR
        (project_id IS NOT NULL AND response_body IS NOT NULL AND completed_at IS NOT NULL)
    )
);
