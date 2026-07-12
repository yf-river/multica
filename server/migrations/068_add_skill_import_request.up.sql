CREATE TABLE skill_import_request (
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (workspace_id, actor_id, idempotency_key),
    CONSTRAINT skill_import_request_completion_check CHECK (
        (response_status IS NULL AND response_body IS NULL AND completed_at IS NULL)
        OR
        (response_status BETWEEN 200 AND 599 AND response_body IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX idx_skill_import_request_completed_at
    ON skill_import_request (completed_at)
    WHERE completed_at IS NOT NULL;

CREATE INDEX idx_skill_import_request_incomplete_created_at
    ON skill_import_request (created_at)
    WHERE completed_at IS NULL;
