CREATE TABLE squad_sop_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    squad_id UUID NOT NULL REFERENCES squad(id) ON DELETE CASCADE,
    leader_task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    profile_key TEXT NOT NULL DEFAULT '',
    profile JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT '进行中',
    current_step_key TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    total_duration_ms BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_squad_sop_run_workspace_created
    ON squad_sop_run(workspace_id, created_at DESC);

CREATE INDEX idx_squad_sop_run_issue_created
    ON squad_sop_run(issue_id, created_at DESC);

CREATE INDEX idx_squad_sop_run_squad_created
    ON squad_sop_run(squad_id, created_at DESC);

CREATE UNIQUE INDEX idx_squad_sop_run_issue_open
    ON squad_sop_run(issue_id)
    WHERE status IN ('待开始', '进行中', '已阻塞');

CREATE TABLE squad_sop_step_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES squad_sop_run(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    squad_id UUID NOT NULL REFERENCES squad(id) ON DELETE CASCADE,
    step_key TEXT NOT NULL,
    step_name TEXT NOT NULL DEFAULT '',
    role_key TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT '',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    reason TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT,
    created_by_type TEXT NOT NULL DEFAULT '',
    created_by_id UUID,
    task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_squad_sop_step_event_run_created
    ON squad_sop_step_event(run_id, created_at ASC);

CREATE INDEX idx_squad_sop_step_event_issue_created
    ON squad_sop_step_event(issue_id, created_at DESC);

CREATE INDEX idx_squad_sop_step_event_squad_created
    ON squad_sop_step_event(squad_id, created_at DESC);
