-- name: RegisterTaskRuntimeCapability :one
WITH target AS MATERIALIZED (
    SELECT t.id, r.workspace_id, t.runtime_id, t.dispatched_at
    FROM agent_task_queue t
    JOIN agent_runtime r ON r.id = t.runtime_id
    JOIN agent a ON a.id = t.agent_id AND a.workspace_id = r.workspace_id
    WHERE t.id = @task_id AND t.runtime_id = @runtime_id
      AND t.dispatched_at = @dispatched_at AND t.status = 'running'
      AND r.provider IN ('codex', 'codebuddy')
      AND (t.issue_id IS NOT NULL OR t.chat_session_id IS NOT NULL)
      AND COALESCE(a.system_key, '') NOT IN ('quality_supervision', 'issue_review')
    FOR UPDATE OF t
)
INSERT INTO task_runtime_capability (task_id, workspace_id, runtime_id, dispatched_at, capabilities)
SELECT id, workspace_id, runtime_id, dispatched_at, @capabilities::text[] FROM target
ON CONFLICT (task_id) DO UPDATE SET task_id = EXCLUDED.task_id
WHERE task_runtime_capability.capabilities = EXCLUDED.capabilities
  AND task_runtime_capability.runtime_id = EXCLUDED.runtime_id
  AND task_runtime_capability.dispatched_at = EXCLUDED.dispatched_at
RETURNING *;

-- name: GetTaskRuntimeCapability :one
SELECT * FROM task_runtime_capability WHERE task_id = $1;

-- name: CreateTaskRuntimeInput :one
WITH target AS MATERIALIZED (
    SELECT t.id, cap.workspace_id, t.issue_id, t.chat_session_id
    FROM agent_task_queue t
    JOIN task_runtime_capability cap ON cap.task_id = t.id
    WHERE t.id = @task_id AND t.status = 'running'
      AND (t.issue_id IS NOT NULL OR t.chat_session_id IS NOT NULL)
      AND cap.runtime_id = t.runtime_id AND cap.dispatched_at = t.dispatched_at
      AND cap.workspace_id = @workspace_id
      AND @kind::text = ANY(cap.capabilities)
    FOR UPDATE OF t
)
INSERT INTO task_runtime_input (task_id, id, workspace_id, issue_id, chat_session_id, author_id, kind, content)
SELECT id, @id, workspace_id, issue_id, chat_session_id, @author_id, @kind, @content
FROM target
RETURNING *;

-- name: GetTaskRuntimeInput :one
SELECT * FROM task_runtime_input WHERE task_id = $1 AND id = $2;

-- name: ListTaskRuntimeInputs :many
SELECT i.* FROM task_runtime_input i
JOIN agent_task_queue t ON t.id = i.task_id
WHERE i.task_id = @task_id
  AND (i.chat_session_id IS NULL OR i.chat_session_id = t.chat_session_id)
  AND (i.created_at, i.id) < (@before_created_at::timestamptz, @before_id::uuid)
ORDER BY i.created_at DESC, i.id DESC
LIMIT @page_limit;

-- name: ClaimTaskRuntimeInput :one
-- delivering 从不自动回到 pending；断连后重放原生控制可能重复产生副作用。
WITH task AS MATERIALIZED (
    SELECT q.id AS queue_task_id FROM agent_task_queue q
    JOIN task_runtime_capability cap ON cap.task_id = q.id
    WHERE q.id = @task_id AND q.status = 'running'
      AND q.runtime_id = @runtime_id AND q.dispatched_at = @dispatched_at
      AND cap.runtime_id = q.runtime_id AND cap.dispatched_at = q.dispatched_at
    FOR UPDATE OF q
), target AS (
    SELECT i.task_id, i.id AS input_id FROM task_runtime_input i JOIN task t ON t.queue_task_id = i.task_id
    WHERE i.status = 'pending' AND i.kind = ANY(@ready_kinds::text[])
      AND (i.chat_session_id IS NULL OR EXISTS (SELECT 1 FROM agent_task_queue q WHERE q.id = i.task_id AND q.chat_session_id = i.chat_session_id))
    ORDER BY i.created_at, i.id LIMIT 1
    FOR UPDATE OF i
)
UPDATE task_runtime_input i SET status = 'delivering', claimed_at = now()
FROM target t WHERE i.task_id = t.task_id AND i.id = t.input_id
RETURNING i.*;

-- name: AckTaskRuntimeInput :one
-- 终态结算可先记为未知，随后到达的同一次原生确认仍可补全真实回执。
UPDATE task_runtime_input SET status = @status, output = @output, failure_reason = @failure_reason, settled_at = now()
WHERE task_id = @task_id AND id = @id
  AND (status = 'delivering' OR (status = 'failed' AND failure_reason = 'delivery_unknown' AND claimed_at IS NOT NULL))
RETURNING *;

-- name: SettleTerminalTaskRuntimeInputs :execrows
UPDATE task_runtime_input i SET status = 'failed',
    failure_reason = CASE WHEN i.status = 'delivering' THEN 'delivery_unknown' ELSE 'turn_ended' END,
    settled_at = now()
FROM agent_task_queue t
WHERE i.task_id = t.id AND t.id = ANY(@task_ids::uuid[])
  AND t.status IN ('completed', 'failed', 'cancelled')
  AND i.status IN ('pending', 'delivering');
