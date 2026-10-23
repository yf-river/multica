-- name: UpsertTaskProviderTrace :exec
-- 幂等写入：同一 (task, provider, trace) 只保留一行。
--
-- 关联强度只允许升级、不允许降级：exact（子进程自报）不能被后来的 inferred_window
-- （事后按会话+时间窗检索的候选）覆盖，`correlation`、`source` 与真实 `session_id`
-- 都保持原值。只有 exact 能把 inferred_window 提升为精确关联。
--
-- 同一强度下 session 不一致时不静默改写：保留先写入的值，并把两个值记进
-- metadata.session_conflict 供审计（谁、何时、原值/拟值都可回溯），而不是让后到的
-- 覆盖先到的。
INSERT INTO task_provider_trace (
    workspace_id, task_id, provider, observation_space, session_id,
    provider_trace_id, correlation, source, started_at, finished_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (task_id, provider, provider_trace_id)
DO UPDATE SET
    observation_space = CASE
        WHEN EXCLUDED.observation_space = '' THEN task_provider_trace.observation_space
        WHEN task_provider_trace.observation_space = '' THEN EXCLUDED.observation_space
        ELSE task_provider_trace.observation_space END,
    session_id = CASE
        WHEN task_provider_trace.correlation = 'inferred_window' AND EXCLUDED.correlation = 'exact'
            THEN EXCLUDED.session_id
        WHEN task_provider_trace.correlation = 'exact' AND EXCLUDED.correlation <> 'exact'
            THEN task_provider_trace.session_id
        WHEN EXCLUDED.session_id = '' THEN task_provider_trace.session_id
        WHEN task_provider_trace.session_id = '' THEN EXCLUDED.session_id
        ELSE task_provider_trace.session_id END,
    correlation = CASE
        WHEN EXCLUDED.correlation = 'exact' THEN 'exact'
        ELSE task_provider_trace.correlation END,
    source = CASE
        WHEN EXCLUDED.correlation = 'exact' THEN EXCLUDED.source
        ELSE task_provider_trace.source END,
    metadata = CASE
        WHEN EXCLUDED.session_id <> ''
         AND task_provider_trace.session_id <> ''
         AND EXCLUDED.session_id <> task_provider_trace.session_id
            THEN task_provider_trace.metadata || jsonb_build_object(
                 'session_conflict', jsonb_build_array(task_provider_trace.session_id, EXCLUDED.session_id),
                 'session_conflict_at', now())
        ELSE task_provider_trace.metadata END,
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
