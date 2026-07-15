-- name: GetIssue :one
SELECT * FROM issue
WHERE id = $1;

-- name: LockIssueForTaskTerminalProjection :one
-- Terminal task projection locks in task -> issue -> SOP run order. Every
-- automatic completion/failure path uses this row lock before changing the
-- run or enqueuing a leader continuation, so concurrent worker terminals for
-- one issue cannot create divergent run/Issue state.
SELECT * FROM issue
WHERE id = $1
FOR UPDATE;

-- name: GetIssueInWorkspace :one
SELECT * FROM issue
WHERE id = $1 AND workspace_id = $2;

-- name: CreateIssue :one
INSERT INTO issue (
    workspace_id, title, description, status, priority,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, position, start_date, due_date, number, project_id,
    scope, owner_id, work_started_at, work_completed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    COALESCE(sqlc.narg('scope'), 'workspace'), sqlc.narg('owner_id'),
    CASE WHEN $4 IN ('in_progress', 'done') THEN now() ELSE NULL END,
    CASE WHEN $4 = 'done' THEN now() ELSE NULL END
) RETURNING *;

-- name: GetIssueByNumber :one
SELECT * FROM issue
WHERE workspace_id = $1 AND number = $2;

-- name: UpdateIssue :one
UPDATE issue SET
    title = COALESCE(sqlc.narg('title'), title),
    description = COALESCE(sqlc.narg('description'), description),
    work_started_at = CASE
        WHEN sqlc.narg('status')::text = 'in_progress' AND status IN ('done', 'cancelled') THEN now()
        WHEN sqlc.narg('status')::text NOT IN ('done', 'cancelled', 'in_progress') AND status IN ('done', 'cancelled') THEN NULL
        WHEN sqlc.narg('status')::text = 'in_progress' AND status <> 'in_progress' AND work_started_at IS NULL THEN now()
        ELSE work_started_at
    END,
    work_completed_at = CASE
        WHEN sqlc.narg('status')::text = 'in_progress' AND status IN ('done', 'cancelled') THEN NULL
        WHEN sqlc.narg('status')::text NOT IN ('done', 'cancelled') AND status IN ('done', 'cancelled') THEN NULL
        WHEN sqlc.narg('status')::text = 'done' AND status <> 'done' THEN now()
        ELSE work_completed_at
    END,
    status = COALESCE(sqlc.narg('status'), status),
    priority = COALESCE(sqlc.narg('priority'), priority),
    assignee_type = sqlc.narg('assignee_type'),
    assignee_id = sqlc.narg('assignee_id'),
    position = COALESCE(sqlc.narg('position'), position),
    start_date = sqlc.narg('start_date'),
    due_date = sqlc.narg('due_date'),
    parent_issue_id = sqlc.narg('parent_issue_id'),
    project_id = sqlc.narg('project_id'),
    scope = COALESCE(sqlc.narg('scope'), scope),
    owner_id = COALESCE(sqlc.narg('owner_id'), owner_id),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateIssueStatus :one
-- Workspace_id in the WHERE clause is a SQL-layer tenant guard; see DeleteIssue.
UPDATE issue SET
    work_started_at = CASE
        WHEN $2 = 'in_progress' AND status IN ('done', 'cancelled') THEN now()
        WHEN $2 NOT IN ('done', 'cancelled', 'in_progress') AND status IN ('done', 'cancelled') THEN NULL
        WHEN $2 = 'in_progress' AND status <> 'in_progress' AND work_started_at IS NULL THEN now()
        ELSE work_started_at
    END,
    work_completed_at = CASE
        WHEN $2 = 'in_progress' AND status IN ('done', 'cancelled') THEN NULL
        WHEN $2 NOT IN ('done', 'cancelled') AND status IN ('done', 'cancelled') THEN NULL
        WHEN $2 = 'done' AND status <> 'done' THEN now()
        ELSE work_completed_at
    END,
    status = $2,
    updated_at = now()
WHERE id = $1 AND workspace_id = $3
RETURNING *;

