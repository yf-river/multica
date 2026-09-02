CREATE OR REPLACE FUNCTION public.capture_life_chat_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
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
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at;

        IF NEW.role = 'assistant' THEN
            INSERT INTO life_cognition_job (
                workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
            ) VALUES (
                target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
                'chat:' || NEW.chat_session_id::text || ':' || NEW.id::text,
                jsonb_build_object('chat_session_id', NEW.chat_session_id, 'through_message_id', NEW.id)
            ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
