ALTER TABLE squad_sop_step_event DROP CONSTRAINT IF EXISTS squad_sop_step_event_evidence_is_object;
ALTER TABLE squad_sop_run DROP CONSTRAINT IF EXISTS squad_sop_run_profile_is_object;
-- The Squad profile constraint predates this migration and remains current.
