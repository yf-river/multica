-- name: ListPromptEvaluationCaseAssertions :many
SELECT * FROM prompt_evaluation_case_assertion
WHERE workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('case_id')::uuid IS NULL OR case_id = sqlc.narg('case_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY asset_id, case_id, assertion_index ASC, created_at ASC;

-- name: CreatePromptEvaluationCaseAssertion :one
INSERT INTO prompt_evaluation_case_assertion (
    workspace_id,
    asset_id,
    case_id,
    assertion_index,
    assertion_type,
    expected_text,
    status,
    source
) VALUES (
    $1,
    $2,
    $3,
    $4,
    COALESCE(sqlc.narg('assertion_type'), '包含文本'),
    $5,
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'expected_contains')
)
RETURNING *;

-- name: DeletePromptEvaluationCaseAssertionsByCase :exec
DELETE FROM prompt_evaluation_case_assertion
WHERE workspace_id = $1 AND case_id = $2;
