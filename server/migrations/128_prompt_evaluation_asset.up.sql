CREATE TABLE prompt_evaluation_asset (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    prompt_id UUID REFERENCES prompt_library_item(id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    asset_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT '启用',
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_asset_type_check CHECK (asset_type IN ('数据集', '测试套件', '实验')),
    CONSTRAINT prompt_evaluation_asset_status_check CHECK (status IN ('启用', '归档')),
    CONSTRAINT prompt_evaluation_asset_name_unique UNIQUE (workspace_id, asset_type, name)
);

CREATE INDEX prompt_evaluation_asset_workspace_type_idx ON prompt_evaluation_asset(workspace_id, asset_type);
CREATE INDEX prompt_evaluation_asset_prompt_idx ON prompt_evaluation_asset(prompt_id);
