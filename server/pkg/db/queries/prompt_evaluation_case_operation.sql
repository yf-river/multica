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
    created_by
) VALUES (
    $1,
    $2,
    $3,
    COALESCE(sqlc.narg('filter')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('input')::jsonb, '{}'::jsonb),
    $4,
    $5,
    COALESCE(sqlc.narg('sample_case_ids')::jsonb, '[]'::jsonb),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ListPromptEvaluationCaseOperations :many
SELECT * FROM prompt_evaluation_case_operation
WHERE workspace_id = $1
  AND asset_id = $2
ORDER BY created_at DESC
LIMIT COALESCE(sqlc.narg('limit')::int, 20);
