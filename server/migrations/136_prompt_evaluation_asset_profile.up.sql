ALTER TABLE prompt_evaluation_asset
    ADD COLUMN structure_schema TEXT NOT NULL DEFAULT 'multica.training_evaluation.asset_profile.v1',
    ADD COLUMN structured_case_count INT NOT NULL DEFAULT 0,
    ADD COLUMN structured_variable_count INT NOT NULL DEFAULT 0,
    ADD COLUMN structured_assertion_count INT NOT NULL DEFAULT 0,
    ADD COLUMN linked_dataset_count INT NOT NULL DEFAULT 0,
    ADD COLUMN linked_prompt_count INT NOT NULL DEFAULT 0,
    ADD COLUMN evaluation_dimension_count INT NOT NULL DEFAULT 0;

UPDATE prompt_evaluation_asset pea
SET
    structured_case_count = COALESCE(c.case_count, 0),
    structured_variable_count = COALESCE(c.variable_count, 0),
    structured_assertion_count = COALESCE(c.assertion_count, 0),
    linked_prompt_count = CASE WHEN pea.prompt_id IS NOT NULL THEN 1 ELSE 0 END,
    evaluation_dimension_count = CASE
        WHEN jsonb_typeof(pea.payload -> '指标') = 'array' THEN jsonb_array_length(pea.payload -> '指标')
        WHEN jsonb_typeof(pea.payload -> 'metric_contract') = 'array' THEN jsonb_array_length(pea.payload -> 'metric_contract')
        ELSE 0
    END
FROM (
    SELECT
        asset_id,
        COUNT(*)::int AS case_count,
        COALESCE(SUM(CASE
            WHEN jsonb_typeof(variables) = 'object'
                THEN (SELECT COUNT(*) FROM jsonb_object_keys(variables))
            ELSE 0
        END), 0)::int AS variable_count,
        COALESCE(SUM(CASE WHEN jsonb_typeof(expected_contains) = 'array' THEN jsonb_array_length(expected_contains) ELSE 0 END), 0)::int AS assertion_count
    FROM prompt_evaluation_case
    GROUP BY asset_id
) c
WHERE pea.id = c.asset_id;

UPDATE prompt_evaluation_asset
SET
    linked_prompt_count = CASE WHEN prompt_id IS NOT NULL THEN 1 ELSE 0 END,
    evaluation_dimension_count = CASE
        WHEN jsonb_typeof(payload -> '指标') = 'array' THEN jsonb_array_length(payload -> '指标')
        WHEN jsonb_typeof(payload -> 'metric_contract') = 'array' THEN jsonb_array_length(payload -> 'metric_contract')
        ELSE 0
    END;
