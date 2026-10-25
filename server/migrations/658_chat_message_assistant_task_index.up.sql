-- 助手回复按执行定位最新消息。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_message_assistant_task
    ON chat_message (task_id, created_at DESC)
    WHERE role = 'assistant';
