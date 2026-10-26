-- 独立记录能力协商与人类输入，旧程序回滚时不改变任务热表的扫描结构。
CREATE TABLE task_runtime_capability (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    dispatched_at TIMESTAMPTZ NOT NULL,
    capabilities TEXT[] NOT NULL CHECK (capabilities <@ ARRAY['text', 'command']::TEXT[]),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_runtime_input (
    task_id UUID NOT NULL,
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID,
    chat_session_id UUID,
    author_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('text', 'command')),
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
    output TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ
);
