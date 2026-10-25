-- 委派失败恢复按证据定位尝试记录。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_delegated_failure_evidence
    ON agent_task_queue (trigger_evidence_ref_id, created_at DESC, id DESC)
    WHERE trigger_evidence_kind IN ('delegated_failure', 'delegated_result');
