-- name: CreateDomainEvent :one
INSERT INTO domain_event_outbox (
    event_type, stream_key, workspace_id, actor_type, actor_id, task_id, chat_session_id, payload
) VALUES (
    sqlc.arg(event_type), sqlc.narg(stream_key), sqlc.narg(workspace_id), sqlc.narg(actor_type),
    sqlc.narg(actor_id), sqlc.narg(task_id), sqlc.narg(chat_session_id),
    sqlc.arg(payload)
)
RETURNING *;

-- name: ClaimDomainEvents :many
WITH picked AS (
    SELECT candidate.id
    FROM domain_event_outbox AS candidate
    WHERE candidate.processed_at IS NULL
      AND candidate.available_at <= now()
      AND (candidate.lease_until IS NULL OR candidate.lease_until < now())
      AND candidate.event_type = ANY(sqlc.arg(event_types)::text[])
      AND (
          candidate.stream_key IS NULL
          OR NOT EXISTS (
              SELECT 1
              FROM domain_event_outbox AS earlier
              WHERE earlier.processed_at IS NULL
                AND earlier.stream_key = candidate.stream_key
                AND earlier.sequence_no < candidate.sequence_no
          )
      )
    ORDER BY candidate.available_at, candidate.sequence_no
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
