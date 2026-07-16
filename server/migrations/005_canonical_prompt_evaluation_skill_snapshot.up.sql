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
