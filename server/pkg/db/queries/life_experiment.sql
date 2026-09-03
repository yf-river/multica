-- name: CreateLifeActionProposal :one
INSERT INTO life_action_proposal (
    workspace_id, user_id, companion_agent_id, proposal_type, status,
    title, summary, payload, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.narg(expires_at))
RETURNING *;

-- name: GetLifeActionProposal :one
SELECT * FROM life_action_proposal
WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: GetLifeActionProposalForUpdate :one
SELECT * FROM life_action_proposal
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
FOR UPDATE;

-- name: ListLifeActionProposals :many
SELECT * FROM life_action_proposal
WHERE workspace_id = $1 AND user_id = $2 AND status <> 'internal_draft'
ORDER BY updated_at DESC, id DESC;

-- name: PresentLifeActionProposal :one
UPDATE life_action_proposal
SET status = 'pending_confirmation', updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'internal_draft'
RETURNING *;

-- name: RejectLifeActionProposal :one
UPDATE life_action_proposal
SET status = 'rejected', updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
  AND status IN ('internal_draft', 'pending_confirmation')
RETURNING *;

-- name: ExpireLifeActionProposals :execrows
UPDATE life_action_proposal SET status = 'expired', updated_at = now()
WHERE status IN ('internal_draft', 'pending_confirmation') AND expires_at IS NOT NULL AND expires_at <= now();

-- name: MarkLifeActionProposalExecuted :one
UPDATE life_action_proposal
SET status = 'executed', confirmed_at = now(), executed_at = now(),
    execution_receipt = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'pending_confirmation'
RETURNING *;

-- name: CreateLifeExperiment :one
INSERT INTO life_experiment (
    workspace_id, user_id, title, problem, hypothesis, method,
    created_by_type, created_by_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetLifeExperiment :one
SELECT * FROM life_experiment
WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: ListLifeExperiments :many
SELECT * FROM life_experiment
WHERE workspace_id = $1 AND user_id = $2
ORDER BY updated_at DESC, id DESC;

-- name: ListLifeExperimentsForContext :many
SELECT * FROM life_experiment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id)
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg('limit')::int;

-- name: CreateLifeExperimentRound :one
INSERT INTO life_experiment_round (
    experiment_id, previous_round_id, proposal_id, issue_id, status, plan,
    starts_at, ends_at, confirmed_at, confirmed_by_id
)
VALUES (
    $1, sqlc.narg(previous_round_id), $2, $3, $4, $5,
    $6, $7, $8, $9
)
RETURNING *;

-- name: GetLifeExperimentRoundForUser :one
SELECT sqlc.embed(round), sqlc.embed(experiment)
FROM life_experiment_round round
JOIN life_experiment experiment ON experiment.id = round.experiment_id
WHERE round.id = $1 AND experiment.workspace_id = $2 AND experiment.user_id = $3;

-- name: ListLifeExperimentRounds :many
SELECT round.* FROM life_experiment_round round
JOIN life_experiment experiment ON experiment.id = round.experiment_id
WHERE experiment.workspace_id = $1 AND experiment.user_id = $2
ORDER BY round.created_at DESC, round.id DESC;

-- name: ListLifeExperimentRoundsForContext :many
SELECT round.* FROM life_experiment_round round
JOIN life_experiment experiment ON experiment.id = round.experiment_id
WHERE experiment.workspace_id = sqlc.arg(workspace_id)
  AND experiment.user_id = sqlc.arg(user_id)
  AND round.status IN ('running', 'awaiting_review')
ORDER BY round.created_at DESC, round.id DESC
LIMIT sqlc.arg('limit')::int;

-- name: LinkLifeExperimentMemory :exec
INSERT INTO life_experiment_memory (round_id, memory_id, role)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: StopExpiredLifeExperimentRounds :many
UPDATE life_experiment_round round
SET status = 'awaiting_review',
    stopped_at = COALESCE(round.stopped_at, round.ends_at, now()),
    stop_reason = CASE WHEN btrim(round.stop_reason) = '' THEN 'expired' ELSE round.stop_reason END,
    updated_at = now()
FROM life_experiment experiment
WHERE round.experiment_id = experiment.id
  AND experiment.workspace_id = $1
  AND experiment.user_id = $2
  AND round.status = 'running'
  AND round.ends_at <= now()
RETURNING round.*;

-- name: StopAllExpiredLifeExperimentRounds :many
UPDATE life_experiment_round
SET status = 'awaiting_review',
    stopped_at = COALESCE(stopped_at, ends_at, now()),
    stop_reason = CASE WHEN btrim(stop_reason) = '' THEN 'expired' ELSE stop_reason END,
    updated_at = now()
WHERE status = 'running' AND ends_at <= now()
RETURNING *;

-- name: StopLifeExperimentRound :one
UPDATE life_experiment_round round
SET status = 'awaiting_review', stopped_at = now(), stop_reason = $4, updated_at = now()
FROM life_experiment experiment
WHERE round.id = $1
  AND round.experiment_id = experiment.id
  AND experiment.workspace_id = $2
  AND experiment.user_id = $3
  AND round.status = 'running'
RETURNING round.*;

-- name: ReviewLifeExperimentRound :one
UPDATE life_experiment_round round
SET status = 'reviewed', review = $4, reviewed_at = now(), updated_at = now()
FROM life_experiment experiment
WHERE round.id = $1
  AND round.experiment_id = experiment.id
  AND experiment.workspace_id = $2
  AND experiment.user_id = $3
  AND round.status = 'awaiting_review'
RETURNING round.*;
