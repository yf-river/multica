-- name: ListPromptEvaluationAssets :many
SELECT * FROM prompt_evaluation_asset
WHERE workspace_id = $1
  AND (sqlc.narg('asset_type')::text IS NULL OR asset_type = sqlc.narg('asset_type'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('prompt_id')::uuid IS NULL OR prompt_id = sqlc.narg('prompt_id'))
ORDER BY updated_at DESC, created_at DESC;

-- name: GetPromptEvaluationAssetInWorkspace :one
SELECT * FROM prompt_evaluation_asset
WHERE id = $1 AND workspace_id = $2;

-- name: CreatePromptEvaluationAsset :one
INSERT INTO prompt_evaluation_asset (
    workspace_id,
    prompt_id,
    name,
    description,
    asset_type,
    payload,
    status,
    created_by,
    structure_schema,
    structured_case_count,
    structured_variable_count,
    structured_assertion_count,
    linked_dataset_count,
    linked_prompt_count,
    evaluation_dimension_count,
    experiment_dimension_count
) VALUES (
    $1,
    sqlc.narg('prompt_id'),
    $2,
    $3,
    $4,
    COALESCE(sqlc.narg('payload')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    $5,
    COALESCE(sqlc.narg('structure_schema'), 'multica.training_evaluation.asset_profile.v1'),
    COALESCE(sqlc.narg('structured_case_count'), 0),
    COALESCE(sqlc.narg('structured_variable_count'), 0),
    COALESCE(sqlc.narg('structured_assertion_count'), 0),
    COALESCE(sqlc.narg('linked_dataset_count'), 0),
    COALESCE(sqlc.narg('linked_prompt_count'), 0),
    COALESCE(sqlc.narg('evaluation_dimension_count'), 0),
    COALESCE(sqlc.narg('experiment_dimension_count'), 0)
)
RETURNING *;

-- name: UpdatePromptEvaluationAsset :one
UPDATE prompt_evaluation_asset SET
    prompt_id = sqlc.narg('prompt_id'),
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    asset_type = COALESCE(sqlc.narg('asset_type'), asset_type),
    payload = COALESCE(sqlc.narg('payload')::jsonb, payload),
    status = COALESCE(sqlc.narg('status'), status),
    structure_schema = COALESCE(sqlc.narg('structure_schema'), structure_schema),
    structured_case_count = COALESCE(sqlc.narg('structured_case_count'), structured_case_count),
    structured_variable_count = COALESCE(sqlc.narg('structured_variable_count'), structured_variable_count),
    structured_assertion_count = COALESCE(sqlc.narg('structured_assertion_count'), structured_assertion_count),
    linked_dataset_count = COALESCE(sqlc.narg('linked_dataset_count'), linked_dataset_count),
    linked_prompt_count = COALESCE(sqlc.narg('linked_prompt_count'), linked_prompt_count),
    evaluation_dimension_count = COALESCE(sqlc.narg('evaluation_dimension_count'), evaluation_dimension_count),
    experiment_dimension_count = COALESCE(sqlc.narg('experiment_dimension_count'), experiment_dimension_count),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeletePromptEvaluationAsset :exec
DELETE FROM prompt_evaluation_asset
WHERE id = $1 AND workspace_id = $2;
