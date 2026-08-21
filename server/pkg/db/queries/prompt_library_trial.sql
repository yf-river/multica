-- name: CreatePromptLibraryTrial :one
INSERT INTO prompt_library_trial (
    workspace_id,
    prompt_id,
    version_id,
    agent_id,
    chat_session_id,
    task_id,
    input,
    rendered_message,
    variables,
    status,
    created_by
) VALUES (
    $1,
    $2,
    $3,
    $4,
    sqlc.narg('chat_session_id'),
    sqlc.narg('task_id'),
    $5,
    $6,
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('status'), 'queued'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ListPromptLibraryTrials :many
SELECT
    plt.id,
    plt.workspace_id,
    plt.prompt_id,
    plt.version_id,
    plt.agent_id,
    plt.chat_session_id,
    plt.task_id,
    plt.input,
    plt.rendered_message,
    plt.variables,
    COALESCE(atq.status, plt.status) AS status,
    COALESCE(NULLIF(assistant_message.content, ''), plt.output_preview) AS output_preview,
    plt.created_by,
    plt.created_at,
    plt.updated_at
FROM prompt_library_trial plt
LEFT JOIN agent_task_queue atq ON atq.id = plt.task_id
LEFT JOIN LATERAL (
    SELECT cm.content
    FROM chat_message cm
    WHERE cm.chat_session_id = plt.chat_session_id
      AND cm.role = 'assistant'
    ORDER BY cm.created_at DESC, cm.id DESC
    LIMIT 1
) assistant_message ON true
WHERE plt.workspace_id = $1
  AND plt.prompt_id = $2
ORDER BY plt.created_at DESC
LIMIT $3;
