CREATE TABLE prompt_evaluation_case_operation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    operation_type TEXT NOT NULL DEFAULT '',
    filter JSONB NOT NULL DEFAULT '{}'::jsonb,
    input JSONB NOT NULL DEFAULT '{}'::jsonb,
    changed_count INT NOT NULL DEFAULT 0,
    skipped_count INT NOT NULL DEFAULT 0,
    sample_case_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prompt_evaluation_case_operation_asset_created
    ON prompt_evaluation_case_operation(asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_operation_workspace_created
    ON prompt_evaluation_case_operation(workspace_id, created_at DESC);
