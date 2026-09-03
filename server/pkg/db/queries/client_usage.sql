-- name: UpsertClientUsageDaily :one
INSERT INTO client_usage_daily (
    user_id,
    client_type,
    install_id,
    activity_date,
    workspace_id,
    client_version,
    os,
    first_active_at,
    last_active_at
) VALUES (
    sqlc.arg('user_id'),
    sqlc.arg('client_type'),
    sqlc.arg('install_id'),
    (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date,
    sqlc.narg('workspace_id'),
    sqlc.arg('client_version'),
    sqlc.arg('os'),
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)
ON CONFLICT (user_id, client_type, install_id, activity_date) DO UPDATE SET
    workspace_id = COALESCE(EXCLUDED.workspace_id, client_usage_daily.workspace_id),
    client_version = EXCLUDED.client_version,
    os = EXCLUDED.os,
    last_active_at = EXCLUDED.last_active_at,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;
