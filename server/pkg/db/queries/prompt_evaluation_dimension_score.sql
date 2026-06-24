-- name: UpsertPromptEvaluationDimensionScore :one
INSERT INTO prompt_evaluation_dimension_score (
    workspace_id,
    run_id,
    asset_id,
    prompt_id,
    dimension_index,
    dimension_name,
    score,
    passed_cases,
    total_cases,
    status,
    rule,
    evidence,
    source
) VALUES (
    $1,
    $2,
    $3,
    sqlc.narg('prompt_id'),
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    COALESCE(sqlc.narg('rule'), ''),
    COALESCE(sqlc.narg('evidence'), ''),
    $10
)
ON CONFLICT (run_id, dimension_index, dimension_name) DO UPDATE SET
    score = EXCLUDED.score,
    passed_cases = EXCLUDED.passed_cases,
    total_cases = EXCLUDED.total_cases,
    status = EXCLUDED.status,
    rule = EXCLUDED.rule,
    evidence = EXCLUDED.evidence,
    source = EXCLUDED.source,
    prompt_id = EXCLUDED.prompt_id,
    updated_at = now()
RETURNING *;

-- name: DeletePromptEvaluationDimensionScoresByRun :exec
DELETE FROM prompt_evaluation_dimension_score
WHERE workspace_id = $1 AND run_id = $2;

-- name: ListPromptEvaluationDimensionScores :many
SELECT * FROM prompt_evaluation_dimension_score
WHERE workspace_id = $1
  AND (sqlc.narg('run_id')::uuid IS NULL OR run_id = sqlc.narg('run_id'))
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('prompt_id')::uuid IS NULL OR prompt_id = sqlc.narg('prompt_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, dimension_index ASC;
