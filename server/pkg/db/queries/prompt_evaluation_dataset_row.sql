-- name: ListPromptEvaluationDatasetRows :many
SELECT * FROM prompt_evaluation_dataset_row
WHERE workspace_id = $1
  AND (sqlc.narg('dataset_asset_id')::uuid IS NULL OR dataset_asset_id = sqlc.narg('dataset_asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY dataset_asset_id, row_index ASC, created_at ASC;

-- name: CreatePromptEvaluationDatasetRow :one
INSERT INTO prompt_evaluation_dataset_row (
    workspace_id,
    dataset_asset_id,
    case_id,
    row_index,
    row_name,
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
    COALESCE(sqlc.narg('row_name'), ''),
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected_contains')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('expected')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'payload'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: DeletePromptEvaluationDatasetRowsByCase :many
DELETE FROM prompt_evaluation_dataset_row
WHERE workspace_id = $1 AND case_id = $2
RETURNING dataset_asset_id;

-- name: RefreshPromptEvaluationDatasetRowCount :exec
UPDATE prompt_evaluation_asset a SET
    dataset_row_count = (
        SELECT count(*)::int
        FROM prompt_evaluation_dataset_row r
        WHERE r.workspace_id = $1 AND r.dataset_asset_id = $2
    ),
    updated_at = now()
WHERE a.workspace_id = $1 AND a.id = $2;
