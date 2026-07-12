-- Metric fields describe aggregate reporting; experiment dimensions describe
-- scored comparisons. Normalize the former mixed alias surface into distinct
-- current fields while preserving unrelated payload metadata.
WITH normalized AS (
    SELECT
        id,
        CASE jsonb_typeof(payload->'metric_contract')
            WHEN 'array' THEN payload->'metric_contract'
            WHEN 'string' THEN jsonb_build_array(payload->'metric_contract' #>> '{}')
            WHEN 'object' THEN COALESCE((
                SELECT jsonb_agg(item.key ORDER BY item.key)
                FROM jsonb_object_keys(payload->'metric_contract') AS item(key)
            ), '[]'::jsonb)
            ELSE '[]'::jsonb
        END AS value
    FROM prompt_evaluation_asset
    WHERE payload ? 'metric_contract'
)
UPDATE prompt_evaluation_asset AS asset
SET payload = jsonb_set(asset.payload, '{metric_contract}', normalized.value, true)
FROM normalized
WHERE asset.id = normalized.id;

UPDATE prompt_evaluation_asset
SET payload = (payload - '指标口径') || jsonb_build_object(
    'metric_notes', CASE
        WHEN jsonb_typeof(COALESCE(payload->'metric_notes', payload->'指标口径')) = 'array'
            THEN COALESCE(payload->'metric_notes', payload->'指标口径')
        WHEN jsonb_typeof(COALESCE(payload->'metric_notes', payload->'指标口径')) = 'string'
            THEN jsonb_build_array(COALESCE(payload->'metric_notes', payload->'指标口径') #>> '{}')
        ELSE '[]'::jsonb
    END
)
WHERE payload ?| ARRAY['metric_notes', '指标口径'];

WITH source AS (
    SELECT
        asset.id,
        COALESCE(
            payload->'experiment_dimensions', payload->'对比维度',
            payload->'实验维度', payload->'evaluation_dimensions',
            payload->'评估维度', payload->'指标'
        ) AS value
    FROM prompt_evaluation_asset AS asset
    WHERE payload ?| ARRAY[
        'experiment_dimensions', '对比维度', '实验维度',
        'evaluation_dimensions', '评估维度', '指标'
    ]
), normalized AS (
    SELECT
        source.id,
        CASE jsonb_typeof(source.value)
            WHEN 'array' THEN source.value
            WHEN 'string' THEN jsonb_build_array(source.value #>> '{}')
            WHEN 'object' THEN CASE
                WHEN source.value ?| ARRAY['name', '名称', 'dimension', '维度']
                    THEN jsonb_build_array(source.value)
                ELSE COALESCE((
                    SELECT jsonb_agg(
                        CASE
                            WHEN jsonb_typeof(item.value) = 'object'
                                THEN item.value || jsonb_build_object('name', item.key)
                            ELSE jsonb_build_object('name', item.key, 'value', item.value)
                        END
                        ORDER BY item.key
                    )
                    FROM jsonb_each(source.value) AS item(key, value)
                ), '[]'::jsonb)
            END
            ELSE '[]'::jsonb
        END AS dimensions
    FROM source
)
UPDATE prompt_evaluation_asset AS asset
SET payload = (asset.payload
        - '对比维度' - '实验维度' - 'evaluation_dimensions' - '评估维度' - '指标')
    || jsonb_build_object('experiment_dimensions', normalized.dimensions)
FROM normalized
WHERE asset.id = normalized.id;

WITH normalized AS (
    SELECT
        asset.id,
        jsonb_agg(
            CASE
                WHEN jsonb_typeof(entry.value) = 'object' THEN
                    (entry.value - '名称' - 'dimension' - '维度')
                    || jsonb_strip_nulls(jsonb_build_object(
                        'name', NULLIF(COALESCE(
                            entry.value->>'name', entry.value->>'名称',
                            entry.value->>'dimension', entry.value->>'维度'
                        ), '')
                    ))
                ELSE entry.value
            END
            ORDER BY entry.ordinality
        ) AS dimensions
    FROM prompt_evaluation_asset AS asset
    CROSS JOIN LATERAL jsonb_array_elements(asset.payload->'experiment_dimensions')
        WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(asset.payload->'experiment_dimensions') = 'array'
    GROUP BY asset.id
)
UPDATE prompt_evaluation_asset AS asset
SET payload = jsonb_set(asset.payload, '{experiment_dimensions}', normalized.dimensions, true)
FROM normalized
WHERE asset.id = normalized.id;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_metric_contract_array') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_metric_contract_array
            CHECK (NOT (payload ? 'metric_contract') OR jsonb_typeof(payload->'metric_contract') = 'array');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_metric_notes_array') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_metric_notes_array
            CHECK (NOT (payload ? 'metric_notes') OR jsonb_typeof(payload->'metric_notes') = 'array');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_experiment_dimensions_array') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_experiment_dimensions_array
            CHECK (NOT (payload ? 'experiment_dimensions') OR jsonb_typeof(payload->'experiment_dimensions') = 'array');
    END IF;
END $$;
