-- name: CreateAgentPlaygroundExperiment :one
INSERT INTO agent_playground_experiment (
    workspace_id,
    name,
    description,
    dataset_asset_id,
    dataset_version_id,
    judge_agent_id,
    status,
    created_by
) VALUES (
    $1,
    $2,
    COALESCE(sqlc.narg('description'), ''),
    sqlc.narg('dataset_asset_id'),
    sqlc.narg('dataset_version_id'),
    sqlc.narg('judge_agent_id'),
    COALESCE(sqlc.narg('status'), 'ready'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ListAgentPlaygroundExperiments :many
SELECT e.*,
       COALESCE((SELECT COUNT(*) FROM agent_playground_input i WHERE i.experiment_id = e.id), 0)::int AS input_count,
       COALESCE((SELECT COUNT(*) FROM agent_playground_agent a WHERE a.experiment_id = e.id), 0)::int AS agent_count
FROM agent_playground_experiment e
WHERE e.workspace_id = $1
ORDER BY e.updated_at DESC, e.created_at DESC
LIMIT $2;

-- name: GetAgentPlaygroundExperiment :one
SELECT * FROM agent_playground_experiment
WHERE id = $1 AND workspace_id = $2;

-- name: UpdateAgentPlaygroundExperimentStatus :one
UPDATE agent_playground_experiment
SET status = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetAgentPlaygroundJudgeAgent :one
UPDATE agent_playground_experiment
SET judge_agent_id = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreateAgentPlaygroundInput :one
INSERT INTO agent_playground_input (
    experiment_id,
    workspace_id,
    dataset_row_id,
    row_index,
    name,
    input,
    variables,
    expected
) VALUES (
    $1,
    $2,
    sqlc.narg('dataset_row_id'),
    $3,
    COALESCE(sqlc.narg('name'), ''),
    $4,
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected'), '')
)
RETURNING *;

-- name: ListAgentPlaygroundInputs :many
SELECT * FROM agent_playground_input
WHERE experiment_id = $1 AND workspace_id = $2
ORDER BY row_index ASC, created_at ASC;

-- name: CreateAgentPlaygroundAgent :one
INSERT INTO agent_playground_agent (
    experiment_id,
    workspace_id,
    agent_id,
    display_order
) VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListAgentPlaygroundAgents :many
SELECT apa.*,
       a.name AS agent_name,
       a.model AS agent_model
FROM agent_playground_agent apa
JOIN agent a ON a.id = apa.agent_id
WHERE apa.experiment_id = $1 AND apa.workspace_id = $2
ORDER BY apa.display_order ASC, apa.created_at ASC;

-- name: CreateAgentPlaygroundResult :one
INSERT INTO agent_playground_result (
    experiment_id,
    input_id,
    experiment_agent_id,
    workspace_id,
    agent_id,
    rendered_input,
    status
) VALUES ($1, $2, $3, $4, $5, $6, 'pending')
ON CONFLICT (input_id, experiment_agent_id) DO UPDATE
SET rendered_input = EXCLUDED.rendered_input,
    status = CASE
        WHEN agent_playground_result.status = 'pending' THEN EXCLUDED.status
        ELSE agent_playground_result.status
    END,
    updated_at = now()
RETURNING *;

-- name: ListAgentPlaygroundResults :many
SELECT * FROM agent_playground_result
WHERE experiment_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: StartAgentPlaygroundResult :one
UPDATE agent_playground_result
SET chat_session_id = $3,
    task_id = $4,
    status = $5,
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SyncAgentPlaygroundResult :one
UPDATE agent_playground_result
SET status = $3,
    output = COALESCE(sqlc.narg('output'), output),
    error = COALESCE(sqlc.narg('error'), error),
    completed_at = COALESCE(sqlc.narg('completed_at'), completed_at),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreateAgentPlaygroundJudgement :one
INSERT INTO agent_playground_judgement (
    experiment_id,
    input_id,
    workspace_id,
    judge_agent_id,
    status
) VALUES ($1, $2, $3, $4, 'pending')
ON CONFLICT (input_id) DO UPDATE
SET judge_agent_id = EXCLUDED.judge_agent_id,
    status = CASE
        WHEN agent_playground_judgement.status = 'pending' THEN EXCLUDED.status
        ELSE agent_playground_judgement.status
    END,
    updated_at = now()
RETURNING *;

-- name: ListAgentPlaygroundJudgements :many
SELECT * FROM agent_playground_judgement
WHERE experiment_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: StartAgentPlaygroundJudgement :one
UPDATE agent_playground_judgement
SET chat_session_id = $3,
    task_id = $4,
    status = $5,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SyncAgentPlaygroundJudgement :one
UPDATE agent_playground_judgement
SET status = $3,
    output = COALESCE(sqlc.narg('output'), output),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;
