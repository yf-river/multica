UPDATE squad SET sop_profile = '{}'::jsonb
WHERE jsonb_typeof(sop_profile) IS DISTINCT FROM 'object';

UPDATE squad_sop_run SET profile = '{}'::jsonb
WHERE jsonb_typeof(profile) IS DISTINCT FROM 'object';

UPDATE squad_sop_step_event SET evidence = '{}'::jsonb
WHERE jsonb_typeof(evidence) IS DISTINCT FROM 'object';

ALTER TABLE squad DROP CONSTRAINT IF EXISTS squad_sop_profile_object;
ALTER TABLE squad ADD CONSTRAINT squad_sop_profile_object CHECK (jsonb_typeof(sop_profile) = 'object');

ALTER TABLE squad_sop_run DROP CONSTRAINT IF EXISTS squad_sop_run_profile_is_object;
ALTER TABLE squad_sop_run ADD CONSTRAINT squad_sop_run_profile_is_object CHECK (jsonb_typeof(profile) = 'object');

ALTER TABLE squad_sop_step_event DROP CONSTRAINT IF EXISTS squad_sop_step_event_evidence_is_object;
ALTER TABLE squad_sop_step_event ADD CONSTRAINT squad_sop_step_event_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object');
