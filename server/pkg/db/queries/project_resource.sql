-- name: ListProjectResources :many
SELECT * FROM project_resource
WHERE project_id = $1
ORDER BY position ASC, created_at ASC;

-- name: ListGongfengProjectResourcesInWorkspace :many
SELECT * FROM project_resource
WHERE workspace_id = $1
  AND resource_type = 'gongfeng_repo'
ORDER BY project_id, position ASC, created_at ASC;

-- name: GetProjectResourceInWorkspace :one
SELECT * FROM project_resource
WHERE id = $1 AND workspace_id = $2;

-- name: CreateProjectResource :one
INSERT INTO project_resource (
    project_id, workspace_id, resource_type, resource_ref, label, position, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: LockProjectForResourcePosition :one
SELECT id FROM project WHERE id = $1 FOR UPDATE;

-- name: NextProjectResourcePosition :one
SELECT (COALESCE(MAX(position), -1) + 1)::int
FROM project_resource
WHERE project_id = $1;

-- name: UpdateProjectResource :one
UPDATE project_resource
SET resource_ref = $2,
    label        = $3,
    position     = $4
WHERE id = $1
RETURNING *;

-- name: DeleteProjectResource :exec
DELETE FROM project_resource WHERE id = $1;

-- name: GetProjectResourceCounts :many
SELECT project_id, count(*)::bigint AS resource_count
FROM project_resource
WHERE project_id = ANY(sqlc.arg('project_ids')::uuid[])
GROUP BY project_id;
