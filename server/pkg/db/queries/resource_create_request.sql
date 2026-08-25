-- name: ReserveResourceCreateRequest :one
INSERT INTO resource_create_request (
    workspace_id,
    actor_id,
    resource_type,
    idempotency_key,
    request_hash
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetResourceCreateRequest :one
SELECT * FROM resource_create_request
WHERE workspace_id = $1
  AND actor_id = $2
  AND resource_type = $3
  AND idempotency_key = $4;

-- name: CompleteResourceCreateRequest :one
UPDATE resource_create_request
SET resource_id = $6,
    response_body = sqlc.arg(response_body)::jsonb,
    completed_at = now()
WHERE workspace_id = $1
  AND actor_id = $2
  AND resource_type = $3
  AND idempotency_key = $4
  AND request_hash = $5
  AND resource_id IS NULL
  AND response_body IS NULL
  AND completed_at IS NULL
RETURNING *;
