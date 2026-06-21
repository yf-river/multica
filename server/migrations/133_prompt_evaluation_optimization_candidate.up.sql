CREATE TABLE prompt_evaluation_optimization_candidate (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    run_id UUID NOT NULL REFERENCES prompt_evaluation_run(id) ON DELETE CASCADE,
    prompt_id UUID NOT NULL REFERENCES prompt_library_item(id) ON DELETE CASCADE,
    candidate_name TEXT NOT NULL,
    candidate_content TEXT NOT NULL,
    rationale TEXT NOT NULL DEFAULT '',
    failed_case_count INT NOT NULL DEFAULT 0,
    source_failure_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_prompt_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT '待确认',
    published_prompt_id UUID REFERENCES prompt_library_item(id) ON DELETE SET NULL,
    published_at TIMESTAMPTZ,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_optimization_candidate_status_check
        CHECK (status IN ('待确认', '已发布', '已拒绝'))
);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_workspace_created
    ON prompt_evaluation_optimization_candidate(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_run
    ON prompt_evaluation_optimization_candidate(run_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_prompt
    ON prompt_evaluation_optimization_candidate(prompt_id, created_at DESC);
