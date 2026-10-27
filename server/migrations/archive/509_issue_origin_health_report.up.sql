-- 体检报告通过现有任务体系执行；扩展来源白名单，表扫描由下一条迁移完成。
ALTER TABLE issue
    DROP CONSTRAINT IF EXISTS issue_origin_type_check,
    ADD CONSTRAINT issue_origin_type_check
        CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'health_report'))
        NOT VALID;
