CREATE TABLE repository (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    identity text NOT NULL,
    config jsonb NOT NULL,
    default_for_tasks boolean NOT NULL DEFAULT false,
    revision integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE repository_location (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    daemon_id text NOT NULL,
    local_path text NOT NULL,
    execution_mode text NOT NULL DEFAULT 'worktree',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE repository_migration_backup (
    kind text NOT NULL,
    id uuid NOT NULL,
    data jsonb NOT NULL
);
