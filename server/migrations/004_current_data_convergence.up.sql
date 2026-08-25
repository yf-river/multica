UPDATE prompt_evaluation_run
SET metrics = (metrics - '输入token' - '输出token' - 'prompt_version')
    || CASE
        WHEN NOT metrics ? '输入 token' AND metrics ? '输入token'
            THEN jsonb_build_object('输入 token', metrics->'输入token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN NOT metrics ? '输出 token' AND metrics ? '输出token'
            THEN jsonb_build_object('输出 token', metrics->'输出token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN NOT metrics ? '提示词版本' AND metrics ? 'prompt_version'
            THEN jsonb_build_object('提示词版本', metrics->'prompt_version')
        ELSE '{}'::jsonb
    END
WHERE metrics ?| ARRAY['输入token', '输出token', 'prompt_version'];
UPDATE prompt_evaluation_optimization_candidate
SET metrics = (metrics - 'Skill Snapshot' - '技能快照') ||
        CASE
            WHEN metrics ? 'skill_snapshot' THEN '{}'::jsonb
            ELSE jsonb_strip_nulls(jsonb_build_object(
                'skill_snapshot',
                jsonb_path_query_first(jsonb_build_array(
                    metrics->'Skill Snapshot',
                    metrics->'技能快照',
                    source_failure_summary->'skill_snapshot',
                    source_failure_summary->'Skill Snapshot',
                    source_failure_summary->'技能快照',
                    source_prompt_snapshot->'skill_snapshot',
                    source_prompt_snapshot->'Skill Snapshot',
                    source_prompt_snapshot->'技能快照',
                    source_prompt_snapshot
                ), '$[*] ? (@.type() == "object" && @.base_commit.type() == "string" && @.skill_path.type() == "string" && @.skill_hash.type() == "string")')
            ))
        END,
    source_failure_summary = source_failure_summary - 'skill_snapshot' - 'Skill Snapshot' - '技能快照',
    source_prompt_snapshot = CASE
        WHEN source_prompt_snapshot ?& ARRAY['base_commit', 'skill_path', 'skill_hash'] THEN '{}'::jsonb
        ELSE source_prompt_snapshot - 'skill_snapshot' - 'Skill Snapshot' - '技能快照'
    END
WHERE metrics ?| ARRAY['Skill Snapshot', '技能快照']
   OR source_failure_summary ?| ARRAY['skill_snapshot', 'Skill Snapshot', '技能快照']
   OR source_prompt_snapshot ?| ARRAY['skill_snapshot', 'Skill Snapshot', '技能快照']
   OR source_prompt_snapshot ?& ARRAY['base_commit', 'skill_path', 'skill_hash'];
ALTER TABLE prompt_evaluation_run
    DROP CONSTRAINT prompt_evaluation_run_run_kind_check;

UPDATE prompt_evaluation_run
SET run_kind = '模板渲染检查'
WHERE run_kind = '本地渲染';

ALTER TABLE prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_run_kind_check
    CHECK (run_kind = ANY (ARRAY['模板渲染检查'::text, 'Agent执行'::text]));

UPDATE prompt_evaluation_asset
SET payload = payload - '用例' - '语义版本' - 'payload_contract'
WHERE payload ?| ARRAY['用例', '语义版本', 'payload_contract'];
UPDATE squad
SET sop_profile = jsonb_set(
    sop_profile,
    '{steps}',
    COALESCE((
        SELECT jsonb_agg(
            (step - 'step_key' - 'id' - 'title' - 'label' - 'role') ||
            jsonb_strip_nulls(jsonb_build_object(
                'key', COALESCE(NULLIF(step->>'key', ''), NULLIF(step->>'step_key', ''), NULLIF(step->>'id', '')),
                'name', COALESCE(NULLIF(step->>'name', ''), NULLIF(step->>'title', ''), NULLIF(step->>'label', '')),
                'role_key', COALESCE(NULLIF(step->>'role_key', ''), NULLIF(step->>'role', ''))
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
            (step - 'step_key' - 'id' - 'title' - 'label' - 'role') ||
            jsonb_strip_nulls(jsonb_build_object(
                'key', COALESCE(NULLIF(step->>'key', ''), NULLIF(step->>'step_key', ''), NULLIF(step->>'id', '')),
                'name', COALESCE(NULLIF(step->>'name', ''), NULLIF(step->>'title', ''), NULLIF(step->>'label', '')),
                'role_key', COALESCE(NULLIF(step->>'role_key', ''), NULLIF(step->>'role', ''))
            ))
            ORDER BY ordinal
        )
        FROM jsonb_array_elements(profile->'steps') WITH ORDINALITY AS items(step, ordinal)
    ), '[]'::jsonb),
    false
)
WHERE jsonb_typeof(profile->'steps') = 'array';
ALTER TABLE chat_session
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS work_dir,
    DROP COLUMN IF EXISTS runtime_id;
