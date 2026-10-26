ALTER TABLE agent_task_queue DROP CONSTRAINT agent_task_queue_status_check,
    ADD CONSTRAINT agent_task_queue_status_check CHECK (status IN ('queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled', 'waiting_local_directory', 'deferred'));
