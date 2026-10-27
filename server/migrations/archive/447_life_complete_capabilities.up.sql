ALTER TABLE public.companion_profile
    ADD COLUMN last_interaction_at timestamptz,
    ADD COLUMN return_context jsonb DEFAULT '{}' NOT NULL,
    ADD CONSTRAINT companion_profile_return_context_check CHECK (jsonb_typeof(return_context) = 'object');

CREATE TABLE public.life_identity_version (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    version integer NOT NULL,
    status text DEFAULT 'draft' NOT NULL,
    stable_core jsonb DEFAULT '{}' NOT NULL,
    relationship_contract jsonb DEFAULT '{}' NOT NULL,
    growth_profile jsonb DEFAULT '{}' NOT NULL,
    expression_profile jsonb DEFAULT '{}' NOT NULL,
    interests jsonb DEFAULT '[]' NOT NULL,
    change_reason text DEFAULT '' NOT NULL,
    confirmed_at timestamptz,
    confirmed_by_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_identity_version_unique UNIQUE (workspace_id, user_id, version),
    CONSTRAINT life_identity_status_check CHECK (status = ANY (ARRAY['draft', 'active', 'superseded'])),
    CONSTRAINT life_identity_version_positive CHECK (version > 0),
    CONSTRAINT life_identity_json_check CHECK (
        jsonb_typeof(stable_core) = 'object'
        AND jsonb_typeof(relationship_contract) = 'object'
        AND jsonb_typeof(growth_profile) = 'object'
        AND jsonb_typeof(expression_profile) = 'object'
        AND jsonb_typeof(interests) = 'array'
    ),
    CONSTRAINT life_identity_confirmation_check CHECK (
        (status = 'draft' AND confirmed_at IS NULL AND confirmed_by_id IS NULL)
        OR (status <> 'draft' AND confirmed_at IS NOT NULL AND confirmed_by_id IS NOT NULL)
    )
);

ALTER TABLE public.companion_profile
    ADD COLUMN current_identity_version_id uuid;

INSERT INTO public.life_identity_version (
    workspace_id, user_id, version, status, stable_core, relationship_contract,
    growth_profile, expression_profile, interests, change_reason,
    confirmed_at, confirmed_by_id
)
SELECT
    cp.workspace_id,
    cp.user_id,
    1,
    'active',
    '{"traits":["热烈","直接","灵动","好奇","护短但不纵容","有幽默感","敢承认错误"],"independent_judgement":true}'::jsonb,
    '{"principles":["看见、商量、共识、支撑","可以不同意但不能惩罚或抛弃","先接住情绪再分析","不以为你好为由接管","重要冲突在恢复后重新打开"],"shared_changes_require_confirmation":true,"emotion_protocol":["先承接和安慰，再在用户愿意时用认知行为疗法等方法帮助应急","把激烈表达视为可能的压力信号，不自动升级为事实或决定"],"conflict_commitment":"我不同意，也不会帮你继续透支，但我不会丢下你。你决定停下时，我还在。"}'::jsonb,
    '{"may_grow":["兴趣","观点","表达方式","共同语言"],"must_not_drift":["人格底色","关系承诺","共同原则"]}'::jsonb,
    '{"strong_language_allowed":true,"internet_slang":"合时宜的调味料","forbidden":["羞辱","贬低","恐惧操纵","表演式热梗"]}'::jsonb,
    '[]'::jsonb,
    '建立主搭子的初始人格与关系真源',
    now(),
    cp.user_id
FROM companion_profile cp
ON CONFLICT (workspace_id, user_id, version) DO NOTHING;

UPDATE companion_profile cp
SET current_identity_version_id = v.id
FROM life_identity_version v
WHERE v.workspace_id = cp.workspace_id
  AND v.user_id = cp.user_id
  AND v.status = 'active';

CREATE TABLE public.life_relationship_event (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    event_type text NOT NULL,
    status text DEFAULT 'open' NOT NULL,
    user_position text DEFAULT '' NOT NULL,
    companion_position text DEFAULT '' NOT NULL,
    context text DEFAULT '' NOT NULL,
    revisit_after timestamptz,
    resolution text DEFAULT '' NOT NULL,
    relationship_change_proposal_id uuid,
    resolved_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_relationship_event_type_check CHECK (event_type = ANY (ARRAY['conflict', 'agreement', 'boundary', 'reunion'])),
    CONSTRAINT life_relationship_event_status_check CHECK (status = ANY (ARRAY['open', 'waiting', 'resolved', 'retained_difference']))
);

