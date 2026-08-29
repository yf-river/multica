CREATE OR REPLACE FUNCTION public.queue_life_material_understanding(
    target_workspace_id uuid,
    target_user_id uuid,
    target_agent_id uuid,
    target_material_id uuid
) RETURNS uuid
    LANGUAGE plpgsql
    AS $$
DECLARE
    batch_start timestamptz := date_trunc('minute', now());
    result_id uuid;
BEGIN
    INSERT INTO life_cognition_job (
        workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at
    ) VALUES (
        target_workspace_id,
        target_user_id,
        target_agent_id,
        'understand_materials',
        'material-batch:' || to_char(batch_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI'),
        jsonb_build_object(
            'material_ids', jsonb_build_array(target_material_id::text),
            'processing_cursors', jsonb_build_array('material:' || target_material_id::text)
        ),
        batch_start + interval '65 seconds'
    )
    ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO UPDATE
    SET input = jsonb_set(
            jsonb_set(
                life_cognition_job.input,
                '{material_ids}',
                (SELECT jsonb_agg(value ORDER BY value)
                   FROM (SELECT DISTINCT value
                           FROM jsonb_array_elements_text(
                               COALESCE(life_cognition_job.input->'material_ids', '[]'::jsonb)
                               || (EXCLUDED.input->'material_ids')
                       )) values_set)
            ),
            '{processing_cursors}',
            (SELECT jsonb_agg(value ORDER BY value)
               FROM (SELECT DISTINCT value
                       FROM jsonb_array_elements_text(
                           COALESCE(life_cognition_job.input->'processing_cursors', '[]'::jsonb)
                           || (EXCLUDED.input->'processing_cursors')
                   )) cursor_set)
        ),
        scheduled_at = LEAST(life_cognition_job.scheduled_at, EXCLUDED.scheduled_at),
        updated_at = now()
    RETURNING id INTO result_id;
    RETURN result_id;
END;
$$;
