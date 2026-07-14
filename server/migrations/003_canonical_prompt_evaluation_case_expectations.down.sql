ALTER TABLE prompt_evaluation_case_assertion
    ADD COLUMN workspace_id uuid,
    ADD COLUMN asset_id uuid,
    ADD COLUMN assertion_type text DEFAULT '包含文本'::text,
    ADD COLUMN expected_text text,
    ADD COLUMN status text DEFAULT '启用'::text,
    ADD COLUMN source text DEFAULT 'expected_contains'::text;

WITH raw_expectations AS (
    SELECT
        c.id AS case_id,
        item.ordinality,
        btrim(CASE jsonb_typeof(item.value)
            WHEN 'string' THEN item.value #>> '{}'
            WHEN 'number' THEN item.value #>> '{}'
            WHEN 'boolean' THEN item.value #>> '{}'
            ELSE ''
        END) AS expected_text
    FROM prompt_evaluation_case c
    CROSS JOIN LATERAL jsonb_array_elements(c.expected_contains) WITH ORDINALITY AS item(value, ordinality)
), normalized AS (
    SELECT
        case_id,
        row_number() OVER (PARTITION BY case_id ORDER BY ordinality) - 1 AS assertion_index,
        expected_text
    FROM raw_expectations
    WHERE expected_text <> ''
)
UPDATE prompt_evaluation_case_assertion a
SET
    workspace_id = c.workspace_id,
    asset_id = c.asset_id,
    expected_text = expected.expected_text,
    status = c.status
FROM prompt_evaluation_case c
JOIN normalized expected ON expected.case_id = c.id
WHERE a.case_id = c.id
  AND a.assertion_index = expected.assertion_index;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM prompt_evaluation_case_assertion
        WHERE workspace_id IS NULL
           OR asset_id IS NULL
           OR expected_text IS NULL
    ) THEN
        RAISE EXCEPTION 'cannot restore prompt evaluation assertion projection columns';
    END IF;
END $$;

ALTER TABLE prompt_evaluation_case_assertion
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN asset_id SET NOT NULL,
    ALTER COLUMN assertion_type SET NOT NULL,
    ALTER COLUMN expected_text SET NOT NULL,
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN source SET NOT NULL,
    ADD CONSTRAINT prompt_evaluation_case_assertion_assertion_type_check CHECK (assertion_type = '包含文本'::text),
    ADD CONSTRAINT prompt_evaluation_case_assertion_source_check CHECK (source = 'expected_contains'::text),
    ADD CONSTRAINT prompt_evaluation_case_assertion_status_check CHECK (status = ANY (ARRAY['启用'::text, '归档'::text, 'draft'::text, 'approved'::text, 'active'::text])),
    ADD CONSTRAINT prompt_evaluation_case_assertion_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    ADD CONSTRAINT prompt_evaluation_case_assertion_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_prompt_evaluation_case_assertion_asset
    ON prompt_evaluation_case_assertion (asset_id, case_id, assertion_index);
CREATE INDEX idx_prompt_evaluation_case_assertion_workspace
    ON prompt_evaluation_case_assertion (workspace_id, created_at DESC);
