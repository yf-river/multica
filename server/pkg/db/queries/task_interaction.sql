-- name: CreateTaskInteraction :one
INSERT INTO task_interaction (task_id, id, workspace_id, issue_id, provider, kind, request)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (task_id, id) DO UPDATE SET id = EXCLUDED.id
WHERE task_interaction.request = EXCLUDED.request
  AND task_interaction.provider = EXCLUDED.provider
  AND task_interaction.kind = EXCLUDED.kind
RETURNING *;

-- name: GetTaskInteraction :one
SELECT * FROM task_interaction WHERE task_id = $1 AND id = $2;

-- name: ListTaskInteractions :many
SELECT * FROM task_interaction WHERE task_id = $1
ORDER BY created_at, id;

-- name: RespondTaskInteraction :one
UPDATE task_interaction SET response = $3, author_id = $4, status = 'responded', responded_at = now()
WHERE task_id = $1 AND id = $2 AND status = 'pending' AND expires_at > now()
RETURNING *;

-- name: ExpireTaskInteraction :exec
UPDATE task_interaction SET status = 'expired'
WHERE task_id = $1 AND id = $2 AND status = 'pending';

-- name: RenewTaskInteractionLease :one
UPDATE task_interaction SET expires_at = now() + interval '60 seconds'
WHERE task_interaction.task_id = $1 AND task_interaction.id = $2 AND task_interaction.status = 'pending' AND task_interaction.expires_at > now()
  AND EXISTS (SELECT 1 FROM agent_task_queue WHERE agent_task_queue.id = task_interaction.task_id AND agent_task_queue.status = 'running')
RETURNING *;
