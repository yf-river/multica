-- Restore the shape understood by the previous application version.
UPDATE prompt_evaluation_optimization_candidate
SET metrics = jsonb_set(
    metrics,
    '{skill_patch}',
    (metrics->'skill_patch') - 'candidate_intent',
    true
)
WHERE jsonb_typeof(metrics->'skill_patch') = 'object'
  AND metrics->'skill_patch'->>'candidate_intent' = 'update_existing_skill';
