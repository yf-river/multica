CREATE TABLE public.companion_profile (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE public.life_memory (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_by_type text NOT NULL,
    created_by_id uuid NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'candidate' NOT NULL,
    content text NOT NULL,
    confidence double precision DEFAULT 0 NOT NULL,
    urgency double precision DEFAULT 0 NOT NULL,
    uncertainty text DEFAULT '' NOT NULL,
    valid_from timestamptz,
    valid_to timestamptz,
    confirmed_at timestamptz,
    confirmed_by_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_memory_created_by_type_check CHECK (created_by_type = ANY (ARRAY['member', 'agent', 'system'])),
    CONSTRAINT life_memory_kind_check CHECK (kind = ANY (ARRAY['current_expression', 'weak_signal', 'understanding', 'fact', 'plan', 'commitment'])),
    CONSTRAINT life_memory_status_check CHECK (status = ANY (ARRAY['candidate', 'confirmed', 'archived'])),
    CONSTRAINT life_memory_content_check CHECK (length(btrim(content)) > 0),
    CONSTRAINT life_memory_confidence_check CHECK (confidence >= 0 AND confidence <= 1),
    CONSTRAINT life_memory_urgency_check CHECK (urgency >= 0 AND urgency <= 1),
    CONSTRAINT life_memory_valid_range_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from),
    CONSTRAINT life_memory_confirmation_check CHECK (
        (status = 'confirmed' AND confirmed_at IS NOT NULL AND confirmed_by_id IS NOT NULL)
        OR (status = 'candidate' AND confirmed_at IS NULL AND confirmed_by_id IS NULL)
        OR (status = 'archived' AND ((confirmed_at IS NULL AND confirmed_by_id IS NULL) OR (confirmed_at IS NOT NULL AND confirmed_by_id IS NOT NULL)))
    )
);

CREATE TABLE public.life_memory_evidence (
    memory_id uuid NOT NULL,
    source_type text NOT NULL,
    source_id uuid NOT NULL,
    excerpt text DEFAULT '' NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (memory_id, source_type, source_id),
    CONSTRAINT life_memory_evidence_source_type_check CHECK (source_type = ANY (ARRAY['chat_message', 'task', 'comment', 'memory', 'experiment_round']))
);

CREATE TABLE public.life_memory_dependency (
    source_memory_id uuid NOT NULL,
    derived_memory_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (source_memory_id, derived_memory_id),
    CONSTRAINT life_memory_dependency_not_self CHECK (source_memory_id <> derived_memory_id)
);

CREATE TABLE public.life_action_proposal (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    companion_agent_id uuid NOT NULL,
    proposal_type text NOT NULL,
    status text DEFAULT 'internal_draft' NOT NULL,
    title text NOT NULL,
    summary text DEFAULT '' NOT NULL,
    payload jsonb DEFAULT '{}' NOT NULL,
    expires_at timestamptz,
    confirmed_at timestamptz,
    executed_at timestamptz,
    failure_reason text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_action_proposal_type_check CHECK (proposal_type = ANY (ARRAY['experiment_start', 'experiment_extend', 'workspace_issue', 'module_adoption'])),
    CONSTRAINT life_action_proposal_status_check CHECK (status = ANY (ARRAY['internal_draft', 'pending_confirmation', 'approved', 'rejected', 'expired', 'executed', 'failed'])),
    CONSTRAINT life_action_proposal_title_check CHECK (length(btrim(title)) > 0),
    CONSTRAINT life_action_proposal_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT life_action_proposal_confirmation_check CHECK (
        (status = ANY (ARRAY['approved', 'executed', 'failed']) AND confirmed_at IS NOT NULL)
        OR (status <> ALL (ARRAY['approved', 'executed', 'failed']) AND confirmed_at IS NULL)
    ),
    CONSTRAINT life_action_proposal_execution_check CHECK (
        (status = 'executed' AND executed_at IS NOT NULL)
        OR (status <> 'executed' AND executed_at IS NULL)
    )
);

CREATE TABLE public.life_experiment (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    problem text NOT NULL,
    hypothesis text NOT NULL,
    method jsonb DEFAULT '{}' NOT NULL,
    created_by_type text NOT NULL,
    created_by_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_experiment_title_check CHECK (length(btrim(title)) > 0),
    CONSTRAINT life_experiment_problem_check CHECK (length(btrim(problem)) > 0),
    CONSTRAINT life_experiment_hypothesis_check CHECK (length(btrim(hypothesis)) > 0),
    CONSTRAINT life_experiment_method_check CHECK (jsonb_typeof(method) = 'object'),
    CONSTRAINT life_experiment_created_by_type_check CHECK (created_by_type = ANY (ARRAY['member', 'agent']))
);

