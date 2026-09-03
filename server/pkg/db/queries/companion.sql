-- name: UpsertCompanionProfile :one
INSERT INTO companion_profile (workspace_id, user_id, agent_id)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, user_id) DO UPDATE
SET agent_id = EXCLUDED.agent_id,
    updated_at = now()
RETURNING *;

-- name: GetCompanionProfile :one
SELECT * FROM companion_profile
WHERE workspace_id = $1 AND user_id = $2;

-- name: LockCompanionProfile :one
SELECT * FROM companion_profile
WHERE workspace_id = $1 AND user_id = $2
FOR UPDATE;

-- name: GetCompanionProfileForAgent :one
SELECT * FROM companion_profile
WHERE workspace_id = $1 AND user_id = $2 AND agent_id = $3;
