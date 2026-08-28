-- name: CreateLifeMemory :one
INSERT INTO life_memory (
    workspace_id,
    user_id,
    created_by_type,
    created_by_id,
    kind,
    content,
    confidence,
    urgency,
    uncertainty,
    valid_from,
    valid_to
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, sqlc.narg(valid_from), sqlc.narg(valid_to))
RETURNING *;

-- name: GetLifeMemory :one
SELECT * FROM life_memory
WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: ListLifeMemories :many
SELECT * FROM life_memory
WHERE workspace_id = $1 AND user_id = $2
ORDER BY updated_at DESC, id DESC;

-- name: ListLifeMemoriesByStatus :many
SELECT * FROM life_memory
WHERE workspace_id = $1 AND user_id = $2 AND status = $3
ORDER BY updated_at DESC, id DESC;

-- name: ListConfirmedLifeMemoriesForContext :many
SELECT * FROM life_memory
WHERE workspace_id = $1 AND user_id = $2 AND status = 'confirmed'
  AND (valid_to IS NULL OR valid_to >= now())
ORDER BY updated_at DESC, id DESC
LIMIT $3;

-- name: UpdateLifeMemoryContent :one
UPDATE life_memory
SET kind = $4,
    content = $5,
    confidence = $6,
    urgency = $7,
    uncertainty = $8,
    valid_from = sqlc.narg(valid_from),
    valid_to = sqlc.narg(valid_to),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: ConfirmLifeMemory :one
UPDATE life_memory
SET status = 'confirmed',
    confirmed_at = now(),
    confirmed_by_id = $4,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: DowngradeLifeMemory :one
UPDATE life_memory
SET status = 'candidate',
    kind = $4,
    confirmed_at = NULL,
    confirmed_by_id = NULL,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: ArchiveLifeMemory :one
UPDATE life_memory
SET status = 'archived',
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: CreateLifeMemoryEvidence :one
INSERT INTO life_memory_evidence (memory_id, source_type, source_id, excerpt, observed_at, stance)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListLifeMemoryEvidence :many
SELECT * FROM life_memory_evidence
WHERE memory_id = $1
ORDER BY observed_at ASC, source_type, source_id;

-- name: CreateLifeMemoryDependency :exec
INSERT INTO life_memory_dependency (source_memory_id, derived_memory_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListDerivedLifeMemoryIDs :many
WITH RECURSIVE doomed(id) AS (
    SELECT id
    FROM life_memory
    WHERE id = $1 AND workspace_id = $2 AND user_id = $3
    UNION
    SELECT dependency.derived_memory_id
    FROM life_memory_dependency dependency
    JOIN doomed ON doomed.id = dependency.source_memory_id
)
SELECT id FROM doomed;

-- name: ListLifeExperimentRoundIDsByMemoryIDs :many
SELECT DISTINCT round_id
FROM life_experiment_memory
WHERE memory_id = ANY($1::uuid[]);

-- name: DeleteLifeActionProposalsByRoundIDs :exec
DELETE FROM life_action_proposal proposal
WHERE EXISTS (
    SELECT 1
    FROM life_experiment_round round
    WHERE round.id = ANY($1::uuid[])
      AND round.proposal_id = proposal.id
);

-- name: DeleteLifeExperimentRoundsByIDs :exec
DELETE FROM life_experiment_round
WHERE id = ANY($1::uuid[]);

-- name: DeleteLifeChronicleEntriesBySources :exec
DELETE FROM life_chronicle_entry entry
WHERE EXISTS (
    SELECT 1
    FROM life_chronicle_evidence evidence
    WHERE evidence.entry_id = entry.id
      AND (
        (evidence.source_type = 'memory' AND evidence.source_id = ANY($1::uuid[]))
        OR (evidence.source_type = 'experiment_round' AND evidence.source_id = ANY($2::uuid[]))
      )
);

-- name: DeleteLifeMemoriesByIDs :exec
DELETE FROM life_memory
WHERE id = ANY($1::uuid[]) AND workspace_id = $2 AND user_id = $3;
