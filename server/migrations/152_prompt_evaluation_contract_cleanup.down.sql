ALTER TABLE prompt_evaluation_asset
    DROP CONSTRAINT IF EXISTS prompt_evaluation_asset_type_check,
    ADD CONSTRAINT prompt_evaluation_asset_type_check
        CHECK (asset_type IN ('数据集', '测试套件', '实验', '优化运行'));

CREATE TABLE IF NOT EXISTS prompt_evaluation_experiment_dimension (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    experiment_asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    dimension_index INT NOT NULL DEFAULT 0,
    dimension_name TEXT NOT NULL DEFAULT '',
    experiment_target TEXT NOT NULL DEFAULT '',
    baseline_output TEXT NOT NULL DEFAULT '',
    comparison_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '归档')),
    source TEXT NOT NULL DEFAULT 'payload' CHECK (source IN ('payload', 'manual')),
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_experiment_dimension_asset_index_unique UNIQUE (experiment_asset_id, dimension_index)
);

CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_experiment_dimension_workspace_created
    ON prompt_evaluation_experiment_dimension(workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_experiment_dimension_asset_index
    ON prompt_evaluation_experiment_dimension(experiment_asset_id, dimension_index ASC);
