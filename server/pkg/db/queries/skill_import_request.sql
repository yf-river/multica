-- name: ReserveSkillImportRequest :one
INSERT INTO skill_import_request (workspace_id, actor_id, idempotency_key, request_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetSkillImportRequest :one
SELECT * FROM skill_import_request
WHERE workspace_id = $1 AND actor_id = $2 AND idempotency_key = $3;

-- name: CompleteSkillImportRequest :one
UPDATE skill_import_request
SET response_status = $5,
    response_body = sqlc.arg(response_body)::jsonb,
    completed_at = now()
WHERE workspace_id = $1 AND actor_id = $2 AND idempotency_key = $3
  AND request_hash = $4
  AND completed_at IS NULL
RETURNING *;

-- name: LockSkillImportName :exec
-- Different request identities can still target the same unique skill name.
-- Serialize that decision so the losing request observes the committed skill
-- and returns the current conflict contract instead of an aborted transaction.
SELECT pg_advisory_xact_lock(
    hashtextextended(sqlc.arg(workspace_id)::uuid::text || ':' || sqlc.arg(name)::text, 0)
);
