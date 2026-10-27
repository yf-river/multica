ALTER TABLE public.life_cognition_job
    DROP CONSTRAINT life_cognition_job_status_check;
ALTER TABLE public.life_cognition_job
    ADD CONSTRAINT life_cognition_job_status_check
    CHECK (status = ANY (ARRAY['queued', 'running', 'completed', 'failed', 'cancelled', 'coalesced']));

CREATE FUNCTION public.queue_life_material_understanding(
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

CREATE OR REPLACE FUNCTION public.capture_life_issue_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
    material_id uuid;
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
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
        RETURNING id INTO material_id;
        PERFORM queue_life_material_understanding(target.workspace_id, target.user_id, target.agent_id, material_id);
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
    material_id uuid;
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
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
        RETURNING id INTO material_id;
        PERFORM queue_life_material_understanding(target.workspace_id, target.user_id, target.agent_id, material_id);
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
    material_id uuid;
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
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
        RETURNING id INTO material_id;
        PERFORM queue_life_material_understanding(target.workspace_id, target.user_id, target.agent_id, material_id);
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
    material_id uuid;
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
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
        RETURNING id INTO material_id;
        PERFORM queue_life_material_understanding(target.workspace_id, target.user_id, target.agent_id, material_id);
    END LOOP;
    RETURN NEW;
END;
$$;

WITH source_jobs AS MATERIALIZED (
    SELECT job.*
      FROM life_cognition_job job
     WHERE job.job_type = 'understand_materials'
       AND job.status IN ('queued', 'running', 'failed')
), source_materials AS MATERIALIZED (
    SELECT DISTINCT job.id AS job_id, job.workspace_id, job.user_id, job.companion_agent_id, source.material_id
      FROM source_jobs job
      CROSS JOIN LATERAL (
          SELECT value AS material_id
            FROM jsonb_array_elements_text(COALESCE(job.input->'material_ids', '[]'::jsonb))
          UNION
          SELECT job.input->>'material_id' WHERE job.input ? 'material_id'
          UNION
          SELECT material->>'id'
            FROM jsonb_array_elements(COALESCE(job.input->'new_materials', '[]'::jsonb)) material
          UNION
          SELECT material.id::text
            FROM life_material material
           WHERE material.workspace_id = job.workspace_id
             AND material.user_id = job.user_id
             AND material.source_type = job.input->>'source_type'
             AND material.source_key = job.input->>'source_key'
             AND material.source_revision = job.input->>'source_revision'
      ) source
     WHERE source.material_id IS NOT NULL AND source.material_id <> ''
), batches AS MATERIALIZED (
    INSERT INTO life_cognition_job (
        workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at
    )
    SELECT workspace_id, user_id, companion_agent_id, 'understand_materials',
           'material-batch:migration-014',
           jsonb_build_object(
               'material_ids', jsonb_agg(DISTINCT material_id ORDER BY material_id),
               'processing_cursors', jsonb_agg(DISTINCT ('material:' || material_id) ORDER BY ('material:' || material_id))
           ),
           now()
      FROM source_materials
     GROUP BY workspace_id, user_id, companion_agent_id
    ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO UPDATE
       SET input = EXCLUDED.input, scheduled_at = now(), updated_at = now()
    RETURNING id, workspace_id, user_id, companion_agent_id
), coalesced AS (
    UPDATE life_cognition_job job
       SET status = 'coalesced',
           output = jsonb_build_object('coalesced_into', batch.id),
           completed_at = now(),
           error = '',
           updated_at = now()
      FROM batches batch
     WHERE job.job_type = 'understand_materials'
       AND job.status IN ('queued', 'running', 'failed')
       AND job.workspace_id = batch.workspace_id
       AND job.user_id = batch.user_id
       AND job.companion_agent_id = batch.companion_agent_id
       AND job.id <> batch.id
    RETURNING job.task_id
)
UPDATE agent_task_queue task
   SET status = 'cancelled', error = 'coalesced into a material batch', completed_at = now()
 WHERE task.id IN (SELECT task_id FROM coalesced WHERE task_id IS NOT NULL)
   AND task.status IN ('queued', 'dispatched', 'running');
