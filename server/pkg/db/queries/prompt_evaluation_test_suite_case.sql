-- name: ListPromptEvaluationTestSuiteCases :many
SELECT * FROM prompt_evaluation_test_suite_case
WHERE workspace_id = $1
  AND (sqlc.narg('test_suite_asset_id')::uuid IS NULL OR test_suite_asset_id = sqlc.narg('test_suite_asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY test_suite_asset_id, case_index ASC, created_at ASC;

-- name: CreatePromptEvaluationTestSuiteCase :one
INSERT INTO prompt_evaluation_test_suite_case (
    workspace_id,
    test_suite_asset_id,
    case_id,
    case_index,
    case_name,
    variables,
    expected_contains,
    expected,
    tags,
    status,
    source,
    created_by
) VALUES (
    $1,
    $2,
    $3,
    $4,
    COALESCE(sqlc.narg('case_name'), ''),
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected_contains')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('expected')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'payload'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: DeletePromptEvaluationTestSuiteCasesByCase :many
DELETE FROM prompt_evaluation_test_suite_case
WHERE workspace_id = $1 AND case_id = $2
RETURNING test_suite_asset_id;

-- name: RefreshPromptEvaluationTestSuiteCaseCount :exec
UPDATE prompt_evaluation_asset a SET
    test_suite_case_count = (
        SELECT count(*)::int
        FROM prompt_evaluation_test_suite_case c
        WHERE c.workspace_id = $1 AND c.test_suite_asset_id = $2
    ),
    updated_at = now()
WHERE a.workspace_id = $1 AND a.id = $2;
