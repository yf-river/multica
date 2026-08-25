-- name: ListPromptEvaluationCaseAssertions :many
SELECT a.*
FROM prompt_evaluation_case_assertion a
JOIN prompt_evaluation_case c ON c.id = a.case_id
WHERE c.workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR c.asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status'))
ORDER BY c.asset_id, a.case_id, a.assertion_index ASC, a.created_at ASC;

-- name: CreatePromptEvaluationCaseAssertion :one
INSERT INTO prompt_evaluation_case_assertion (
    case_id,
    assertion_index
) VALUES (
    $1,
    $2
)
RETURNING *;

-- name: DeletePromptEvaluationCaseAssertionsByCase :exec
DELETE FROM prompt_evaluation_case_assertion
WHERE case_id = $1;