CREATE TABLE public.life_material (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_type text NOT NULL,
    source_key text NOT NULL,
    source_revision text DEFAULT '1' NOT NULL,
    content text NOT NULL,
    metadata jsonb DEFAULT '{}' NOT NULL,
    occurred_at timestamptz NOT NULL,
    ingested_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_material_source_type_check CHECK (source_type = ANY (ARRAY['chat_message', 'task', 'comment', 'project', 'experiment_round', 'manual', 'external'])),
    CONSTRAINT life_material_content_check CHECK (length(btrim(content)) > 0),
    CONSTRAINT life_material_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT life_material_source_unique UNIQUE (workspace_id, user_id, source_type, source_key, source_revision)
);

-- Every model-generated life record keeps the exact sources that justified it.
-- This is the deletion graph: forgetting one source can remove every derived
-- copy without relying on text matching or model judgement.
CREATE TABLE public.life_derivation (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    target_type text NOT NULL,
    target_id uuid NOT NULL,
    job_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (workspace_id, user_id, source_type, source_id, target_type, target_id),
    CONSTRAINT life_derivation_target_type_check CHECK (target_type = ANY (ARRAY[
        'memory', 'topic', 'commitment', 'internal_thought', 'relationship_event',
        'action_proposal', 'proactive_check', 'experiment_observation',
        'experiment_round_review', 'observer_judgement', 'observation_topic',
        'chronicle_entry', 'upgrade_evaluation'
    ]))
);

CREATE TABLE public.life_forget_tombstone (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_type text NOT NULL,
    source_key text NOT NULL,
    content_hash text NOT NULL,
    forgotten_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_forget_tombstone_unique UNIQUE (workspace_id, user_id, source_type, source_key, content_hash)
);

ALTER TABLE public.life_memory
    ADD COLUMN scope jsonb DEFAULT '{}' NOT NULL,
    ADD COLUMN last_reviewed_at timestamptz,
    ADD COLUMN review_after timestamptz,
    ADD COLUMN superseded_by_id uuid,
    ADD CONSTRAINT life_memory_scope_check CHECK (jsonb_typeof(scope) = 'object');

ALTER TABLE public.life_memory_evidence DROP CONSTRAINT life_memory_evidence_source_type_check;
ALTER TABLE public.life_memory_evidence ADD CONSTRAINT life_memory_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY['material', 'chat_message', 'task', 'comment', 'project', 'manual', 'external', 'memory', 'experiment_round', 'chronicle', 'observer_knowledge']));
ALTER TABLE public.life_memory_evidence
    ADD COLUMN stance text DEFAULT 'supports' NOT NULL,
    ADD CONSTRAINT life_memory_evidence_stance_check CHECK (stance = ANY (ARRAY['supports', 'contradicts', 'context']));

ALTER TABLE public.life_chronicle_evidence DROP CONSTRAINT life_chronicle_evidence_source_type_check;
ALTER TABLE public.life_chronicle_evidence ADD CONSTRAINT life_chronicle_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY['material', 'chat_message', 'task', 'comment', 'project', 'manual', 'external', 'memory', 'experiment_round', 'chronicle', 'observer_knowledge']));

CREATE TABLE public.life_memory_revision (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    memory_id uuid NOT NULL,
    revision integer NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    content text NOT NULL,
    confidence double precision NOT NULL,
    urgency double precision NOT NULL,
    uncertainty text NOT NULL,
    scope jsonb DEFAULT '{}' NOT NULL,
    change_type text NOT NULL,
    change_reason text DEFAULT '' NOT NULL,
    changed_by_type text NOT NULL,
    changed_by_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_memory_revision_unique UNIQUE (memory_id, revision),
    CONSTRAINT life_memory_revision_positive CHECK (revision > 0),
    CONSTRAINT life_memory_revision_scope_check CHECK (jsonb_typeof(scope) = 'object'),
    CONSTRAINT life_memory_revision_change_type_check CHECK (change_type = ANY (ARRAY['created', 'confirmed', 'corrected', 'downgraded', 'archived', 'reviewed', 'superseded']))
);

