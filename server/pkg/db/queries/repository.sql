-- name: ListRepositories :many
SELECT * FROM repository WHERE workspace_id=$1 ORDER BY created_at, id;

-- name: GetRepository :one
SELECT * FROM repository WHERE workspace_id=$1 AND id=$2;

-- name: UpsertRepository :one
INSERT INTO repository(workspace_id,identity,config,default_for_tasks)
VALUES ($1,$2,$3,$4)
ON CONFLICT (workspace_id,identity) DO UPDATE SET identity=EXCLUDED.identity
RETURNING *;

-- name: UpdateRepository :one
UPDATE repository SET config=$3, default_for_tasks=$4, revision=revision+1, updated_at=now()
WHERE workspace_id=$1 AND id=$2 RETURNING *;

-- name: DeleteRepository :exec
DELETE FROM repository WHERE workspace_id=$1 AND id=$2;

-- name: ListRepositoryLocations :many
SELECT * FROM repository_location WHERE workspace_id=$1 ORDER BY created_at,id;

-- name: UpsertRepositoryLocation :one
INSERT INTO repository_location(workspace_id,repository_id,daemon_id,local_path,execution_mode)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (workspace_id,repository_id,daemon_id) DO UPDATE
SET local_path=EXCLUDED.local_path, execution_mode=EXCLUDED.execution_mode
RETURNING *;

-- name: DeleteRepositoryLocations :exec
DELETE FROM repository_location WHERE workspace_id=$1 AND repository_id=$2;

-- name: DeleteRepositoryLocation :exec
DELETE FROM repository_location WHERE workspace_id=$1 AND repository_id=$2 AND id=$3;
