ALTER TABLE life_cognition_job
    ADD COLUMN claim_token text,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN context_version bigint NOT NULL DEFAULT 1,
    ADD COLUMN processing_cursor text NOT NULL DEFAULT '',
    ADD COLUMN source_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN output_summary jsonb,
    ADD CONSTRAINT life_cognition_job_source_ids_check CHECK (jsonb_typeof(source_ids) = 'array'),
    ADD CONSTRAINT life_cognition_job_output_summary_check CHECK (output_summary IS NULL OR jsonb_typeof(output_summary) = 'object');

ALTER TABLE life_cognition_job DROP CONSTRAINT IF EXISTS life_cognition_job_status_check;
ALTER TABLE life_cognition_job ADD CONSTRAINT life_cognition_job_status_check
    CHECK (status = ANY (ARRAY['queued', 'running', 'completed', 'failed', 'cancelled', 'coalesced']));

CREATE TABLE life_context_state (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT life_context_state_version_check CHECK (version > 0)
);

INSERT INTO life_context_state (workspace_id, user_id)
SELECT workspace_id, user_id FROM companion_profile
ON CONFLICT (workspace_id, user_id) DO NOTHING;

-- Every change to governed life material advances one shared context version.
-- Cognition jobs capture the version at claim time and can only write results
-- against that version, so a stale worker cannot resurrect a corrected or
-- deleted fact.  The trigger is deliberately limited to tables that carry the
-- workspace/user scope directly; child rows are covered by their parent job's
-- source ids and the deletion fence.
CREATE OR REPLACE FUNCTION life_bump_context_version() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target_workspace_id uuid;
    target_user_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_workspace_id := OLD.workspace_id;
        target_user_id := OLD.user_id;
    ELSE
        target_workspace_id := NEW.workspace_id;
        target_user_id := NEW.user_id;
    END IF;
    IF target_workspace_id IS NOT NULL AND target_user_id IS NOT NULL THEN
        INSERT INTO life_context_state (workspace_id, user_id, version, updated_at)
        VALUES (target_workspace_id, target_user_id, 2, now())
        ON CONFLICT (workspace_id, user_id) DO UPDATE
        SET version = life_context_state.version + 1, updated_at = now();
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

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
        EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION life_bump_context_version()', 'life_context_version_' || table_name, table_name);
    END LOOP;
END;
$$;
