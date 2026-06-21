CREATE TABLE prompt_evaluation_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    prompt_id UUID REFERENCES prompt_library_item(id) ON DELETE SET NULL,
    run_kind TEXT NOT NULL CHECK (run_kind IN ('本地渲染', 'Agent执行')),
    status TEXT NOT NULL DEFAULT '已入队'
        CHECK (status IN ('已入队', '运行中', '通过', '未通过', '失败', '已取消')),
    trigger_source TEXT NOT NULL DEFAULT '手动',
    agent_id UUID REFERENCES agent(id) ON DELETE SET NULL,
    runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL,
    task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    chat_session_id UUID REFERENCES chat_session(id) ON DELETE SET NULL,
    model TEXT NOT NULL DEFAULT '',
    runtime_provider TEXT NOT NULL DEFAULT '',
    total_cases INT NOT NULL DEFAULT 0,
    passed_cases INT NOT NULL DEFAULT 0,
    failed_cases INT NOT NULL DEFAULT 0,
    pass_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL DEFAULT 0,
    average_duration_ms BIGINT NOT NULL DEFAULT 0,
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    estimated_cost DOUBLE PRECISION NOT NULL DEFAULT 0,
    failure_reason TEXT NOT NULL DEFAULT '',
    conclusion TEXT NOT NULL DEFAULT '',
    metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prompt_evaluation_run_workspace_created
    ON prompt_evaluation_run(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_run_asset_created
    ON prompt_evaluation_run(asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_run_task
    ON prompt_evaluation_run(task_id)
    WHERE task_id IS NOT NULL;

CREATE TABLE prompt_evaluation_trial (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES prompt_evaluation_run(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    case_index INT NOT NULL DEFAULT 0,
    case_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '待执行'
        CHECK (status IN ('待执行', '通过', '未通过', '失败', '已跳过')),
    input JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected JSONB NOT NULL DEFAULT '{}'::jsonb,
    output JSONB NOT NULL DEFAULT '{}'::jsonb,
    rendered_prompt TEXT NOT NULL DEFAULT '',
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    failure_reason TEXT NOT NULL DEFAULT '',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prompt_evaluation_trial_run_case
    ON prompt_evaluation_trial(run_id, case_index ASC);

CREATE INDEX idx_prompt_evaluation_trial_asset_created
    ON prompt_evaluation_trial(asset_id, created_at DESC);
