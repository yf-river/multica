-- name: ReserveAutopilotTriggerRotationRequest :one
INSERT INTO autopilot_trigger_rotation_request (
    workspace_id, actor_id, idempotency_key, trigger_id, request_hash
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetAutopilotTriggerRotationRequest :one
SELECT * FROM autopilot_trigger_rotation_request
WHERE workspace_id = $1 AND actor_id = $2 AND idempotency_key = $3;

-- name: CompleteAutopilotTriggerRotationRequest :one
UPDATE autopilot_trigger_rotation_request
SET response_status = $6,
    response_body = sqlc.arg(response_body)::jsonb,
    completed_at = now()
WHERE workspace_id = $1 AND actor_id = $2 AND idempotency_key = $3
  AND trigger_id = $4 AND request_hash = $5 AND completed_at IS NULL
RETURNING *;
