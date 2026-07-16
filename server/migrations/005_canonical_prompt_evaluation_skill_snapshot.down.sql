UPDATE prompt_evaluation_optimization_candidate
SET source_failure_summary = source_failure_summary ||
        jsonb_build_object('skill_snapshot', metrics->'skill_snapshot')
WHERE metrics ? 'skill_snapshot';
