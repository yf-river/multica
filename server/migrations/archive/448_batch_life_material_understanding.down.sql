UPDATE public.life_cognition_job
SET status = 'completed', updated_at = now()
WHERE status = 'coalesced';

ALTER TABLE public.life_cognition_job
    DROP CONSTRAINT life_cognition_job_status_check;
ALTER TABLE public.life_cognition_job
    ADD CONSTRAINT life_cognition_job_status_check
    CHECK (status = ANY (ARRAY['queued', 'running', 'completed', 'failed', 'cancelled']));

CREATE OR REPLACE FUNCTION public.capture_life_issue_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'task', NEW.id::text, revision,
            concat_ws(E'\n', NEW.title, NEW.description),
            jsonb_build_object('status', NEW.status, 'priority', NEW.priority, 'due_date', NEW.due_date, 'project_id', NEW.project_id),
            NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'task:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'task', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.capture_life_comment_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'comment', NEW.id::text, revision,
            NEW.content,
            jsonb_build_object('issue_id', NEW.issue_id, 'author_type', NEW.author_type, 'author_id', NEW.author_id, 'comment_type', NEW.type),
            NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'comment:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'comment', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.capture_life_project_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'project', NEW.id::text, revision,
            concat_ws(E'\n', NEW.title, NEW.description),
            jsonb_build_object('status', NEW.status, 'priority', NEW.priority), NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'project:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'project', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.capture_life_experiment_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
    body text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    body := jsonb_build_object(
        'round_id', NEW.id,
        'status', NEW.status,
        'plan', NEW.plan,
        'starts_at', NEW.starts_at,
        'ends_at', NEW.ends_at,
        'stop_reason', NEW.stop_reason,
        'review', NEW.review
    )::text;
    FOR target IN
        SELECT experiment.workspace_id, experiment.user_id, profile.agent_id
        FROM life_experiment experiment
        JOIN companion_profile profile
          ON profile.workspace_id = experiment.workspace_id
         AND profile.user_id = experiment.user_id
        WHERE experiment.id = NEW.experiment_id
    LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'experiment_round', NEW.id::text, revision,
            body, jsonb_build_object('experiment_id', NEW.experiment_id, 'status', NEW.status), NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'experiment_round:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'experiment_round', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

DROP FUNCTION public.queue_life_material_understanding(uuid, uuid, uuid, uuid);