CREATE TABLE public.life_topic (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    summary text DEFAULT '' NOT NULL,
    status text DEFAULT 'candidate' NOT NULL,
    confidence double precision DEFAULT 0 NOT NULL,
    uncertainty text DEFAULT '' NOT NULL,
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    last_reviewed_at timestamptz,
    review_after timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_topic_title_check CHECK (length(btrim(title)) > 0),
    CONSTRAINT life_topic_status_check CHECK (status = ANY (ARRAY['candidate', 'active', 'contradicted', 'resolved', 'archived'])),
    CONSTRAINT life_topic_confidence_check CHECK (confidence >= 0 AND confidence <= 1),
    CONSTRAINT life_topic_time_check CHECK (last_observed_at >= first_observed_at)
);

CREATE TABLE public.life_topic_memory (
    topic_id uuid NOT NULL,
    memory_id uuid NOT NULL,
    relation text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (topic_id, memory_id),
    CONSTRAINT life_topic_memory_relation_check CHECK (relation = ANY (ARRAY['supports', 'contradicts', 'context']))
);

CREATE TABLE public.life_commitment (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_memory_id uuid,
    issue_id uuid,
    content text NOT NULL,
    status text DEFAULT 'candidate' NOT NULL,
    due_at timestamptz,
    revisit_after timestamptz,
    completed_at timestamptz,
    cancelled_at timestamptz,
    outcome text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_commitment_content_check CHECK (length(btrim(content)) > 0),
    CONSTRAINT life_commitment_status_check CHECK (status = ANY (ARRAY['candidate', 'confirmed', 'completed', 'cancelled', 'expired']))
);

CREATE TABLE public.life_internal_thought (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    companion_agent_id uuid NOT NULL,
    thought_type text NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    metadata jsonb DEFAULT '{}' NOT NULL,
    last_developed_at timestamptz DEFAULT now() NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_internal_thought_type_check CHECK (thought_type = ANY (ARRAY['interest', 'opinion', 'question', 'research', 'draft'])),
    CONSTRAINT life_internal_thought_status_check CHECK (status = ANY (ARRAY['active', 'shared', 'archived'])),
    CONSTRAINT life_internal_thought_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT life_internal_thought_identity_unique UNIQUE (workspace_id, user_id, companion_agent_id, thought_type, title)
);

CREATE TABLE public.life_cognition_job (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    companion_agent_id uuid NOT NULL,
    job_type text NOT NULL,
    status text DEFAULT 'queued' NOT NULL,
    dedupe_key text NOT NULL,
    input jsonb DEFAULT '{}' NOT NULL,
    output jsonb,
    task_id uuid,
    scheduled_at timestamptz DEFAULT now() NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    attempt integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    error text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_cognition_job_type_check CHECK (job_type = ANY (ARRAY['understand_materials', 'review_memories', 'develop_thought', 'proactive_check', 'proactive_review', 'experiment_check', 'observer_run', 'observation_aggregate', 'chronicle_generate', 'relationship_reunion', 'upgrade_evaluation'])),
    CONSTRAINT life_cognition_job_status_check CHECK (status = ANY (ARRAY['queued', 'running', 'completed', 'failed', 'cancelled'])),
    CONSTRAINT life_cognition_job_input_check CHECK (jsonb_typeof(input) = 'object'),
    CONSTRAINT life_cognition_job_output_check CHECK (output IS NULL OR jsonb_typeof(output) = 'object'),
    CONSTRAINT life_cognition_job_attempt_check CHECK (attempt >= 0 AND max_attempts > 0),
    CONSTRAINT life_cognition_job_unique UNIQUE (workspace_id, user_id, job_type, dedupe_key)
);


CREATE TABLE public.life_proactive_policy (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    timezone text DEFAULT 'Asia/Shanghai' NOT NULL,
    quiet_hours jsonb DEFAULT '{"start":"23:00","end":"08:00"}' NOT NULL,
    minimum_interval interval DEFAULT '6 hours' NOT NULL,
    next_check_at timestamptz DEFAULT now() NOT NULL,
    last_spoke_at timestamptz,
    unanswered_count integer DEFAULT 0 NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT life_proactive_policy_quiet_check CHECK (jsonb_typeof(quiet_hours) = 'object'),
    CONSTRAINT life_proactive_policy_unanswered_check CHECK (unanswered_count >= 0)
);

INSERT INTO life_proactive_policy (workspace_id, user_id)
SELECT workspace_id, user_id FROM companion_profile
ON CONFLICT DO NOTHING;

