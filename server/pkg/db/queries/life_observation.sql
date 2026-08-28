-- name: CreateLifeProactiveCheck :one
INSERT INTO life_proactive_check (
    workspace_id, user_id, companion_agent_id, status, trigger_source,
    reason, context_snapshot
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListLifeProactiveChecks :many
SELECT * FROM life_proactive_check
WHERE workspace_id = $1 AND user_id = $2
ORDER BY checked_at DESC, id DESC
LIMIT $3;

-- name: CreateLifeChronicleEntry :one
INSERT INTO life_chronicle_entry (
    workspace_id, user_id, period_start, period_end, facts, feelings,
    understanding_then, understanding_later, actions
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListLifeChronicleEntries :many
SELECT * FROM life_chronicle_entry
WHERE workspace_id = $1 AND user_id = $2
ORDER BY period_start DESC, id DESC;

-- name: GetLifeChronicleEntry :one
SELECT * FROM life_chronicle_entry
WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: UpdateLifeChronicleLaterUnderstanding :one
UPDATE life_chronicle_entry
SET understanding_later = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: CreateLifeChronicleEvidence :exec
INSERT INTO life_chronicle_evidence (entry_id, source_type, source_id)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: ListLifeChronicleEvidence :many
SELECT * FROM life_chronicle_evidence
WHERE entry_id = $1
ORDER BY source_type, source_id;
