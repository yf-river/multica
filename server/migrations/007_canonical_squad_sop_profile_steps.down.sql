UPDATE squad
SET sop_profile = jsonb_set(
    sop_profile,
    '{steps}',
    COALESCE((
        SELECT jsonb_agg(
            step || jsonb_strip_nulls(jsonb_build_object(
                'step_key', NULLIF(step->>'key', ''),
                'title', NULLIF(step->>'name', ''),
                'role', NULLIF(step->>'role_key', '')
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
            step || jsonb_strip_nulls(jsonb_build_object(
                'step_key', NULLIF(step->>'key', ''),
                'title', NULLIF(step->>'name', ''),
                'role', NULLIF(step->>'role_key', '')
            ))
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(profile->'steps') WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb),
    false
)
WHERE jsonb_typeof(profile->'steps') = 'array';
