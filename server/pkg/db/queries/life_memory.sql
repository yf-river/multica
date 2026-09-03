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

-- name: GetLifeMemoryForUpdate :one
SELECT * FROM life_memory
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
FOR UPDATE;

-- name: ListLifeMemories :many
SELECT * FROM life_memory
WHERE workspace_id = $1 AND user_id = $2
ORDER BY updated_at DESC, id DESC;

-- name: ListLifeMemoriesByStatus :many
SELECT * FROM life_memory
WHERE workspace_id = $1 AND user_id = $2 AND status = $3
ORDER BY updated_at DESC, id DESC;

-- name: ListLifeMemoryCandidatesForContext :many
SELECT * FROM life_memory
WHERE workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id)
  AND status = 'candidate'
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg('limit')::int;

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
ON CONFLICT (memory_id, source_type, source_id) DO UPDATE
SET excerpt = EXCLUDED.excerpt,
    observed_at = LEAST(life_memory_evidence.observed_at, EXCLUDED.observed_at),
    stance = EXCLUDED.stance
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
WITH target_rounds AS MATERIALIZED (
    SELECT id FROM life_experiment_round WHERE id = ANY($1::uuid[])
), deleted_experiment_memories AS (
    DELETE FROM life_experiment_memory
    WHERE round_id IN (SELECT id FROM target_rounds)
), deleted_experiment_observations AS (
    DELETE FROM life_experiment_observation
    WHERE round_id IN (SELECT id FROM target_rounds)
), deleted_chronicle_evidence AS (
    DELETE FROM life_chronicle_evidence
    WHERE source_type = 'experiment_round'
      AND source_id IN (SELECT id FROM target_rounds)
), cleared_previous_rounds AS (
    UPDATE life_experiment_round
    SET previous_round_id = NULL, updated_at = now()
    WHERE previous_round_id IN (SELECT id FROM target_rounds)
      AND id NOT IN (SELECT id FROM target_rounds)
), deleted_rounds AS (
    DELETE FROM life_experiment_round
    WHERE id IN (SELECT id FROM target_rounds)
)
SELECT 1;

-- name: DeleteLifeChronicleEntriesBySources :exec
WITH target_entries AS MATERIALIZED (
    SELECT DISTINCT entry.id
    FROM life_chronicle_entry entry
    JOIN life_chronicle_evidence evidence ON evidence.entry_id = entry.id
    WHERE (evidence.source_type = 'memory' AND evidence.source_id = ANY($1::uuid[]))
       OR (evidence.source_type = 'experiment_round' AND evidence.source_id = ANY($2::uuid[]))
), deleted_chronicle_evidence AS (
    DELETE FROM life_chronicle_evidence
    WHERE entry_id IN (SELECT id FROM target_entries)
), deleted_chronicle_revisions AS (
    DELETE FROM life_chronicle_revision
    WHERE entry_id IN (SELECT id FROM target_entries)
), deleted_entries AS (
    DELETE FROM life_chronicle_entry
    WHERE id IN (SELECT id FROM target_entries)
)
SELECT 1;

-- name: DeleteLifeMemoriesByIDs :exec
WITH target_memories AS MATERIALIZED (
    SELECT life_memory.id
    FROM life_memory
    WHERE life_memory.id = ANY($1::uuid[])
      AND life_memory.workspace_id = $2
      AND life_memory.user_id = $3
), deleted_memory_evidence AS (
    DELETE FROM life_memory_evidence
    WHERE memory_id IN (SELECT id FROM target_memories)
), deleted_memory_dependencies AS (
    DELETE FROM life_memory_dependency
    WHERE source_memory_id IN (SELECT id FROM target_memories)
       OR derived_memory_id IN (SELECT id FROM target_memories)
), deleted_memory_revisions AS (
    DELETE FROM life_memory_revision
    WHERE memory_id IN (SELECT id FROM target_memories)
), deleted_experiment_memories AS (
    DELETE FROM life_experiment_memory
    WHERE memory_id IN (SELECT id FROM target_memories)
), deleted_topic_memories AS (
    DELETE FROM life_topic_memory
    WHERE memory_id IN (SELECT id FROM target_memories)
), deleted_chronicle_evidence AS (
    DELETE FROM life_chronicle_evidence
    WHERE source_type = 'memory'
      AND source_id IN (SELECT id FROM target_memories)
), deleted_memories AS (
    DELETE FROM life_memory
    WHERE id IN (SELECT id FROM target_memories)
)
SELECT 1;
