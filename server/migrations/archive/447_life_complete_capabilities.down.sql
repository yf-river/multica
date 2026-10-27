DROP TRIGGER IF EXISTS capture_life_experiment_material_after_write ON public.life_experiment_round;
DROP FUNCTION IF EXISTS public.capture_life_experiment_material();
DROP TRIGGER IF EXISTS capture_life_project_material_after_write ON public.project;
DROP FUNCTION IF EXISTS public.capture_life_project_material();
DROP TRIGGER IF EXISTS capture_life_comment_material_after_write ON public.comment;
DROP FUNCTION IF EXISTS public.capture_life_comment_material();
DROP TRIGGER IF EXISTS capture_life_issue_material_after_write ON public.issue;
DROP FUNCTION IF EXISTS public.capture_life_issue_material();
DROP TRIGGER IF EXISTS capture_life_chat_material_after_write ON public.chat_message;
DROP FUNCTION IF EXISTS public.capture_life_chat_material();
DROP TABLE IF EXISTS public.life_upgrade_evaluation;
DROP INDEX IF EXISTS public.life_chronicle_one_published_period;
DROP TABLE IF EXISTS public.life_chronicle_revision;
ALTER TABLE public.life_chronicle_entry
    DROP CONSTRAINT IF EXISTS life_chronicle_revision_positive,
    DROP CONSTRAINT IF EXISTS life_chronicle_generated_by_check,
    DROP CONSTRAINT IF EXISTS life_chronicle_status_check,
    DROP CONSTRAINT IF EXISTS life_chronicle_period_kind_check,
    DROP COLUMN IF EXISTS revision,
    DROP COLUMN IF EXISTS generated_by,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS actions,
    DROP COLUMN IF EXISTS period_kind;
DROP TABLE IF EXISTS public.life_observation_topic_judgement;
DROP TABLE IF EXISTS public.life_observation_topic;
DROP TABLE IF EXISTS public.life_observer_judgement;
DROP TABLE IF EXISTS public.life_observer_knowledge;
DROP TABLE IF EXISTS public.life_observer_version;
DROP TABLE IF EXISTS public.life_observer;
DROP TABLE IF EXISTS public.life_module_version;
DROP TABLE IF EXISTS public.life_module;
DROP TABLE IF EXISTS public.life_experiment_observation;
ALTER TABLE public.life_experiment_round DROP CONSTRAINT IF EXISTS life_experiment_round_review_draft_check;
ALTER TABLE public.life_experiment_round DROP COLUMN IF EXISTS review_draft;
ALTER TABLE public.life_action_proposal
    DROP CONSTRAINT IF EXISTS life_action_proposal_receipt_check,
    DROP COLUMN IF EXISTS execution_receipt,
    DROP COLUMN IF EXISTS rejection_reason,
    DROP COLUMN IF EXISTS rejected_at;
ALTER TABLE public.life_action_proposal DROP CONSTRAINT IF EXISTS life_action_proposal_type_check;
ALTER TABLE public.life_action_proposal ADD CONSTRAINT life_action_proposal_type_check
    CHECK (proposal_type = ANY (ARRAY['experiment_start', 'experiment_extend', 'workspace_issue', 'module_adoption']));
ALTER TABLE public.life_proactive_check
    DROP COLUMN IF EXISTS value_assessment,
    DROP COLUMN IF EXISTS user_responded_at,
    DROP COLUMN IF EXISTS message;
DROP TABLE IF EXISTS public.life_proactive_policy;
DROP TABLE IF EXISTS public.life_derivation;
DROP TABLE IF EXISTS public.life_cognition_job;
DROP TABLE IF EXISTS public.life_internal_thought;
DROP TABLE IF EXISTS public.life_commitment;
DROP TABLE IF EXISTS public.life_topic_memory;
DROP TABLE IF EXISTS public.life_topic;
DROP TABLE IF EXISTS public.life_memory_revision;
ALTER TABLE public.life_memory
    DROP CONSTRAINT IF EXISTS life_memory_scope_check,
    DROP COLUMN IF EXISTS superseded_by_id,
    DROP COLUMN IF EXISTS review_after,
    DROP COLUMN IF EXISTS last_reviewed_at,
    DROP COLUMN IF EXISTS scope;

ALTER TABLE public.life_memory_evidence DROP CONSTRAINT IF EXISTS life_memory_evidence_source_type_check;
ALTER TABLE public.life_memory_evidence
    DROP CONSTRAINT IF EXISTS life_memory_evidence_stance_check,
    DROP COLUMN IF EXISTS stance;
ALTER TABLE public.life_memory_evidence ADD CONSTRAINT life_memory_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY['chat_message', 'task', 'comment', 'memory', 'experiment_round']));
ALTER TABLE public.life_chronicle_evidence DROP CONSTRAINT IF EXISTS life_chronicle_evidence_source_type_check;
ALTER TABLE public.life_chronicle_evidence ADD CONSTRAINT life_chronicle_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY['chat_message', 'task', 'memory', 'experiment_round']));
DROP TABLE IF EXISTS public.life_forget_tombstone;
DROP TABLE IF EXISTS public.life_material;
DROP TABLE IF EXISTS public.life_relationship_event;
ALTER TABLE public.companion_profile DROP COLUMN IF EXISTS current_identity_version_id;
DROP TABLE IF EXISTS public.life_identity_version;
ALTER TABLE public.companion_profile
    DROP CONSTRAINT IF EXISTS companion_profile_return_context_check,
    DROP COLUMN IF EXISTS return_context,
    DROP COLUMN IF EXISTS last_interaction_at;
