-- 运行时清理按运行时定位聊天恢复指针。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_session_runtime_id
    ON chat_session (runtime_id)
    WHERE runtime_id IS NOT NULL;
