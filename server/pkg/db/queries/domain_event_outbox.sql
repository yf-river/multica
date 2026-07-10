-- name: CreateDomainEvent :one
INSERT INTO domain_event_outbox (
    event_type, workspace_id, actor_type, actor_id, task_id, chat_session_id, payload
) VALUES (
    sqlc.arg(event_type), sqlc.narg(workspace_id), sqlc.narg(actor_type),
    sqlc.narg(actor_id), sqlc.narg(task_id), sqlc.narg(chat_session_id),
    sqlc.arg(payload)
)
RETURNING *;

-- name: ClaimDomainEvents :many
WITH picked AS (
    SELECT id
    FROM domain_event_outbox
    WHERE processed_at IS NULL
      AND available_at <= now()
      AND (lease_until IS NULL OR lease_until < now())
      AND event_type = ANY(sqlc.arg(event_types)::text[])
    ORDER BY available_at, created_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE domain_event_outbox AS event
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = now() + sqlc.arg(lease_duration)::interval
FROM picked
WHERE event.id = picked.id
RETURNING event.*;

-- name: HasDomainEventDelivery :one
SELECT EXISTS (
    SELECT 1
    FROM domain_event_delivery
    WHERE event_id = sqlc.arg(event_id)
      AND consumer = sqlc.arg(consumer)
);

-- name: RecordDomainEventDelivery :exec
INSERT INTO domain_event_delivery (event_id, consumer)
VALUES (sqlc.arg(event_id), sqlc.arg(consumer))
ON CONFLICT (event_id, consumer) DO NOTHING;

-- name: CompleteDomainEvent :execrows
UPDATE domain_event_outbox
SET processed_at = now(),
    lease_owner = NULL,
    lease_until = NULL,
    last_error = NULL
WHERE id = sqlc.arg(id)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: RetryDomainEvent :execrows
UPDATE domain_event_outbox
SET attempts = attempts + 1,
    available_at = now() + sqlc.arg(retry_delay)::interval,
    lease_owner = NULL,
    lease_until = NULL,
    last_error = sqlc.arg(last_error)
WHERE id = sqlc.arg(id)
  AND lease_owner = sqlc.arg(lease_owner);
