-- 工作区级代码审查配置：挂在「审核中」状态类别上，一个工作区一份。
-- 审查员由系统托管（agent.system_key='issue_review'），运行时、模型与提示词都在这里维护。
CREATE TABLE review_config (
    workspace_id uuid PRIMARY KEY,
    agent_id uuid,
    owner_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    instructions text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 一个工作区只能有一个托管审查 Agent，避免并发保存时重复创建。
CREATE UNIQUE INDEX agent_issue_review_unique ON agent (workspace_id) WHERE system_key = 'issue_review';
