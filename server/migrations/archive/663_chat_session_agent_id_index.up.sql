-- 运行时清理按 Agent 定位聊天。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_session_agent_id
    ON chat_session (agent_id);