CREATE TABLE public.life_experiment_round (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    experiment_id uuid NOT NULL,
    previous_round_id uuid,
    proposal_id uuid,
    issue_id uuid,
    status text DEFAULT 'draft' NOT NULL,
    plan jsonb DEFAULT '{}' NOT NULL,
    starts_at timestamptz,
    ends_at timestamptz,
    stopped_at timestamptz,
    stop_reason text DEFAULT '' NOT NULL,
    confirmed_at timestamptz,
    confirmed_by_id uuid,
    review jsonb,
    reviewed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_experiment_round_status_check CHECK (status = ANY (ARRAY['draft', 'pending_confirmation', 'running', 'stopped', 'awaiting_review', 'reviewed', 'start_failed'])),
    CONSTRAINT life_experiment_round_plan_check CHECK (jsonb_typeof(plan) = 'object'),
    CONSTRAINT life_experiment_round_review_check CHECK (review IS NULL OR jsonb_typeof(review) = 'object'),
    CONSTRAINT life_experiment_round_time_check CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at),
    CONSTRAINT life_experiment_round_confirmation_check CHECK (
        (status = ANY (ARRAY['running', 'stopped', 'awaiting_review', 'reviewed', 'start_failed']) AND confirmed_at IS NOT NULL AND confirmed_by_id IS NOT NULL)
        OR (status = ANY (ARRAY['draft', 'pending_confirmation']) AND confirmed_at IS NULL AND confirmed_by_id IS NULL)
    ),
    CONSTRAINT life_experiment_round_running_time_check CHECK (
        status <> 'running' OR (starts_at IS NOT NULL AND ends_at IS NOT NULL AND stopped_at IS NULL)
    ),
    CONSTRAINT life_experiment_round_stopped_time_check CHECK (
        status NOT IN ('stopped', 'awaiting_review', 'reviewed') OR stopped_at IS NOT NULL
    ),
    CONSTRAINT life_experiment_round_reviewed_check CHECK (
        (status = 'reviewed' AND review IS NOT NULL AND reviewed_at IS NOT NULL)
        OR (status <> 'reviewed' AND reviewed_at IS NULL)
    )
);

CREATE TABLE public.life_experiment_memory (
    round_id uuid NOT NULL,
    memory_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (round_id, memory_id, role),
    CONSTRAINT life_experiment_memory_role_check CHECK (role = ANY (ARRAY['input', 'observation', 'result']))
);

CREATE TABLE public.life_proactive_check (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    companion_agent_id uuid NOT NULL,
    status text NOT NULL,
    trigger_source text NOT NULL,
    reason text NOT NULL,
    context_snapshot jsonb DEFAULT '{}' NOT NULL,
    checked_at timestamptz DEFAULT now() NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_proactive_check_status_check CHECK (status = ANY (ARRAY['silent', 'spoke', 'failed'])),
    CONSTRAINT life_proactive_check_trigger_check CHECK (trigger_source = ANY (ARRAY['schedule', 'commitment', 'risk', 'manual'])),
    CONSTRAINT life_proactive_check_reason_check CHECK (length(btrim(reason)) > 0),
    CONSTRAINT life_proactive_check_context_check CHECK (jsonb_typeof(context_snapshot) = 'object')
);

CREATE TABLE public.life_chronicle_entry (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    facts text NOT NULL,
    feelings text DEFAULT '' NOT NULL,
    understanding_then text DEFAULT '' NOT NULL,
    understanding_later text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT life_chronicle_period_check CHECK (period_end >= period_start),
    CONSTRAINT life_chronicle_facts_check CHECK (length(btrim(facts)) > 0)
);

CREATE TABLE public.life_chronicle_evidence (
    entry_id uuid NOT NULL,
    source_type text NOT NULL,
    source_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    PRIMARY KEY (entry_id, source_type, source_id),
    CONSTRAINT life_chronicle_evidence_source_type_check CHECK (source_type = ANY (ARRAY['chat_message', 'task', 'memory', 'experiment_round']))
);
