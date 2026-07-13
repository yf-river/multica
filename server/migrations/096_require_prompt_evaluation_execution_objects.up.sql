UPDATE prompt_evaluation_trial SET
    input = CASE WHEN jsonb_typeof(input) = 'object' THEN input ELSE '{}'::jsonb END,
    expected = CASE WHEN jsonb_typeof(expected) = 'object' THEN expected ELSE '{}'::jsonb END,
    evidence = CASE WHEN jsonb_typeof(evidence) = 'object' THEN evidence ELSE '{}'::jsonb END;

UPDATE prompt_evaluation_case_operation SET
    filter = CASE WHEN jsonb_typeof(filter) = 'object' THEN filter ELSE '{}'::jsonb END,
    input = CASE WHEN jsonb_typeof(input) = 'object' THEN input ELSE '{}'::jsonb END,
    sample_case_ids = CASE WHEN jsonb_typeof(sample_case_ids) = 'array' THEN sample_case_ids ELSE '[]'::jsonb END;

UPDATE prompt_evaluation_evidence_snapshot SET
    summary = CASE WHEN jsonb_typeof(summary) = 'object' THEN summary ELSE '{}'::jsonb END,
    evidence = CASE WHEN jsonb_typeof(evidence) = 'object' THEN evidence ELSE '{}'::jsonb END;

ALTER TABLE prompt_evaluation_trial
DROP CONSTRAINT IF EXISTS prompt_evaluation_trial_input_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_trial_expected_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_trial_output_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_trial_evidence_is_object;
ALTER TABLE prompt_evaluation_trial
ADD CONSTRAINT prompt_evaluation_trial_input_is_object CHECK (jsonb_typeof(input) = 'object'),
ADD CONSTRAINT prompt_evaluation_trial_expected_is_object CHECK (jsonb_typeof(expected) = 'object'),
ADD CONSTRAINT prompt_evaluation_trial_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object');

ALTER TABLE prompt_evaluation_case_operation
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_operation_filter_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_operation_input_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_operation_sample_case_ids_is_array;
ALTER TABLE prompt_evaluation_case_operation
ADD CONSTRAINT prompt_evaluation_case_operation_filter_is_object CHECK (jsonb_typeof(filter) = 'object'),
ADD CONSTRAINT prompt_evaluation_case_operation_input_is_object CHECK (jsonb_typeof(input) = 'object'),
ADD CONSTRAINT prompt_evaluation_case_operation_sample_case_ids_is_array CHECK (jsonb_typeof(sample_case_ids) = 'array');

ALTER TABLE prompt_evaluation_evidence_snapshot
DROP CONSTRAINT IF EXISTS prompt_evaluation_evidence_snapshot_summary_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_evidence_snapshot_evidence_is_object;
ALTER TABLE prompt_evaluation_evidence_snapshot
ADD CONSTRAINT prompt_evaluation_evidence_snapshot_summary_is_object CHECK (jsonb_typeof(summary) = 'object'),
ADD CONSTRAINT prompt_evaluation_evidence_snapshot_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object');
