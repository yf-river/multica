CREATE TABLE release_agent_config (
    workspace_id uuid NOT NULL,
    project_id uuid NOT NULL,
    agent_id uuid,
    owner_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    instructions text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
