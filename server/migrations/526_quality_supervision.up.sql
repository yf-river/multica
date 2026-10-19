CREATE TABLE supervision_config (
    workspace_id uuid PRIMARY KEY,
    agent_id uuid,
    owner_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    interval_seconds integer NOT NULL DEFAULT 300 CHECK (interval_seconds BETWEEN 60 AND 3600),
    inventory_complete boolean NOT NULL DEFAULT false,
    last_scan_at timestamptz,
    last_inventory_at timestamptz,
    last_success_at timestamptz,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE supervision_subject (
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    desired_version bigint NOT NULL DEFAULT 1,
    reviewed_version bigint NOT NULL DEFAULT 0,
    force_review boolean NOT NULL DEFAULT false,
    summary text NOT NULL DEFAULT '',
    efficiency text NOT NULL DEFAULT 'unreviewed' CHECK (efficiency IN ('unreviewed','clear','problem','uncertain')),
    correctness text NOT NULL DEFAULT 'unreviewed' CHECK (correctness IN ('unreviewed','clear','problem','uncertain')),
    gaps jsonb NOT NULL DEFAULT '[]',
    last_checked_at timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    failures integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(workspace_id, issue_id)
);
CREATE TABLE supervision_material (
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('issue','comment','task','message','trace')),
    source_id uuid NOT NULL,
    run_id uuid,
    generation bigint NOT NULL DEFAULT 1,
    reviewed_generation bigint NOT NULL DEFAULT 0,
    reviewed_offset integer NOT NULL DEFAULT 0,
    changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(workspace_id, kind, source_id)
);
CREATE TABLE supervision_batch (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    target_version bigint NOT NULL,
    task_id uuid,
    status text NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','running','completed','failed','cancelled')),
    materials jsonb NOT NULL DEFAULT '[]',
    result jsonb,
    error text NOT NULL DEFAULT '',
    rules_version text NOT NULL DEFAULT 'supervision-v1',
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE TABLE supervision_finding (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    fingerprint text NOT NULL,
    batch_id uuid NOT NULL,
    title text NOT NULL,
    description text NOT NULL,
    impact text NOT NULL,
    recommendation text NOT NULL,
    confidence text NOT NULL CHECK(confidence IN ('confirmed','risk')),
    evidence jsonb NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','in_progress','resolved','ignored')),
    snoozed_until timestamptz,
    human_resolution boolean NOT NULL DEFAULT false,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE supervision_action (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    finding_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    action text NOT NULL,
    reason text NOT NULL DEFAULT '',
    receipt jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION supervision_touch(p_issue uuid, p_kind text, p_source uuid, p_run uuid DEFAULT NULL)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE ws uuid;
BEGIN
    SELECT i.workspace_id INTO ws FROM issue i JOIN supervision_config c ON c.workspace_id=i.workspace_id
      WHERE i.id=p_issue AND COALESCE(i.origin_type,'') <> 'health_report';
    IF ws IS NULL THEN RETURN; END IF;
    INSERT INTO supervision_subject(workspace_id,issue_id) VALUES(ws,p_issue)
      ON CONFLICT(workspace_id,issue_id) DO UPDATE
      SET desired_version=supervision_subject.desired_version+1,changed_at=now(),next_attempt_at=LEAST(supervision_subject.next_attempt_at,now());
    INSERT INTO supervision_material(workspace_id,issue_id,kind,source_id,run_id)
      VALUES(ws,p_issue,p_kind,p_source,p_run)
      ON CONFLICT(workspace_id,kind,source_id) DO UPDATE
      SET generation=supervision_material.generation+1,reviewed_offset=0,changed_at=now();
END $$;

CREATE FUNCTION supervision_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid; run uuid; material_kind text;
BEGIN
    IF TG_TABLE_NAME='issue' THEN
      IF TG_OP='DELETE' THEN RETURN OLD; END IF;
      target:=NEW.id; material_kind:='issue';
    ELSIF TG_TABLE_NAME='comment' THEN
      target:=COALESCE(NEW.issue_id,OLD.issue_id); material_kind:='comment';
      IF TG_OP='UPDATE' AND NEW.content IS NOT DISTINCT FROM OLD.content THEN RETURN NEW; END IF;
      IF TG_OP<>'DELETE' AND NEW.source_task_id IS NOT NULL AND EXISTS(
        SELECT 1 FROM agent_task_queue WHERE id=NEW.source_task_id AND context->>'type'='quality_supervision'
      ) THEN RETURN NEW; END IF;
    ELSIF TG_TABLE_NAME='agent_task_queue' THEN
      IF COALESCE(NEW.context->>'type','')='quality_supervision' THEN RETURN NEW; END IF;
      IF TG_OP='UPDATE' AND ROW(NEW.status,NEW.result,NEW.error,NEW.issue_id) IS NOT DISTINCT FROM ROW(OLD.status,OLD.result,OLD.error,OLD.issue_id) THEN RETURN NEW; END IF;
      target:=NEW.issue_id; run:=NEW.id; material_kind:='task';
    ELSIF TG_TABLE_NAME='task_message' THEN
      SELECT issue_id INTO target FROM agent_task_queue WHERE id=NEW.task_id AND COALESCE(context->>'type','')<>'quality_supervision';
      run:=NEW.task_id; material_kind:='message';
    ELSIF TG_TABLE_NAME='task_trace_event' THEN
      target:=NEW.issue_id; run:=NEW.task_id; material_kind:='trace';
    END IF;
    IF TG_OP='DELETE' THEN
      DELETE FROM supervision_material m WHERE m.kind=material_kind AND m.source_id=OLD.id;
      PERFORM supervision_touch(target,'issue',target);
      RETURN OLD;
    END IF;
    IF target IS NOT NULL THEN PERFORM supervision_touch(target,material_kind,NEW.id,run); END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER supervision_issue_change AFTER INSERT OR UPDATE OR DELETE ON issue FOR EACH ROW EXECUTE FUNCTION supervision_source_changed();
CREATE TRIGGER supervision_comment_change AFTER INSERT OR UPDATE OR DELETE ON comment FOR EACH ROW EXECUTE FUNCTION supervision_source_changed();
CREATE TRIGGER supervision_task_change AFTER INSERT OR UPDATE OF status,result,error,issue_id ON agent_task_queue FOR EACH ROW EXECUTE FUNCTION supervision_source_changed();
CREATE TRIGGER supervision_message_change AFTER INSERT ON task_message FOR EACH ROW EXECUTE FUNCTION supervision_source_changed();
CREATE TRIGGER supervision_trace_change AFTER INSERT ON task_trace_event FOR EACH ROW EXECUTE FUNCTION supervision_source_changed();
