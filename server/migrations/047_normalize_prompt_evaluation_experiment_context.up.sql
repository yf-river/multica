-- Experiment context has one current persisted field set. Scalar retired
-- aliases are preserved as strings; malformed composite values are removed.
WITH normalized AS (
    SELECT
        id,
        COALESCE(payload->'experiment_target', payload->'实验对象', payload->'target', payload->'对象') AS target,
        COALESCE(payload->'baseline_output', payload->'基线输出', payload->'baseline', payload->'baseline_result') AS baseline
    FROM prompt_evaluation_asset
    WHERE payload ?| ARRAY[
        'experiment_target', '实验对象', 'target', '对象',
        'baseline_output', '基线输出', 'baseline', 'baseline_result'
    ]
)
UPDATE prompt_evaluation_asset AS asset
SET payload = (asset.payload
        - '实验对象' - 'target' - '对象'
        - '基线输出' - 'baseline' - 'baseline_result')
    || jsonb_strip_nulls(jsonb_build_object(
        'experiment_target', CASE
            WHEN jsonb_typeof(normalized.target) IN ('string', 'number', 'boolean')
                THEN to_jsonb(normalized.target #>> '{}')
            ELSE NULL
        END,
        'baseline_output', CASE
            WHEN jsonb_typeof(normalized.baseline) IN ('string', 'number', 'boolean')
                THEN to_jsonb(normalized.baseline #>> '{}')
            ELSE NULL
        END
    ))
FROM normalized
WHERE asset.id = normalized.id;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_experiment_target_string') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_experiment_target_string
            CHECK (NOT (payload ? 'experiment_target') OR jsonb_typeof(payload->'experiment_target') = 'string');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_baseline_output_string') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_baseline_output_string
            CHECK (NOT (payload ? 'baseline_output') OR jsonb_typeof(payload->'baseline_output') = 'string');
    END IF;
END $$;
