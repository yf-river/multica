ALTER TABLE prompt_evaluation_asset
    ADD COLUMN test_suite_case_count INT NOT NULL DEFAULT 0;

CREATE TABLE prompt_evaluation_test_suite_case (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    test_suite_asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    case_id UUID NOT NULL REFERENCES prompt_evaluation_case(id) ON DELETE CASCADE,
    case_index INT NOT NULL DEFAULT 0,
    case_name TEXT NOT NULL DEFAULT '',
    variables JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_contains JSONB NOT NULL DEFAULT '[]'::jsonb,
    expected JSONB NOT NULL DEFAULT '{}'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '归档')),
    source TEXT NOT NULL DEFAULT 'payload' CHECK (source IN ('payload', 'manual')),
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_test_suite_case_case_unique UNIQUE (case_id),
    CONSTRAINT prompt_evaluation_test_suite_case_asset_index_unique UNIQUE (test_suite_asset_id, case_index)
);

CREATE INDEX idx_prompt_evaluation_test_suite_case_workspace_created
    ON prompt_evaluation_test_suite_case(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_test_suite_case_asset_index
    ON prompt_evaluation_test_suite_case(test_suite_asset_id, case_index ASC);

INSERT INTO prompt_evaluation_test_suite_case (
    workspace_id,
    test_suite_asset_id,
    case_id,
    case_index,
    case_name,
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
WHERE a.asset_type = '测试套件';

UPDATE prompt_evaluation_asset a
SET test_suite_case_count = COALESCE(cases.case_count, 0)
FROM (
    SELECT test_suite_asset_id, count(*)::int AS case_count
    FROM prompt_evaluation_test_suite_case
    GROUP BY test_suite_asset_id
) cases
WHERE a.id = cases.test_suite_asset_id;
