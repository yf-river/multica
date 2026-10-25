-- Keep negotiated capability outside agent_task_queue. Older application
-- builds use SELECT * for that hot table, so adding columns would make a
-- server rollback fail while scanning otherwise-valid task rows.
CREATE TABLE task_supplement_capability (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    capability TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Durable delivery receipts for explicit, human-authored additions to one
-- exact run. Relationships intentionally have no database foreign keys: task,
-- issue, comment and workspace teardown remain application-owned and never
-- acquire a new cascade/lock tree.
CREATE TABLE task_supplement (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    comment_id UUID NOT NULL,
    author_id UUID NOT NULL,
    client_request_id UUID NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
    failure_reason TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);

