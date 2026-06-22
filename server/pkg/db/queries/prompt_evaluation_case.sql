-- name: ListPromptEvaluationCases :many
SELECT * FROM prompt_evaluation_case
WHERE workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY asset_id, case_index ASC, created_at ASC;

-- name: GetPromptEvaluationCaseInWorkspace :one
SELECT * FROM prompt_evaluation_case
WHERE id = $1 AND workspace_id = $2;

-- name: DeletePromptEvaluationCasesByAsset :exec
DELETE FROM prompt_evaluation_case
WHERE workspace_id = $1 AND asset_id = $2;

-- name: DeletePromptEvaluationPayloadCasesByAsset :exec
DELETE FROM prompt_evaluation_case
WHERE workspace_id = $1 AND asset_id = $2 AND source = 'payload';

-- name: CreatePromptEvaluationCase :one
INSERT INTO prompt_evaluation_case (
    workspace_id,
    asset_id,
    prompt_id,
    case_index,
    case_name,
    variables,
    expected_contains,
    input,
    expected,
    tags,
    status,
    source,
    created_by
) VALUES (
    $1,
    $2,
    sqlc.narg('prompt_id'),
    $3,
    COALESCE(sqlc.narg('case_name'), ''),
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected_contains')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('input')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'payload'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: UpdatePromptEvaluationCase :one
UPDATE prompt_evaluation_case SET
    asset_id = $3,
    prompt_id = sqlc.narg('prompt_id'),
    case_index = $4,
    case_name = $5,
    variables = COALESCE(sqlc.narg('variables')::jsonb, variables),
    expected_contains = COALESCE(sqlc.narg('expected_contains')::jsonb, expected_contains),
    input = COALESCE(sqlc.narg('input')::jsonb, input),
    expected = COALESCE(sqlc.narg('expected')::jsonb, expected),
    tags = COALESCE(sqlc.narg('tags')::jsonb, tags),
    status = $6,
    source = $7,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeletePromptEvaluationCase :exec
DELETE FROM prompt_evaluation_case
WHERE id = $1 AND workspace_id = $2;
