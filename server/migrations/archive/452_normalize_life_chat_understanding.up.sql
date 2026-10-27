WITH queued_jobs AS MATERIALIZED (
    SELECT *
    FROM life_cognition_job
    WHERE job_type = 'understand_materials'
      AND status = 'queued' AND task_id IS NULL
), source_jobs AS MATERIALIZED (
    SELECT keeper.id AS keeper_id, source.*
    FROM queued_jobs keeper
    JOIN life_cognition_job source
      ON source.id = keeper.id
      OR (source.status = 'coalesced'
          AND source.output->>'coalesced_into' = keeper.id::text)
), source_materials AS MATERIALIZED (
    SELECT DISTINCT source.keeper_id, material_id
    FROM source_jobs source
    CROSS JOIN LATERAL (
        SELECT value AS material_id
        FROM jsonb_array_elements_text(COALESCE(source.input->'material_ids', '[]'::jsonb)) item(value)
        WHERE value IS NOT NULL AND value <> ''
        UNION
        SELECT source.input->>'material_id'
        WHERE COALESCE(source.input->>'material_id', '') <> ''
        UNION
        SELECT item->>'id'
        FROM jsonb_array_elements(COALESCE(source.input->'new_materials', '[]'::jsonb)) item
        WHERE COALESCE(item->>'id', '') <> ''
        UNION
        SELECT material.id::text
        FROM life_material material
        WHERE material.workspace_id = source.workspace_id
          AND material.user_id = source.user_id
          AND material.source_type = source.input->>'source_type'
          AND material.source_key = source.input->>'source_key'
          AND material.source_revision = source.input->>'source_revision'
        UNION
        SELECT material.id::text
        FROM life_material target
        JOIN life_material material
          ON material.workspace_id = source.workspace_id
         AND material.user_id = source.user_id
         AND material.source_type = 'chat_message'
         AND material.metadata->>'chat_session_id' = source.input->>'chat_session_id'
         AND material.occurred_at <= target.occurred_at
         AND material.occurred_at > COALESCE((
             SELECT max(previous.occurred_at)
             FROM life_material previous
             WHERE previous.workspace_id = source.workspace_id
               AND previous.user_id = source.user_id
               AND previous.source_type = 'chat_message'
               AND previous.metadata->>'chat_session_id' = source.input->>'chat_session_id'
               AND previous.metadata->>'role' = 'assistant'
               AND previous.occurred_at < target.occurred_at
         ), '-infinity'::timestamptz)
        WHERE target.workspace_id = source.workspace_id
          AND target.user_id = source.user_id
          AND target.source_type = 'chat_message'
          AND target.source_key = source.input->>'through_message_id'
    ) resolved(material_id)
), normalized AS MATERIALIZED (
    SELECT keeper_id,
           jsonb_agg(material_id ORDER BY material_id) AS material_ids,
           jsonb_agg('material:' || material_id ORDER BY material_id) AS processing_cursors
    FROM source_materials
    GROUP BY keeper_id
)
UPDATE life_cognition_job keeper
SET input = jsonb_set(
        jsonb_set(
            keeper.input - 'material_id' - 'chat_session_id' - 'through_message_id'
                         - 'source_type' - 'source_key' - 'source_revision' - 'new_materials',
            '{material_ids}', normalized.material_ids
        ),
        '{processing_cursors}', normalized.processing_cursors
    ),
    updated_at = now()
FROM normalized
WHERE keeper.id = normalized.keeper_id;

CREATE OR REPLACE FUNCTION public.capture_life_chat_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    material_id uuid;
    source_material record;
BEGIN
    FOR target IN
        SELECT cp.workspace_id, cp.user_id, cp.agent_id
          FROM companion_profile cp
          JOIN chat_session cs
            ON cs.id = NEW.chat_session_id
           AND cs.workspace_id = cp.workspace_id
           AND cs.creator_id = cp.user_id
           AND cs.agent_id = cp.agent_id
    LOOP
        IF NEW.role = 'user' THEN
            UPDATE companion_profile
            SET return_context = CASE
                    WHEN last_interaction_at IS NOT NULL AND last_interaction_at < NEW.created_at - interval '7 days'
                    THEN jsonb_build_object('reunion', true, 'last_interaction_at', last_interaction_at)
                    ELSE '{}'::jsonb
                END,
                last_interaction_at = NEW.created_at,
                updated_at = now()
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id;
            UPDATE life_proactive_policy
            SET unanswered_count = 0, updated_at = now()
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id;
            UPDATE life_proactive_check
            SET user_responded_at = NEW.created_at
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id
              AND status = 'spoke' AND user_responded_at IS NULL;
        END IF;

        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'chat_message', NEW.id::text, '1',
            NEW.content, jsonb_build_object('role', NEW.role, 'chat_session_id', NEW.chat_session_id), NEW.created_at
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
        RETURNING id INTO material_id;

        IF NEW.role = 'assistant' THEN
            FOR source_material IN
                WITH previous_reply AS (
                    SELECT max(material.occurred_at) AS occurred_at
                    FROM life_material material
                    WHERE material.workspace_id = target.workspace_id
                      AND material.user_id = target.user_id
                      AND material.source_type = 'chat_message'
                      AND material.metadata->>'chat_session_id' = NEW.chat_session_id::text
                      AND material.metadata->>'role' = 'assistant'
                      AND material.occurred_at < NEW.created_at
                )
                SELECT material.id
                FROM life_material material, previous_reply
                WHERE material.workspace_id = target.workspace_id
                  AND material.user_id = target.user_id
                  AND material.source_type = 'chat_message'
                  AND material.metadata->>'chat_session_id' = NEW.chat_session_id::text
                  AND material.occurred_at > COALESCE(previous_reply.occurred_at, '-infinity'::timestamptz)
                  AND material.occurred_at <= NEW.created_at
                ORDER BY material.occurred_at, material.id
            LOOP
                PERFORM queue_life_material_understanding(
                    target.workspace_id, target.user_id, target.agent_id, source_material.id
                );
            END LOOP;
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
