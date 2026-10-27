-- 工作区清理按工作区定位钉钉身份。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dingtalk_bot_identity_workspace
ON dingtalk_bot_identity (workspace_id);
