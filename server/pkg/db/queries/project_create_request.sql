-- name: ReserveProjectCreateRequest :one
-- The incomplete row and all Project/resource writes share one transaction.
-- A concurrent request waits on the primary-key conflict and can only observe
-- the completed response after that transaction commits.
INSERT INTO project_create_request (
    workspace_id,
    actor_id,
    idempotency_key,
    request_hash
) VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetProjectCreateRequest :one
SELECT * FROM project_create_request
WHERE workspace_id = $1
  AND actor_id = $2
  AND idempotency_key = $3;

-- name: CompleteProjectCreateRequest :one
UPDATE project_create_request
SET project_id = $5,
    response_body = sqlc.arg(response_body)::jsonb,
    completed_at = now()
WHERE workspace_id = $1
  AND actor_id = $2
  AND idempotency_key = $3
  AND request_hash = $4
  AND project_id IS NULL
  AND response_body IS NULL
  AND completed_at IS NULL
RETURNING *;
