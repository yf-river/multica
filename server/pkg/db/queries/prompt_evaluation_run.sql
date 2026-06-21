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

-- name: GetPromptEvaluationSummary :one
WITH asset_summary AS (
    SELECT
        COUNT(*)::bigint AS total_assets,
        COUNT(*) FILTER (WHERE status = '启用')::bigint AS active_assets,
        COUNT(*) FILTER (WHERE asset_type = '数据集')::bigint AS dataset_assets,
        COUNT(*) FILTER (WHERE asset_type = '测试套件')::bigint AS test_suite_assets,
        COUNT(*) FILTER (WHERE asset_type = '实验')::bigint AS experiment_assets,
        COUNT(*) FILTER (WHERE asset_type = '优化运行')::bigint AS optimization_assets
    FROM prompt_evaluation_asset pea
    WHERE pea.workspace_id = $1
),
case_summary AS (
    SELECT
        COUNT(*)::bigint AS total_cases,
        COUNT(*) FILTER (WHERE status = '启用')::bigint AS active_cases
    FROM prompt_evaluation_case pec
    WHERE pec.workspace_id = $1
),
run_summary AS (
    SELECT
        COUNT(*)::bigint AS total_runs,
        COUNT(*) FILTER (WHERE run_kind = '本地渲染')::bigint AS local_runs,
        COUNT(*) FILTER (WHERE run_kind = 'Agent执行')::bigint AS agent_runs,
        COUNT(*) FILTER (WHERE status = '已入队')::bigint AS queued_runs,
        COUNT(*) FILTER (WHERE status = '运行中')::bigint AS running_runs,
        COUNT(*) FILTER (WHERE status = '通过')::bigint AS passed_runs,
        COUNT(*) FILTER (WHERE status = '未通过')::bigint AS not_passed_runs,
        COUNT(*) FILTER (WHERE status = '失败')::bigint AS failed_runs,
        COUNT(*) FILTER (WHERE status = '已取消')::bigint AS cancelled_runs,
        COALESCE(SUM(total_cases), 0)::bigint AS evaluated_cases,
        COALESCE(SUM(passed_cases), 0)::bigint AS passed_cases,
        COALESCE(SUM(failed_cases), 0)::bigint AS failed_cases,
        COALESCE(SUM(total_duration_ms), 0)::bigint AS total_duration_ms,
        COALESCE(AVG(NULLIF(average_duration_ms, 0)), 0)::double precision AS average_duration_ms,
        COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
        COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
        COALESCE(SUM(estimated_cost), 0)::double precision AS estimated_cost,
        MAX(created_at)::timestamptz AS last_run_at
    FROM prompt_evaluation_run per
    WHERE per.workspace_id = $1
),
candidate_summary AS (
    SELECT
        COUNT(*)::bigint AS total_candidates,
        COUNT(*) FILTER (WHERE status = '待确认')::bigint AS pending_candidates,
        COUNT(*) FILTER (WHERE status = '已发布')::bigint AS published_candidates,
        COUNT(*) FILTER (WHERE status = '已拒绝')::bigint AS rejected_candidates
    FROM prompt_evaluation_optimization_candidate peoc
    WHERE peoc.workspace_id = $1
)
SELECT
    a.total_assets,
    a.active_assets,
    a.dataset_assets,
    a.test_suite_assets,
    a.experiment_assets,
    a.optimization_assets,
    c.total_cases,
    c.active_cases,
    r.total_runs,
    r.local_runs,
    r.agent_runs,
    r.queued_runs,
    r.running_runs,
    r.passed_runs,
    r.not_passed_runs,
    r.failed_runs,
    r.cancelled_runs,
    r.evaluated_cases,
    r.passed_cases,
    r.failed_cases,
    CASE
        WHEN r.evaluated_cases > 0 THEN r.passed_cases::double precision / r.evaluated_cases::double precision
        ELSE 0::double precision
    END AS pass_rate,
    r.total_duration_ms,
    r.average_duration_ms,
    r.input_tokens,
    r.output_tokens,
    r.estimated_cost,
    oc.total_candidates,
    oc.pending_candidates,
    oc.published_candidates,
    oc.rejected_candidates,
    r.last_run_at
FROM asset_summary a
CROSS JOIN case_summary c
CROSS JOIN run_summary r
CROSS JOIN candidate_summary oc;

-- name: GetPromptEvaluationRunInWorkspace :one
SELECT * FROM prompt_evaluation_run
WHERE id = $1 AND workspace_id = $2;

-- name: GetPromptEvaluationRunByTask :one
SELECT * FROM prompt_evaluation_run
WHERE task_id = $1;

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

-- name: UpdatePromptEvaluationTrialsFromTask :exec
UPDATE prompt_evaluation_trial SET
    status = $3,
    input_tokens = $4,
    output_tokens = $5,
    duration_ms = $6,
    failure_reason = $7,
    evidence = COALESCE(sqlc.narg('evidence')::jsonb, evidence)
WHERE run_id = $1 AND workspace_id = $2;

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
