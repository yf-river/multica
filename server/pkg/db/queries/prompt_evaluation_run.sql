-- name: CreatePromptEvaluationRun :one
INSERT INTO prompt_evaluation_run (
    workspace_id,
    asset_id,
    prompt_id,
    run_kind,
    status,
    trigger_source,
    agent_id,
    runtime_id,
    task_id,
    chat_session_id,
    model,
    runtime_provider,
    total_cases,
    passed_cases,
    failed_cases,
    pass_rate,
    total_duration_ms,
    average_duration_ms,
    input_tokens,
    output_tokens,
    estimated_cost,
    failure_reason,
    conclusion,
    metrics,
    evidence,
    started_at,
    completed_at,
    created_by
) VALUES (
    $1,
    $2,
    sqlc.narg('prompt_id'),
    $3,
    $4,
    COALESCE(sqlc.narg('trigger_source'), '手动'),
    sqlc.narg('agent_id'),
    sqlc.narg('runtime_id'),
    sqlc.narg('task_id'),
    sqlc.narg('chat_session_id'),
    COALESCE(sqlc.narg('model'), ''),
    COALESCE(sqlc.narg('runtime_provider'), ''),
    $5,
    $6,
    $7,
    $8,
    $9,
    $10,
    $11,
    $12,
    $13,
    COALESCE(sqlc.narg('failure_reason'), ''),
    COALESCE(sqlc.narg('conclusion'), ''),
    COALESCE(sqlc.narg('metrics')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('evidence')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('started_at'), now()),
    sqlc.narg('completed_at'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ListPromptEvaluationRuns :many
SELECT * FROM prompt_evaluation_run
WHERE workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC
LIMIT $2;

-- name: GetPromptEvaluationRunInWorkspace :one
SELECT * FROM prompt_evaluation_run
WHERE id = $1 AND workspace_id = $2;

-- name: UpdatePromptEvaluationRunFromTask :one
UPDATE prompt_evaluation_run SET
    status = $3,
    passed_cases = $4,
    failed_cases = $5,
    pass_rate = $6,
    total_duration_ms = $7,
    average_duration_ms = $8,
    input_tokens = $9,
    output_tokens = $10,
    estimated_cost = $11,
    failure_reason = COALESCE(sqlc.narg('failure_reason'), failure_reason),
    conclusion = COALESCE(sqlc.narg('conclusion'), conclusion),
    metrics = COALESCE(sqlc.narg('metrics')::jsonb, metrics),
    evidence = COALESCE(sqlc.narg('evidence')::jsonb, evidence),
    started_at = COALESCE(sqlc.narg('started_at'), started_at),
    completed_at = sqlc.narg('completed_at'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreatePromptEvaluationTrial :one
INSERT INTO prompt_evaluation_trial (
    run_id,
    workspace_id,
    asset_id,
    case_index,
    case_name,
    status,
    input,
    expected,
    output,
    rendered_prompt,
    input_tokens,
    output_tokens,
    duration_ms,
    failure_reason,
    evidence
) VALUES (
    $1,
    $2,
    $3,
    $4,
    COALESCE(sqlc.narg('case_name'), ''),
    $5,
    COALESCE(sqlc.narg('input')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('output')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('rendered_prompt'), ''),
    $6,
    $7,
    $8,
    COALESCE(sqlc.narg('failure_reason'), ''),
    COALESCE(sqlc.narg('evidence')::jsonb, '{}'::jsonb)
)
RETURNING *;

-- name: ListPromptEvaluationTrialsByRun :many
SELECT * FROM prompt_evaluation_trial
WHERE run_id = $1 AND workspace_id = $2
ORDER BY case_index ASC, created_at ASC;
