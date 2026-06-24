CREATE TABLE prompt_evaluation_dataset_version (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    dataset_asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    version INT NOT NULL,
    version_label TEXT NOT NULL DEFAULT '',
    row_count INT NOT NULL DEFAULT 0,
    row_fingerprint TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_dataset_version_unique UNIQUE (dataset_asset_id, version)
);

CREATE INDEX idx_prompt_evaluation_dataset_version_asset_created
    ON prompt_evaluation_dataset_version(dataset_asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dataset_version_workspace_created
    ON prompt_evaluation_dataset_version(workspace_id, created_at DESC);

CREATE TABLE prompt_evaluation_dataset_version_row (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    dataset_version_id UUID NOT NULL REFERENCES prompt_evaluation_dataset_version(id) ON DELETE CASCADE,
    dataset_asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    source_row_id UUID REFERENCES prompt_evaluation_dataset_row(id) ON DELETE SET NULL,
    case_id UUID REFERENCES prompt_evaluation_case(id) ON DELETE SET NULL,
    row_index INT NOT NULL DEFAULT 0,
    row_name TEXT NOT NULL DEFAULT '',
    variables JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_contains JSONB NOT NULL DEFAULT '[]'::jsonb,
    expected JSONB NOT NULL DEFAULT '{}'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT NOT NULL DEFAULT 'payload',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_dataset_version_row_unique UNIQUE (dataset_version_id, row_index)
);

CREATE INDEX idx_prompt_evaluation_dataset_version_row_version_index
    ON prompt_evaluation_dataset_version_row(dataset_version_id, row_index ASC);

CREATE INDEX idx_prompt_evaluation_dataset_version_row_asset
    ON prompt_evaluation_dataset_version_row(dataset_asset_id, row_index ASC);
