-- 每个 issue 的当前审查轮次。round 在每次进入「审核中」时递增，用于「累计轮次」与
-- 「同一问题原地打转」两类停止判断；findings 保留上一轮问题清单，供下一轮核对进展。
CREATE TABLE issue_review (
    issue_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    developer_agent_id uuid,
    round integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'idle'
        CHECK (state IN ('idle', 'dispatched', 'rejected', 'passed', 'stopped')),
    head_sha text NOT NULL DEFAULT '',
    findings jsonb NOT NULL DEFAULT '{}'::jsonb,
    summary text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX issue_review_workspace_state ON issue_review (workspace_id, state);
