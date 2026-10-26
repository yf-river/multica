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

-- name: CreateSuspendedTaskInteraction :one
INSERT INTO task_interaction (task_id, id, workspace_id, issue_id, provider, kind, request, delivery_mode, expires_at)
VALUES ($1, $2, $3, $4, 'dsh', $5, $6, 'suspended', now() + interval '24 hours')
RETURNING *;

-- name: SuspendAgentTaskForInteraction :one
UPDATE agent_task_queue
SET status = sqlc.arg('status'), completed_at = now(), result = sqlc.arg('result'),
    session_id = sqlc.arg('session_id'), work_dir = sqlc.arg('work_dir'),
    prepare_lease_expires_at = NULL
WHERE id = sqlc.arg('id') AND status = 'running'
RETURNING *;

-- name: GetSuspendedTaskInteraction :one
SELECT * FROM task_interaction WHERE task_id = $1 AND delivery_mode = 'suspended'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: GetTaskInteractionFollowup :one
SELECT * FROM task_interaction WHERE followup_task_id = $1;

-- name: GetAgentTaskForInteractionUpdate :one
SELECT * FROM agent_task_queue WHERE id = $1 FOR UPDATE;

-- name: ResolveSuspendedTaskInteraction :one
UPDATE task_interaction
SET response = sqlc.arg('response'), author_id = sqlc.arg('author_id'), status = 'responded',
    responded_at = now(), followup_task_id = sqlc.narg('followup_task_id')
WHERE task_id = sqlc.arg('task_id') AND id = sqlc.arg('id') AND delivery_mode = 'suspended'
  AND status = 'pending' AND expires_at > now()
RETURNING *;

-- name: ResolveSuspendedAgentTask :one
UPDATE agent_task_queue SET status = sqlc.arg('status')
WHERE id = sqlc.arg('id') AND status IN ('waiting_plan', 'waiting_input')
RETURNING *;

-- name: HasNewerInteractionTask :one
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue newer JOIN agent_task_queue source ON source.id = $1
    WHERE newer.agent_id = source.agent_id
      AND ((source.chat_session_id IS NOT NULL AND newer.chat_session_id = source.chat_session_id)
        OR (source.chat_session_id IS NULL AND source.issue_id IS NOT NULL AND newer.issue_id = source.issue_id))
      AND (newer.created_at, newer.id) > (source.created_at, source.id)
) AS has_newer;

-- name: ExpirePendingTaskInteractionsByTask :exec
UPDATE task_interaction SET status = 'expired'
WHERE task_id = ANY(sqlc.arg('task_ids')::uuid[]) AND status = 'pending';

-- name: ListExpiredSuspendedTaskInteractions :many
SELECT task_id FROM task_interaction
WHERE delivery_mode = 'suspended' AND status = 'pending' AND expires_at <= now()
ORDER BY expires_at, task_id
LIMIT 32;

-- name: MarkTaskInteractionContinuation :one
UPDATE agent_task_queue
SET context = COALESCE(context, '{}'::jsonb) || '{"dsh_interaction_continuation": true}'::jsonb
WHERE id = $1 AND status = 'queued'
RETURNING *;
