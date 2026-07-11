-- name: ReserveSquadCreateRequest :one
-- Reservation, Squad, memberships, and replay response share one transaction.
INSERT INTO squad_create_request (
    workspace_id,
    actor_id,
    idempotency_key,
    request_hash
) VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetSquadCreateRequest :one
SELECT * FROM squad_create_request
WHERE workspace_id = $1
  AND actor_id = $2
  AND idempotency_key = $3;

-- name: CompleteSquadCreateRequest :one
UPDATE squad_create_request
SET squad_id = $5,
    response_body = sqlc.arg(response_body)::jsonb,
    completed_at = now()
WHERE workspace_id = $1
  AND actor_id = $2
  AND idempotency_key = $3
  AND request_hash = $4
  AND squad_id IS NULL
  AND response_body IS NULL
  AND completed_at IS NULL
RETURNING *;
