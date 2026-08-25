-- name: CreateFeedback :one
INSERT INTO feedback (user_id, workspace_id, message, metadata, idempotency_key, request_hash)
VALUES ($1, sqlc.narg(workspace_id), $2, $3, $4, $5)
RETURNING *;

-- name: GetFeedbackByCreateRequest :one
SELECT * FROM feedback
WHERE user_id = $1 AND idempotency_key = $2;

-- name: CountRecentFeedbackByUser :one
SELECT count(*) FROM feedback
WHERE user_id = $1 AND created_at > now() - interval '1 hour';
