ALTER TABLE prompt_evaluation_asset
    ADD COLUMN dataset_row_count INT NOT NULL DEFAULT 0;

CREATE TABLE prompt_evaluation_dataset_row (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    dataset_asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    case_id UUID NOT NULL REFERENCES prompt_evaluation_case(id) ON DELETE CASCADE,
    row_index INT NOT NULL DEFAULT 0,
    row_name TEXT NOT NULL DEFAULT '',
    variables JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_contains JSONB NOT NULL DEFAULT '[]'::jsonb,
    expected JSONB NOT NULL DEFAULT '{}'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '归档')),
    source TEXT NOT NULL DEFAULT 'payload' CHECK (source IN ('payload', 'manual')),
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_dataset_row_case_unique UNIQUE (case_id),
    CONSTRAINT prompt_evaluation_dataset_row_asset_index_unique UNIQUE (dataset_asset_id, row_index)
);

CREATE INDEX idx_prompt_evaluation_dataset_row_workspace_created
    ON prompt_evaluation_dataset_row(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dataset_row_asset_index
    ON prompt_evaluation_dataset_row(dataset_asset_id, row_index ASC);

INSERT INTO prompt_evaluation_dataset_row (
    workspace_id,
    dataset_asset_id,
    case_id,
    row_index,
    row_name,
    variables,
    expected_contains,
    expected,
    tags,
    status,
    source,
    created_by,
    created_at,
    updated_at
)
SELECT
    c.workspace_id,
    c.asset_id,
    c.id,
    c.case_index,
    c.case_name,
    c.variables,
    c.expected_contains,
    c.expected,
    c.tags,
    c.status,
    c.source,
    c.created_by,
    c.created_at,
    c.updated_at
FROM prompt_evaluation_case c
JOIN prompt_evaluation_asset a ON a.id = c.asset_id
WHERE a.asset_type = '数据集';

UPDATE prompt_evaluation_asset a
SET dataset_row_count = COALESCE(rows.row_count, 0)
FROM (
    SELECT dataset_asset_id, count(*)::int AS row_count
    FROM prompt_evaluation_dataset_row
    GROUP BY dataset_asset_id
) rows
WHERE a.id = rows.dataset_asset_id;
