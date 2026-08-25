-- name: ReserveChatIdempotencyRecord :one
-- The row is intentionally incomplete while its transaction is open. A
-- concurrent request with the same key waits on the primary-key conflict and
-- can only observe the completed row after the first transaction commits.
INSERT INTO chat_idempotency_record (
    workspace_id,
    actor_type,
    actor_id,
    operation,
    idempotency_key,
    request_hash
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: LockChatIdempotencyRecord :one
SELECT * FROM chat_idempotency_record
WHERE workspace_id = $1
  AND actor_type = $2
  AND actor_id = $3
  AND operation = $4
  AND idempotency_key = $5
FOR UPDATE;

-- name: CompleteChatIdempotencyRecord :one
UPDATE chat_idempotency_record
SET response_status = $7,
    response_body = sqlc.arg(response_body)::jsonb
WHERE workspace_id = $1
  AND actor_type = $2
  AND actor_id = $3
  AND operation = $4
  AND idempotency_key = $5
  AND request_hash = $6
  AND response_status IS NULL
  AND response_body IS NULL
RETURNING *;