-- name: CreateIssueWithOrigin :one
INSERT INTO issue (
    workspace_id, title, description, status, priority,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, position, start_date, due_date, number, project_id,
    origin_type, origin_id, scope, owner_id, work_started_at, work_completed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    sqlc.narg('origin_type'), sqlc.narg('origin_id'),
    COALESCE(sqlc.narg('scope'), 'workspace'), sqlc.narg('owner_id'),
    CASE WHEN $4 IN ('in_progress', 'done') THEN now() ELSE NULL END,
    CASE WHEN $4 = 'done' THEN now() ELSE NULL END
) RETURNING *;

-- name: DeleteIssue :exec
-- Defense-in-depth: the workspace_id predicate makes the tenant invariant a
-- SQL-layer guarantee rather than a handler-layer one. Handler loaders
-- (loadIssueForUser / GetIssueInWorkspace) already enforce membership today,
-- but a future loader bypass or a new caller skipping the loader would be
-- silently catastrophic without this guard. See incident #1661.
DELETE FROM issue WHERE id = $1 AND workspace_id = $2;

-- name: ListChildIssues :many
SELECT * FROM issue
WHERE parent_issue_id = $1
ORDER BY position ASC, created_at DESC;

-- name: ListChildrenByParents :many
-- Batched variant of ListChildIssues: returns all children for the given
-- parent set in one round trip. Used by Swimlane to avoid an N+1 fan-out
-- (one request per visible parent lane). Result is grouped client-side by
-- parent_issue_id; the workspace filter is also enforced so callers can't
-- enumerate children of parents in workspaces they don't belong to.
SELECT * FROM issue
WHERE workspace_id = sqlc.arg('workspace_id')
  AND parent_issue_id = ANY(sqlc.arg('parent_ids')::uuid[])
ORDER BY parent_issue_id, position ASC, created_at DESC;

-- name: GetIssueByOrigin :one
-- Finds the issue stamped with a specific (origin_type, origin_id) pair.
-- Used by quick-create completion to deterministically locate the issue
-- produced by a given agent_task_queue.id — robust against concurrent
-- issue creates by the same agent (assignment task + quick-create both
-- running with max_concurrent_tasks > 1).
SELECT * FROM issue
WHERE workspace_id = $1
  AND origin_type = $2
  AND origin_id = $3
LIMIT 1;

-- name: CountCreatedIssueAssignees :many
-- Count assignees on issues created by a specific user.
SELECT
  assignee_type,
  assignee_id,
  COUNT(*)::bigint as frequency
FROM issue
WHERE workspace_id = $1
  AND creator_id = $2
  AND creator_type = 'member'
  AND assignee_type IS NOT NULL
  AND assignee_id IS NOT NULL
GROUP BY assignee_type, assignee_id;

-- name: ChildIssueProgress :many
SELECT parent_issue_id,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE status IN ('done', 'cancelled'))::bigint AS done
FROM issue
WHERE workspace_id = $1
  AND parent_issue_id IS NOT NULL
GROUP BY parent_issue_id;

-- SearchIssues: moved to handler (dynamic SQL for multi-word search support).

-- name: SetIssueMetadataKey :one
-- Atomically sets a single key in the issue's metadata JSONB. The
-- workspace_id filter is the authorization gate — handler resolves the
-- issue first so this is also the tenant check.
UPDATE issue SET
    metadata = jsonb_set(metadata, ARRAY[sqlc.arg('key')::text], sqlc.arg('value')::jsonb),
    updated_at = now()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: DeleteIssueMetadataKey :one
-- Atomically removes a single key from the issue's metadata JSONB.
-- Deleting a missing key is a no-op (still returns the row).
UPDATE issue SET
    metadata = metadata - sqlc.arg('key')::text,
    updated_at = now()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: MarkIssueFirstExecuted :one
-- Flips first_executed_at from NULL to now() atomically. Returns the row if
-- this was the first time the issue was executed; no rows otherwise. The
-- analytics issue_executed event fires exactly when this returns a row —
-- retries and re-assignments hit the WHERE clause and no-op.
UPDATE issue
SET first_executed_at = now()
WHERE id = $1 AND first_executed_at IS NULL
RETURNING id, workspace_id, creator_type, creator_id, first_executed_at;
