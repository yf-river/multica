UPDATE squad
SET sop_profile = jsonb_set(
    sop_profile,
    '{steps}',
    COALESCE((
        SELECT jsonb_agg(
            (step - 'step_key' - 'id' - 'title' - 'label' - 'role') ||
            jsonb_strip_nulls(jsonb_build_object(
                'key', COALESCE(NULLIF(step->>'key', ''), NULLIF(step->>'step_key', ''), NULLIF(step->>'id', '')),
                'name', COALESCE(NULLIF(step->>'name', ''), NULLIF(step->>'title', ''), NULLIF(step->>'label', '')),
                'role_key', COALESCE(NULLIF(step->>'role_key', ''), NULLIF(step->>'role', ''))
            ))
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(sop_profile->'steps') WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb),
    false
)
WHERE jsonb_typeof(sop_profile->'steps') = 'array';

UPDATE squad_sop_run
SET profile = jsonb_set(
    profile,
    '{steps}',
    COALESCE((
        SELECT jsonb_agg(
            (step - 'step_key' - 'id' - 'title' - 'label' - 'role') ||
            jsonb_strip_nulls(jsonb_build_object(
                'key', COALESCE(NULLIF(step->>'key', ''), NULLIF(step->>'step_key', ''), NULLIF(step->>'id', '')),
                'name', COALESCE(NULLIF(step->>'name', ''), NULLIF(step->>'title', ''), NULLIF(step->>'label', '')),
                'role_key', COALESCE(NULLIF(step->>'role_key', ''), NULLIF(step->>'role', ''))
            ))
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(profile->'steps') WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb),
    false
)
WHERE jsonb_typeof(profile->'steps') = 'array';
ALTER TABLE chat_session
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS work_dir,
    DROP COLUMN IF EXISTS runtime_id;
