-- Result evidence and metrics are current JSON object contracts. Normalize
-- historical incompatible values once, then make read-side corruption
-- impossible instead of replacing it with successful empty responses.
UPDATE prompt_evaluation_run
SET metrics = '{}'::jsonb
WHERE jsonb_typeof(metrics) IS DISTINCT FROM 'object';

UPDATE prompt_evaluation_run
SET evidence = '{}'::jsonb
WHERE jsonb_typeof(evidence) IS DISTINCT FROM 'object';

UPDATE prompt_evaluation_optimization_candidate
SET source_failure_summary = '{}'::jsonb
WHERE jsonb_typeof(source_failure_summary) IS DISTINCT FROM 'object';

UPDATE prompt_evaluation_optimization_candidate
SET source_prompt_snapshot = '{}'::jsonb
WHERE jsonb_typeof(source_prompt_snapshot) IS DISTINCT FROM 'object';

UPDATE prompt_evaluation_optimization_candidate
SET metrics = '{}'::jsonb
WHERE jsonb_typeof(metrics) IS DISTINCT FROM 'object';

ALTER TABLE prompt_evaluation_run
DROP CONSTRAINT IF EXISTS prompt_evaluation_run_metrics_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_run_evidence_is_object;

ALTER TABLE prompt_evaluation_run
ADD CONSTRAINT prompt_evaluation_run_metrics_is_object CHECK (jsonb_typeof(metrics) = 'object'),
ADD CONSTRAINT prompt_evaluation_run_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object');

ALTER TABLE prompt_evaluation_optimization_candidate
DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_source_failure_summary_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_source_prompt_snapshot_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_metrics_is_object;

ALTER TABLE prompt_evaluation_optimization_candidate
ADD CONSTRAINT prompt_evaluation_optimization_candidate_source_failure_summary_is_object CHECK (jsonb_typeof(source_failure_summary) = 'object'),
ADD CONSTRAINT prompt_evaluation_optimization_candidate_source_prompt_snapshot_is_object CHECK (jsonb_typeof(source_prompt_snapshot) = 'object'),
ADD CONSTRAINT prompt_evaluation_optimization_candidate_metrics_is_object CHECK (jsonb_typeof(metrics) = 'object');
