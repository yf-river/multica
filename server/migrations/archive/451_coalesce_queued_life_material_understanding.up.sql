DO $$
DECLARE
    target record;
    keeper_id uuid;
BEGIN
    FOR target IN
        SELECT workspace_id, user_id, companion_agent_id
        FROM life_cognition_job
        WHERE job_type = 'understand_materials'
          AND status = 'queued' AND task_id IS NULL
        GROUP BY workspace_id, user_id, companion_agent_id
        HAVING count(*) > 1
    LOOP
        SELECT id INTO keeper_id
        FROM life_cognition_job
        WHERE workspace_id = target.workspace_id
          AND user_id = target.user_id
          AND companion_agent_id = target.companion_agent_id
          AND job_type = 'understand_materials'
          AND status = 'queued' AND task_id IS NULL
        ORDER BY scheduled_at, created_at, id
        LIMIT 1;

        UPDATE life_cognition_job keeper
        SET input = jsonb_set(
                jsonb_set(
                    keeper.input - 'material_id',
                    '{material_ids}',
                    (SELECT jsonb_agg(DISTINCT material.value ORDER BY material.value)
                     FROM life_cognition_job batch
                     CROSS JOIN LATERAL jsonb_array_elements_text(
                         COALESCE(batch.input->'material_ids', jsonb_build_array(batch.input->>'material_id'))
                     ) AS material(value)
                     WHERE batch.workspace_id = target.workspace_id
                       AND batch.user_id = target.user_id
                       AND batch.companion_agent_id = target.companion_agent_id
                       AND batch.job_type = 'understand_materials'
                       AND batch.status = 'queued' AND batch.task_id IS NULL)
                ),
                '{processing_cursors}',
                COALESCE((SELECT jsonb_agg(DISTINCT cursor_item.value ORDER BY cursor_item.value)
                 FROM life_cognition_job batch
                 CROSS JOIN LATERAL jsonb_array_elements_text(
                     COALESCE(batch.input->'processing_cursors', '[]'::jsonb)
                 ) AS cursor_item(value)
                 WHERE batch.workspace_id = target.workspace_id
                   AND batch.user_id = target.user_id
                   AND batch.companion_agent_id = target.companion_agent_id
                   AND batch.job_type = 'understand_materials'
                   AND batch.status = 'queued' AND batch.task_id IS NULL), '[]'::jsonb)
            ),
            scheduled_at = (SELECT min(batch.scheduled_at)
                            FROM life_cognition_job batch
                            WHERE batch.workspace_id = target.workspace_id
                              AND batch.user_id = target.user_id
                              AND batch.companion_agent_id = target.companion_agent_id
                              AND batch.job_type = 'understand_materials'
                              AND batch.status = 'queued' AND batch.task_id IS NULL),
            updated_at = now()
        WHERE keeper.id = keeper_id;

        UPDATE life_cognition_job
        SET status = 'coalesced', completed_at = now(), updated_at = now(),
            output = jsonb_build_object('coalesced_into', keeper_id::text),
            error = 'coalesced into ' || keeper_id::text
        WHERE workspace_id = target.workspace_id
          AND user_id = target.user_id
          AND companion_agent_id = target.companion_agent_id
          AND job_type = 'understand_materials'
          AND status = 'queued' AND task_id IS NULL
          AND id <> keeper_id;
    END LOOP;
END
$$;

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
    PERFORM pg_advisory_xact_lock(hashtextextended(
        target_workspace_id::text || ':' || target_user_id::text || ':' || target_agent_id::text, 0
    ));

    SELECT id INTO result_id
    FROM life_cognition_job
    WHERE workspace_id = target_workspace_id
      AND user_id = target_user_id
      AND companion_agent_id = target_agent_id
      AND job_type = 'understand_materials'
      AND status = 'queued' AND task_id IS NULL
    ORDER BY scheduled_at, created_at, id
    LIMIT 1
    FOR UPDATE;

    IF result_id IS NULL THEN
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at
        ) VALUES (
            target_workspace_id,
            target_user_id,
            target_agent_id,
            'understand_materials',
            'material-batch:' || target_material_id::text,
            jsonb_build_object(
                'material_ids', jsonb_build_array(target_material_id::text),
                'processing_cursors', jsonb_build_array('material:' || target_material_id::text)
            ),
            batch_start + interval '65 seconds'
        )
        ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING
        RETURNING id INTO result_id;

        IF result_id IS NULL THEN
            SELECT id INTO result_id
            FROM life_cognition_job
            WHERE workspace_id = target_workspace_id
              AND user_id = target_user_id
              AND job_type = 'understand_materials'
              AND dedupe_key = 'material-batch:' || target_material_id::text;
        END IF;
    ELSE
        UPDATE life_cognition_job
        SET input = jsonb_set(
                jsonb_set(
                    input,
                    '{material_ids}',
                    (SELECT jsonb_agg(value ORDER BY value)
                     FROM (SELECT DISTINCT value
                           FROM jsonb_array_elements_text(
                               COALESCE(input->'material_ids', '[]'::jsonb)
                               || jsonb_build_array(target_material_id::text)
                           )) values_set)
                ),
                '{processing_cursors}',
                (SELECT jsonb_agg(value ORDER BY value)
                 FROM (SELECT DISTINCT value
                       FROM jsonb_array_elements_text(
                           COALESCE(input->'processing_cursors', '[]'::jsonb)
                           || jsonb_build_array('material:' || target_material_id::text)
                       )) cursor_set)
            ),
            updated_at = now()
        WHERE id = result_id;
    END IF;

    RETURN result_id;
END;
$$;
