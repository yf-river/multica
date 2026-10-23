-- name: InsertRunTextChange :one
INSERT INTO run_text_change (
    workspace_id, task_id, target_kind, target_id, target_name, field,
    before_text, after_text, added, deleted, redacted
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;

-- name: ListRunTextChangesByTask :many
SELECT * FROM run_text_change
WHERE task_id = $1
ORDER BY created_at ASC;
