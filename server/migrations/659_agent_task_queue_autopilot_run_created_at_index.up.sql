-- 自动任务派发恢复按运行定位最早执行。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_autopilot_run_created_at
    ON agent_task_queue (autopilot_run_id, created_at)
    WHERE autopilot_run_id IS NOT NULL;
