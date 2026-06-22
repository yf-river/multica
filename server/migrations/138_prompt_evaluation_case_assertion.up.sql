CREATE TABLE prompt_evaluation_case_assertion (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    case_id UUID NOT NULL REFERENCES prompt_evaluation_case(id) ON DELETE CASCADE,
    assertion_index INT NOT NULL DEFAULT 0,
    assertion_type TEXT NOT NULL DEFAULT '包含文本' CHECK (assertion_type IN ('包含文本')),
    expected_text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '归档')),
    source TEXT NOT NULL DEFAULT 'expected_contains' CHECK (source IN ('expected_contains')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_case_assertion_case_index_unique UNIQUE (case_id, assertion_index)
);

CREATE INDEX idx_prompt_evaluation_case_assertion_workspace
    ON prompt_evaluation_case_assertion(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_assertion_asset
    ON prompt_evaluation_case_assertion(asset_id, case_id, assertion_index ASC);

CREATE INDEX idx_prompt_evaluation_case_assertion_case
    ON prompt_evaluation_case_assertion(case_id, assertion_index ASC);

INSERT INTO prompt_evaluation_case_assertion (
    workspace_id,
    asset_id,
    case_id,
    assertion_index,
    assertion_type,
    expected_text,
    status,
    source,
    created_at
)
SELECT
    c.workspace_id,
    c.asset_id,
    c.id,
    expected.ordinality::int - 1,
    '包含文本',
    btrim(expected.value),
    c.status,
    'expected_contains',
    c.created_at
FROM prompt_evaluation_case c
CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE
        WHEN jsonb_typeof(c.expected_contains) = 'array' THEN c.expected_contains
        ELSE '[]'::jsonb
    END
) WITH ORDINALITY AS expected(value, ordinality)
WHERE btrim(expected.value) <> '';
