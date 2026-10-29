CREATE FUNCTION capture_openviking_chat_session() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    item jsonb;
    target_agent uuid;
    native_user text;
BEGIN
    IF current_setting('multica.openviking_enabled', true) IS DISTINCT FROM 'true' THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN item := to_jsonb(OLD); ELSE item := to_jsonb(NEW); END IF;
    target_agent := (item->>'agent_id')::uuid;
    SELECT COALESCE(NULLIF(custom_env->>'MULTICA_OPENVIKING_USER', ''), 'agent-' || id::text)
      INTO native_user FROM agent WHERE id = target_agent;
    native_user := COALESCE(native_user, 'agent-' || target_agent::text, 'multica');
    INSERT INTO openviking_event (workspace_id,agent_id,chat_session_id,target_user,kind,payload)
    VALUES ((item->>'workspace_id')::uuid,target_agent,(item->>'id')::uuid,native_user,'chat_session',
      jsonb_build_object('operation',TG_OP,'row',item,'content_changed',false));
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$;

CREATE TRIGGER openviking_chat_session AFTER INSERT OR UPDATE OR DELETE ON chat_session
FOR EACH ROW EXECUTE FUNCTION capture_openviking_chat_session();
