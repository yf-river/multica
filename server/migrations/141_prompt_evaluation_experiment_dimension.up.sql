ALTER TABLE prompt_evaluation_asset
    ADD COLUMN experiment_dimension_count INT NOT NULL DEFAULT 0;

CREATE TABLE prompt_evaluation_experiment_dimension (
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

CREATE INDEX idx_prompt_evaluation_experiment_dimension_workspace_created
    ON prompt_evaluation_experiment_dimension(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_experiment_dimension_asset_index
    ON prompt_evaluation_experiment_dimension(experiment_asset_id, dimension_index ASC);

WITH experiment_assets AS (
    SELECT
        id,
        workspace_id,
        status,
        created_by,
        created_at,
        updated_at,
        COALESCE(payload->>'实验对象', payload->>'experiment_target', payload->>'target', '') AS experiment_target,
        COALESCE(payload->>'基线输出', payload->>'baseline_output', payload->>'baseline', '') AS baseline_output,
        CASE
            WHEN jsonb_typeof(payload->'对比维度') = 'array' THEN payload->'对比维度'
            WHEN jsonb_typeof(payload->'evaluation_dimensions') = 'array' THEN payload->'evaluation_dimensions'
            WHEN jsonb_typeof(payload->'评估维度') = 'array' THEN payload->'评估维度'
            WHEN jsonb_typeof(payload->'指标') = 'array' THEN payload->'指标'
            WHEN jsonb_typeof(payload->'metric_contract') = 'array' THEN payload->'metric_contract'
            WHEN jsonb_typeof(payload->'对比维度') = 'object' THEN (
                SELECT jsonb_agg(key ORDER BY key)
                FROM jsonb_object_keys(payload->'对比维度') AS key
            )
            WHEN jsonb_typeof(payload->'evaluation_dimensions') = 'object' THEN (
                SELECT jsonb_agg(key ORDER BY key)
                FROM jsonb_object_keys(payload->'evaluation_dimensions') AS key
            )
            ELSE '[]'::jsonb
        END AS dimensions
    FROM prompt_evaluation_asset
    WHERE asset_type = '实验'
),
dimension_rows AS (
    SELECT
        a.workspace_id,
        a.id AS experiment_asset_id,
        (d.ordinality - 1)::int AS dimension_index,
        trim(BOTH '"' FROM d.value::text) AS dimension_name,
        a.experiment_target,
        a.baseline_output,
        '{}'::jsonb AS comparison_payload,
        a.status,
        a.created_by,
        a.created_at,
        a.updated_at
    FROM experiment_assets a
    CROSS JOIN LATERAL jsonb_array_elements(a.dimensions) WITH ORDINALITY AS d(value, ordinality)
    WHERE trim(BOTH '"' FROM d.value::text) <> ''
)
INSERT INTO prompt_evaluation_experiment_dimension (
    workspace_id,
    experiment_asset_id,
    dimension_index,
    dimension_name,
    experiment_target,
    baseline_output,
    comparison_payload,
    status,
    source,
    created_by,
    created_at,
    updated_at
)
SELECT
    workspace_id,
    experiment_asset_id,
    dimension_index,
    dimension_name,
    experiment_target,
    baseline_output,
    comparison_payload,
    status,
    'payload',
    created_by,
    created_at,
    updated_at
FROM dimension_rows;

UPDATE prompt_evaluation_asset a
SET experiment_dimension_count = COALESCE(dimensions.dimension_count, 0)
FROM (
    SELECT experiment_asset_id, count(*)::int AS dimension_count
    FROM prompt_evaluation_experiment_dimension
    GROUP BY experiment_asset_id
) dimensions
WHERE a.id = dimensions.experiment_asset_id;
