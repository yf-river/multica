-- name: NextPromptEvaluationDatasetVersion :one
SELECT COALESCE(MAX(version), 0)::int + 1
FROM prompt_evaluation_dataset_version
WHERE workspace_id = $1
  AND dataset_asset_id = $2;

-- name: CreatePromptEvaluationDatasetVersion :one
INSERT INTO prompt_evaluation_dataset_version (
    workspace_id,
    dataset_asset_id,
    version,
    version_label,
    row_count,
    row_fingerprint,
    metadata,
    created_by
) VALUES (
    $1,
    $2,
    $3,
    COALESCE(sqlc.narg('version_label'), ''),
    $4,
    $5,
    COALESCE(sqlc.narg('metadata')::jsonb, '{}'::jsonb),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ListPromptEvaluationDatasetVersions :many
SELECT * FROM prompt_evaluation_dataset_version
WHERE workspace_id = $1
  AND dataset_asset_id = $2
ORDER BY version DESC, created_at DESC
LIMIT $3;

-- name: GetLatestPromptEvaluationDatasetVersion :one
SELECT * FROM prompt_evaluation_dataset_version
WHERE workspace_id = $1
  AND dataset_asset_id = $2
ORDER BY version DESC, created_at DESC
LIMIT 1;

-- name: GetPromptEvaluationDatasetVersionInAsset :one
SELECT * FROM prompt_evaluation_dataset_version
WHERE workspace_id = $1
  AND dataset_asset_id = $2
  AND id = $3;

-- name: CreatePromptEvaluationDatasetVersionRowsFromCurrent :exec
INSERT INTO prompt_evaluation_dataset_version_row (
    workspace_id,
    dataset_version_id,
    dataset_asset_id,
    source_row_id,
    case_id,
    row_index,
    row_name,
    variables,
    expected_contains,
    expected,
    tags,
    source
)
SELECT
    r.workspace_id,
    $3,
    r.dataset_asset_id,
    r.id,
    r.case_id,
    r.row_index,
    r.row_name,
    r.variables,
    r.expected_contains,
    r.expected,
    r.tags,
    r.source
FROM prompt_evaluation_dataset_row r
WHERE r.workspace_id = $1
  AND r.dataset_asset_id = $2
  AND r.status = '启用'
ORDER BY r.row_index ASC, r.created_at ASC;

-- name: ListPromptEvaluationDatasetVersionRows :many
SELECT * FROM prompt_evaluation_dataset_version_row
WHERE workspace_id = $1
  AND dataset_version_id = $2
ORDER BY row_index ASC, created_at ASC;
