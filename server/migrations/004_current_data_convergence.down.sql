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
ALTER TABLE prompt_evaluation_run
    DROP CONSTRAINT prompt_evaluation_run_run_kind_check;

UPDATE prompt_evaluation_run
SET run_kind = '本地渲染'
WHERE run_kind = '模板渲染检查';

ALTER TABLE prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_run_kind_check
    CHECK (run_kind = ANY (ARRAY['本地渲染'::text, 'Agent执行'::text]));

UPDATE prompt_evaluation_asset
SET payload = payload || jsonb_build_object(
    '用例', COALESCE(payload->'cases', '[]'::jsonb),
    '语义版本', 'multica.training_evaluation.v1',
    'payload_contract', jsonb_build_object(
        'schema_version', 1,
        'schema', 'multica.training_evaluation.payload.v1',
        'cases', 'cases[].case_name / variables / expected_contains / tags',
        '写入策略', '新建和更新统一写入规范 cases。'
    )
)
WHERE payload->>'schema' = 'multica.training_evaluation.payload.v1';
UPDATE prompt_evaluation_optimization_candidate
SET source_failure_summary = source_failure_summary ||
        jsonb_build_object('skill_snapshot', metrics->'skill_snapshot')
WHERE metrics ? 'skill_snapshot';
UPDATE prompt_evaluation_run
SET metrics = metrics
    || CASE
        WHEN metrics ? '输入 token'
            THEN jsonb_build_object('输入token', metrics->'输入 token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN metrics ? '输出 token'
            THEN jsonb_build_object('输出token', metrics->'输出 token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN metrics ? '提示词版本'
            THEN jsonb_build_object('prompt_version', metrics->'提示词版本')
        ELSE '{}'::jsonb
    END
WHERE metrics ?| ARRAY['输入 token', '输出 token', '提示词版本'];
