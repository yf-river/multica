CREATE TABLE openviking_event (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid,
    agent_id uuid,
    chat_session_id uuid,
    issue_id uuid,
    target_user text NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz
);

-- 事务内保存原文和关联，排队后取消、编辑或删除也能归档。
CREATE FUNCTION capture_openviking_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    item jsonb;
    target_agent uuid;
    target_workspace uuid;
    target_chat uuid;
    target_issue uuid;
    native_user text;
    changed boolean := true;
BEGIN
    IF current_setting('multica.openviking_enabled', true) IS DISTINCT FROM 'true' THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN item := to_jsonb(OLD); ELSE item := to_jsonb(NEW); END IF;
    target_workspace := (item->>'workspace_id')::uuid;
    target_chat := (item->>'chat_session_id')::uuid;
    target_issue := (item->>'issue_id')::uuid;
    IF TG_TABLE_NAME = 'chat_message' THEN
        SELECT agent_id, workspace_id INTO target_agent, target_workspace
        FROM chat_session WHERE id = target_chat;
        IF TG_OP = 'UPDATE' THEN changed := OLD.content IS DISTINCT FROM NEW.content; END IF;
    ELSIF TG_TABLE_NAME = 'comment' THEN
        IF item->>'author_type' = 'agent' THEN target_agent := (item->>'author_id')::uuid;
        ELSE SELECT assignee_id INTO target_agent FROM issue WHERE id = target_issue AND assignee_type = 'agent'; END IF;
        IF TG_OP = 'UPDATE' THEN changed := OLD.content IS DISTINCT FROM NEW.content; END IF;
    ELSE
        IF item->>'task_id' IS NOT NULL THEN SELECT agent_id INTO target_agent FROM agent_task_queue WHERE id = (item->>'task_id')::uuid; END IF;
        IF target_agent IS NULL AND target_chat IS NOT NULL THEN SELECT agent_id INTO target_agent FROM chat_session WHERE id = target_chat; END IF;
        IF target_agent IS NULL AND target_issue IS NOT NULL THEN SELECT assignee_id INTO target_agent FROM issue WHERE id = target_issue AND assignee_type = 'agent'; END IF;
        IF target_agent IS NULL AND item->>'uploader_type' = 'agent' THEN target_agent := (item->>'uploader_id')::uuid; END IF;
    END IF;
    IF target_agent IS NOT NULL THEN
        SELECT COALESCE(NULLIF(custom_env->>'MULTICA_OPENVIKING_USER', ''), 'agent-' || id::text)
        INTO native_user FROM agent WHERE id = target_agent;
    END IF;
    native_user := COALESCE(native_user, 'multica');
    INSERT INTO openviking_event (workspace_id,agent_id,chat_session_id,issue_id,target_user,kind,payload)
    VALUES (target_workspace,target_agent,target_chat,target_issue,native_user,TG_TABLE_NAME,
        jsonb_build_object('operation',TG_OP,'row',item,'content_changed',changed));
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$;

CREATE TRIGGER openviking_chat_message AFTER INSERT OR UPDATE OR DELETE ON chat_message
FOR EACH ROW EXECUTE FUNCTION capture_openviking_event();
CREATE TRIGGER openviking_comment AFTER INSERT OR UPDATE OR DELETE ON comment
FOR EACH ROW EXECUTE FUNCTION capture_openviking_event();
CREATE TRIGGER openviking_attachment AFTER INSERT OR UPDATE OR DELETE ON attachment
FOR EACH ROW EXECUTE FUNCTION capture_openviking_event();
