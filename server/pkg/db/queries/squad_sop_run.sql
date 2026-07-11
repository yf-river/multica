-- name: CreateSquadSOPRun :one
INSERT INTO squad_sop_run (
    workspace_id, issue_id, squad_id, leader_task_id,
    profile_key, profile, status, current_step_key
) VALUES (
    $1, $2, $3, sqlc.narg('leader_task_id'),
    $4, COALESCE(sqlc.narg('profile')::jsonb, '{}'::jsonb), $5, $6
)
ON CONFLICT (issue_id) WHERE status IN ('待开始', '进行中', '已阻塞')
DO UPDATE SET
    leader_task_id = COALESCE(EXCLUDED.leader_task_id, squad_sop_run.leader_task_id),
    profile_key = EXCLUDED.profile_key,
    profile = EXCLUDED.profile,
    current_step_key = CASE
        WHEN squad_sop_run.current_step_key = '' THEN EXCLUDED.current_step_key
        ELSE squad_sop_run.current_step_key
    END,
    updated_at = now()
RETURNING *;

-- name: AttachLeaderTaskToSquadSOPRun :one
UPDATE squad_sop_run
SET leader_task_id = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetOpenSquadSOPRunByIssue :one
SELECT * FROM squad_sop_run
WHERE issue_id = $1 AND status IN ('待开始', '进行中', '已阻塞')
ORDER BY created_at DESC
LIMIT 1;

-- name: LockOpenSquadSOPRunByIssue :one
SELECT * FROM squad_sop_run
WHERE issue_id = $1 AND status IN ('待开始', '进行中', '已阻塞')
ORDER BY created_at DESC
LIMIT 1
FOR UPDATE;

-- name: LockSquadSOPRunForAutomaticTaskEvent :one
-- Replays of an already-terminal task must repair the exact run that owns the
-- durable automatic event, not whichever newer run happens to be open now.
SELECT r.*
FROM squad_sop_run r
JOIN squad_sop_step_event e ON e.run_id = r.id
WHERE e.task_id = $1
  AND e.event_type = $2
  AND e.created_by_type = 'system'
ORDER BY e.created_at DESC, e.id DESC
LIMIT 1
FOR UPDATE OF r;

-- name: GetSquadSOPRunInWorkspace :one
SELECT * FROM squad_sop_run
WHERE id = $1 AND workspace_id = $2;

-- name: LockSquadSOPRunInWorkspace :one
SELECT * FROM squad_sop_run
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: ListIssueSquadSOPRuns :many
SELECT * FROM squad_sop_run
WHERE issue_id = $1 AND workspace_id = $2
ORDER BY created_at DESC, id DESC;

-- name: ListWorkspaceSquadSOPRuns :many
SELECT r.*
FROM squad_sop_run r
JOIN issue i ON i.id = r.issue_id
LEFT JOIN agent_task_queue atq ON atq.id = r.leader_task_id
WHERE r.workspace_id = $1
  AND (sqlc.narg('since')::timestamptz IS NULL OR r.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('squad_id')::uuid IS NULL OR r.squad_id = sqlc.narg('squad_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('agent_id')::uuid IS NULL OR atq.agent_id = sqlc.narg('agent_id'))
ORDER BY r.created_at DESC, r.id DESC
LIMIT $2 OFFSET $3;

-- name: UpdateSquadSOPRunStatus :one
UPDATE squad_sop_run
SET status = $3,
    current_step_key = COALESCE(sqlc.narg('current_step_key'), current_step_key),
    completed_at = CASE WHEN $3 IN ('已完成', '已失败') THEN COALESCE(completed_at, now()) ELSE completed_at END,
    total_duration_ms = CASE
        WHEN $3 IN ('已完成', '已失败') THEN COALESCE(total_duration_ms, (EXTRACT(EPOCH FROM (now() - started_at)) * 1000)::bigint)
        ELSE total_duration_ms
    END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreateSquadSOPStepEvent :one
INSERT INTO squad_sop_step_event (
    run_id, workspace_id, issue_id, squad_id,
    step_key, step_name, role_key, event_type, status,
    evidence, reason, duration_ms, created_by_type, created_by_id, task_id
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    COALESCE(sqlc.narg('evidence')::jsonb, '{}'::jsonb),
    $10, sqlc.narg('duration_ms'), $11, sqlc.narg('created_by_id'), sqlc.narg('task_id')
)
RETURNING *;

-- name: UpsertAutomaticSquadSOPTerminalEvent :one
-- Migration 006 guarantees one system-owned terminal event per
-- (run, task, event type). The no-op conflict update returns the durable row
-- so replay can continue repairing run/Issue projection instead of treating
-- an existing event as proof that every downstream write succeeded.
INSERT INTO squad_sop_step_event (
    run_id, workspace_id, issue_id, squad_id,
    step_key, step_name, role_key, event_type, status,
    evidence, reason, duration_ms, created_by_type, task_id
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    '{}'::jsonb, $10, sqlc.narg('duration_ms'), 'system', $11
)
ON CONFLICT (run_id, task_id, event_type)
WHERE task_id IS NOT NULL
  AND created_by_type = 'system'
  AND event_type IN ('步骤完成', '步骤失败')
DO UPDATE SET status = squad_sop_step_event.status
RETURNING *;

-- name: UpdateAutomaticSquadSOPTerminalEventEvidence :one
UPDATE squad_sop_step_event
SET evidence = sqlc.arg('evidence')::jsonb
WHERE id = $1
  AND task_id IS NOT NULL
  AND created_by_type = 'system'
  AND event_type IN ('步骤完成', '步骤失败')
RETURNING *;

-- name: ListSquadSOPStepEventsByRun :many
SELECT * FROM squad_sop_step_event
WHERE run_id = $1
ORDER BY created_at ASC, id ASC;

-- name: ListIssueSquadSOPStepEvents :many
SELECT * FROM squad_sop_step_event
WHERE issue_id = $1 AND workspace_id = $2
ORDER BY created_at ASC, id ASC;

-- name: ListWorkspaceSquadSOPStepEvents :many
SELECT e.*
FROM squad_sop_step_event e
JOIN issue i ON i.id = e.issue_id
LEFT JOIN agent_task_queue atq ON atq.id = e.task_id
WHERE e.workspace_id = $1
  AND (sqlc.narg('since')::timestamptz IS NULL OR e.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('squad_id')::uuid IS NULL OR e.squad_id = sqlc.narg('squad_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('agent_id')::uuid IS NULL OR atq.agent_id = sqlc.narg('agent_id'))
ORDER BY e.created_at DESC, e.id DESC
LIMIT $2 OFFSET $3;

-- name: CountWorkspaceSquadSOPStepEvents :one
SELECT count(*)
FROM squad_sop_step_event e
JOIN issue i ON i.id = e.issue_id
LEFT JOIN agent_task_queue atq ON atq.id = e.task_id
WHERE e.workspace_id = $1
  AND (sqlc.narg('since')::timestamptz IS NULL OR e.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('squad_id')::uuid IS NULL OR e.squad_id = sqlc.narg('squad_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('agent_id')::uuid IS NULL OR atq.agent_id = sqlc.narg('agent_id'));
