-- name: CreateTaskTraceEvent :one
INSERT INTO task_trace_event (
    workspace_id, task_id, issue_id, agent_id, runtime_id, squad_id, project_id,
    source, event_type, event_name, status, attempt,
    duration_ms, queue_wait_ms, run_ms, total_ms,
    provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    failure_reason, error_type, trigger_comment_id, autopilot_run_id, chat_session_id,
    metadata
) VALUES (
    $1, $2, sqlc.narg('issue_id'), $3, sqlc.narg('runtime_id'), sqlc.narg('squad_id'), sqlc.narg('project_id'),
    $4, $5, $6, $7, $8,
    sqlc.narg('duration_ms'), sqlc.narg('queue_wait_ms'), sqlc.narg('run_ms'), sqlc.narg('total_ms'),
    $9, $10, $11, $12, $13, $14,
    $15, $16, sqlc.narg('trigger_comment_id'), sqlc.narg('autopilot_run_id'), sqlc.narg('chat_session_id'),
    COALESCE(sqlc.narg('metadata')::jsonb, '{}'::jsonb)
)
RETURNING *;

-- name: ListTaskTraceEventsByTask :many
SELECT * FROM task_trace_event
WHERE task_id = $1
ORDER BY created_at ASC, id ASC;

-- name: ListIssueTaskTraceEvents :many
SELECT * FROM task_trace_event
WHERE issue_id = $1
ORDER BY created_at ASC, id ASC;

-- name: ListWorkspaceTaskTraceEvents :many
SELECT * FROM task_trace_event
WHERE workspace_id = $1
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type'))
  AND (sqlc.narg('squad_id')::uuid IS NULL OR squad_id = sqlc.narg('squad_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;