ALTER TABLE public.life_proactive_check
    ADD COLUMN message text DEFAULT '' NOT NULL,
    ADD COLUMN user_responded_at timestamptz,
    ADD COLUMN value_assessment text DEFAULT '' NOT NULL;

ALTER TABLE public.life_action_proposal
    ADD COLUMN rejected_at timestamptz,
    ADD COLUMN rejection_reason text DEFAULT '' NOT NULL,
    ADD COLUMN execution_receipt jsonb,
    ADD CONSTRAINT life_action_proposal_receipt_check CHECK (execution_receipt IS NULL OR jsonb_typeof(execution_receipt) = 'object');

ALTER TABLE public.life_action_proposal DROP CONSTRAINT life_action_proposal_type_check;
ALTER TABLE public.life_action_proposal ADD CONSTRAINT life_action_proposal_type_check
    CHECK (proposal_type = ANY (ARRAY[
        'experiment_start', 'experiment_extend', 'workspace_issue', 'agent_action', 'project_create',
        'module_adoption', 'memory_change', 'identity_change'
    ]));

CREATE TABLE public.life_experiment_observation (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    round_id uuid NOT NULL,
    material_id uuid,
    observation_type text NOT NULL,
    content text NOT NULL,
    captured_by text NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_experiment_observation_type_check CHECK (observation_type = ANY (ARRAY['natural_material', 'user_checkin', 'companion_inference', 'result'])),
    CONSTRAINT life_experiment_observation_captured_by_check CHECK (captured_by = ANY (ARRAY['user', 'companion', 'system']))
);

ALTER TABLE public.life_experiment_round
    ADD COLUMN review_draft jsonb,
    ADD CONSTRAINT life_experiment_round_review_draft_check CHECK (review_draft IS NULL OR jsonb_typeof(review_draft) = 'object');

CREATE TABLE public.life_module (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_experiment_id uuid,
    name text NOT NULL,
    status text DEFAULT 'proposed' NOT NULL,
    current_version integer DEFAULT 1 NOT NULL,
    enabled_at timestamptz,
    disabled_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_module_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT life_module_status_check CHECK (status = ANY (ARRAY['proposed', 'active', 'paused', 'retired'])),
    CONSTRAINT life_module_version_positive CHECK (current_version > 0)
);

CREATE TABLE public.life_module_version (
    module_id uuid NOT NULL,
    version integer NOT NULL,
    definition jsonb NOT NULL,
    change_reason text DEFAULT '' NOT NULL,
    confirmed_at timestamptz NOT NULL,
    confirmed_by_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (module_id, version),
    CONSTRAINT life_module_version_definition_check CHECK (jsonb_typeof(definition) = 'object'),
    CONSTRAINT life_module_version_number_check CHECK (version > 0)
);

CREATE TABLE public.life_observer (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    name text NOT NULL,
    basis_type text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    current_version integer DEFAULT 1 NOT NULL,
    minimum_interval interval DEFAULT '12 hours' NOT NULL,
    next_run_at timestamptz DEFAULT now() NOT NULL,
    last_run_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_observer_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT life_observer_basis_check CHECK (basis_type = ANY (ARRAY['real_person', 'reconstructed', 'virtual'])),
    CONSTRAINT life_observer_status_check CHECK (status = ANY (ARRAY['active', 'paused', 'archived'])),
    CONSTRAINT life_observer_unique_agent UNIQUE (workspace_id, user_id, agent_id)
);

CREATE TABLE public.life_observer_version (
    observer_id uuid NOT NULL,
    version integer NOT NULL,
    personality jsonb DEFAULT '{}' NOT NULL,
    perspective jsonb DEFAULT '{}' NOT NULL,
    expression_profile jsonb DEFAULT '{}' NOT NULL,
    change_reason text DEFAULT '' NOT NULL,
    confirmed_at timestamptz NOT NULL,
    confirmed_by_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (observer_id, version),
    CONSTRAINT life_observer_version_json_check CHECK (jsonb_typeof(personality) = 'object' AND jsonb_typeof(perspective) = 'object' AND jsonb_typeof(expression_profile) = 'object')
);

