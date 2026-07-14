DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM prompt_evaluation_case c
        WHERE COALESCE(
            (
                SELECT array_agg(normalized.expected_text ORDER BY normalized.ordinality)
                FROM (
                    SELECT
                        item.ordinality,
                        btrim(CASE jsonb_typeof(item.value)
                            WHEN 'string' THEN item.value #>> '{}'
                            WHEN 'number' THEN item.value #>> '{}'
                            WHEN 'boolean' THEN item.value #>> '{}'
                            ELSE ''
                        END) AS expected_text
                    FROM jsonb_array_elements(c.expected_contains) WITH ORDINALITY AS item(value, ordinality)
                ) normalized
                WHERE normalized.expected_text <> ''
            ),
            ARRAY[]::text[]
        ) IS DISTINCT FROM COALESCE(
            (
                SELECT array_agg(a.expected_text ORDER BY a.assertion_index)
                FROM prompt_evaluation_case_assertion a
                WHERE a.case_id = c.id
            ),
            ARRAY[]::text[]
        )
    ) THEN
        RAISE EXCEPTION 'prompt evaluation case assertions diverge from canonical expected_contains';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_prompt_evaluation_case_assertion_workspace;
DROP INDEX IF EXISTS idx_prompt_evaluation_case_assertion_asset;

ALTER TABLE prompt_evaluation_case_assertion
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_workspace_id_fkey,
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_asset_id_fkey,
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_assertion_type_check,
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_source_check,
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_status_check,
    DROP COLUMN workspace_id,
    DROP COLUMN asset_id,
    DROP COLUMN assertion_type,
    DROP COLUMN expected_text,
    DROP COLUMN status,
    DROP COLUMN source;
