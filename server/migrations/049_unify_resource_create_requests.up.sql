CREATE TABLE resource_create_request (
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    resource_type text NOT NULL CHECK (resource_type IN ('project', 'squad', 'agent', 'skill')),
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    resource_id uuid,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (workspace_id, actor_id, resource_type, idempotency_key),
    CONSTRAINT resource_create_request_completion_check CHECK (
        (resource_id IS NULL AND response_body IS NULL AND completed_at IS NULL)
        OR
        (resource_id IS NOT NULL AND response_body IS NOT NULL AND completed_at IS NOT NULL)
    )
);

INSERT INTO resource_create_request (
    workspace_id, actor_id, resource_type, idempotency_key, request_hash,
    resource_id, response_body, created_at, completed_at
)
SELECT workspace_id, actor_id, 'project', idempotency_key, request_hash,
       project_id, response_body, created_at, completed_at
FROM project_create_request;

INSERT INTO resource_create_request (
    workspace_id, actor_id, resource_type, idempotency_key, request_hash,
    resource_id, response_body, created_at, completed_at
)
SELECT workspace_id, actor_id, 'squad', idempotency_key, request_hash,
       squad_id, response_body, created_at, completed_at
FROM squad_create_request;

DROP TABLE project_create_request;
DROP TABLE squad_create_request;