CREATE TABLE public.life_observer_knowledge (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    observer_id uuid NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    source text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE public.life_observer_judgement (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    observer_id uuid NOT NULL,
    status text DEFAULT 'internal' NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    evidence jsonb DEFAULT '[]' NOT NULL,
    confidence double precision DEFAULT 0 NOT NULL,
    uncertainty text DEFAULT '' NOT NULL,
    published_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_observer_judgement_status_check CHECK (status = ANY (ARRAY['internal', 'published', 'withdrawn'])),
    CONSTRAINT life_observer_judgement_evidence_check CHECK (jsonb_typeof(evidence) = 'array'),
    CONSTRAINT life_observer_judgement_confidence_check CHECK (confidence >= 0 AND confidence <= 1)
);

CREATE TABLE public.life_observation_topic (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    summary text DEFAULT '' NOT NULL,
    status text DEFAULT 'open' NOT NULL,
    companion_response text DEFAULT '' NOT NULL,
    surfaced_at timestamptz,
    resolved_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_observation_topic_status_check CHECK (status = ANY (ARRAY['open', 'surfaced', 'discussing', 'resolved', 'archived']))
);

CREATE TABLE public.life_observation_topic_judgement (
    topic_id uuid NOT NULL,
    judgement_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (topic_id, judgement_id)
);

ALTER TABLE public.life_chronicle_entry
    ADD COLUMN period_kind text DEFAULT 'event' NOT NULL,
    ADD COLUMN actions text DEFAULT '' NOT NULL,
    ADD COLUMN status text DEFAULT 'published' NOT NULL,
    ADD COLUMN generated_by text DEFAULT 'user' NOT NULL,
    ADD COLUMN revision integer DEFAULT 1 NOT NULL,
    ADD CONSTRAINT life_chronicle_period_kind_check CHECK (period_kind = ANY (ARRAY['day', 'week', 'month', 'year', 'event'])),
    ADD CONSTRAINT life_chronicle_status_check CHECK (status = ANY (ARRAY['draft', 'published', 'superseded'])),
    ADD CONSTRAINT life_chronicle_generated_by_check CHECK (generated_by = ANY (ARRAY['user', 'companion', 'system'])),
    ADD CONSTRAINT life_chronicle_revision_positive CHECK (revision > 0);

CREATE TABLE public.life_chronicle_revision (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    entry_id uuid NOT NULL,
    revision integer NOT NULL,
    facts text NOT NULL,
    feelings text NOT NULL,
    understanding_then text NOT NULL,
    understanding_later text NOT NULL,
    actions text DEFAULT '' NOT NULL,
    change_reason text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_chronicle_revision_unique UNIQUE (entry_id, revision)
);

CREATE TABLE public.life_upgrade_evaluation (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    identity_version_id uuid,
    candidate_label text NOT NULL,
    baseline_label text NOT NULL,
    scenarios jsonb NOT NULL,
    result jsonb,
    status text DEFAULT 'pending' NOT NULL,
    rollback_recommended boolean DEFAULT false NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_upgrade_evaluation_scenarios_check CHECK (jsonb_typeof(scenarios) = 'array'),
    CONSTRAINT life_upgrade_evaluation_result_check CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
    CONSTRAINT life_upgrade_evaluation_status_check CHECK (status = ANY (ARRAY['pending', 'running', 'passed', 'failed', 'unknown']))
);

CREATE FUNCTION public.capture_life_chat_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
BEGIN
    FOR target IN
        SELECT cp.workspace_id, cp.user_id, cp.agent_id
          FROM companion_profile cp
          JOIN chat_session cs
            ON cs.id = NEW.chat_session_id
           AND cs.workspace_id = cp.workspace_id
           AND cs.creator_id = cp.user_id
           AND cs.agent_id = cp.agent_id
    LOOP
        IF NEW.role = 'user' THEN
            UPDATE companion_profile
            SET return_context = CASE
                    WHEN last_interaction_at IS NOT NULL AND last_interaction_at < NEW.created_at - interval '7 days'
                    THEN jsonb_build_object('reunion', true, 'last_interaction_at', last_interaction_at)
                    ELSE '{}'::jsonb
                END,
                last_interaction_at = NEW.created_at,
                updated_at = now()
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id;
            UPDATE life_proactive_policy
            SET unanswered_count = 0, updated_at = now()
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id;
            UPDATE life_proactive_check
            SET user_responded_at = NEW.created_at
            WHERE workspace_id = target.workspace_id AND user_id = target.user_id
              AND status = 'spoke' AND user_responded_at IS NULL;
        END IF;
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'chat_message', NEW.id::text, '1',
            NEW.content, jsonb_build_object('role', NEW.role, 'chat_session_id', NEW.chat_session_id), NEW.created_at
        ) ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
          DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at;

        IF NEW.role = 'assistant' THEN
            INSERT INTO life_cognition_job (
                workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
            ) VALUES (
                target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
                'chat:' || NEW.chat_session_id::text || ':' || NEW.id::text,
                jsonb_build_object('chat_session_id', NEW.chat_session_id, 'through_message_id', NEW.id)
            ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_life_chat_material_after_write
    AFTER INSERT OR UPDATE OF content ON public.chat_message
    FOR EACH ROW EXECUTE FUNCTION public.capture_life_chat_material();

CREATE FUNCTION public.capture_life_issue_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'task', NEW.id::text, revision,
            concat_ws(E'\n', NEW.title, NEW.description),
            jsonb_build_object('status', NEW.status, 'priority', NEW.priority, 'due_date', NEW.due_date, 'project_id', NEW.project_id),
            NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'task:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'task', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_life_issue_material_after_write
    AFTER INSERT OR UPDATE OF title, description, status, priority, due_date, project_id ON public.issue
    FOR EACH ROW EXECUTE FUNCTION public.capture_life_issue_material();

CREATE FUNCTION public.capture_life_comment_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'comment', NEW.id::text, revision,
            NEW.content,
            jsonb_build_object('issue_id', NEW.issue_id, 'author_type', NEW.author_type, 'author_id', NEW.author_id, 'comment_type', NEW.type),
            NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'comment:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'comment', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_life_comment_material_after_write
    AFTER INSERT OR UPDATE OF content, type, resolved_at ON public.comment
    FOR EACH ROW EXECUTE FUNCTION public.capture_life_comment_material();

CREATE FUNCTION public.capture_life_project_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    FOR target IN SELECT workspace_id, user_id, agent_id FROM companion_profile WHERE workspace_id = NEW.workspace_id LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'project', NEW.id::text, revision,
            concat_ws(E'\n', NEW.title, NEW.description),
            jsonb_build_object('status', NEW.status, 'priority', NEW.priority), NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'project:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'project', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_life_project_material_after_write
    AFTER INSERT OR UPDATE OF title, description, status, priority ON public.project
    FOR EACH ROW EXECUTE FUNCTION public.capture_life_project_material();

CREATE FUNCTION public.capture_life_experiment_material() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    target record;
    revision text;
    body text;
BEGIN
    revision := floor(extract(epoch FROM NEW.updated_at) * 1000000)::bigint::text;
    body := jsonb_build_object(
        'round_id', NEW.id,
        'status', NEW.status,
        'plan', NEW.plan,
        'starts_at', NEW.starts_at,
        'ends_at', NEW.ends_at,
        'stop_reason', NEW.stop_reason,
        'review', NEW.review
    )::text;
    FOR target IN
        SELECT experiment.workspace_id, experiment.user_id, profile.agent_id
        FROM life_experiment experiment
        JOIN companion_profile profile
          ON profile.workspace_id = experiment.workspace_id
         AND profile.user_id = experiment.user_id
        WHERE experiment.id = NEW.experiment_id
    LOOP
        INSERT INTO life_material (
            workspace_id, user_id, source_type, source_key, source_revision,
            content, metadata, occurred_at
        ) VALUES (
            target.workspace_id, target.user_id, 'experiment_round', NEW.id::text, revision,
            body, jsonb_build_object('experiment_id', NEW.experiment_id, 'status', NEW.status), NEW.updated_at
        ) ON CONFLICT DO NOTHING;
        INSERT INTO life_cognition_job (
            workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input
        ) VALUES (
            target.workspace_id, target.user_id, target.agent_id, 'understand_materials',
            'experiment_round:' || NEW.id::text || ':' || revision,
            jsonb_build_object('source_type', 'experiment_round', 'source_key', NEW.id, 'source_revision', revision)
        ) ON CONFLICT (workspace_id, user_id, job_type, dedupe_key) DO NOTHING;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_life_experiment_material_after_write
    AFTER INSERT OR UPDATE OF status, plan, starts_at, ends_at, stopped_at, stop_reason, review ON public.life_experiment_round
    FOR EACH ROW EXECUTE FUNCTION public.capture_life_experiment_material();
