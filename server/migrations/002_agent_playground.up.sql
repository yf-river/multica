CREATE TABLE IF NOT EXISTS public.agent_playground_experiment (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES public.workspace(id) ON DELETE CASCADE,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    dataset_asset_id uuid REFERENCES public.prompt_evaluation_asset(id) ON DELETE SET NULL,
    dataset_version_id uuid REFERENCES public.prompt_evaluation_dataset_version(id) ON DELETE SET NULL,
    judge_agent_id uuid REFERENCES public.agent(id) ON DELETE SET NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    created_by uuid REFERENCES public."user"(id) ON DELETE SET NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_experiment_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'ready'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);

CREATE TABLE IF NOT EXISTS public.agent_playground_input (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    experiment_id uuid NOT NULL REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES public.workspace(id) ON DELETE CASCADE,
    dataset_row_id uuid REFERENCES public.prompt_evaluation_dataset_version_row(id) ON DELETE SET NULL,
    row_index integer DEFAULT 0 NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    input text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_input_experiment_index_key UNIQUE (experiment_id, row_index)
);

CREATE TABLE IF NOT EXISTS public.agent_playground_agent (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    experiment_id uuid NOT NULL REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES public.workspace(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL REFERENCES public.agent(id) ON DELETE CASCADE,
    display_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_agent_unique UNIQUE (experiment_id, agent_id)
);

CREATE TABLE IF NOT EXISTS public.agent_playground_result (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    experiment_id uuid NOT NULL REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE,
    input_id uuid NOT NULL REFERENCES public.agent_playground_input(id) ON DELETE CASCADE,
    experiment_agent_id uuid NOT NULL REFERENCES public.agent_playground_agent(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES public.workspace(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL REFERENCES public.agent(id) ON DELETE CASCADE,
    chat_session_id uuid REFERENCES public.chat_session(id) ON DELETE SET NULL,
    task_id uuid REFERENCES public.agent_task_queue(id) ON DELETE SET NULL,
    rendered_input text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    output text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_result_unique UNIQUE (input_id, experiment_agent_id),
    CONSTRAINT agent_playground_result_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'queued'::text, 'dispatched'::text, 'running'::text, 'waiting_local_directory'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

CREATE TABLE IF NOT EXISTS public.agent_playground_judgement (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    experiment_id uuid NOT NULL REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE,
    input_id uuid NOT NULL REFERENCES public.agent_playground_input(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES public.workspace(id) ON DELETE CASCADE,
    judge_agent_id uuid NOT NULL REFERENCES public.agent(id) ON DELETE CASCADE,
    chat_session_id uuid REFERENCES public.chat_session(id) ON DELETE SET NULL,
    task_id uuid REFERENCES public.agent_task_queue(id) ON DELETE SET NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    output text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_judgement_unique UNIQUE (input_id),
    CONSTRAINT agent_playground_judgement_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'queued'::text, 'dispatched'::text, 'running'::text, 'waiting_local_directory'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

CREATE INDEX IF NOT EXISTS idx_agent_playground_experiment_workspace_created ON public.agent_playground_experiment USING btree (workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_playground_input_experiment_index ON public.agent_playground_input USING btree (experiment_id, row_index);
CREATE INDEX IF NOT EXISTS idx_agent_playground_agent_experiment_order ON public.agent_playground_agent USING btree (experiment_id, display_order);
CREATE INDEX IF NOT EXISTS idx_agent_playground_result_experiment ON public.agent_playground_result USING btree (experiment_id, input_id, experiment_agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_playground_judgement_experiment ON public.agent_playground_judgement USING btree (experiment_id, input_id);
