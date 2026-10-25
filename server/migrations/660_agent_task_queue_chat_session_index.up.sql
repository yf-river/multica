-- 聊天清理与取消指针检查按会话定位执行。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_chat_session
ON agent_task_queue (chat_session_id, created_at DESC)
WHERE chat_session_id IS NOT NULL;
