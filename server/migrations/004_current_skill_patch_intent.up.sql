-- Exit the pre-current skill patch shape before readers require intent.
UPDATE prompt_evaluation_optimization_candidate
SET metrics = jsonb_set(
    metrics,
    '{skill_patch,candidate_intent}',
    '"update_existing_skill"'::jsonb,
    true
)
WHERE jsonb_typeof(metrics->'skill_patch') = 'object'
  AND COALESCE(metrics->'skill_patch'->>'candidate_intent', '') = '';
