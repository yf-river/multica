DROP TABLE IF EXISTS life_context_state;
DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'companion_profile', 'life_identity_version', 'life_relationship_event',
        'life_material', 'life_memory', 'life_topic', 'life_commitment',
        'life_internal_thought', 'life_proactive_policy', 'life_experiment',
        'life_proactive_check', 'life_module', 'life_observer',
        'life_observation_topic', 'life_upgrade_evaluation', 'life_chronicle_entry'
    ] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', 'life_context_version_' || table_name, table_name);
    END LOOP;
END;
$$;
DROP FUNCTION IF EXISTS life_bump_context_version();
ALTER TABLE life_cognition_job
    DROP CONSTRAINT IF EXISTS life_cognition_job_output_summary_check,
    DROP CONSTRAINT IF EXISTS life_cognition_job_source_ids_check,
    DROP COLUMN IF EXISTS output_summary,
    DROP COLUMN IF EXISTS source_ids,
    DROP COLUMN IF EXISTS processing_cursor,
    DROP COLUMN IF EXISTS context_version,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS claim_token;
