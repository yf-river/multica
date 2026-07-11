-- The current daemon has no producer for waiting_local_directory: issue tasks
-- use managed worktrees and the old path-lock helper was never called. Requeue
-- any historical parked rows before removing the unreachable state.
UPDATE agent_task_queue
SET status = 'queued',
    dispatched_at = NULL,
    started_at = NULL
WHERE status = 'waiting_local_directory';

UPDATE agent_playground_result
SET status = 'queued'
WHERE status = 'waiting_local_directory';

UPDATE agent_playground_judgement
SET status = 'queued'
WHERE status = 'waiting_local_directory';

ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_queue_status_check,
    DROP COLUMN IF EXISTS wait_reason,
    ADD CONSTRAINT agent_task_queue_status_check
        CHECK (status IN ('queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled'));

ALTER TABLE agent_playground_result
    DROP CONSTRAINT IF EXISTS agent_playground_result_status_check,
    ADD CONSTRAINT agent_playground_result_status_check
        CHECK (status IN ('pending', 'queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled'));

ALTER TABLE agent_playground_judgement
    DROP CONSTRAINT IF EXISTS agent_playground_judgement_status_check,
    ADD CONSTRAINT agent_playground_judgement_status_check
        CHECK (status IN ('pending', 'queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled'));
