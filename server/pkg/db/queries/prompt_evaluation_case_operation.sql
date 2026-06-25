-- name: CreatePromptEvaluationCaseOperation :one
INSERT INTO prompt_evaluation_case_operation (
    workspace_id,
    asset_id,
    operation_type,
    filter,
    input,
    changed_count,
    skipped_count,
    sample_case_ids,
    created_by,
    status,
    error_message,
    started_at,
    completed_at
) VALUES (
    $1,
    $2,
    $3,
    COALESCE(sqlc.narg('filter')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('input')::jsonb, '{}'::jsonb),
    $4,
    $5,
    COALESCE(sqlc.narg('sample_case_ids')::jsonb, '[]'::jsonb),
    sqlc.narg('created_by'),
    COALESCE(sqlc.narg('status')::text, '已完成'),
    COALESCE(sqlc.narg('error_message')::text, ''),
    sqlc.narg('started_at'),
    sqlc.narg('completed_at')
)
RETURNING *;

-- name: GetPromptEvaluationCaseOperationInWorkspace :one
SELECT * FROM prompt_evaluation_case_operation
WHERE id = $1
  AND workspace_id = $2;

-- name: MarkPromptEvaluationCaseOperationRunning :one
UPDATE prompt_evaluation_case_operation
SET status = '运行中',
    error_message = '',
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
RETURNING *;

-- name: CompletePromptEvaluationCaseOperation :one
UPDATE prompt_evaluation_case_operation
SET status = '已完成',
    changed_count = $3,
    skipped_count = $4,
    sample_case_ids = COALESCE(sqlc.narg('sample_case_ids')::jsonb, '[]'::jsonb),
    error_message = '',
    completed_at = now(),
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
RETURNING *;

-- name: FailPromptEvaluationCaseOperation :one
UPDATE prompt_evaluation_case_operation
SET status = '失败',
    error_message = $3,
    completed_at = now(),
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
RETURNING *;

-- name: ListPromptEvaluationCaseOperations :many
SELECT * FROM prompt_evaluation_case_operation
WHERE workspace_id = $1
  AND asset_id = $2
ORDER BY created_at DESC
LIMIT COALESCE(sqlc.narg('limit')::int, 20);
