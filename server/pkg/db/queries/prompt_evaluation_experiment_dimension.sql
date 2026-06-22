-- name: ListPromptEvaluationExperimentDimensions :many
SELECT * FROM prompt_evaluation_experiment_dimension
WHERE workspace_id = $1
  AND (sqlc.narg('experiment_asset_id')::uuid IS NULL OR experiment_asset_id = sqlc.narg('experiment_asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY experiment_asset_id, dimension_index ASC, created_at ASC;

-- name: CreatePromptEvaluationExperimentDimension :one
INSERT INTO prompt_evaluation_experiment_dimension (
    workspace_id,
    experiment_asset_id,
    dimension_index,
    dimension_name,
    experiment_target,
    baseline_output,
    comparison_payload,
    status,
    source,
    created_by
) VALUES (
    $1,
    $2,
    $3,
    COALESCE(sqlc.narg('dimension_name'), ''),
    COALESCE(sqlc.narg('experiment_target'), ''),
    COALESCE(sqlc.narg('baseline_output'), ''),
    COALESCE(sqlc.narg('comparison_payload')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'payload'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: DeletePromptEvaluationExperimentDimensionsByAsset :exec
DELETE FROM prompt_evaluation_experiment_dimension
WHERE workspace_id = $1 AND experiment_asset_id = $2;

-- name: RefreshPromptEvaluationExperimentDimensionCount :exec
UPDATE prompt_evaluation_asset a SET
    experiment_dimension_count = (
        SELECT count(*)::int
        FROM prompt_evaluation_experiment_dimension d
        WHERE d.workspace_id = $1 AND d.experiment_asset_id = $2
    ),
    updated_at = now()
WHERE a.workspace_id = $1 AND a.id = $2;
