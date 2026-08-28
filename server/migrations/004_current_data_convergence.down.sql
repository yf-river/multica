ALTER TABLE chat_session
    ADD COLUMN session_id text,
    ADD COLUMN work_dir text,
    ADD COLUMN runtime_id uuid REFERENCES agent_runtime(id) ON DELETE SET NULL;

WITH latest_resume AS (
    SELECT DISTINCT ON (chat_session_id)
        chat_session_id,
        session_id,
        work_dir,
        runtime_id
    FROM agent_task_queue
    WHERE chat_session_id IS NOT NULL
      AND session_id IS NOT NULL
      AND (
          status = 'completed'
          OR (
              status = 'failed'
              AND COALESCE(failure_reason, '') NOT IN (
                  'iteration_limit',
                  'agent_fallback_message',
                  'api_invalid_request',
                  'codex_semantic_inactivity'
              )
          )
      )
    ORDER BY chat_session_id, completed_at DESC NULLS LAST
)
UPDATE chat_session AS session
SET session_id = resume.session_id,
    work_dir = resume.work_dir,
    runtime_id = resume.runtime_id
FROM latest_resume AS resume
WHERE session.id = resume.chat_session_id;
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
