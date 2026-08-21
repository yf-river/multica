-- name: ListPromptEvaluationOptimizationCandidates :many
SELECT * FROM prompt_evaluation_optimization_candidate
WHERE workspace_id = $1
  AND (sqlc.narg('run_id')::uuid IS NULL OR run_id = sqlc.narg('run_id'))
  AND (sqlc.narg('prompt_id')::uuid IS NULL OR prompt_id = sqlc.narg('prompt_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC
LIMIT $2;

-- name: GetPromptEvaluationOptimizationCandidateInWorkspace :one
SELECT * FROM prompt_evaluation_optimization_candidate
WHERE id = $1 AND workspace_id = $2;

-- name: CreatePromptEvaluationOptimizationCandidate :one
INSERT INTO prompt_evaluation_optimization_candidate (
    workspace_id,
    asset_id,
    run_id,
    prompt_id,
    candidate_name,
    candidate_content,
    rationale,
    failed_case_count,
    source_failure_summary,
    source_prompt_snapshot,
    metrics,
    status,
    created_by
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    COALESCE(sqlc.narg('rationale'), ''),
    $7,
    COALESCE(sqlc.narg('source_failure_summary')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('source_prompt_snapshot')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('metrics')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('status'), '待确认'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: PublishPromptEvaluationOptimizationCandidate :one
UPDATE prompt_evaluation_optimization_candidate SET
    status = '已发布',
    published_prompt_id = $3,
    published_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = '待确认'
RETURNING *;

-- name: UpdatePromptEvaluationOptimizationCandidateDraft :one
UPDATE prompt_evaluation_optimization_candidate SET
    candidate_name = $3,
    candidate_content = $4,
    rationale = COALESCE(sqlc.narg('rationale'), ''),
    metrics = COALESCE(metrics, '{}'::jsonb) || jsonb_build_object(
      '人工编辑',
      jsonb_build_object(
        '编辑人', COALESCE(sqlc.narg('edited_by')::uuid::text, ''),
        '编辑时间', now(),
        '编辑说明', COALESCE(sqlc.narg('edit_note'), '')
      )
    ),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = '待确认'
RETURNING *;

-- name: RejectPromptEvaluationOptimizationCandidate :one
UPDATE prompt_evaluation_optimization_candidate SET
    status = '已拒绝',
    metrics = COALESCE(metrics, '{}'::jsonb) || jsonb_build_object(
      '人工处理',
      jsonb_build_object(
        '处理结果', '已拒绝',
        '拒绝原因', COALESCE(sqlc.narg('reason'), ''),
        '处理人', COALESCE(sqlc.narg('handled_by')::uuid::text, ''),
        '处理时间', now()
      )
    ),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = '待确认'
RETURNING *;
