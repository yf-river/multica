-- 测试环境的期望配置与发布器回报分离；生产业务 Agent 不读取此表。
CREATE TABLE test_agent_control (
 workspace_id uuid NOT NULL,
 name text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 desired jsonb NOT NULL DEFAULT '{"policy":{"provider":"codebuddy","model":"deepseek-v4.1-flash","thinking_level":"","service_tier":""},"overrides":{}}',
 observed jsonb NOT NULL DEFAULT '{}',
 observed_at timestamptz,
 updated_by text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
