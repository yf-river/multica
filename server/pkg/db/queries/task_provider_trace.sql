-- name: UpsertTaskProviderTrace :exec
-- 幂等写入：同一 (task, provider, trace) 只保留一行，重复上报只刷新可变字段。
-- 不改 token/用量相关字段——关联与计量是两条独立的链路。
INSERT INTO task_provider_trace (
    workspace_id, task_id, provider, observation_space, session_id,
    provider_trace_id, correlation, source, started_at, finished_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (task_id, provider, provider_trace_id)
DO UPDATE SET
    observation_space = CASE WHEN EXCLUDED.observation_space <> '' THEN EXCLUDED.observation_space ELSE task_provider_trace.observation_space END,
    session_id        = CASE WHEN EXCLUDED.session_id <> '' THEN EXCLUDED.session_id ELSE task_provider_trace.session_id END,
    correlation       = EXCLUDED.correlation,
    source            = EXCLUDED.source,
    started_at        = COALESCE(task_provider_trace.started_at, EXCLUDED.started_at),
    finished_at       = COALESCE(EXCLUDED.finished_at, task_provider_trace.finished_at),
    updated_at        = now();

-- name: ListTaskProviderTraces :many
SELECT * FROM task_provider_trace
WHERE task_id = $1
ORDER BY created_at, provider_trace_id;

-- name: ListIssueTaskProviderTraces :many
-- 某个 issue 下所有执行的链路关联，供执行日志按 task_id 分组。
SELECT
    t.task_id,
    t.provider,
    t.observation_space,
    t.session_id,
    t.provider_trace_id,
    t.correlation,
    t.source,
    t.created_at
FROM task_provider_trace t
JOIN agent_task_queue atq ON atq.id = t.task_id
WHERE atq.issue_id = $1
ORDER BY t.task_id, t.created_at;
