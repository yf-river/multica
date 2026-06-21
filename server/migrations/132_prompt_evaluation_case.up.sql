CREATE TABLE prompt_evaluation_case (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    prompt_id UUID REFERENCES prompt_library_item(id) ON DELETE SET NULL,
    case_index INT NOT NULL DEFAULT 0,
    case_name TEXT NOT NULL DEFAULT '',
    variables JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_contains JSONB NOT NULL DEFAULT '[]'::jsonb,
    input JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected JSONB NOT NULL DEFAULT '{}'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '归档')),
    source TEXT NOT NULL DEFAULT 'payload',
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_case_asset_index_unique UNIQUE (asset_id, case_index)
);

CREATE INDEX idx_prompt_evaluation_case_workspace_created
    ON prompt_evaluation_case(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_asset_index
    ON prompt_evaluation_case(asset_id, case_index ASC);
