CREATE TABLE public.autopilot_trigger_rotation_request (
    workspace_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    trigger_id uuid NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT autopilot_trigger_rotation_request_completion_check CHECK ((((response_status IS NULL) AND (response_body IS NULL) AND (completed_at IS NULL)) OR (((response_status >= 200) AND (response_status <= 599)) AND (response_body IS NOT NULL) AND (completed_at IS NOT NULL)))),
    CONSTRAINT autopilot_trigger_rotation_request_request_hash_check CHECK ((request_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT autopilot_trigger_rotation_request_pkey PRIMARY KEY (workspace_id, actor_id, idempotency_key),
    CONSTRAINT autopilot_trigger_rotation_request_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES public.autopilot_trigger(id) ON DELETE CASCADE,
    CONSTRAINT autopilot_trigger_rotation_request_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE
);

CREATE INDEX idx_autopilot_trigger_rotation_request_completed_at ON public.autopilot_trigger_rotation_request (completed_at) WHERE (completed_at IS NOT NULL);
CREATE INDEX idx_autopilot_trigger_rotation_request_incomplete_created_at ON public.autopilot_trigger_rotation_request (created_at) WHERE (completed_at IS NULL);

CREATE TABLE public.chat_idempotency_record (
    workspace_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    CONSTRAINT chat_idempotency_record_actor_type_check CHECK ((actor_type = ANY (ARRAY['member'::text, 'agent'::text]))),
    CONSTRAINT chat_idempotency_record_operation_check CHECK ((operation = ANY (ARRAY['create_session'::text, 'send_message'::text]))),
    CONSTRAINT chat_idempotency_record_request_hash_check CHECK ((request_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT chat_idempotency_record_response_check CHECK ((((response_status IS NULL) AND (response_body IS NULL)) OR ((response_status = 201) AND (response_body IS NOT NULL)))),
    CONSTRAINT chat_idempotency_record_pkey PRIMARY KEY (workspace_id, actor_type, actor_id, operation, idempotency_key),
    CONSTRAINT chat_idempotency_record_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE
);

CREATE TABLE public.domain_event_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    event_type text NOT NULL,
    workspace_id uuid,
    actor_type text,
    actor_id text,
    task_id text,
    chat_session_id text,
    payload jsonb NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text,
    lease_until timestamp with time zone,
    last_error text,
    processed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    stream_key text,
    sequence_no bigint GENERATED ALWAYS AS IDENTITY,
    dead_lettered_at timestamp with time zone,
    dead_letter_reason text,
    CONSTRAINT domain_event_outbox_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT domain_event_outbox_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT domain_event_outbox_single_terminal_state CHECK (((processed_at IS NULL) OR (dead_lettered_at IS NULL))),
    CONSTRAINT domain_event_outbox_stream_key_length CHECK (((stream_key IS NULL) OR ((char_length(stream_key) >= 1) AND (char_length(stream_key) <= 512)))),
    CONSTRAINT domain_event_outbox_pkey PRIMARY KEY (id),
    CONSTRAINT domain_event_outbox_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE
);

CREATE INDEX idx_domain_event_outbox_dead_lettered ON public.domain_event_outbox (dead_lettered_at, sequence_no) WHERE (dead_lettered_at IS NOT NULL);
CREATE INDEX idx_domain_event_outbox_pending ON public.domain_event_outbox (available_at, sequence_no) WHERE ((processed_at IS NULL) AND (dead_lettered_at IS NULL));
CREATE INDEX idx_domain_event_outbox_pending_stream ON public.domain_event_outbox (stream_key, sequence_no) WHERE ((processed_at IS NULL) AND (dead_lettered_at IS NULL) AND (stream_key IS NOT NULL));

CREATE TABLE public.domain_event_delivery (
    event_id uuid NOT NULL,
    consumer text NOT NULL,
    CONSTRAINT domain_event_delivery_pkey PRIMARY KEY (event_id, consumer),
    CONSTRAINT domain_event_delivery_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.domain_event_outbox(id) ON DELETE CASCADE
);

CREATE TABLE public.resource_create_request (
    workspace_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    resource_type text NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL,
    resource_id uuid,
    response_body jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT resource_create_request_completion_check CHECK ((((resource_id IS NULL) AND (response_body IS NULL) AND (completed_at IS NULL)) OR ((resource_id IS NOT NULL) AND (response_body IS NOT NULL) AND (completed_at IS NOT NULL)))),
    CONSTRAINT resource_create_request_request_hash_check CHECK ((request_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT resource_create_request_resource_type_check CHECK ((resource_type = ANY (ARRAY['workspace'::text, 'workspace_member'::text, 'project'::text, 'squad'::text, 'agent'::text, 'skill'::text, 'attachment'::text, 'quick_create'::text, 'issue'::text, 'comment'::text, 'autopilot_trigger'::text, 'issue_rerun'::text, 'runtime_profile'::text, 'label'::text, 'project_resource'::text, 'prompt_library_item'::text, 'prompt_library_version'::text, 'prompt_library_trial'::text, 'agent_playground_experiment'::text, 'prompt_evaluation_agent_run'::text, 'prompt_evaluation_local_run'::text, 'prompt_evaluation_re_eval_asset'::text, 'prompt_evaluation_candidate'::text, 'prompt_evaluation_candidate_publish'::text, 'prompt_evaluation_candidate_reject'::text, 'prompt_evaluation_asset'::text, 'prompt_evaluation_case'::text, 'prompt_evaluation_trace_import'::text, 'prompt_evaluation_dataset_version'::text, 'prompt_evaluation_evidence_snapshot'::text, 'prompt_evaluation_evidence_batch'::text, 'prompt_evaluation_dataset_restore'::text, 'prompt_evaluation_skill_inventory'::text, 'prompt_evaluation_skill_snapshot'::text, 'prompt_evaluation_skill_case_drafts'::text, 'prompt_evaluation_skill_apply'::text]))),
    CONSTRAINT resource_create_request_pkey PRIMARY KEY (workspace_id, actor_id, resource_type, idempotency_key),
    CONSTRAINT resource_create_request_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE
);

CREATE INDEX idx_resource_create_request_completed_at ON public.resource_create_request (completed_at) WHERE (completed_at IS NOT NULL);
CREATE INDEX idx_resource_create_request_incomplete_created_at ON public.resource_create_request (created_at) WHERE (completed_at IS NULL);

CREATE TABLE public.skill_import_request (
    workspace_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT skill_import_request_completion_check CHECK ((((response_status IS NULL) AND (response_body IS NULL) AND (completed_at IS NULL)) OR (((response_status >= 200) AND (response_status <= 599)) AND (response_body IS NOT NULL) AND (completed_at IS NOT NULL)))),
    CONSTRAINT skill_import_request_request_hash_check CHECK ((request_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT skill_import_request_pkey PRIMARY KEY (workspace_id, actor_id, idempotency_key),
    CONSTRAINT skill_import_request_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE
);

CREATE INDEX idx_skill_import_request_completed_at ON public.skill_import_request (completed_at) WHERE (completed_at IS NOT NULL);
CREATE INDEX idx_skill_import_request_incomplete_created_at ON public.skill_import_request (created_at) WHERE (completed_at IS NULL);
