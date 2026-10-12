CREATE TABLE IF NOT EXISTS meal_workspace (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    revision bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
