-- 有体检任务时拒绝收窄约束，避免既有任务成为不可更新的数据。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM issue WHERE origin_type = 'health_report') THEN
        RAISE EXCEPTION '存在体检报告任务，不能回滚体检来源约束';
    END IF;
    ALTER TABLE issue
        DROP CONSTRAINT IF EXISTS issue_origin_type_check,
        ADD CONSTRAINT issue_origin_type_check
            CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat'))
            NOT VALID;
END $$;
