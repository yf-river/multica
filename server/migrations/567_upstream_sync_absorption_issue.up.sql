CREATE TABLE upstream_sync_absorption_task (
    item_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'task_created'
        CHECK (status IN ('task_created','in_progress','published')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX CONCURRENTLY upstream_sync_absorption_task_item
    ON upstream_sync_absorption_task(item_id);
