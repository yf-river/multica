CREATE TABLE task_trace_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    issue_id UUID REFERENCES issue(id) ON DELETE SET NULL,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL,
    squad_id UUID REFERENCES squad(id) ON DELETE SET NULL,
    project_id UUID REFERENCES project(id) ON DELETE SET NULL,
    source TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    event_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 1,
    duration_ms BIGINT,
    queue_wait_ms BIGINT,
    run_ms BIGINT,
    total_ms BIGINT,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    failure_reason TEXT NOT NULL DEFAULT '',
    error_type TEXT NOT NULL DEFAULT '',
    trigger_comment_id UUID REFERENCES comment(id) ON DELETE SET NULL,
    autopilot_run_id UUID REFERENCES autopilot_run(id) ON DELETE SET NULL,
    chat_session_id UUID REFERENCES chat_session(id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_task_trace_event_workspace_created
    ON task_trace_event(workspace_id, created_at DESC);

CREATE INDEX idx_task_trace_event_task_created
    ON task_trace_event(task_id, created_at ASC);

CREATE INDEX idx_task_trace_event_issue_created
    ON task_trace_event(issue_id, created_at DESC)
    WHERE issue_id IS NOT NULL;

CREATE INDEX idx_task_trace_event_squad_created
    ON task_trace_event(squad_id, created_at DESC)
    WHERE squad_id IS NOT NULL;

CREATE INDEX idx_task_trace_event_agent_created
    ON task_trace_event(agent_id, created_at DESC);
