-- Dataset bindings use one current field set. Normalize top-level aliases and
-- each nested reference while preserving unrelated payload metadata.
UPDATE prompt_evaluation_asset
SET payload = (payload - '数据集版本' - '关联数据集版本') || jsonb_build_object(
    'linked_dataset_versions', CASE
        WHEN jsonb_typeof(COALESCE(payload->'linked_dataset_versions', payload->'数据集版本', payload->'关联数据集版本')) = 'array'
            THEN COALESCE(payload->'linked_dataset_versions', payload->'数据集版本', payload->'关联数据集版本')
        ELSE '[]'::jsonb
    END
)
WHERE payload ?| ARRAY['linked_dataset_versions', '数据集版本', '关联数据集版本'];

WITH normalized AS (
    SELECT
        asset.id,
        COALESCE(jsonb_agg(
            (entry.value
                - 'version_id' - '数据集版本ID'
                - 'dataset_id' - '数据集ID'
                - 'name' - '名称' - '数据集名称')
            || jsonb_strip_nulls(jsonb_build_object(
                'dataset_version_id', NULLIF(COALESCE(entry.value->>'dataset_version_id', entry.value->>'version_id', entry.value->>'数据集版本ID'), ''),
                'dataset_asset_id', NULLIF(COALESCE(entry.value->>'dataset_asset_id', entry.value->>'dataset_id', entry.value->>'数据集ID'), ''),
                'dataset_name', NULLIF(COALESCE(entry.value->>'dataset_name', entry.value->>'数据集名称', entry.value->>'name', entry.value->>'名称'), '')
            )) ORDER BY entry.ordinality
        ) FILTER (WHERE jsonb_typeof(entry.value) = 'object'), '[]'::jsonb) AS refs
    FROM prompt_evaluation_asset AS asset
    CROSS JOIN LATERAL jsonb_array_elements(asset.payload->'linked_dataset_versions') WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(asset.payload->'linked_dataset_versions') = 'array'
    GROUP BY asset.id
)
UPDATE prompt_evaluation_asset AS asset
SET payload = jsonb_set(asset.payload, '{linked_dataset_versions}', normalized.refs, true)
FROM normalized
WHERE asset.id = normalized.id;

UPDATE prompt_evaluation_asset
SET payload = (payload - 'dataset_ids' - '数据集ID' - '关联数据集ID') || jsonb_build_object(
    'linked_dataset_ids', CASE
        WHEN jsonb_typeof(COALESCE(payload->'linked_dataset_ids', payload->'dataset_ids', payload->'数据集ID', payload->'关联数据集ID')) = 'array'
            THEN COALESCE(payload->'linked_dataset_ids', payload->'dataset_ids', payload->'数据集ID', payload->'关联数据集ID')
        WHEN jsonb_typeof(COALESCE(payload->'linked_dataset_ids', payload->'dataset_ids', payload->'数据集ID', payload->'关联数据集ID')) = 'string'
            THEN jsonb_build_array(COALESCE(payload->'linked_dataset_ids', payload->'dataset_ids', payload->'数据集ID', payload->'关联数据集ID') #>> '{}')
        ELSE '[]'::jsonb
    END
)
WHERE payload ?| ARRAY['linked_dataset_ids', 'dataset_ids', '数据集ID', '关联数据集ID'];
