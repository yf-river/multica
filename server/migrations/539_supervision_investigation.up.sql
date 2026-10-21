ALTER TABLE supervision_config ADD COLUMN investigation_enabled boolean NOT NULL DEFAULT false;
CREATE TABLE supervision_investigation (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    task_id uuid NOT NULL,
    request jsonb NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
